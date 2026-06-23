package server

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/textproto"
	"strings"
	"time"

	"github.com/symysak/ntrip-caster/internal/caster"
)

// sourceReadTimeout drops a source that goes silent for this long.
const sourceReadTimeout = 60 * time.Second

// readChunkSize is the read buffer used when pumping source data.
const readChunkSize = 8192

// handleSourceV1 handles an NTRIP 1.0 server push:
//
//	SOURCE <password> /<mountpoint>\r\n
//	Source-Agent: ...\r\n
//	\r\n
//	<rtcm bytes...>
func (s *Server) handleSourceV1(ctx context.Context, conn net.Conn, br *bufio.Reader, bw *bufio.Writer) {
	line, err := br.ReadString('\n')
	if err != nil {
		return
	}
	fields := strings.Fields(strings.TrimSpace(line))
	if len(fields) < 3 {
		writeRaw(bw, "ERROR - Bad Request\r\n")
		return
	}
	password := fields[1]
	name := mountpointName(fields[2])

	// Consume the remaining request headers.
	tp := textproto.NewReader(br)
	hdr, _ := tp.ReadMIMEHeader()
	agent := hdr.Get("Source-Agent")
	if agent == "" {
		agent = hdr.Get("User-Agent")
	}

	cfg := s.mgr.Config()
	mp, ok := cfg.LookupMountpoint(name)
	if !ok {
		writeRaw(bw, "ERROR - Bad Mountpoint\r\n")
		return
	}
	if !authSourcePassword(mp, password) {
		writeRaw(bw, "ERROR - Bad Password\r\n")
		return
	}

	s.runSource(ctx, conn, bw, br, name, agent, ntripV1)
}

// handleSourceV2 handles an NTRIP 2.0 server push via HTTP POST with Basic auth.
func (s *Server) handleSourceV2(ctx context.Context, conn net.Conn, bw *bufio.Writer, req *http.Request) {
	name := mountpointName(req.URL.Path)
	cfg := s.mgr.Config()
	mp, ok := cfg.LookupMountpoint(name)
	if !ok {
		writeError(bw, ntripV2, s.version, 404, "Not Found")
		return
	}
	_, pass, hasAuth := basicCreds(req)
	if !hasAuth || !authSourcePassword(mp, pass) {
		writeUnauthorized(bw, ntripV2, s.version, "NTRIP "+name)
		return
	}
	agent := req.Header.Get("User-Agent")
	s.runSource(ctx, conn, bw, req.Body, name, agent, ntripV2)
}

// runSource attaches the source, acknowledges, and pumps bytes to subscribers
// until the source disconnects.
func (s *Server) runSource(ctx context.Context, conn net.Conn, bw *bufio.Writer, body io.Reader, name, agent string, v protoVersion) {
	mp := s.mgr.GetOrCreate(name)
	src := &caster.Source{
		RemoteAddr: conn.RemoteAddr().String(),
		Agent:      agent,
		ConnectAt:  nowFunc(),
	}
	if !mp.AttachSource(src) {
		writeError(bw, v, s.version, 409, "Conflict")
		s.log.Warn("source rejected: mountpoint in use", "mountpoint", name, "remote", src.RemoteAddr)
		return
	}
	defer mp.DetachSource(src)

	if err := writeSourceOK(bw, v, s.version); err != nil {
		return
	}
	s.log.Info("source connected", "mountpoint", name, "remote", src.RemoteAddr, "agent", agent, "version", int(v))
	defer s.log.Info("source disconnected", "mountpoint", name, "remote", src.RemoteAddr)

	buf := make([]byte, readChunkSize)
	for {
		if ctx.Err() != nil {
			return
		}
		conn.SetReadDeadline(nowFunc().Add(sourceReadTimeout))
		n, err := body.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			mp.Broadcast(chunk)
		}
		if err != nil {
			return
		}
	}
}

func writeRaw(bw *bufio.Writer, s string) {
	bw.WriteString(s)
	bw.Flush()
}

// nowFunc indirects time.Now for testability.
var nowFunc = time.Now
