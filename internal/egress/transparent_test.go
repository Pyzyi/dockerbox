package egress

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strconv"
	"strings"
	"testing"
	"time"
)

func startTransparent(t *testing.T, allow []string, mode string, upstreamPort int) string {
	t.Helper()
	f, err := NewFilter(allow, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	res := staticResolver{"box.test": {netip.MustParseAddr("127.0.0.1")}, "example.com": {netip.MustParseAddr("127.0.0.1")}}
	p := New(Options{Filter: f, Resolver: res, DialTimeout: 2 * time.Second, IdleTimeout: time.Second})
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = p.ServeTransparent(ctx, l, mode, upstreamPort) }()
	return l.Addr().String()
}

func port(t *testing.T, addr string) int {
	_, ps, _ := net.SplitHostPort(addr)
	n, _ := strconv.Atoi(ps)
	return n
}

func TestTransparentTLSForwardsBySNI(t *testing.T) {
	up := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "hello "+r.TLS.ServerName)
	}))
	defer up.Close()
	get := func(addr string) (string, error) {
		// the test certificate is valid for example.com: TLS passes through untouched
		pool := up.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs
		c, err := tls.Dial("tcp", addr, &tls.Config{ServerName: "example.com", RootCAs: pool})
		if err != nil {
			return "", err
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		io.WriteString(c, "GET / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n")
		resp, err := http.ReadResponse(bufio.NewReader(c), nil)
		if err != nil {
			return "", err
		}
		b, _ := io.ReadAll(resp.Body)
		return string(b), nil
	}

	allowed := startTransparent(t, []string{"127.0.0.1"}, ModeTLS, port(t, up.Listener.Addr().String()))
	if got, err := get(allowed); err != nil || got != "hello example.com" {
		t.Fatalf("allowed: %q %v", got, err)
	}
	// loopback is private: without the allow entry the proxy must refuse
	denied := startTransparent(t, nil, ModeTLS, port(t, up.Listener.Addr().String()))
	if got, err := get(denied); err == nil {
		t.Fatalf("private destination reached via SNI: %q", got)
	}
}

func TestTransparentHTTPForwardsByHost(t *testing.T) {
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "host="+r.Host)
	}))
	defer up.Close()
	get := func(addr, host string) string {
		c, err := net.Dial("tcp", addr)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		_ = c.SetDeadline(time.Now().Add(3 * time.Second))
		io.WriteString(c, "GET / HTTP/1.1\r\nHost: "+host+"\r\nConnection: close\r\n\r\n")
		b, _ := io.ReadAll(c)
		return string(b)
	}
	allowed := startTransparent(t, []string{"127.0.0.1"}, ModeHTTP, port(t, up.Listener.Addr().String()))
	if got := get(allowed, "box.test"); !strings.HasSuffix(got, "host=box.test") {
		t.Fatalf("allowed: %q", got)
	}
	for _, host := range []string{"127.0.0.1", "", "a..b", "x/y"} {
		if got := get(allowed, host); got != "" {
			t.Fatalf("host %q must be refused, got %q", host, got)
		}
	}
	denied := startTransparent(t, nil, ModeHTTP, port(t, up.Listener.Addr().String()))
	if got := get(denied, "box.test"); got != "" {
		t.Fatalf("private destination reached via Host header: %q", got)
	}
}

func dnsQuery(id uint16, name string, qtype uint16) []byte {
	q := []byte{byte(id >> 8), byte(id), 0x01, 0x00, 0, 1, 0, 0, 0, 0, 0, 0}
	for _, l := range strings.Split(name, ".") {
		q = append(q, byte(len(l)))
		q = append(q, l...)
	}
	return append(q, 0, byte(qtype>>8), byte(qtype), 0, 1)
}

func TestDNSAnswersWithOwnAddress(t *testing.T) {
	self := netip.MustParseAddr("172.30.0.2")
	r := dnsAnswer(dnsQuery(0x1234, "os3.example.com", dnsTypeA), self)
	if r == nil || binary.BigEndian.Uint16(r[0:2]) != 0x1234 || r[2]&0x80 == 0 || r[3]&0x0f != 0 {
		t.Fatalf("bad header: %x", r)
	}
	if binary.BigEndian.Uint16(r[6:8]) != 1 || !strings.HasSuffix(string(r), string(self.AsSlice())) {
		t.Fatalf("A answer missing: %x", r)
	}
	// AAAA: empty answer, so clients fall back to IPv4
	if r := dnsAnswer(dnsQuery(1, "example.com", 28), self); r == nil || binary.BigEndian.Uint16(r[6:8]) != 0 || r[3]&0x0f != 0 {
		t.Fatalf("AAAA: %x", r)
	}
	// garbage and responses are ignored
	for _, q := range [][]byte{nil, {1, 2, 3}, append(dnsQuery(1, "a", 1)[:12], 0xc0, 0x0c), func() []byte { q := dnsQuery(1, "a", 1); q[2] |= 0x80; return q }()} {
		if r := dnsAnswer(q, self); r != nil {
			t.Fatalf("answered %x with %x", q, r)
		}
	}
}

func TestDNSServerOverUDP(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &DNSServer{LocalAddr: func(netip.Addr) (netip.Addr, bool) { return netip.MustParseAddr("10.9.8.7"), true }}
	go func() { _ = s.Serve(ctx, pc) }()
	c, err := net.Dial("udp", pc.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(2 * time.Second))
	c.Write(dnsQuery(7, "agent.example", dnsTypeA))
	buf := make([]byte, 512)
	n, err := c.Read(buf)
	if err != nil || !strings.HasSuffix(string(buf[:n]), "\x0a\x09\x08\x07") {
		t.Fatalf("answer %x %v", buf[:n], err)
	}
}
