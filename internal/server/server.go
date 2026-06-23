// Package server implements the NTRIP wire protocol (v1 and v2) on top of raw
// TCP connections and dispatches each connection to the caster: client reads,
// server (source) pushes, sourcetable requests, and handover endpoints.
package server

import (
	"bufio"
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"log/slog"

	"github.com/symysak/ntrip-caster/internal/caster"
)

// requestTimeout bounds how long a client has to send its request line+headers.
const requestTimeout = 30 * time.Second

// Server serves NTRIP over a TCP listener.
type Server struct {
	mgr     *caster.Manager
	log     *slog.Logger
	version string
}

// New creates a Server. version is advertised in the Server response header.
func New(mgr *caster.Manager, log *slog.Logger, version string) *Server {
	return &Server{mgr: mgr, log: log, version: version}
}

// Serve accepts connections on ln until ctx is cancelled.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	go func() {
		<-ctx.Done()
		ln.Close()
	}()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return err
		}
		go s.handleConn(ctx, conn)
	}
}

func (s *Server) handleConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	if tc, ok := conn.(*net.TCPConn); ok {
		tc.SetKeepAlive(true)
		tc.SetKeepAlivePeriod(30 * time.Second)
	}

	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)

	// Bound the time to read the request preamble.
	conn.SetReadDeadline(time.Now().Add(requestTimeout))

	// Peek the method token to separate NTRIP v1 SOURCE (non-HTTP) from the
	// HTTP-shaped GET/POST verbs.
	peek, err := br.Peek(7)
	if err != nil && len(peek) == 0 {
		return
	}
	if strings.HasPrefix(string(peek), "SOURCE ") {
		s.handleSourceV1(ctx, conn, br, bw)
		return
	}

	req, err := http.ReadRequest(br)
	if err != nil {
		writeError(bw, ntripV1, s.version, 400, "Bad Request")
		return
	}

	switch req.Method {
	case http.MethodGet:
		s.handleClient(ctx, conn, br, bw, req)
	case http.MethodPost:
		s.handleSourceV2(ctx, conn, bw, req)
	default:
		writeError(bw, versionOf(req), s.version, 405, "Method Not Allowed")
	}
}

// versionOf reports the NTRIP protocol version from the Ntrip-Version header:
// "Ntrip/2.0" selects v2; anything else (including a missing header) is v1.
func versionOf(r *http.Request) protoVersion {
	if strings.EqualFold(r.Header.Get("Ntrip-Version"), "Ntrip/2.0") {
		return ntripV2
	}
	return ntripV1
}

// mountpointName strips the leading slash from a request path.
func mountpointName(p string) string {
	return strings.TrimPrefix(p, "/")
}

// streamToClient writes subscriber chunks to the client until the subscriber
// is dropped, the client disconnects, or ctx is cancelled.
func (s *Server) streamToClient(ctx context.Context, conn net.Conn, bw *bufio.Writer, sub *caster.Subscriber, v protoVersion) {
	// Stream writes have no request timeout.
	conn.SetReadDeadline(time.Time{})
	conn.SetWriteDeadline(time.Time{})

	for {
		select {
		case <-ctx.Done():
			return
		case <-sub.Done():
			return
		case chunk, ok := <-sub.Chunks():
			if !ok {
				return
			}
			conn.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
			var werr error
			if v == ntripV2 {
				werr = writeChunk(bw, chunk)
			} else {
				_, werr = bw.Write(chunk)
			}
			if werr == nil {
				werr = bw.Flush()
			}
			if werr != nil {
				return
			}
		}
	}
}

const streamWriteTimeout = 30 * time.Second
