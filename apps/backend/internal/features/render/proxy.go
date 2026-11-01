package render

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Resolver resolves host names. *net.Resolver satisfies it; tests inject fakes.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// DialFunc dials a vetted "ip:port" address. Tests inject fakes.
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// ProxyConfig configures the egress proxy.
type ProxyConfig struct {
	// Allow reports whether a host name (lowercase, no port, no trailing dot) may be tunnelled to.
	Allow func(host string) bool
	// Resolver defaults to net.DefaultResolver.
	Resolver Resolver
	// Dial defaults to a net.Dialer that re-checks the address at connect time.
	Dial DialFunc
	// MaxConns caps concurrent client connections (default 128).
	MaxConns int
	// TunnelTimeout caps the lifetime of one tunnel (default 2 min).
	TunnelTimeout time.Duration
	Logger        *slog.Logger
}

// Proxy is the egress proxy Chrome is pointed at with --proxy-server. It only tunnels CONNECT to
// allowlisted hosts on port 443, resolves DNS itself, refuses non-public addresses and dials the vetted
// IP (no second resolution, so no DNS rebinding). Plain-HTTP proxy requests are refused.
type Proxy struct {
	cfg ProxyConfig
	ln  net.Listener
	sem chan struct{}
	log *slog.Logger

	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup

	refused atomic.Int64
}

// StartProxy listens on 127.0.0.1 on a random port and serves until Close.
func StartProxy(cfg ProxyConfig) (*Proxy, error) {
	if cfg.Allow == nil {
		return nil, errors.New("render: proxy needs an Allow func")
	}
	if cfg.Resolver == nil {
		cfg.Resolver = net.DefaultResolver
	}
	if cfg.Dial == nil {
		d := &net.Dialer{Timeout: 10 * time.Second, Control: dialControl}
		cfg.Dial = d.DialContext
	}
	if cfg.MaxConns <= 0 {
		cfg.MaxConns = 128
	}
	if cfg.TunnelTimeout <= 0 {
		cfg.TunnelTimeout = 2 * time.Minute
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("render: proxy listen: %w", err)
	}
	p := &Proxy{
		cfg:   cfg,
		ln:    ln,
		sem:   make(chan struct{}, cfg.MaxConns),
		log:   cfg.Logger.With("component", "render-proxy"),
		conns: map[net.Conn]struct{}{},
	}
	p.wg.Add(1)
	go p.serve()
	return p, nil
}

// Addr is the listen address, "127.0.0.1:port".
func (p *Proxy) Addr() string { return p.ln.Addr().String() }

// URL is the value for Chrome's --proxy-server.
func (p *Proxy) URL() string { return "http://" + p.Addr() }

// Refused counts refused proxy requests (for tests and diagnostics).
func (p *Proxy) Refused() int64 { return p.refused.Load() }

// Close stops listening and closes every open connection.
func (p *Proxy) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	err := p.ln.Close()
	for c := range p.conns {
		_ = c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
	return err
}

func (p *Proxy) track(c net.Conn, add bool) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if add {
		if p.closed {
			return false
		}
		p.conns[c] = struct{}{}
	} else {
		delete(p.conns, c)
	}
	return true
}

func (p *Proxy) serve() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			p.log.Warn("accept failed", "err", err)
			time.Sleep(50 * time.Millisecond)
			continue
		}
		select {
		case p.sem <- struct{}{}:
		default:
			p.refused.Add(1)
			_ = c.Close()
			continue
		}
		if !p.track(c, true) {
			_ = c.Close()
			<-p.sem
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer func() { <-p.sem }()
			defer p.track(c, false)
			defer c.Close()
			p.handle(c)
		}()
	}
}

func (p *Proxy) refuse(c net.Conn, status int, reason string, attrs ...any) {
	p.refused.Add(1)
	p.log.Info("refused", append([]any{"status", status, "reason", reason}, attrs...)...)
	_, _ = fmt.Fprintf(c, "HTTP/1.1 %d %s\r\nContent-Length: 0\r\nConnection: close\r\n\r\n", status, http.StatusText(status))
}

func (p *Proxy) handle(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(15 * time.Second))
	br := bufio.NewReaderSize(c, 4096)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		p.refuse(c, http.StatusMethodNotAllowed, "plain http proxying is not allowed", "url", truncate(req.RequestURI, 200))
		return
	}
	target := req.RequestURI
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		p.refuse(c, http.StatusBadRequest, "bad CONNECT target", "target", truncate(target, 200))
		return
	}
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	if port != "443" {
		p.refuse(c, http.StatusForbidden, "port not allowed", "target", truncate(target, 200))
		return
	}
	if !p.cfg.Allow(host) {
		p.refuse(c, http.StatusForbidden, "host not allowed", "host", truncate(host, 200))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ips, err := p.vettedIPs(ctx, host)
	if err != nil {
		p.refuse(c, http.StatusForbidden, err.Error(), "host", host)
		return
	}
	var upstream net.Conn
	for _, ip := range ips {
		// Dial the vetted IP literal: nothing is resolved again after the check.
		upstream, err = p.cfg.Dial(ctx, "tcp", netip.AddrPortFrom(ip, 443).String())
		if err == nil {
			break
		}
	}
	if upstream == nil {
		p.refused.Add(1)
		p.log.Info("upstream dial failed", "host", host, "err", err)
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\nConnection: close\r\n\r\n")
		return
	}
	defer upstream.Close()
	if !p.track(upstream, true) {
		return
	}
	defer p.track(upstream, false)

	deadline := time.Now().Add(p.cfg.TunnelTimeout)
	_ = c.SetDeadline(deadline)
	_ = upstream.SetDeadline(deadline)
	if _, err := io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() {
		// br may already hold the start of the TLS handshake.
		_, _ = io.Copy(upstream, br)
		closeWrite(upstream)
		done <- struct{}{}
	}()
	go func() {
		_, _ = io.Copy(c, upstream)
		closeWrite(c)
		done <- struct{}{}
	}()
	<-done
	<-done
}

func closeWrite(c net.Conn) {
	if cw, ok := c.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
		return
	}
	_ = c.Close()
}

var errNoPublicAddr = errors.New("host has no public address")

// vettedIPs resolves host once and keeps only public addresses.
func (p *Proxy) vettedIPs(ctx context.Context, host string) ([]netip.Addr, error) {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !IsPublicAddr(ip) {
			return nil, errNoPublicAddr
		}
		return []netip.Addr{ip.Unmap()}, nil
	}
	addrs, err := p.cfg.Resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve: %w", err)
	}
	var out []netip.Addr
	for _, a := range addrs {
		if IsPublicAddr(a) {
			out = append(out, a.Unmap())
		}
	}
	if len(out) == 0 {
		return nil, errNoPublicAddr
	}
	return out, nil
}

// dialControl re-checks the socket address right before connect (belt and braces for the default dialer).
func dialControl(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("render proxy: refusing non-literal address %q", address)
	}
	if !IsPublicAddr(ap.Addr()) {
		return fmt.Errorf("render proxy: refusing non-public address %s", ap.Addr())
	}
	return nil
}

var nonPublicPrefixes = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // "this" network
		"100.64.0.0/10",   // CGNAT shared address space
		"192.0.0.0/24",    // IETF protocol assignments
		"192.0.2.0/24",    // TEST-NET-1
		"192.88.99.0/24",  // 6to4 relay anycast
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // TEST-NET-2
		"203.0.113.0/24",  // TEST-NET-3
		"240.0.0.0/4",     // reserved + broadcast
		"::/96",           // IPv4-compatible (deprecated)
		"64:ff9b::/96",    // NAT64 (could reach internal IPv4)
		"64:ff9b:1::/48",  // local-use NAT64
		"100::/64",        // discard-only
		"2001::/23",       // IETF protocol assignments (incl. Teredo)
		"2001:db8::/32",   // documentation
		"2002::/16",       // 6to4 (embeds IPv4)
		"3fff::/20",       // documentation
		"fec0::/10",       // site-local (deprecated)
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// IsPublicAddr reports whether a is a globally routable unicast address: not loopback, private (RFC 1918,
// IPv6 ULA), link-local (incl. 169.254.169.254 metadata), CGNAT, multicast, unspecified or otherwise
// reserved. IPv4-mapped IPv6 addresses are judged by their IPv4 address.
func IsPublicAddr(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.Zone() != "" {
		return false
	}
	if a.IsUnspecified() || a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() ||
		a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() || a.IsMulticast() || !a.IsGlobalUnicast() {
		return false
	}
	for _, p := range nonPublicPrefixes {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
