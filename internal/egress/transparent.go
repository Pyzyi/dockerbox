package egress

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"time"
)

// Some programs ignore HTTP(S)_PROXY and open connections directly (for
// example WebSocket clients of the Bun runtime). Inside a box, every name
// resolves to the egress proxy (see DNS), so these connections arrive here on
// port 443 or 80. The destination is taken from the TLS server name (SNI) or
// the HTTP Host header and goes through exactly the same checks as a CONNECT
// request. TLS is not terminated: the bytes are passed through unchanged.

// Transparent modes.
const (
	ModeTLS  = "tls"
	ModeHTTP = "http"
)

const (
	sniffTimeout = 10 * time.Second
	maxSniff     = 64 << 10
)

var errSniffDone = errors.New("sniff done")

// ServeTransparent accepts connections on l and forwards each one to the
// destination named in its first bytes, on port upstreamPort.
func (p *Proxy) ServeTransparent(ctx context.Context, l net.Listener, mode string, upstreamPort int) error {
	go func() { <-ctx.Done(); l.Close() }()
	for {
		c, err := l.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			return err
		}
		go p.handleTransparent(ctx, c, mode, upstreamPort)
	}
}

func (p *Proxy) handleTransparent(ctx context.Context, c net.Conn, mode string, upstreamPort int) {
	defer c.Close()
	client := clientHost(c.RemoteAddr().String())
	if !p.acquire(client) {
		return
	}
	defer p.release(client)

	_ = c.SetReadDeadline(time.Now().Add(sniffTimeout))
	var seen bytes.Buffer
	rec := io.TeeReader(io.LimitReader(c, maxSniff), &seen)
	var host string
	switch mode {
	case ModeTLS:
		host = sniffSNI(rec)
	case ModeHTTP:
		host = sniffHost(rec)
	}
	_ = c.SetReadDeadline(time.Time{})
	if host == "" {
		p.log.Info("egress: direct connection without destination name", "client", client, "mode", mode)
		return
	}
	target := net.JoinHostPort(host, strconv.Itoa(upstreamPort))
	dctx, cancel := context.WithTimeout(ctx, p.dialTimeout)
	upstream, err := p.DialContext(dctx, "tcp", target)
	cancel()
	if err != nil {
		if errors.Is(err, ErrForbidden) {
			p.log.Warn("egress denied", "client", client, "target", target, "mode", mode)
		} else {
			p.log.Info("egress upstream error", "client", client, "target", target, "err", err)
		}
		return
	}
	defer upstream.Close()
	if _, err := upstream.Write(seen.Bytes()); err != nil {
		return
	}
	p.splice(c, upstream)
}

// splice copies both directions until one side ends or the tunnel is idle.
func (p *Proxy) splice(client, upstream net.Conn) {
	clientSide := &idleConn{Conn: client, peer: upstream, timeout: p.idleTimeout}
	upstreamSide := &idleConn{Conn: upstream, peer: client, timeout: p.idleTimeout}
	clientSide.touch()
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(upstream, clientSide); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, upstreamSide); done <- struct{}{} }()
	<-done
}

// sniffSNI reads a TLS ClientHello from r and returns its server name. The
// standard library parses the hello; the handshake is aborted right after.
func sniffSNI(r io.Reader) string {
	var name string
	cfg := &tls.Config{GetConfigForClient: func(h *tls.ClientHelloInfo) (*tls.Config, error) {
		name = h.ServerName
		return nil, errSniffDone
	}}
	_ = tls.Server(readOnlyConn{r: r}, cfg).Handshake()
	if !validHost(name) {
		return ""
	}
	return name
}

// sniffHost reads an HTTP request head from r and returns its host name.
func sniffHost(r io.Reader) string {
	req, err := http.ReadRequest(bufio.NewReader(r))
	if err != nil {
		return ""
	}
	host := req.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	if !validHost(host) {
		return ""
	}
	return host
}

// validHost accepts DNS names only (no addresses, no empty labels).
func validHost(h string) bool {
	if h == "" || len(h) > 253 || net.ParseIP(h) != nil {
		return false
	}
	for _, label := range bytes.Split([]byte(h), []byte(".")) {
		if len(label) == 0 || len(label) > 63 {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_') {
				return false
			}
		}
	}
	return true
}

// readOnlyConn lets crypto/tls read a ClientHello without ever writing.
type readOnlyConn struct{ r io.Reader }

func (c readOnlyConn) Read(b []byte) (int, error)     { return c.r.Read(b) }
func (readOnlyConn) Write([]byte) (int, error)        { return 0, io.ErrClosedPipe }
func (readOnlyConn) Close() error                     { return nil }
func (readOnlyConn) LocalAddr() net.Addr              { return nil }
func (readOnlyConn) RemoteAddr() net.Addr             { return nil }
func (readOnlyConn) SetDeadline(time.Time) error      { return nil }
func (readOnlyConn) SetReadDeadline(time.Time) error  { return nil }
func (readOnlyConn) SetWriteDeadline(time.Time) error { return nil }
