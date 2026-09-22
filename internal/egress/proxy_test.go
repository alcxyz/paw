package egress

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseDestinationsValid(t *testing.T) {
	input := `
# comment line
example.com approved-provider-api

api.example.com. selected-git-read
  Api2.Example.com   approved-provider-api
`
	got, err := ParseDestinations(strings.NewReader(input))
	if err != nil {
		t.Fatalf("ParseDestinations: %v", err)
	}
	want := []Destination{
		{Host: "example.com", Purpose: "approved-provider-api"},
		{Host: "api.example.com", Purpose: "selected-git-read"},
		{Host: "api2.example.com", Purpose: "approved-provider-api"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d destinations, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("destination %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseDestinationsRejections(t *testing.T) {
	for _, test := range []struct {
		name  string
		input string
	}{
		{"missing purpose", "example.com\n"},
		{"too many fields", "example.com a b\n"},
		{"single label host", "localhost approved-provider-api\n"},
		{"invalid label chars", "ex_ample.com approved-provider-api\n"},
		{"leading hyphen", "-example.com approved-provider-api\n"},
		{"trailing hyphen", "example-.com approved-provider-api\n"},
		{"ipv4 literal", "10.0.0.1 approved-provider-api\n"},
		{"ipv6 literal", "::1 approved-provider-api\n"},
		{"invalid purpose", "example.com Not_Valid\n"},
		{"duplicate host", "example.com approved-provider-api\nexample.com selected-git-read\n"},
		{"duplicate host after normalization", "Example.com. approved-provider-api\nexample.com selected-git-read\n"},
		{"label too long", strings.Repeat("a", 64) + ".com approved-provider-api\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseDestinations(strings.NewReader(test.input))
			if err == nil {
				t.Fatalf("expected error for input %q", test.input)
			}
		})
	}
}

func TestParseDestinationsEmptyPurposeIsAccepted(t *testing.T) {
	// "-" is a valid purpose token under [a-z0-9-]+, sanity check it is not
	// rejected as "empty".
	got, err := ParseDestinations(strings.NewReader("example.com -\n"))
	if err != nil {
		t.Fatalf("ParseDestinations: %v", err)
	}
	if len(got) != 1 || got[0].Purpose != "-" {
		t.Fatalf("got %+v", got)
	}
}

func TestLoadDestinationsMissingFile(t *testing.T) {
	_, err := LoadDestinations("/nonexistent/path/does-not-exist")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

func TestAddressAllowed(t *testing.T) {
	deny := []string{
		"0.1.2.3",
		"10.1.2.3",
		"100.64.1.2",
		"127.0.0.1",
		"169.254.1.1",
		"172.16.1.1",
		"172.31.255.255",
		"192.0.0.1",
		"192.0.2.1",
		"192.168.1.1",
		"198.18.0.1",
		"198.19.255.255",
		"198.51.100.1",
		"203.0.113.1",
		"224.0.0.1",
		"240.0.0.1",
		"255.255.255.255",
		"::",
		"::1",
		"::1.2.3.4",
		"::ffff:10.0.0.1",
		"64:ff9b::1",
		"100::1",
		"2001:db8::1",
		"fc00::1",
		"fd00::1",
		"fe80::1",
		"ff00::1",
	}
	for _, s := range deny {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		if AddressAllowed(ip) {
			t.Errorf("AddressAllowed(%s) = true, want false", s)
		}
	}

	allow := []string{
		"8.8.8.8",
		"1.1.1.1",
		"172.15.255.255",
		"172.32.0.0",
		"192.167.255.255",
		"192.169.0.0",
		"198.17.255.255",
		"198.20.0.0",
		"2606:4700:4700::1111", // public IPv6 (Cloudflare)
		"2001:4860:4860::8888", // public IPv6 (Google)
	}
	for _, s := range allow {
		ip := net.ParseIP(s)
		if ip == nil {
			t.Fatalf("bad test IP %q", s)
		}
		if !AddressAllowed(ip) {
			t.Errorf("AddressAllowed(%s) = false, want true", s)
		}
	}
}

func TestAddressAllowedIPv4Mapped(t *testing.T) {
	// IPv4-mapped IPv6 addresses must be unmapped and tested under IPv4 rules.
	denied := net.ParseIP("::ffff:10.0.0.5")
	if AddressAllowed(denied) {
		t.Errorf("AddressAllowed(::ffff:10.0.0.5) = true, want false")
	}
	allowed := net.ParseIP("::ffff:8.8.8.8")
	if !AddressAllowed(allowed) {
		t.Errorf("AddressAllowed(::ffff:8.8.8.8) = false, want true")
	}
}

// fakeResolver is a Resolver test double.
type fakeResolver struct {
	addrs map[string][]net.IPAddr
	err   map[string]error
}

func (f *fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	if err, ok := f.err[host]; ok {
		return nil, err
	}
	return f.addrs[host], nil
}

// fakeDialer is a Dialer test double that either returns one end of a
// net.Pipe (keeping the other end for the test to assert on) or fails.
type fakeDialer struct {
	mu    sync.Mutex
	fail  bool
	conns []net.Conn // server-side ends of pipes handed out
}

func (f *fakeDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if f.fail {
		return nil, errors.New("dial failed")
	}
	client, server := net.Pipe()
	f.mu.Lock()
	f.conns = append(f.conns, server)
	f.mu.Unlock()
	return client, nil
}

func ipAddrs(ips ...string) []net.IPAddr {
	out := make([]net.IPAddr, len(ips))
	for i, s := range ips {
		out[i] = net.IPAddr{IP: net.ParseIP(s)}
	}
	return out
}

// testServer wraps an httptest-style real listener so hijacking works.
type testServer struct {
	ln     net.Listener
	server *http.Server
	url    string
}

func startServer(t *testing.T, handler http.Handler) *testServer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(ln)
	t.Cleanup(func() {
		srv.Close()
	})
	return &testServer{ln: ln, server: srv, url: ln.Addr().String()}
}

// connect sends a raw CONNECT request over a fresh TCP connection to the
// test server and returns the connection plus the status line.
func connectRaw(t *testing.T, addr, target string) (net.Conn, string) {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial test server: %v", err)
	}
	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read status line: %v", err)
	}
	// Drain headers until blank line.
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("read header line: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	return conn, status
}

func TestHandlerRejectsNonConnect(t *testing.T) {
	var events []Event
	h := NewHandler(Config{Log: func(e Event) { events = append(events, e) }})
	srv := startServer(t, h)

	resp, err := http.Get("http://" + srv.url + "/")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want 405", resp.StatusCode)
	}
	if len(events) != 1 || events[0].Outcome != "denied-method" {
		t.Errorf("events = %+v", events)
	}
}

func TestHandlerDeniesNonStandardPort(t *testing.T) {
	var events []Event
	dest := []Destination{{Host: "example.com", Purpose: "approved-provider-api"}}
	h := NewHandler(Config{Destinations: dest, Log: func(e Event) { events = append(events, e) }})
	srv := startServer(t, h)

	conn, status := connectRaw(t, srv.url, "example.com:80")
	defer conn.Close()
	if !strings.Contains(status, "403") {
		t.Errorf("status = %q, want 403", status)
	}
	if len(events) != 1 || events[0].Outcome != "denied-port" || events[0].Port != 80 {
		t.Errorf("events = %+v", events)
	}
}

func TestHandlerDeniesUnlistedHost(t *testing.T) {
	var events []Event
	h := NewHandler(Config{Log: func(e Event) { events = append(events, e) }})
	srv := startServer(t, h)

	conn, status := connectRaw(t, srv.url, "unlisted.example.com:443")
	defer conn.Close()
	if !strings.Contains(status, "403") {
		t.Errorf("status = %q, want 403", status)
	}
	if len(events) != 1 || events[0].Outcome != "denied-host" || events[0].Host != "unlisted.example.com" {
		t.Errorf("events = %+v", events)
	}
}

func TestHandlerDeniesIPLiteral(t *testing.T) {
	var events []Event
	h := NewHandler(Config{Log: func(e Event) { events = append(events, e) }})
	srv := startServer(t, h)

	conn, status := connectRaw(t, srv.url, "93.184.216.34:443")
	defer conn.Close()
	if !strings.Contains(status, "403") {
		t.Errorf("status = %q, want 403", status)
	}
	if len(events) != 1 || events[0].Outcome != "denied-host" {
		t.Errorf("events = %+v", events)
	}
}

func TestHandlerDeniesPrivateAddress(t *testing.T) {
	var events []Event
	dest := []Destination{{Host: "example.com", Purpose: "approved-provider-api"}}
	resolver := &fakeResolver{addrs: map[string][]net.IPAddr{
		"example.com": ipAddrs("10.0.0.5"),
	}}
	h := NewHandler(Config{Destinations: dest, Resolver: resolver, Log: func(e Event) { events = append(events, e) }})
	srv := startServer(t, h)

	conn, status := connectRaw(t, srv.url, "example.com:443")
	defer conn.Close()
	if !strings.Contains(status, "403") {
		t.Errorf("status = %q, want 403", status)
	}
	if len(events) != 1 || events[0].Outcome != "denied-address" || events[0].Purpose != "approved-provider-api" {
		t.Errorf("events = %+v", events)
	}
}

func TestHandlerDeniesIfAnyResolvedAddressIsPrivate(t *testing.T) {
	var events []Event
	dest := []Destination{{Host: "example.com", Purpose: "approved-provider-api"}}
	resolver := &fakeResolver{addrs: map[string][]net.IPAddr{
		"example.com": ipAddrs("8.8.8.8", "10.0.0.5"),
	}}
	h := NewHandler(Config{Destinations: dest, Resolver: resolver, Log: func(e Event) { events = append(events, e) }})
	srv := startServer(t, h)

	conn, status := connectRaw(t, srv.url, "example.com:443")
	defer conn.Close()
	if !strings.Contains(status, "403") {
		t.Errorf("status = %q, want 403", status)
	}
	if len(events) != 1 || events[0].Outcome != "denied-address" {
		t.Errorf("events = %+v", events)
	}
}

func TestHandlerResolutionFailure(t *testing.T) {
	var events []Event
	dest := []Destination{{Host: "example.com", Purpose: "approved-provider-api"}}
	resolver := &fakeResolver{err: map[string]error{
		"example.com": errors.New("no such host"),
	}}
	h := NewHandler(Config{Destinations: dest, Resolver: resolver, Log: func(e Event) { events = append(events, e) }})
	srv := startServer(t, h)

	conn, status := connectRaw(t, srv.url, "example.com:443")
	defer conn.Close()
	if !strings.Contains(status, "502") {
		t.Errorf("status = %q, want 502", status)
	}
	if len(events) != 1 || events[0].Outcome != "denied-resolve" {
		t.Errorf("events = %+v", events)
	}
}

func TestHandlerDialFailure(t *testing.T) {
	var events []Event
	dest := []Destination{{Host: "example.com", Purpose: "approved-provider-api"}}
	resolver := &fakeResolver{addrs: map[string][]net.IPAddr{
		"example.com": ipAddrs("8.8.8.8"),
	}}
	dialer := &fakeDialer{fail: true}
	h := NewHandler(Config{Destinations: dest, Resolver: resolver, Dialer: dialer, Log: func(e Event) { events = append(events, e) }})
	srv := startServer(t, h)

	conn, status := connectRaw(t, srv.url, "example.com:443")
	defer conn.Close()
	if !strings.Contains(status, "502") {
		t.Errorf("status = %q, want 502", status)
	}
	if len(events) != 1 || events[0].Outcome != "denied-dial" {
		t.Errorf("events = %+v", events)
	}
}

func TestHandlerSuccessTunnelsBytes(t *testing.T) {
	var mu sync.Mutex
	var events []Event
	dest := []Destination{{Host: "example.com", Purpose: "approved-provider-api"}}
	resolver := &fakeResolver{addrs: map[string][]net.IPAddr{
		"example.com": ipAddrs("8.8.8.8"),
	}}
	dialer := &fakeDialer{}
	h := NewHandler(Config{Destinations: dest, Resolver: resolver, Dialer: dialer, Log: func(e Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	}})
	srv := startServer(t, h)

	conn, status := connectRaw(t, srv.url, "example.com:443")
	defer conn.Close()
	if !strings.Contains(status, "200") {
		t.Fatalf("status = %q, want 200", status)
	}

	// Wait for the dialer to have handed out the server-side pipe end.
	var serverConn net.Conn
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		dialer.mu.Lock()
		if len(dialer.conns) == 1 {
			serverConn = dialer.conns[0]
		}
		dialer.mu.Unlock()
		if serverConn != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if serverConn == nil {
		t.Fatal("dialer never received a connection")
	}

	// Client -> upstream.
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write client->upstream: %v", err)
	}
	buf := make([]byte, 4)
	serverConn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(serverConn, buf); err != nil {
		t.Fatalf("read on upstream side: %v", err)
	}
	if string(buf) != "ping" {
		t.Errorf("upstream got %q, want %q", buf, "ping")
	}

	// Upstream -> client.
	if _, err := serverConn.Write([]byte("pong")); err != nil {
		t.Fatalf("write upstream->client: %v", err)
	}
	buf2 := make([]byte, 4)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf2); err != nil {
		t.Fatalf("read on client side: %v", err)
	}
	if string(buf2) != "pong" {
		t.Errorf("client got %q, want %q", buf2, "pong")
	}

	mu.Lock()
	defer mu.Unlock()
	if len(events) != 1 || events[0].Outcome != "allowed" || events[0].Purpose != "approved-provider-api" || events[0].Address != "8.8.8.8" {
		t.Errorf("events = %+v", events)
	}
}

func TestHandlerCapacityLimit(t *testing.T) {
	var mu sync.Mutex
	var events []Event
	dest := []Destination{{Host: "example.com", Purpose: "approved-provider-api"}}
	resolver := &fakeResolver{addrs: map[string][]net.IPAddr{
		"example.com": ipAddrs("8.8.8.8"),
	}}
	dialer := &fakeDialer{}
	h := NewHandler(Config{
		Destinations:   dest,
		Resolver:       resolver,
		Dialer:         dialer,
		MaxConnections: 1,
		Log: func(e Event) {
			mu.Lock()
			events = append(events, e)
			mu.Unlock()
		},
	})
	srv := startServer(t, h)

	// Hold the first tunnel open.
	conn1, status1 := connectRaw(t, srv.url, "example.com:443")
	defer conn1.Close()
	if !strings.Contains(status1, "200") {
		t.Fatalf("first status = %q, want 200", status1)
	}

	// Give the handler goroutine time to acquire the semaphore slot before
	// the second attempt.
	time.Sleep(50 * time.Millisecond)

	conn2, status2 := connectRaw(t, srv.url, "example.com:443")
	defer conn2.Close()
	if !strings.Contains(status2, "503") {
		t.Errorf("second status = %q, want 503", status2)
	}

	mu.Lock()
	defer mu.Unlock()
	var sawCapacity bool
	for _, e := range events {
		if e.Outcome == "denied-capacity" {
			sawCapacity = true
		}
	}
	if !sawCapacity {
		t.Errorf("events = %+v, want a denied-capacity event", events)
	}
}

func TestHandlerIdleTimeoutClosesTunnel(t *testing.T) {
	dest := []Destination{{Host: "example.com", Purpose: "approved-provider-api"}}
	resolver := &fakeResolver{addrs: map[string][]net.IPAddr{
		"example.com": ipAddrs("8.8.8.8"),
	}}
	dialer := &fakeDialer{}
	h := NewHandler(Config{
		Destinations: dest,
		Resolver:     resolver,
		Dialer:       dialer,
		IdleTimeout:  100 * time.Millisecond,
	})
	srv := startServer(t, h)

	conn, status := connectRaw(t, srv.url, "example.com:443")
	defer conn.Close()
	if !strings.Contains(status, "200") {
		t.Fatalf("status = %q, want 200", status)
	}

	// No traffic sent; the tunnel should close itself after the idle
	// timeout. Detect this as a read returning EOF/error.
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 1)
	_, err := conn.Read(buf)
	if err == nil {
		t.Fatalf("expected client connection to be closed after idle timeout")
	}
}
