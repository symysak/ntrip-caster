package server

import (
	"bufio"
	"context"
	"encoding/base64"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/symysak/ntrip-caster/internal/caster"
	"github.com/symysak/ntrip-caster/internal/config"
)

func testConfig() *config.Config {
	cfg := &config.Config{
		Listen: "127.0.0.1:0",
		ClientUsers: []config.ClientUser{
			{Username: "rover1", Password: "pw1", Mountpoints: []string{"*"}},
		},
		Mountpoints: []config.Mountpoint{
			{Name: "TOKYO", Password: "tpush", Lat: 35.6586, Lon: 139.7454, Authentication: "B", Fee: "N"},
			{Name: "OSAKA", Password: "opush", Lat: 34.6937, Lon: 135.5023, Authentication: "B", Fee: "N"},
			{Name: "OPEN", Password: "openpush", Lat: 35.0, Lon: 135.0, Open: true, Fee: "N"},
		},
		Handover: []config.HandoverGroup{
			{Name: "AUTO", Members: []string{"TOKYO", "OSAKA"}},
		},
	}
	return cfg
}

// startCaster boots a caster on a random port and returns its address.
func startCaster(t *testing.T) (string, *caster.Manager, context.CancelFunc) {
	t.Helper()
	cfg := testConfig()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := caster.New(cfg, log)
	srv := New(mgr, log, "ntrip-caster/test")

	ln, err := net.Listen("tcp", cfg.Listen)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go srv.Serve(ctx, ln)
	return ln.Addr().String(), mgr, func() { cancel(); ln.Close() }
}

func basicHeader(user, pass string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(user+":"+pass))
}

// connectSourceV1 attaches a v1 SOURCE and returns the live connection.
func connectSourceV1(t *testing.T, addr, mp, pass string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial source: %v", err)
	}
	io.WriteString(c, "SOURCE "+pass+" /"+mp+"\r\nSource-Agent: test\r\n\r\n")
	line := readLine(t, c)
	if !strings.HasPrefix(line, "ICY 200") {
		t.Fatalf("source %s: expected ICY 200, got %q", mp, line)
	}
	return c
}

func readN(t *testing.T, br *bufio.Reader, c net.Conn, n int) string {
	t.Helper()
	buf := make([]byte, n)
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return string(buf)
}

func readLine(t *testing.T, c net.Conn) string {
	t.Helper()
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	br := bufio.NewReader(c)
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read line: %v", err)
	}
	return strings.TrimSpace(line)
}

func TestV1ClientStream(t *testing.T) {
	addr, mgr, stop := startCaster(t)
	defer stop()

	src := connectSourceV1(t, addr, "TOKYO", "tpush")
	defer src.Close()
	waitOnline(t, mgr, "TOKYO")

	cli, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial client: %v", err)
	}
	defer cli.Close()
	io.WriteString(cli, "GET /TOKYO HTTP/1.0\r\nUser-Agent: NTRIP test\r\nAuthorization: "+basicHeader("rover1", "pw1")+"\r\n\r\n")

	br := bufio.NewReader(cli)
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	status, _ := br.ReadString('\n')
	if !strings.HasPrefix(status, "ICY 200") {
		t.Fatalf("client: expected ICY 200, got %q", status)
	}
	// drain the (empty) blank line
	br.ReadString('\n')

	// Give the client a moment to register, then push data.
	waitSubscribers(t, mgr, "TOKYO", 1)
	io.WriteString(src, "RTCM-PAYLOAD-123")

	buf := make([]byte, 16)
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := io.ReadFull(br, buf)
	if err != nil {
		t.Fatalf("read stream: %v (got %q)", err, buf[:n])
	}
	if got := string(buf[:n]); got != "RTCM-PAYLOAD-123" {
		t.Fatalf("stream payload = %q", got)
	}
}

func TestV2ClientStream(t *testing.T) {
	addr, mgr, stop := startCaster(t)
	defer stop()

	src := connectSourceV1(t, addr, "OSAKA", "opush")
	defer src.Close()
	waitOnline(t, mgr, "OSAKA")

	cli, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()
	io.WriteString(cli, "GET /OSAKA HTTP/1.1\r\nHost: x\r\nNtrip-Version: Ntrip/2.0\r\nAuthorization: "+basicHeader("rover1", "pw1")+"\r\n\r\n")

	br := bufio.NewReader(cli)
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	resp, err := http.ReadResponse(br, nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "gnss/data" {
		t.Fatalf("content-type = %q", ct)
	}

	waitSubscribers(t, mgr, "OSAKA", 1)
	io.WriteString(src, "MSM4-DATA")

	buf := make([]byte, 9)
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := io.ReadFull(resp.Body, buf) // chunked decoded by http
	if err != nil {
		t.Fatalf("read body: %v (got %q)", err, buf[:n])
	}
	if got := string(buf[:n]); got != "MSM4-DATA" {
		t.Fatalf("v2 payload = %q", got)
	}
}

func TestSourcetable(t *testing.T) {
	addr, _, stop := startCaster(t)
	defer stop()

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	io.WriteString(c, "GET / HTTP/1.0\r\n\r\n")

	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	data, _ := io.ReadAll(c)
	s := string(data)
	if !strings.HasPrefix(s, "SOURCETABLE 200") {
		t.Fatalf("expected SOURCETABLE 200, got: %q", firstLine(s))
	}
	for _, want := range []string{"STR;TOKYO;", "STR;OSAKA;", "STR;AUTO;", "ENDSOURCETABLE"} {
		if !strings.Contains(s, want) {
			t.Errorf("sourcetable missing %q", want)
		}
	}
}

func TestHandoverSelectsNearest(t *testing.T) {
	addr, mgr, stop := startCaster(t)
	defer stop()

	tokyo := connectSourceV1(t, addr, "TOKYO", "tpush")
	defer tokyo.Close()
	osaka := connectSourceV1(t, addr, "OSAKA", "opush")
	defer osaka.Close()
	waitOnline(t, mgr, "TOKYO")
	waitOnline(t, mgr, "OSAKA")

	cli, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()
	io.WriteString(cli, "GET /AUTO HTTP/1.0\r\nUser-Agent: NTRIP test\r\nAuthorization: "+basicHeader("rover1", "pw1")+"\r\n\r\n")

	br := bufio.NewReader(cli)
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	status, _ := br.ReadString('\n')
	if !strings.HasPrefix(status, "ICY 200") {
		t.Fatalf("handover: expected ICY 200, got %q", status)
	}
	br.ReadString('\n')

	// Send a GGA near Yokohama -> should attach to TOKYO.
	io.WriteString(cli, "$GPGGA,123519,3526.6,N,13938.2,E,1,08,0.9,40,M,46,M,,*00\r\n")
	waitSubscribers(t, mgr, "TOKYO", 1)

	io.WriteString(tokyo, "FROM-TOKYO")
	buf := make([]byte, 10)
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err := io.ReadFull(br, buf)
	if err != nil {
		t.Fatalf("read handover stream: %v (got %q)", err, buf[:n])
	}
	if got := string(buf[:n]); got != "FROM-TOKYO" {
		t.Fatalf("handover payload = %q, want FROM-TOKYO", got)
	}

	// Move near Kobe -> should switch to OSAKA.
	io.WriteString(cli, "$GPGGA,123519,3441.4,N,13511.7,E,1,08,0.9,40,M,46,M,,*00\r\n")
	waitSubscribers(t, mgr, "OSAKA", 1)
	if mgr.Mountpoint("TOKYO").SubscriberCount() != 0 {
		t.Fatalf("expected TOKYO to be unsubscribed after switch")
	}

	io.WriteString(osaka, "FROM-OSAKA")
	buf2 := make([]byte, 10)
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	n, err = io.ReadFull(br, buf2)
	if err != nil {
		t.Fatalf("read after switch: %v (got %q)", err, buf2[:n])
	}
	if got := string(buf2[:n]); got != "FROM-OSAKA" {
		t.Fatalf("post-switch payload = %q, want FROM-OSAKA", got)
	}
}

func TestHandoverAllOffline(t *testing.T) {
	addr, _, stop := startCaster(t)
	defer stop()

	// No member sources are connected.
	cli, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()
	io.WriteString(cli, "GET /AUTO HTTP/1.0\r\nUser-Agent: test\r\nAuthorization: "+basicHeader("rover1", "pw1")+"\r\n\r\n")

	line := readLine(t, cli)
	if !strings.HasPrefix(line, "SOURCETABLE 200") {
		t.Fatalf("all members offline: expected SOURCETABLE (fail), got %q", line)
	}
}

func TestHandoverRerouteOnSourceDrop(t *testing.T) {
	addr, mgr, stop := startCaster(t)
	defer stop()

	tokyo := connectSourceV1(t, addr, "TOKYO", "tpush")
	osaka := connectSourceV1(t, addr, "OSAKA", "opush")
	defer osaka.Close()
	waitOnline(t, mgr, "TOKYO")
	waitOnline(t, mgr, "OSAKA")

	cli, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()
	io.WriteString(cli, "GET /AUTO HTTP/1.0\r\nUser-Agent: test\r\nAuthorization: "+basicHeader("rover1", "pw1")+"\r\n\r\n")

	br := bufio.NewReader(cli)
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	status, _ := br.ReadString('\n')
	if !strings.HasPrefix(status, "ICY 200") {
		t.Fatalf("expected ICY 200, got %q", status)
	}
	br.ReadString('\n')

	// Position near Tokyo -> attach TOKYO.
	io.WriteString(cli, "$GPGGA,123519,3526.6,N,13938.2,E,1,08,0.9,40,M,46,M,,*00\r\n")
	waitSubscribers(t, mgr, "TOKYO", 1)
	io.WriteString(tokyo, "TOKYO-1")
	if got := readN(t, br, cli, 7); got != "TOKYO-1" {
		t.Fatalf("pre-drop payload = %q", got)
	}

	// Drop the TOKYO source. Without sending a new GGA, the client should be
	// re-routed to the next-nearest online member (OSAKA) and stay connected.
	tokyo.Close()
	waitSubscribers(t, mgr, "OSAKA", 1)
	io.WriteString(osaka, "OSAKA-1")
	if got := readN(t, br, cli, 7); got != "OSAKA-1" {
		t.Fatalf("post-reroute payload = %q (expected OSAKA-1)", got)
	}
}

func TestClientAuthRejected(t *testing.T) {
	addr, mgr, stop := startCaster(t)
	defer stop()
	src := connectSourceV1(t, addr, "TOKYO", "tpush")
	defer src.Close()
	waitOnline(t, mgr, "TOKYO")

	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	io.WriteString(c, "GET /TOKYO HTTP/1.0\r\nAuthorization: "+basicHeader("rover1", "wrong")+"\r\n\r\n")
	line := readLine(t, c)
	if !strings.Contains(line, "401") {
		t.Fatalf("expected 401, got %q", line)
	}
}

func TestOpenMountpointNoAuth(t *testing.T) {
	addr, mgr, stop := startCaster(t)
	defer stop()

	src := connectSourceV1(t, addr, "OPEN", "openpush")
	defer src.Close()
	waitOnline(t, mgr, "OPEN")

	// Client sends NO Authorization header.
	cli, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()
	io.WriteString(cli, "GET /OPEN HTTP/1.0\r\nUser-Agent: NTRIP test\r\n\r\n")

	br := bufio.NewReader(cli)
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	status, _ := br.ReadString('\n')
	if !strings.HasPrefix(status, "ICY 200") {
		t.Fatalf("open mountpoint: expected ICY 200 without auth, got %q", status)
	}
	br.ReadString('\n')

	waitSubscribers(t, mgr, "OPEN", 1)
	io.WriteString(src, "OPEN-DATA1")
	buf := make([]byte, 10)
	cli.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(br, buf); err != nil {
		t.Fatalf("read open stream: %v", err)
	}
	if string(buf) != "OPEN-DATA1" {
		t.Fatalf("open payload = %q", buf)
	}
}

func TestOpenMountpointSourcetableAuthFlag(t *testing.T) {
	addr, _, stop := startCaster(t)
	defer stop()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	io.WriteString(c, "GET / HTTP/1.0\r\n\r\n")
	c.SetReadDeadline(time.Now().Add(2 * time.Second))
	data, _ := io.ReadAll(c)
	s := string(data)
	// OPEN advertises authentication "N"; TOKYO advertises "B".
	if !strings.Contains(s, "STR;OPEN;") || !strings.Contains(s, ";N;N;0;") {
		t.Errorf("expected OPEN STR with auth=N, got:\n%s", s)
	}
}

func TestSourceBadPassword(t *testing.T) {
	addr, _, stop := startCaster(t)
	defer stop()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer c.Close()
	io.WriteString(c, "SOURCE wrongpw /TOKYO\r\n\r\n")
	line := readLine(t, c)
	if !strings.Contains(line, "Bad Password") {
		t.Fatalf("expected Bad Password, got %q", line)
	}
}

// helpers

func waitOnline(t *testing.T, mgr *caster.Manager, name string) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if mgr.Online(name) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("mountpoint %s never came online", name)
}

func waitSubscribers(t *testing.T, mgr *caster.Manager, name string, n int) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if mp := mgr.Mountpoint(name); mp != nil && mp.SubscriberCount() == n {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	got := -1
	if mp := mgr.Mountpoint(name); mp != nil {
		got = mp.SubscriberCount()
	}
	t.Fatalf("mountpoint %s subscribers = %d, want %d", name, got, n)
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
