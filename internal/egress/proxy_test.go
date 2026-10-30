package egress

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"
)

// staticResolver maps every name to fixed addresses, like a rebinding DNS server would.
type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	if a, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{a}, nil
	}
	if ips, ok := r[host]; ok {
		return ips, nil
	}
	return nil, fmt.Errorf("no such host %s", host)
}

func newTestProxy(t *testing.T, allow []string, res Resolver) *httptest.Server {
	t.Helper()
	f, err := NewFilter(allow, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	p := New(Options{Filter: f, Resolver: res, DialTimeout: 2 * time.Second, MaxPerClient: 3, IdleTimeout: 300 * time.Millisecond})
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return srv
}

func proxyClient(proxyURL string) *http.Client {
	u, _ := url.Parse(proxyURL)
	return &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(u)}, Timeout: 5 * time.Second}
}

func TestProxyDeniesLoopbackAndRebinding(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "secret") }))
	defer target.Close()
	_, port, _ := net.SplitHostPort(target.Listener.Addr().String())

	res := staticResolver{"rebind.example": {netip.MustParseAddr("127.0.0.1")}}
	proxy := newTestProxy(t, nil, res)
	c := proxyClient(proxy.URL)

	for _, host := range []string{"127.0.0.1", "rebind.example", "[::1]"} {
		resp, err := c.Get("http://" + host + ":" + port + "/")
		if err != nil {
			t.Fatalf("%s: %v", host, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusForbidden || strings.Contains(string(body), "secret") {
			t.Errorf("%s: status %d body %q, want 403", host, resp.StatusCode, body)
		}
	}
}

func TestProxyConnectDenied(t *testing.T) {
	proxy := newTestProxy(t, nil, staticResolver{"lan.example": {netip.MustParseAddr("192.168.1.1")}})
	for _, target := range []string{"lan.example:443", "10.0.0.1:22", "169.254.169.254:80"} {
		conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
		resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("CONNECT %s: status %d, want 403", target, resp.StatusCode)
		}
	}
}

func TestProxyAllowsListedTargets(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "" {
			t.Error("proxy credentials leaked upstream")
		}
		io.WriteString(w, "hello")
	}))
	defer target.Close()
	// Allow loopback explicitly to exercise the success path locally.
	proxy := newTestProxy(t, []string{"127.0.0.1/32"}, staticResolver{})

	resp, err := proxyClient(proxy.URL).Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 || string(body) != "hello" {
		t.Fatalf("got %d %q", resp.StatusCode, body)
	}

	// CONNECT tunnel round trip
	echo, _ := net.Listen("tcp", "127.0.0.1:0")
	defer echo.Close()
	go func() {
		c, err := echo.Accept()
		if err == nil {
			io.Copy(c, c)
			c.Close()
		}
	}()
	conn, err := net.Dial("tcp", proxy.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	addr := echo.Addr().String()
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\nping", addr, addr)
	br := bufio.NewReader(conn)
	resp, err = http.ReadResponse(br, nil)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("CONNECT failed: %v %v", resp, err)
	}
	buf := make([]byte, 4)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(br, buf); err != nil || string(buf) != "ping" {
		t.Fatalf("tunnel echo = %q, %v", buf, err)
	}
}

func TestProxyRejectsOriginRequests(t *testing.T) {
	proxy := newTestProxy(t, nil, staticResolver{})
	resp, err := http.Get(proxy.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d", resp.StatusCode)
	}
}

// openTunnel opens a CONNECT tunnel to addr and returns the connection.
func openTunnel(t *testing.T, proxyAddr, addr string) (net.Conn, int) {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", addr, addr)
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	return conn, resp.StatusCode
}

func TestProxyLimitsConnectionsPerBox(t *testing.T) {
	sink, _ := net.Listen("tcp", "127.0.0.1:0")
	defer sink.Close()
	go func() {
		for {
			c, err := sink.Accept()
			if err != nil {
				return
			}
			go io.Copy(io.Discard, c)
		}
	}()
	proxy := newTestProxy(t, []string{"127.0.0.1/32"}, staticResolver{})
	var open []net.Conn
	for range 3 {
		c, code := openTunnel(t, proxy.Listener.Addr().String(), sink.Addr().String())
		if code != 200 {
			t.Fatalf("tunnel refused early: %d", code)
		}
		open = append(open, c)
	}
	c, code := openTunnel(t, proxy.Listener.Addr().String(), sink.Addr().String())
	c.Close()
	if code != http.StatusServiceUnavailable {
		t.Fatalf("4th tunnel from one box: %d, want 503", code)
	}
	for _, c := range open {
		c.Close()
	}
}

func TestProxyClosesIdleTunnels(t *testing.T) {
	sink, _ := net.Listen("tcp", "127.0.0.1:0")
	defer sink.Close()
	go func() {
		c, err := sink.Accept()
		if err == nil {
			io.Copy(io.Discard, c)
		}
	}()
	proxy := newTestProxy(t, []string{"127.0.0.1/32"}, staticResolver{})
	conn, code := openTunnel(t, proxy.Listener.Addr().String(), sink.Addr().String())
	if code != 200 {
		t.Fatalf("tunnel: %d", code)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	_, err := conn.Read(make([]byte, 1))
	if err == nil || time.Since(start) > 2*time.Second {
		t.Fatalf("idle tunnel not closed by the proxy (err=%v after %v)", err, time.Since(start))
	}
}
