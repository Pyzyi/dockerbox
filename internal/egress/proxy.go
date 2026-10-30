package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"strconv"
	"sync"
	"time"
)

// ErrForbidden is returned when a destination is not allowed.
var ErrForbidden = errors.New("destination not allowed")

// Resolver looks up IP addresses; *net.Resolver satisfies it.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// Proxy is an HTTP forward proxy supporting CONNECT tunnels and plain HTTP.
type Proxy struct {
	filter      *Filter
	resolver    Resolver
	dialer      *net.Dialer
	log         *slog.Logger
	sem         chan struct{}
	dialTimeout time.Duration
	idleTimeout time.Duration
	transport   *http.Transport

	mu           sync.Mutex
	perClient    map[string]int
	maxPerClient int
}

// Options configure a Proxy.
type Options struct {
	Filter         *Filter
	Resolver       Resolver
	MaxConnections int
	// MaxPerClient caps concurrent connections per agent box (client IP), so
	// one box cannot exhaust the proxy for all others.
	MaxPerClient int
	// IdleTimeout closes CONNECT tunnels without traffic in either direction.
	IdleTimeout time.Duration
	DialTimeout time.Duration
	Logger      *slog.Logger
}

// New returns a Proxy.
func New(o Options) *Proxy {
	if o.Resolver == nil {
		o.Resolver = net.DefaultResolver
	}
	if o.MaxConnections <= 0 {
		o.MaxConnections = 512
	}
	if o.DialTimeout <= 0 {
		o.DialTimeout = 15 * time.Second
	}
	if o.MaxPerClient <= 0 {
		o.MaxPerClient = 64
	}
	if o.IdleTimeout <= 0 {
		o.IdleTimeout = 15 * time.Minute
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	p := &Proxy{
		filter:       o.Filter,
		resolver:     o.Resolver,
		dialer:       &net.Dialer{Timeout: o.DialTimeout, KeepAlive: 30 * time.Second},
		log:          o.Logger,
		sem:          make(chan struct{}, o.MaxConnections),
		dialTimeout:  o.DialTimeout,
		idleTimeout:  o.IdleTimeout,
		perClient:    map[string]int{},
		maxPerClient: o.MaxPerClient,
	}
	p.transport = &http.Transport{
		Proxy:                 nil, // never chain to an upstream proxy from the environment
		DialContext:           p.DialContext,
		MaxIdleConns:          64,
		IdleConnTimeout:       60 * time.Second,
		ResponseHeaderTimeout: 2 * time.Minute,
		ForceAttemptHTTP2:     false,
	}
	return p
}

// DialContext resolves host itself, checks every candidate address and dials
// only checked addresses. This closes the DNS rebinding gap of name-based checks.
func (p *Proxy) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portStr)
	if err != nil || port < 1 || port > 65535 {
		return nil, fmt.Errorf("invalid port %q", portStr)
	}
	ctx, cancel := context.WithTimeout(ctx, p.dialTimeout)
	defer cancel()
	ips, err := p.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}
	var lastErr error = ErrForbidden
	for _, ip := range ips {
		if !p.filter.Allowed(ip, port) {
			continue
		}
		conn, err := p.dialer.DialContext(ctx, "tcp", netip.AddrPortFrom(ip.Unmap(), uint16(port)).String())
		if err == nil {
			return conn, nil
		}
		lastErr = err
	}
	return nil, lastErr
}

// ServeHTTP implements the proxy protocol.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	client := clientHost(r.RemoteAddr)
	if !p.acquire(client) {
		http.Error(w, "proxy busy", http.StatusServiceUnavailable)
		return
	}
	defer p.release(client)
	switch {
	case r.Method == http.MethodConnect:
		p.connect(w, r)
	case r.URL.IsAbs() && r.URL.Scheme == "http":
		p.forward(w, r)
	default:
		http.Error(w, "this is a forward proxy; use it via HTTP_PROXY/HTTPS_PROXY", http.StatusBadRequest)
	}
}

func clientHost(remote string) string {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return remote
	}
	return host
}

// acquire takes a global and a per-client connection slot.
func (p *Proxy) acquire(client string) bool {
	select {
	case p.sem <- struct{}{}:
	default:
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.perClient[client] >= p.maxPerClient {
		<-p.sem
		return false
	}
	p.perClient[client]++
	return true
}

func (p *Proxy) release(client string) {
	p.mu.Lock()
	if p.perClient[client]--; p.perClient[client] <= 0 {
		delete(p.perClient, client)
	}
	p.mu.Unlock()
	<-p.sem
}

// idleConn closes a tunnel after a period without traffic in either
// direction: every read or write pushes the shared deadline forward.
type idleConn struct {
	net.Conn
	peer    net.Conn
	timeout time.Duration
}

func (c *idleConn) touch() {
	d := time.Now().Add(c.timeout)
	_ = c.Conn.SetDeadline(d)
	_ = c.peer.SetDeadline(d)
}

func (c *idleConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	if n > 0 {
		c.touch()
	}
	return n, err
}

func (p *Proxy) connect(w http.ResponseWriter, r *http.Request) {
	target := r.Host
	if _, _, err := net.SplitHostPort(target); err != nil {
		http.Error(w, "CONNECT target must be host:port", http.StatusBadRequest)
		return
	}
	upstream, err := p.DialContext(r.Context(), "tcp", target)
	if err != nil {
		p.deny(w, r, target, err)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	client, rw, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		return
	}
	defer client.Close()
	defer upstream.Close()
	if _, err := io.WriteString(client, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	// Flush bytes the client sent right after the CONNECT header (e.g. TLS hello).
	if n := rw.Reader.Buffered(); n > 0 {
		buf, _ := rw.Reader.Peek(n)
		if _, err := upstream.Write(buf); err != nil {
			return
		}
	}
	// When either side ends, both connections are closed by the defers above, so
	// a half-open peer can never pin a connection slot forever.
	clientSide := &idleConn{Conn: client, peer: upstream, timeout: p.idleTimeout}
	upstreamSide := &idleConn{Conn: upstream, peer: client, timeout: p.idleTimeout}
	clientSide.touch()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, clientSide); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstreamSide); done <- struct{}{} }()
	<-done
}

func (p *Proxy) forward(w http.ResponseWriter, r *http.Request) {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL = pr.In.URL
			pr.Out.Host = pr.In.Host
			pr.Out.Header.Del("Proxy-Authorization")
			pr.Out.Header.Del("Proxy-Connection")
		},
		Transport: p.transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			p.deny(w, r, r.URL.Host, err)
		},
	}
	rp.ServeHTTP(w, r)
}

func (p *Proxy) deny(w http.ResponseWriter, r *http.Request, target string, err error) {
	if errors.Is(err, ErrForbidden) {
		p.log.Warn("egress denied", "client", r.RemoteAddr, "target", target)
		http.Error(w, "dockerbox egress: destination "+target+" is not allowed (private or reserved address)", http.StatusForbidden)
		return
	}
	p.log.Info("egress upstream error", "client", r.RemoteAddr, "target", target, "err", err)
	http.Error(w, "dockerbox egress: cannot reach "+target, http.StatusBadGateway)
}
