// Package web serves the dashboard and translates browser requests into broker
// calls. It never talks to the Docker Engine directly (ADR 0002).
package web

import (
	"bytes"
	"embed"
	"encoding/json"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"
)

//go:embed static
var staticFiles embed.FS

var agentNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// Config holds the web settings.
type Config struct {
	BrokerURL    *url.URL
	SessionTTL   time.Duration
	SecureCookie string // "auto", "true" or "false"
}

// Server is the dashboard HTTP server.
type Server struct {
	cfg      Config
	auth     *Auth
	settings *SettingsStore
	limiter  *loginLimiter
	broker   *http.Client
	log      *slog.Logger
	static   http.Handler
	now      func() time.Time
}

// NewServer returns a dashboard server.
func NewServer(cfg Config, auth *Auth, settings *SettingsStore, log *slog.Logger) *Server {
	sub, err := fs.Sub(staticFiles, "static")
	if err != nil {
		panic(err)
	}
	return &Server{
		cfg: cfg, auth: auth, settings: settings, log: log,
		limiter: newLoginLimiter(10, 15*time.Minute),
		broker:  &http.Client{Timeout: 16 * time.Minute},
		static:  http.FileServerFS(sub),
		now:     time.Now,
	}
}

// Handler returns all routes wrapped in security headers.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /", s.serveStatic)
	mux.HandleFunc("POST /api/login", s.login)
	mux.HandleFunc("POST /api/logout", requireCSRF(s.logout))
	mux.HandleFunc("GET /api/session", s.session)

	mux.Handle("GET /api/settings", s.protect(s.getSettings))
	mux.Handle("PUT /api/settings", s.protect(s.putSettings))
	mux.Handle("GET /api/info", s.protect(s.forward))
	mux.Handle("GET /api/agents", s.protect(s.forward))
	mux.Handle("POST /api/agents", s.protect(s.forward))
	mux.Handle("GET /api/agents/{name}", s.protect(s.withAgent(s.forward)))
	mux.Handle("DELETE /api/agents/{name}", s.protect(s.withAgent(s.forward)))
	mux.Handle("GET /api/agents/{name}/usage", s.protect(s.withAgent(s.forward)))
	mux.Handle("POST /api/agents/{name}/{action}", s.protect(s.withAgent(s.forwardAction)))
	mux.Handle("GET /api/agents/{name}/terminal", s.protect(s.withAgent(s.terminal)))
	return securityHeaders(mux)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		// xterm.js injects <style> elements for its theme, hence 'unsafe-inline'
		// for styles only. Scripts are strictly same-origin files.
		h.Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; "+
			"img-src 'self' data:; font-src 'self'; connect-src 'self'; object-src 'none'; base-uri 'none'; "+
			"form-action 'self'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
		next.ServeHTTP(w, r)
	})
}

func (s *Server) serveStatic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	s.static.ServeHTTP(w, r)
}

// clientAddr is the rate-limiting key of a request: the IPv4 address, or the
// /64 network for IPv6 (one end user usually controls a whole /64).
func clientAddr(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if addr = addr.Unmap(); addr.Is6() {
		return netip.PrefixFrom(addr, 64).Masked().String()
	}
	return addr.String()
}

// requireCSRF rejects requests without the custom CSRF header.
func requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Dockerbox-CSRF") != "1" {
			writeError(w, http.StatusForbidden, "missing CSRF header")
			return
		}
		next(w, r)
	}
}

func (s *Server) secure(r *http.Request) bool {
	switch s.cfg.SecureCookie {
	case "true":
		return true
	case "false":
		return false
	}
	return r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https"
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	client := clientAddr(r)
	now := s.now()
	if !s.limiter.Attempt(client, now) {
		writeError(w, http.StatusTooManyRequests, "too many failed attempts, try again later")
		return
	}
	var req struct {
		Password string `json:"password"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request")
		return
	}
	if !s.auth.CheckPassword(req.Password) {
		s.log.Warn("login failed", "client", client)
		writeError(w, http.StatusUnauthorized, "wrong password")
		return
	}
	s.limiter.Reset(client)
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: s.auth.Issue(now), Path: "/",
		MaxAge: int(s.cfg.SessionTTL.Seconds()), HttpOnly: true,
		Secure: s.secure(r), SameSite: http.SameSiteStrictMode,
	})
	s.log.Info("login", "client", client)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.auth.Revoke(c.Value, s.now())
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: s.secure(r), SameSite: http.SameSiteStrictMode,
	})
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) authenticated(r *http.Request) bool {
	c, err := r.Cookie(sessionCookie)
	return err == nil && s.auth.Verify(c.Value, s.now())
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": s.authenticated(r)})
}

// protect requires a valid session. Mutating requests additionally need the
// custom CSRF header, which browsers never attach to cross-site requests
// without a CORS preflight (which this server does not answer).
func (s *Server) protect(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.authenticated(r) {
			writeError(w, http.StatusUnauthorized, "login required")
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-Dockerbox-CSRF") != "1" {
			writeError(w, http.StatusForbidden, "missing CSRF header")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, r.Host) {
			writeError(w, http.StatusForbidden, "cross-origin request rejected")
			return
		}
		next(w, r)
	})
}

func sameOrigin(origin, host string) bool {
	u, err := url.Parse(origin)
	return err == nil && strings.EqualFold(u.Host, host)
}

func (s *Server) withAgent(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !agentNameRE.MatchString(r.PathValue("name")) {
			writeError(w, http.StatusBadRequest, "invalid agent name")
			return
		}
		next(w, r)
	}
}

func (s *Server) forwardAction(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("action") {
	case "start", "stop", "restart", "recreate":
		s.forward(w, r)
	default:
		writeError(w, http.StatusNotFound, "unknown action")
	}
}

// forward relays an already routed and validated request to the broker,
// mapping /api/... to /v1/....
func (s *Server) forward(w http.ResponseWriter, r *http.Request) {
	target := *s.cfg.BrokerURL
	target.Path = "/v1" + strings.TrimPrefix(r.URL.Path, "/api")
	if r.Method == http.MethodDelete && r.URL.Query().Get("keepData") == "false" {
		target.RawQuery = "keepData=false"
	}
	var body io.Reader
	if r.Method == http.MethodPost {
		b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<10))
		if err != nil {
			writeError(w, http.StatusRequestEntityTooLarge, "request too large")
			return
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, target.String(), body)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.broker.Do(req)
	if err != nil {
		s.log.Error("broker unreachable", "err", err)
		writeError(w, http.StatusBadGateway, "broker unreachable")
		return
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 1<<20))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
