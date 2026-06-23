package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/symysak/ntrip-caster/internal/caster"
	"github.com/symysak/ntrip-caster/internal/config"
	"github.com/symysak/ntrip-caster/internal/handover"
	"github.com/symysak/ntrip-caster/internal/nmea"
	"github.com/symysak/ntrip-caster/internal/sourcetable"
)

// handleClient serves an NTRIP client (rover) GET: a root/unknown path yields
// the sourcetable; a mountpoint yields its stream; a handover endpoint yields a
// stream that follows the client's NMEA position.
func (s *Server) handleClient(ctx context.Context, conn net.Conn, br *bufio.Reader, bw *bufio.Writer, req *http.Request) {
	v := versionOf(req)
	name := mountpointName(req.URL.Path)
	cfg := s.mgr.Config()

	// Root request or unknown stream: return the sourcetable.
	mpCfg, isMP := cfg.LookupMountpoint(name)
	group, isHandover := cfg.LookupHandover(name)
	if name == "" || (!isMP && !isHandover) {
		reason := "unknown-mountpoint"
		if name == "" {
			reason = "root"
		}
		body := sourcetable.Build(cfg, s.mgr.Online, false)
		writeSourcetable(bw, v, s.version, body)
		s.log.Info("sourcetable served",
			"remote", conn.RemoteAddr().String(), "agent", req.Header.Get("User-Agent"),
			"path", req.URL.Path, "reason", reason, "version", int(v))
		return
	}

	// Authenticate the rover against the requested stream, unless the stream is
	// configured as open (anonymous read access).
	open := (isHandover && group.Open) || (isMP && mpCfg.Open)
	if !open && !authClient(cfg, req, name) {
		user, _, _ := basicCreds(req)
		s.log.Warn("client auth failed",
			"remote", conn.RemoteAddr().String(), "user", user, "stream", name,
			"agent", req.Header.Get("User-Agent"), "version", int(v))
		writeUnauthorized(bw, v, s.version, "NTRIP "+name)
		return
	}

	agent := req.Header.Get("User-Agent")
	if isHandover {
		s.handleHandover(ctx, conn, br, bw, req, group, v, agent)
		return
	}
	s.handleMountpoint(ctx, conn, br, bw, name, v, agent)
}

// handleMountpoint streams a single static mountpoint to the client.
func (s *Server) handleMountpoint(ctx context.Context, conn net.Conn, br *bufio.Reader, bw *bufio.Writer, name string, v protoVersion, agent string) {
	mp := s.mgr.Mountpoint(name)
	sub := caster.NewSubscriber(conn.RemoteAddr().String())
	if mp == nil || !mp.Subscribe(sub) {
		// Offline: clients expect the sourcetable when a stream is unavailable.
		body := sourcetable.Build(s.mgr.Config(), s.mgr.Online, false)
		writeSourcetable(bw, v, s.version, body)
		s.log.Info("sourcetable served",
			"remote", sub.Addr, "agent", agent,
			"path", "/"+name, "reason", "offline-mountpoint", "version", int(v))
		return
	}
	defer mp.Unsubscribe(sub)

	if err := writeStreamOK(bw, v, s.version); err != nil {
		return
	}
	s.log.Info("client connected", "mountpoint", name, "remote", sub.Addr, "agent", agent, "version", int(v))
	defer s.log.Info("client disconnected", "mountpoint", name, "remote", sub.Addr)

	// Detect client disconnect (and drain any NMEA the client sends).
	go drainClient(conn, br, sub)

	s.streamToClient(ctx, conn, bw, sub, v)
}

// anyMemberOnline reports whether at least one member of the group currently
// has a connected source.
func (s *Server) anyMemberOnline(group config.HandoverGroup) bool {
	return slices.ContainsFunc(group.Members, s.mgr.Online)
}

// handleHandover streams the member mountpoint nearest the client's reported
// NMEA position, re-selecting when the client moves or the active source drops.
// The single client connection is preserved across switches; it is closed only
// when no member is available (so an all-offline group fails rather than hangs).
func (s *Server) handleHandover(ctx context.Context, conn net.Conn, br *bufio.Reader, bw *bufio.Writer, req *http.Request, group config.HandoverGroup, v protoVersion, agent string) {
	addr := conn.RemoteAddr().String()

	// Fail fast if no member has a source connected.
	if !s.anyMemberOnline(group) {
		body := sourcetable.Build(s.mgr.Config(), s.mgr.Online, false)
		writeSourcetable(bw, v, s.version, body)
		s.log.Info("handover unavailable: no online members",
			"group", group.Name, "remote", addr, "agent", agent, "version", int(v))
		return
	}

	if err := writeStreamOK(bw, v, s.version); err != nil {
		return
	}
	s.log.Info("handover client connected", "group", group.Name, "remote", addr, "agent", agent, "version", int(v))
	defer s.log.Info("handover client disconnected", "group", group.Name, "remote", addr)

	// Position state, updated by the NMEA reader goroutine.
	var posMu sync.Mutex
	var haveFix bool
	var lat, lon float64
	fixCh := make(chan struct{}, 1)   // wakes the control loop on a new fix
	clientGone := make(chan struct{}) // closed when the client read side ends
	notifyFix := func() {
		select {
		case fixCh <- struct{}{}:
		default:
		}
	}

	// Seed from an initial position carried in the request header, if any.
	if gga := req.Header.Get("Ntrip-GGA"); gga != "" {
		if fix, err := nmea.ParseGGA(gga); err == nil {
			lat, lon, haveFix = fix.Lat, fix.Lon, true
		}
	}

	go func() {
		defer close(clientGone)
		conn.SetReadDeadline(time.Time{})
		sc := bufio.NewScanner(br)
		sc.Buffer(make([]byte, 0, 4096), 64*1024)
		for sc.Scan() {
			fix, err := nmea.ParseGGA(sc.Text())
			if err != nil {
				continue
			}
			posMu.Lock()
			lat, lon, haveFix = fix.Lat, fix.Lon, true
			posMu.Unlock()
			notifyFix()
		}
	}()

	nearestFor := func(la, lo float64) string {
		cfg := s.mgr.Config()
		g, ok := cfg.LookupHandover(group.Name)
		if !ok {
			g = group // fall back to the group captured at connect time
		}
		return handover.NewSelector(cfg, s.mgr.Online).Nearest(g, la, lo)
	}

	current := "" // currently subscribed member, "" if none
	var sub *caster.Subscriber
	detach := func() {
		if sub != nil {
			if current != "" {
				if mp := s.mgr.Mountpoint(current); mp != nil {
					mp.Unsubscribe(sub)
				}
			}
			sub.Close()
		}
		sub, current = nil, ""
	}
	defer detach()

	for {
		posMu.Lock()
		hf, la, lo := haveFix, lat, lon
		posMu.Unlock()

		if !hf {
			// Connected; waiting for the client's first NMEA fix.
			select {
			case <-ctx.Done():
				return
			case <-clientGone:
				return
			case <-fixCh:
				continue
			}
		}

		target := nearestFor(la, lo)
		if target == "" {
			// No member is online (or locatable) for this position.
			s.log.Info("handover: no online member available; disconnecting",
				"group", group.Name, "remote", addr)
			return
		}

		if target != current {
			detach()
			mp := s.mgr.Mountpoint(target)
			ns := caster.NewSubscriber(addr)
			if mp == nil || !mp.Subscribe(ns) {
				continue // raced with the source detaching; re-select
			}
			sub, current = ns, target
			s.log.Info("handover switch", "group", group.Name, "remote", addr, "mountpoint", target)
		}

		// Stream the current member until it ends, the nearest member changes,
		// the source drops, or the client leaves.
	stream:
		for {
			select {
			case <-ctx.Done():
				return
			case <-clientGone:
				return
			case <-sub.Done():
				detach() // source disconnected; re-select on the next iteration
				break stream
			case <-fixCh:
				posMu.Lock()
				la, lo = lat, lon
				posMu.Unlock()
				if nearestFor(la, lo) != current {
					break stream // outer loop performs the switch
				}
			case chunk, ok := <-sub.Chunks():
				if !ok {
					detach()
					break stream
				}
				conn.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
				if err := writeStreamChunk(bw, v, chunk); err != nil {
					return
				}
			}
		}
	}
}

// drainClient reads and discards client-to-caster bytes (NMEA, keep-alives) so
// that a client disconnect promptly drops the subscriber.
func drainClient(conn net.Conn, br *bufio.Reader, sub *caster.Subscriber) {
	defer sub.Close()
	conn.SetReadDeadline(time.Time{})
	buf := make([]byte, 512)
	for {
		if _, err := br.Read(buf); err != nil {
			return
		}
	}
}
