package broker

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/pyzyi/dockerbox/internal/dockerapi"
)

// Info describes the broker's limits for the UI.
type Info struct {
	Version         string  `json:"version"`
	AgentImage      string  `json:"agentImage"`
	Runtime         string  `json:"runtime"`
	MaxAgents       int     `json:"maxAgents"`
	DefaultMemoryMB int     `json:"defaultMemoryMB"`
	MaxMemoryMB     int     `json:"maxMemoryMB"`
	DefaultCPUs     float64 `json:"defaultCPUs"`
	MaxCPUs         float64 `json:"maxCPUs"`
}

// Server exposes the broker API on the internal control network. It is the
// contract between web and broker; see docs/architecture.md.
type Server struct {
	token string
	svc   *Service
	info  Info
	log   *slog.Logger
}

// NewServer returns the HTTP handler of the broker. Every /v1 request must
// carry "Authorization: Bearer <token>"; only web knows the token.
func NewServer(svc *Service, cfg *Config, token, version string, log *slog.Logger) *Server {
	return &Server{
		svc: svc, log: log, token: token,
		info: Info{
			Version: version, AgentImage: cfg.AgentImage, Runtime: cfg.Runtime,
			MaxAgents: cfg.MaxAgents, DefaultMemoryMB: cfg.DefaultMemoryMB, MaxMemoryMB: cfg.MaxMemoryMB,
			DefaultCPUs: cfg.DefaultCPUs, MaxCPUs: cfg.MaxCPUs,
		},
	}
}

// Handler returns the routes of the broker API.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.HandleFunc("GET /v1/info", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.info) })
	mux.HandleFunc("GET /v1/agents", s.list)
	mux.HandleFunc("POST /v1/agents", s.create)
	mux.HandleFunc("GET /v1/agents/{name}", s.get)
	mux.HandleFunc("DELETE /v1/agents/{name}", s.delete)
	mux.HandleFunc("POST /v1/agents/{name}/{action}", s.action)
	mux.HandleFunc("GET /v1/agents/{name}/usage", s.usage)
	mux.HandleFunc("GET /v1/agents/{name}/terminal", s.terminal)
	mux.HandleFunc("POST /v1/terminals/{exec}/resize", s.resize)
	return s.requireToken(mux)
}

// requireToken rejects API calls without the shared secret, so that other
// containers or host processes on the same network cannot drive the broker.
func (s *Server) requireToken(next http.Handler) http.Handler {
	want := []byte("Bearer " + s.token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/healthz" &&
			(s.token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1) {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// LoadOrCreateToken returns the shared broker token stored at path, creating
// a random one (0600) on first start.
func LoadOrCreateToken(path string) (string, error) {
	if b, err := os.ReadFile(path); err == nil && len(strings.TrimSpace(string(b))) >= 32 {
		return strings.TrimSpace(string(b)), nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	token := hex.EncodeToString(buf)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(token), 0o600); err != nil {
		return "", err
	}
	return token, os.Rename(tmp, path)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	agents, err := s.svc.List(r.Context())
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	a, err := s.svc.Get(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var req CreateRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		s.fail(w, errors.Join(ErrInvalid, err))
		return
	}
	// Image pulls can take minutes on slow links. Once started, a lifecycle
	// operation must finish even if the browser goes away (see detached).
	ctx, cancel := detached(r, 15*time.Minute)
	defer cancel()
	a, err := s.svc.Create(ctx, req)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, a)
}

func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	keep := r.URL.Query().Get("keepData") != "false"
	ctx, cancel := detached(r, 2*time.Minute)
	defer cancel()
	if err := s.svc.Delete(ctx, r.PathValue("name"), keep); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) action(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	timeout := 2 * time.Minute
	if r.PathValue("action") == "recreate" {
		timeout = 15 * time.Minute
	}
	ctx, cancel := detached(r, timeout)
	defer cancel()
	var err error
	switch r.PathValue("action") {
	case "start":
		err = s.svc.Start(ctx, name)
	case "stop":
		err = s.svc.Stop(ctx, name)
	case "restart":
		err = s.svc.Restart(ctx, name)
	case "recreate":
		_, err = s.svc.Recreate(ctx, name)
	default:
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.fail(w, err)
		return
	}
	a, err := s.svc.Get(r.Context(), name)
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}

func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	u, err := s.svc.Usage(r.Context(), r.PathValue("name"))
	if err != nil {
		s.fail(w, err)
		return
	}
	writeJSON(w, http.StatusOK, u)
}

// terminal upgrades the HTTP connection to a raw TCP stream connected to a
// TTY inside the agent box ("Upgrade: tcp", same scheme as the Docker API).
func (s *Server) terminal(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Upgrade") != "tcp" {
		http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		return
	}
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	// The exec session must outlive the request context once hijacked.
	execID, box, boxReader, err := s.svc.Terminal(context.WithoutCancel(r.Context()), r.PathValue("name"), rows, cols)
	if err != nil {
		s.fail(w, err)
		return
	}
	defer s.svc.CloseTerminal(execID)
	defer box.Close()

	hj, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported", http.StatusInternalServerError)
		return
	}
	client, clientRW, err := hj.Hijack()
	if err != nil {
		return
	}
	defer client.Close()

	_, err = io.WriteString(client, "HTTP/1.1 101 Switching Protocols\r\nConnection: Upgrade\r\nUpgrade: tcp\r\nX-Exec-Id: "+execID+"\r\n\r\n")
	if err != nil {
		return
	}
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(box, clientRW); done <- struct{}{} }()
	go func() { _, _ = io.Copy(client, boxReader); done <- struct{}{} }()
	<-done
}

func (s *Server) resize(w http.ResponseWriter, r *http.Request) {
	rows, _ := strconv.Atoi(r.URL.Query().Get("rows"))
	cols, _ := strconv.Atoi(r.URL.Query().Get("cols"))
	if err := s.svc.Resize(r.Context(), r.PathValue("exec"), rows, cols); err != nil {
		s.fail(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fail maps domain errors to status codes. Engine errors are logged in full but
// only summarised to the caller.
func (s *Server) fail(w http.ResponseWriter, err error) {
	status := http.StatusInternalServerError
	msg := "internal error"
	switch {
	case errors.Is(err, ErrInvalid):
		status, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, ErrNotFound):
		status, msg = http.StatusNotFound, err.Error()
	case errors.Is(err, ErrConflict):
		status, msg = http.StatusConflict, err.Error()
	case errors.Is(err, ErrLimit), errors.Is(err, ErrStopped):
		status, msg = http.StatusConflict, err.Error()
	case errors.Is(err, ErrNoEgress):
		status, msg = http.StatusServiceUnavailable, err.Error()
	case errors.Is(err, ErrPolicy):
		status, msg = http.StatusForbidden, "request rejected by security policy"
	case errors.Is(err, context.DeadlineExceeded):
		status, msg = http.StatusGatewayTimeout, "operation timed out"
	default:
		var apiErr *dockerapi.APIError
		if errors.As(err, &apiErr) {
			status, msg = http.StatusBadGateway, "docker: "+apiErr.Message
		}
	}
	if status >= 500 {
		s.log.Error("request failed", "err", err)
	}
	writeJSON(w, status, map[string]string{"error": msg})
}

// detached returns a context that is not cancelled when the client disconnects
// (closed tab, proxy timeout), so a half-done create/recreate/delete cannot
// leave orphaned containers, networks or volumes behind.
func detached(r *http.Request, timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), timeout)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
