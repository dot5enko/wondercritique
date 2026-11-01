package render

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestIsPublicAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		// Public unicast.
		{"8.8.8.8", true},
		{"1.1.1.1", true},
		{"93.184.216.34", true},
		{"2606:4700:4700::1111", true},
		{"2001:4860:4860::8888", true},
		{"::ffff:8.8.8.8", true}, // IPv4-mapped, judged by the IPv4 address
		// Loopback.
		{"127.0.0.1", false},
		{"127.255.255.254", false},
		{"::1", false},
		// RFC1918 + IPv6 ULA.
		{"10.0.0.1", false},
		{"172.16.0.1", false},
		{"192.168.1.1", false},
		{"fc00::1", false},
		{"fd12:3456::1", false},
		// Link-local, incl. the cloud metadata address.
		{"169.254.169.254", false},
		{"fe80::1", false},
		{"fe80::1%en0", false},
		// CGNAT shared space.
		{"100.64.0.1", false},
		{"100.127.255.255", false},
		// Multicast / unspecified / broadcast / "this network".
		{"224.0.0.1", false},
		{"ff02::1", false},
		{"0.0.0.0", false},
		{"0.1.2.3", false},
		{"::", false},
		{"255.255.255.255", false},
		{"240.0.0.1", false},
		// IPv4-mapped private.
		{"::ffff:10.0.0.1", false},
		{"::ffff:169.254.169.254", false},
		// NAT64 (could reach internal IPv4), 6to4, Teredo, documentation.
		{"64:ff9b::a00:1", false},
		{"2001::1", false},
		{"2002::1", false},
		{"192.0.2.1", false},
		{"198.51.100.1", false},
		{"203.0.113.1", false},
		{"2001:db8::1", false},
		// Benchmarking + 6to4 relay anycast.
		{"198.18.0.1", false},
		{"192.88.99.1", false},
	}
	for _, tc := range cases {
		addr, err := netip.ParseAddr(tc.addr)
		if err != nil {
			t.Fatalf("parse %q: %v", tc.addr, err)
		}
		if got := IsPublicAddr(addr); got != tc.want {
			t.Errorf("IsPublicAddr(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

// fakeResolver returns fixed addresses and counts lookups.
type fakeResolver struct {
	mu    sync.Mutex
	addrs []netip.Addr
	calls int
}

func (r *fakeResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.addrs, nil
}

func (r *fakeResolver) callCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// newTestProxy starts a proxy with an injected resolver and dialer.
func newTestProxy(t *testing.T, allow func(string) bool, resolver Resolver, dial DialFunc) *Proxy {
	t.Helper()
	p, err := StartProxy(ProxyConfig{Allow: allow, Resolver: resolver, Dial: dial})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.Close() })
	return p
}

// proxyDo sends one request to the proxy and returns the status line plus the
// connection (for CONNECT tunnels that succeeded).
func proxyDo(t *testing.T, addr, request string) (string, net.Conn) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 3*time.Second)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := conn.Write([]byte(request)); err != nil {
		t.Fatalf("write: %v", err)
	}
	br := bufio.NewReader(conn)
	status, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("read status: %v", err)
	}
	// Drain headers.
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("read header: %v", err)
		}
		if line == "\r\n" {
			break
		}
	}
	if br.Buffered() > 0 {
		// A tunnel may already carry bytes; keep them for the caller by
		// prepending them to the connection via a peeking wrapper.
		peek, _ := br.Peek(br.Buffered())
		return strings.TrimSpace(status), &prefixConn{Conn: conn, prefix: append([]byte(nil), peek...), br: br}
	}
	return strings.TrimSpace(status), conn
}

type prefixConn struct {
	net.Conn
	prefix []byte
	br     *bufio.Reader
}

func (c *prefixConn) Read(b []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(b, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	// After the prefix is drained, read through the buffered reader so no
	// bytes are lost.
	return c.br.Read(b)
}

func TestProxyRefusesNonPublicResolution(t *testing.T) {
	allowAll := func(string) bool { return true }
	cases := []struct {
		name string
		addr string
	}{
		{"loopback", "127.0.0.1"},
		{"metadata", "169.254.169.254"},
		{"rfc1918", "10.1.2.3"},
		{"cgnat", "100.64.9.9"},
		{"ipv6-ula", "fc00::1"},
		{"ipv6-loopback", "::1"},
		{"ipv4-mapped-link-local", "::ffff:169.254.169.254"},
		{"ipv4-mapped-private", "::ffff:10.0.0.1"},
		{"natt64", "64:ff9b::a00:1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &fakeResolver{addrs: mustAddrs(t, tc.addr)}
			dialed := 0
			p := newTestProxy(t, allowAll, resolver, func(context.Context, string, string) (net.Conn, error) {
				dialed++
				return nil, fmt.Errorf("dial must not be called")
			})
			status, _ := proxyDo(t, p.Addr(),
				"CONNECT fonts.googleapis.com:443 HTTP/1.1\r\nHost: fonts.googleapis.com:443\r\n\r\n")
			if !strings.Contains(status, "403") {
				t.Fatalf("status = %q, want 403", status)
			}
			if dialed != 0 {
				t.Fatalf("dial called %d times for a non-public address", dialed)
			}
			if resolver.callCount() != 1 {
				t.Fatalf("resolver calls = %d, want 1", resolver.callCount())
			}
		})
	}
}

func TestProxyRefusesNonAllowlisted(t *testing.T) {
	resolver := &fakeResolver{addrs: mustAddrs(t, "8.8.8.8")}
	dialed := 0
	p := newTestProxy(t,
		func(h string) bool { return h == "fonts.googleapis.com" },
		resolver,
		func(context.Context, string, string) (net.Conn, error) {
			dialed++
			return nil, fmt.Errorf("dial must not be called")
		})

	t.Run("host", func(t *testing.T) {
		status, _ := proxyDo(t, p.Addr(),
			"CONNECT evil.example:443 HTTP/1.1\r\nHost: evil.example:443\r\n\r\n")
		if !strings.Contains(status, "403") {
			t.Fatalf("status = %q, want 403", status)
		}
		if resolver.callCount() != 0 {
			t.Fatalf("resolver called for a non-allowlisted host")
		}
	})
	t.Run("port", func(t *testing.T) {
		status, _ := proxyDo(t, p.Addr(),
			"CONNECT fonts.googleapis.com:8080 HTTP/1.1\r\nHost: fonts.googleapis.com:8080\r\n\r\n")
		if !strings.Contains(status, "403") {
			t.Fatalf("status = %q, want 403", status)
		}
		if resolver.callCount() != 0 {
			t.Fatalf("resolver called for a non-443 port")
		}
	})
	if dialed != 0 {
		t.Fatalf("dial called %d times", dialed)
	}
}

func TestProxyRefusesPlainHTTP(t *testing.T) {
	resolver := &fakeResolver{addrs: mustAddrs(t, "8.8.8.8")}
	p := newTestProxy(t, func(string) bool { return true }, resolver, nil)
	status, _ := proxyDo(t, p.Addr(),
		"GET http://fonts.googleapis.com/css2 HTTP/1.1\r\nHost: fonts.googleapis.com\r\n\r\n")
	if !strings.Contains(status, "405") {
		t.Fatalf("status = %q, want 405", status)
	}
	if resolver.callCount() != 0 {
		t.Fatalf("resolver called for plain HTTP")
	}
}

func TestProxyTunnelsPublicAddress(t *testing.T) {
	resolver := &fakeResolver{addrs: mustAddrs(t, "93.184.216.34")}
	upClient, upServer := net.Pipe()
	defer upServer.Close()

	var mu sync.Mutex
	var dialedAddr string
	p := newTestProxy(t, func(string) bool { return true }, resolver,
		func(_ context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			dialedAddr = addr
			mu.Unlock()
			return upClient, nil
		})

	status, conn := proxyDo(t, p.Addr(),
		"CONNECT fonts.gstatic.com:443 HTTP/1.1\r\nHost: fonts.gstatic.com:443\r\n\r\n")
	if !strings.Contains(status, "200") {
		t.Fatalf("status = %q, want 200 Connection Established", status)
	}
	mu.Lock()
	got := dialedAddr
	mu.Unlock()
	// The dialer must receive the vetted IP literal — never a hostname, so no
	// second DNS lookup can race the check (no DNS rebinding).
	if got != "93.184.216.34:443" {
		t.Fatalf("dial addr = %q, want the vetted IP literal", got)
	}

	_ = upServer.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := upServer.Write([]byte("pong")); err != nil {
		t.Fatalf("upstream write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := readFull(conn, buf); err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(buf) != "pong" {
		t.Fatalf("client got %q", buf)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	back := make([]byte, 4)
	if _, err := readFull(upServer, back); err != nil {
		t.Fatalf("upstream read: %v", err)
	}
	if string(back) != "ping" {
		t.Fatalf("upstream got %q", back)
	}
}

func readFull(c net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := c.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func mustAddrs(t *testing.T, addrs ...string) []netip.Addr {
	t.Helper()
	var out []netip.Addr
	for _, a := range addrs {
		addr, err := netip.ParseAddr(a)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, addr)
	}
	return out
}
