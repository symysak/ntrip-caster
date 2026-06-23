package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
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
	_, isMP := cfg.LookupMountpoint(name)
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

	// Authenticate the rover against the requested stream.
	if !authClient(cfg, req, name) {
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

// handleHandover streams the nearest member mountpoint, switching as the
// client's reported NMEA GGA position changes.
func (s *Server) handleHandover(ctx context.Context, conn net.Conn, br *bufio.Reader, bw *bufio.Writer, req *http.Request, group config.HandoverGroup, v protoVersion, agent string) {
	sub := caster.NewSubscriber(conn.RemoteAddr().String())
	if err := writeStreamOK(bw, v, s.version); err != nil {
		return
	}
	s.log.Info("handover client connected", "group", group.Name, "remote", sub.Addr, "agent", agent, "version", int(v))
	defer s.log.Info("handover client disconnected", "group", group.Name, "remote", sub.Addr)

	var mu sync.Mutex
	current := "" // currently subscribed member mountpoint

	// switchTo moves the subscriber from the current member to target. A failed
	// (re)subscribe leaves current empty so the next GGA retries.
	switchTo := func(target string) {
		mu.Lock()
		defer mu.Unlock()
		if target == "" || target == current {
			return
		}
		if current != "" {
			if old := s.mgr.Mountpoint(current); old != nil {
				old.Unsubscribe(sub)
			}
		}
		mp := s.mgr.Mountpoint(target)
		if mp == nil || !mp.Subscribe(sub) {
			current = ""
			return
		}
		current = target
		s.log.Info("handover switch", "group", group.Name, "remote", sub.Addr, "mountpoint", target)
	}
	defer func() {
		mu.Lock()
		if current != "" {
			if mp := s.mgr.Mountpoint(current); mp != nil {
				mp.Unsubscribe(sub)
			}
		}
		mu.Unlock()
	}()

	selectNearest := func(lat, lon float64) {
		cfg := s.mgr.Config()
		g, ok := cfg.LookupHandover(group.Name)
		if !ok {
			g = group // fall back to the group captured at connect time
		}
		sel := handover.NewSelector(cfg, s.mgr.Online)
		switchTo(sel.Nearest(g, lat, lon))
	}

	// Seed from an initial position carried in the request header, if any.
	if gga := req.Header.Get("Ntrip-GGA"); gga != "" {
		if fix, err := nmea.ParseGGA(gga); err == nil {
			selectNearest(fix.Lat, fix.Lon)
		}
	}

	// Read NMEA GGA from the client and re-select on each fix.
	go func() {
		defer sub.Close()
		conn.SetReadDeadline(time.Time{})
		sc := bufio.NewScanner(br)
		sc.Buffer(make([]byte, 0, 4096), 64*1024)
		for sc.Scan() {
			fix, err := nmea.ParseGGA(sc.Text())
			if err != nil {
				continue
			}
			selectNearest(fix.Lat, fix.Lon)
		}
	}()

	s.streamToClient(ctx, conn, bw, sub, v)
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
