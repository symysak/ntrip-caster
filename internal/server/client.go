package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"time"

	"github.com/symysak/ntrip-caster/internal/caster"
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
