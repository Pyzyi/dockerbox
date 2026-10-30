package web

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
)

// terminal bridges a browser WebSocket to a TTY session in an agent box.
// Binary frames carry terminal data in both directions; text frames from the
// browser carry JSON control messages ({"type":"resize","rows":R,"cols":C}).
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))

	// Accept enforces Origin == Host, which blocks cross-site WebSocket hijacking.
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	ws.SetReadLimit(1 << 20) // allow pasting large text

	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	defer cancel()
	var token string
	if c, err := r.Cookie(sessionCookie); err == nil {
		token = c.Value
	}

	name := r.PathValue("name")
	box, reader, execID, err := s.dialTerminal(ctx, name, rows, cols)
	if err != nil {
		s.log.Warn("terminal unavailable", "agent", name, "err", err)
		ws.Close(websocket.StatusTryAgainLater, truncate(err.Error(), 120))
		return
	}
	defer box.Close()

	// box -> browser
	go func() {
		defer cancel()
		buf := make([]byte, 32<<10)
		for {
			n, err := reader.Read(buf)
			if n > 0 {
				if werr := ws.Write(ctx, websocket.MessageBinary, buf[:n]); werr != nil {
					return
				}
			}
			if err != nil {
				ws.Close(websocket.StatusNormalClosure, "session ended")
				return
			}
		}
	}()

	// Keep intermediaries (reverse proxies) from closing idle consoles and end
	// the console when the session expires or is revoked by a logout.
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if !s.auth.Verify(token, s.now()) {
					ws.Close(websocket.StatusPolicyViolation, "session expired")
					cancel()
					return
				}
				pctx, pcancel := context.WithTimeout(ctx, 10*time.Second)
				err := ws.Ping(pctx)
				pcancel()
				if err != nil {
					cancel()
					return
				}
			}
		}
	}()

	// browser -> box
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageBinary {
			if _, err := box.Write(data); err != nil {
				return
			}
			continue
		}
		var msg struct {
			Type string `json:"type"`
			Rows int    `json:"rows"`
			Cols int    `json:"cols"`
		}
		if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
			// Asynchronous: a slow broker must not stall keyboard input.
			go s.resizeTerminal(ctx, execID, msg.Rows, msg.Cols)
		}
	}
}

// dialTerminal opens an upgraded connection to the broker's terminal endpoint.
func (s *Server) dialTerminal(ctx context.Context, name string, rows, cols int) (net.Conn, *bufio.Reader, string, error) {
	network, addr := "unix", s.cfg.BrokerSocket
	if addr == "" {
		network, addr = "tcp", s.cfg.BrokerURL.Host
		if s.cfg.BrokerURL.Port() == "" {
			addr = net.JoinHostPort(s.cfg.BrokerURL.Hostname(), "80")
		}
	}
	dctx, dcancel := context.WithTimeout(ctx, 10*time.Second)
	defer dcancel()
	var d net.Dialer
	conn, err := d.DialContext(dctx, network, addr)
	if err != nil {
		return nil, nil, "", fmt.Errorf("broker unreachable: %w", err)
	}
	u := url.URL{Scheme: "http", Host: s.cfg.BrokerURL.Host, Path: "/v1/agents/" + name + "/terminal",
		RawQuery: url.Values{"rows": {strconv.Itoa(rows)}, "cols": {strconv.Itoa(cols)}}.Encode()}
	req, _ := http.NewRequestWithContext(dctx, http.MethodGet, u.String(), nil)
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Upgrade", "tcp")
	req.Header.Set("Authorization", "Bearer "+s.cfg.BrokerToken)
	_ = conn.SetDeadline(time.Now().Add(30 * time.Second))
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, nil, "", err
	}
	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		conn.Close()
		return nil, nil, "", err
	}
	if resp.StatusCode != http.StatusSwitchingProtocols {
		defer conn.Close()
		var e struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 4<<10)).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return nil, nil, "", errors.New(e.Error)
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, br, resp.Header.Get("X-Exec-Id"), nil
}

func (s *Server) resizeTerminal(ctx context.Context, execID string, rows, cols int) {
	if execID == "" || rows < 1 || cols < 1 || rows > 1000 || cols > 1000 {
		return
	}
	u := *s.cfg.BrokerURL
	u.Path = "/v1/terminals/" + url.PathEscape(execID) + "/resize"
	u.RawQuery = url.Values{"rows": {strconv.Itoa(rows)}, "cols": {strconv.Itoa(cols)}}.Encode()
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(rctx, http.MethodPost, u.String(), nil)
	resp, err := s.broker.Do(req)
	if err == nil {
		resp.Body.Close()
	}
}

// truncate shortens s to at most n bytes without splitting a UTF-8 sequence
// (WebSocket close reasons must be valid UTF-8 and at most 123 bytes).
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}
