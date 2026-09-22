// Package egress implements a CONNECT-only forward proxy that bounds a
// workspace's external network access to a reviewed destination list, per
// ADR-013. The proxy never terminates TLS: it tunnels raw bytes after
// admitting a request on hostname, port, and resolved address.
package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Destination is one allowed hostname and the declared purpose that admitted it.
type Destination struct {
	Host    string
	Purpose string
}

// ParseDestinations parses the destination list format: one entry per line,
// "<host> <purpose>", separated by whitespace. Blank lines and lines starting
// with '#' are ignored. Hosts are lowercased; a trailing dot is removed.
func ParseDestinations(r io.Reader) ([]Destination, error) {
	var destinations []Destination
	seen := make(map[string]bool)

	scanner := bufio.NewScanner(r)
	line := 0
	for scanner.Scan() {
		line++
		text := strings.TrimSpace(scanner.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		fields := strings.Fields(text)
		if len(fields) != 2 {
			return nil, fmt.Errorf("egress: line %d: expected \"<host> <purpose>\", got %d fields", line, len(fields))
		}

		host := strings.ToLower(fields[0])
		host = strings.TrimSuffix(host, ".")
		if !validHostname(host) {
			return nil, fmt.Errorf("egress: line %d: invalid hostname %q", line, fields[0])
		}
		if net.ParseIP(host) != nil {
			return nil, fmt.Errorf("egress: line %d: host %q is an IP address literal, not a hostname", line, fields[0])
		}

		purpose := fields[1]
		if !validPurpose(purpose) {
			return nil, fmt.Errorf("egress: line %d: invalid purpose %q", line, purpose)
		}

		if seen[host] {
			return nil, fmt.Errorf("egress: line %d: duplicate host %q", line, host)
		}
		seen[host] = true

		destinations = append(destinations, Destination{Host: host, Purpose: purpose})
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("egress: reading destinations: %w", err)
	}

	return destinations, nil
}

// LoadDestinations reads and parses a destination file. A missing, unreadable,
// or empty list is not an error for the caller's purposes of starting, but
// this function returns the error so main can log it; main then runs with an
// empty list, which denies everything.
func LoadDestinations(path string) ([]Destination, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("egress: opening destination list: %w", err)
	}
	defer f.Close()

	destinations, err := ParseDestinations(f)
	if err != nil {
		return nil, err
	}
	return destinations, nil
}

func validHostname(host string) bool {
	if host == "" || len(host) > 253 {
		return false
	}
	labels := strings.Split(host, ".")
	if len(labels) < 2 {
		return false
	}
	for _, label := range labels {
		if !validLabel(label) {
			return false
		}
	}
	return true
}

func validLabel(label string) bool {
	if len(label) == 0 || len(label) > 63 {
		return false
	}
	if label[0] == '-' || label[len(label)-1] == '-' {
		return false
	}
	for i := 0; i < len(label); i++ {
		c := label[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-':
		default:
			return false
		}
	}
	return true
}

func validPurpose(purpose string) bool {
	if purpose == "" {
		return false
	}
	for i := 0; i < len(purpose); i++ {
		c := purpose[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-':
		default:
			return false
		}
	}
	return true
}

// Resolver resolves a hostname to IP addresses.
type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

// Dialer dials an upstream address.
type Dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

// Event is a metadata-only log record. Never include headers, bodies, or credentials.
type Event struct {
	Host    string // requested host, lowercased
	Port    int
	Purpose string // purpose of the matched destination, empty if none
	Outcome string // "allowed", "denied-method", "denied-port", "denied-host", "denied-address", "denied-resolve", "denied-dial", "denied-capacity"
	Address string // the upstream IP actually dialed, when allowed
}

// Config configures the proxy handler.
type Config struct {
	Destinations []Destination
	// DialTimeout for the upstream connection (default 10s if zero).
	DialTimeout time.Duration
	// IdleTimeout: close a tunnel with no traffic in either direction for this long (default 5m if zero).
	IdleTimeout time.Duration
	// MaxConnections: max concurrent tunnels (default 256 if zero). Excess requests get 503.
	MaxConnections int
	// Resolver may be nil (net.DefaultResolver). Tests inject a fake.
	Resolver Resolver
	// Dialer may be nil. Tests inject a fake. Signature: DialContext(ctx, "tcp", "ip:port").
	Dialer Dialer
	// Log receives one Event per decision. May be nil.
	Log func(Event)
}

const (
	defaultDialTimeout    = 10 * time.Second
	defaultIdleTimeout    = 5 * time.Minute
	defaultMaxConnections = 256
	allowedPort           = 443
)

type netDialer struct {
	dialer net.Dialer
}

func (d netDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return d.dialer.DialContext(ctx, network, address)
}

type netResolver struct{}

func (netResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

type handler struct {
	byHost      map[string]string // host -> purpose
	dialTimeout time.Duration
	idleTimeout time.Duration
	resolver    Resolver
	dialer      Dialer
	log         func(Event)
	sem         chan struct{}
}

// NewHandler returns an http.Handler implementing the proxy.
func NewHandler(config Config) http.Handler {
	h := &handler{
		byHost:      make(map[string]string, len(config.Destinations)),
		dialTimeout: config.DialTimeout,
		idleTimeout: config.IdleTimeout,
		resolver:    config.Resolver,
		dialer:      config.Dialer,
		log:         config.Log,
	}
	for _, d := range config.Destinations {
		h.byHost[d.Host] = d.Purpose
	}
	if h.dialTimeout == 0 {
		h.dialTimeout = defaultDialTimeout
	}
	if h.idleTimeout == 0 {
		h.idleTimeout = defaultIdleTimeout
	}
	if h.resolver == nil {
		h.resolver = netResolver{}
	}
	if h.dialer == nil {
		h.dialer = netDialer{}
	}
	maxConnections := config.MaxConnections
	if maxConnections == 0 {
		maxConnections = defaultMaxConnections
	}
	h.sem = make(chan struct{}, maxConnections)
	return h
}

func (h *handler) emit(e Event) {
	if h.log != nil {
		h.log(e)
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		h.emit(Event{Outcome: "denied-method"})
		http.Error(w, "CONNECT only", http.StatusMethodNotAllowed)
		return
	}

	host, portStr, err := net.SplitHostPort(r.Host)
	if err != nil {
		h.emit(Event{Host: r.Host, Outcome: "denied-port"})
		http.Error(w, "port not allowed", http.StatusForbidden)
		return
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		h.emit(Event{Host: host, Outcome: "denied-port"})
		http.Error(w, "port not allowed", http.StatusForbidden)
		return
	}

	host = strings.ToLower(host)
	host = strings.TrimSuffix(host, ".")

	if port != allowedPort {
		h.emit(Event{Host: host, Port: port, Outcome: "denied-port"})
		http.Error(w, "port not allowed", http.StatusForbidden)
		return
	}

	purpose, ok := h.byHost[host]
	if !ok || net.ParseIP(host) != nil {
		h.emit(Event{Host: host, Port: port, Outcome: "denied-host"})
		http.Error(w, "destination not allowed", http.StatusForbidden)
		return
	}

	ctx := r.Context()
	addrs, err := h.resolver.LookupIPAddr(ctx, host)
	if err != nil || len(addrs) == 0 {
		h.emit(Event{Host: host, Port: port, Purpose: purpose, Outcome: "denied-resolve"})
		http.Error(w, "resolution failed", http.StatusBadGateway)
		return
	}

	var dialIP net.IP
	for _, addr := range addrs {
		if !AddressAllowed(addr.IP) {
			h.emit(Event{Host: host, Port: port, Purpose: purpose, Outcome: "denied-address"})
			http.Error(w, "destination resolves to a disallowed address", http.StatusForbidden)
			return
		}
		if dialIP == nil {
			dialIP = addr.IP
		}
	}

	select {
	case h.sem <- struct{}{}:
	default:
		h.emit(Event{Host: host, Port: port, Purpose: purpose, Outcome: "denied-capacity"})
		http.Error(w, "proxy at capacity", http.StatusServiceUnavailable)
		return
	}
	release := func() { <-h.sem }

	dialCtx, cancel := context.WithTimeout(ctx, h.dialTimeout)
	upstream, err := h.dialer.DialContext(dialCtx, "tcp", net.JoinHostPort(dialIP.String(), portStr))
	cancel()
	if err != nil {
		release()
		h.emit(Event{Host: host, Port: port, Purpose: purpose, Outcome: "denied-dial"})
		http.Error(w, "upstream connection failed", http.StatusBadGateway)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		release()
		upstream.Close()
		http.Error(w, "hijack not supported", http.StatusInternalServerError)
		return
	}
	clientConn, clientBuf, err := hijacker.Hijack()
	if err != nil {
		release()
		upstream.Close()
		return
	}

	if _, err := clientConn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n")); err != nil {
		release()
		clientConn.Close()
		upstream.Close()
		return
	}

	h.emit(Event{Host: host, Port: port, Purpose: purpose, Outcome: "allowed", Address: dialIP.String()})

	go func() {
		defer release()
		tunnel(clientConn, clientBuf, upstream, h.idleTimeout)
	}()
}

// tunnel copies bytes in both directions between client and upstream until
// either side closes or idleTimeout elapses without traffic in either
// direction. It closes both connections before returning.
func tunnel(client net.Conn, clientBuf *bufio.ReadWriter, upstream net.Conn, idleTimeout time.Duration) {
	defer client.Close()
	defer upstream.Close()

	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())

	done := make(chan struct{})
	var closeOnce int32

	stop := func() {
		if atomic.CompareAndSwapInt32(&closeOnce, 0, 1) {
			client.Close()
			upstream.Close()
			close(done)
		}
	}

	copyDirection := func(dst net.Conn, src io.Reader) {
		buf := make([]byte, 32*1024)
		for {
			n, err := src.Read(buf)
			if n > 0 {
				lastActivity.Store(time.Now().UnixNano())
				if _, werr := dst.Write(buf[:n]); werr != nil {
					break
				}
			}
			if err != nil {
				break
			}
		}
		stop()
	}

	// Forward any bytes the client already sent that were buffered by the
	// hijacked bufio.Reader before copying the rest of the stream.
	if clientBuf != nil && clientBuf.Reader.Buffered() > 0 {
		buffered := make([]byte, clientBuf.Reader.Buffered())
		if _, err := io.ReadFull(clientBuf.Reader, buffered); err == nil {
			if _, err := upstream.Write(buffered); err != nil {
				return
			}
			lastActivity.Store(time.Now().UnixNano())
		}
	}

	go copyDirection(upstream, client)
	go copyDirection(client, upstream)

	if idleTimeout <= 0 {
		<-done
		return
	}

	ticker := time.NewTicker(idleTimeout / 4)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			last := time.Unix(0, lastActivity.Load())
			if time.Since(last) >= idleTimeout {
				stop()
				return
			}
		}
	}
}

// disallowedRange is an IP range that AddressAllowed refuses.
type disallowedRange struct {
	net *net.IPNet
}

var disallowedRanges = mustParseRanges(
	"0.0.0.0/8",
	"10.0.0.0/8",
	"100.64.0.0/10",
	"127.0.0.0/8",
	"169.254.0.0/16",
	"172.16.0.0/12",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"192.168.0.0/16",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"224.0.0.0/4",
	"240.0.0.0/4",
	"255.255.255.255/32",
	"::/96",
	"::1/128",
	"64:ff9b::/96",
	"100::/64",
	"2001:db8::/32",
	"fc00::/7",
	"fe80::/10",
	"ff00::/8",
)

func mustParseRanges(cidrs ...string) []disallowedRange {
	ranges := make([]disallowedRange, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(fmt.Sprintf("egress: invalid CIDR literal %q: %v", cidr, err))
		}
		ranges = append(ranges, disallowedRange{net: n})
	}
	return ranges
}

// AddressAllowed reports whether ip is permitted as an egress destination.
// It denies loopback, link-local, private, carrier-grade NAT, documentation,
// multicast, reserved, and cloud/relay ranges for both IPv4 and IPv6.
// IPv4-mapped IPv6 addresses are unmapped and tested against the IPv4 rules;
// deprecated IPv4-compatible addresses (::/96) are refused outright.
func AddressAllowed(ip net.IP) bool {
	if ip == nil {
		return false
	}

	if mapped := ip.To4(); mapped != nil {
		ip = mapped
	}

	for _, r := range disallowedRanges {
		if r.net.Contains(ip) {
			return false
		}
	}
	return true
}
