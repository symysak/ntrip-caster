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

	hs := &handoverSession{
		srv:        s,
		conn:       conn,
		bw:         bw,
		group:      group,
		v:          v,
		addr:       addr,
		fixCh:      make(chan struct{}, 1),
		clientGone: make(chan struct{}),
	}
	hs.seed(req)
	go hs.readNMEA(br)
	defer hs.detach()
	hs.run(ctx)
}

// handoverSession drives one handover client connection. A reader goroutine
// tracks the client's latest NMEA position; the control loop (run) keeps the
// client subscribed to the nearest online member.
type handoverSession struct {
	srv   *Server
	conn  net.Conn
	bw    *bufio.Writer
	group config.HandoverGroup
	v     protoVersion
	addr  string

	// fixCh wakes the control loop when a new position arrives; clientGone is
	// closed when the client's read side ends (disconnect).
	fixCh      chan struct{}
	clientGone chan struct{}

	posMu    sync.Mutex
	haveFix  bool
	lat, lon float64

	// current is the subscribed member ("" if none); sub is its subscriber.
	current string
	sub     *caster.Subscriber
}

// seed applies an initial position carried in the request's Ntrip-GGA header.
func (hs *handoverSession) seed(req *http.Request) {
	if gga := req.Header.Get("Ntrip-GGA"); gga != "" {
		if fix, err := nmea.ParseGGA(gga); err == nil {
			hs.setPosition(fix.Lat, fix.Lon)
		}
	}
}

// readNMEA parses GGA sentences from the client until the connection ends.
func (hs *handoverSession) readNMEA(br *bufio.Reader) {
	defer close(hs.clientGone)
	hs.conn.SetReadDeadline(time.Time{})
	sc := bufio.NewScanner(br)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		if fix, err := nmea.ParseGGA(sc.Text()); err == nil {
			hs.setPosition(fix.Lat, fix.Lon)
		}
	}
}

// setPosition records the latest fix and wakes the control loop.
func (hs *handoverSession) setPosition(lat, lon float64) {
	hs.posMu.Lock()
	hs.lat, hs.lon, hs.haveFix = lat, lon, true
	hs.posMu.Unlock()
	select {
	case hs.fixCh <- struct{}{}:
	default: // a wake-up is already pending; the loop reads the latest position
	}
}

// position returns the latest fix and whether one has been received.
func (hs *handoverSession) position() (haveFix bool, lat, lon float64) {
	hs.posMu.Lock()
	defer hs.posMu.Unlock()
	return hs.haveFix, hs.lat, hs.lon
}

// nearest resolves the nearest online member for the given position, using the
// live config so reloads take effect.
func (hs *handoverSession) nearest(lat, lon float64) string {
	cfg := hs.srv.mgr.Config()
	g, ok := cfg.LookupHandover(hs.group.Name)
	if !ok {
		g = hs.group // fall back to the group captured at connect time
	}
	return handover.NewSelector(cfg, hs.srv.mgr.Online).Nearest(g, lat, lon)
}

// switchTo detaches the current member and subscribes to target. It returns
// false if the subscribe lost a race with the source detaching.
func (hs *handoverSession) switchTo(target string) bool {
	hs.detach()
	mp := hs.srv.mgr.Mountpoint(target)
	sub := caster.NewSubscriber(hs.addr)
	if mp == nil || !mp.Subscribe(sub) {
		return false
	}
	hs.sub, hs.current = sub, target
	hs.srv.log.Info("handover switch", "group", hs.group.Name, "remote", hs.addr, "mountpoint", target)
	return true
}

// detach unsubscribes from and closes the current subscriber, if any.
func (hs *handoverSession) detach() {
	if hs.sub != nil {
		if hs.current != "" {
			if mp := hs.srv.mgr.Mountpoint(hs.current); mp != nil {
				mp.Unsubscribe(hs.sub)
			}
		}
		hs.sub.Close()
	}
	hs.sub, hs.current = nil, ""
}

// run is the control loop: select the nearest member, stream it, and re-select
// when the client moves, the source drops, or a member goes offline.
func (hs *handoverSession) run(ctx context.Context) {
	for {
		haveFix, lat, lon := hs.position()
		if !haveFix {
			// Connected; waiting for the client's first NMEA fix.
			select {
			case <-ctx.Done():
				return
			case <-hs.clientGone:
				return
			case <-hs.fixCh:
				continue
			}
		}

		target := hs.nearest(lat, lon)
		if target == "" {
			// No member is online (or locatable) for this position.
			hs.srv.log.Info("handover: no online member available; disconnecting",
				"group", hs.group.Name, "remote", hs.addr)
			return
		}
		if target != hs.current && !hs.switchTo(target) {
			continue // raced with the source detaching; re-select
		}

		if !hs.stream(ctx) {
			return
		}
	}
}

// stream forwards the current member's bytes to the client until it should
// re-select (returns true) or the session should end (returns false).
func (hs *handoverSession) stream(ctx context.Context) bool {
	for {
		select {
		case <-ctx.Done():
			return false
		case <-hs.clientGone:
			return false
		case <-hs.sub.Done():
			hs.detach() // source disconnected; re-select on the next iteration
			return true
		case <-hs.fixCh:
			_, lat, lon := hs.position()
			if hs.nearest(lat, lon) != hs.current {
				return true // a different member is now nearest; outer loop switches
			}
		case chunk, ok := <-hs.sub.Chunks():
			if !ok {
				hs.detach()
				return true
			}
			hs.conn.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
			if err := writeStreamChunk(hs.bw, hs.v, chunk); err != nil {
				return false
			}
		}
	}
}
