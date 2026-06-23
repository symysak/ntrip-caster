package server

import (
	"bufio"
	"fmt"
	"strconv"
)

// protoVersion distinguishes NTRIP 1.0 from 2.0 response framing.
type protoVersion int

const (
	ntripV1 protoVersion = 1
	ntripV2 protoVersion = 2
)

// writeStreamOK writes the success header that precedes a data stream.
//   - v1: "ICY 200 OK"
//   - v2: HTTP/1.1 200 OK with gnss/data and chunked transfer-encoding.
func writeStreamOK(w *bufio.Writer, v protoVersion, server string) error {
	switch v {
	case ntripV2:
		fmt.Fprintf(w,
			"HTTP/1.1 200 OK\r\n"+
				"Ntrip-Version: Ntrip/2.0\r\n"+
				"Server: %s\r\n"+
				"Cache-Control: no-store, no-cache, max-age=0\r\n"+
				"Pragma: no-cache\r\n"+
				"Connection: close\r\n"+
				"Content-Type: gnss/data\r\n"+
				"Transfer-Encoding: chunked\r\n"+
				"\r\n", server)
	default:
		w.WriteString("ICY 200 OK\r\n\r\n")
	}
	return w.Flush()
}

// writeSourceOK acknowledges a connected NTRIP server (push) connection.
func writeSourceOK(w *bufio.Writer, v protoVersion, server string) error {
	switch v {
	case ntripV2:
		fmt.Fprintf(w,
			"HTTP/1.1 200 OK\r\n"+
				"Ntrip-Version: Ntrip/2.0\r\n"+
				"Server: %s\r\n"+
				"Connection: close\r\n"+
				"\r\n", server)
	default:
		w.WriteString("ICY 200 OK\r\n\r\n")
	}
	return w.Flush()
}

// writeSourcetable writes a full sourcetable response (with terminator).
func writeSourcetable(w *bufio.Writer, v protoVersion, server, body string) error {
	full := body + "ENDSOURCETABLE\r\n"
	switch v {
	case ntripV2:
		fmt.Fprintf(w,
			"HTTP/1.1 200 OK\r\n"+
				"Ntrip-Version: Ntrip/2.0\r\n"+
				"Server: %s\r\n"+
				"Connection: close\r\n"+
				"Content-Type: gnss/sourcetable\r\n"+
				"Content-Length: %d\r\n"+
				"\r\n", server, len(full))
	default:
		fmt.Fprintf(w,
			"SOURCETABLE 200 OK\r\n"+
				"Server: %s\r\n"+
				"Content-Type: text/plain\r\n"+
				"Content-Length: %d\r\n"+
				"\r\n", server, len(full))
	}
	w.WriteString(full)
	return w.Flush()
}

// writeUnauthorized prompts for Basic credentials.
func writeUnauthorized(w *bufio.Writer, v protoVersion, server, realm string) error {
	switch v {
	case ntripV2:
		fmt.Fprintf(w,
			"HTTP/1.1 401 Unauthorized\r\n"+
				"Ntrip-Version: Ntrip/2.0\r\n"+
				"Server: %s\r\n"+
				"WWW-Authenticate: Basic realm=\"%s\"\r\n"+
				"Connection: close\r\n"+
				"Content-Length: 0\r\n"+
				"\r\n", server, realm)
	default:
		fmt.Fprintf(w,
			"HTTP/1.0 401 Unauthorized\r\n"+
				"Server: %s\r\n"+
				"WWW-Authenticate: Basic realm=\"%s\"\r\n"+
				"\r\n", server, realm)
	}
	return w.Flush()
}

// writeError writes a short status-only error response.
func writeError(w *bufio.Writer, v protoVersion, server string, code int, status string) error {
	switch v {
	case ntripV2:
		fmt.Fprintf(w,
			"HTTP/1.1 %d %s\r\n"+
				"Server: %s\r\n"+
				"Connection: close\r\n"+
				"Content-Length: 0\r\n"+
				"\r\n", code, status, server)
	default:
		fmt.Fprintf(w, "HTTP/1.0 %d %s\r\n\r\n", code, status)
	}
	return w.Flush()
}

// writeStreamChunk writes one stream payload to the client using the framing of
// the negotiated protocol version (raw bytes for v1, a chunked frame for v2),
// then flushes.
func writeStreamChunk(w *bufio.Writer, v protoVersion, data []byte) error {
	if v == ntripV2 {
		if err := writeChunk(w, data); err != nil {
			return err
		}
	} else if _, err := w.Write(data); err != nil {
		return err
	}
	return w.Flush()
}

// writeChunk writes one HTTP chunked-encoding frame.
func writeChunk(w *bufio.Writer, data []byte) error {
	if _, err := w.WriteString(strconv.FormatInt(int64(len(data)), 16)); err != nil {
		return err
	}
	if _, err := w.WriteString("\r\n"); err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	_, err := w.WriteString("\r\n")
	return err
}
