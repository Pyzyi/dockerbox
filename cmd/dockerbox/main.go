// Command dockerbox runs one of the Dockerbox services: web, broker or egress.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pyzyi/dockerbox/internal/broker"
	"github.com/pyzyi/dockerbox/internal/dockerapi"
	"github.com/pyzyi/dockerbox/internal/egress"
	"github.com/pyzyi/dockerbox/internal/envconf"
	"github.com/pyzyi/dockerbox/internal/privdrop"
	"github.com/pyzyi/dockerbox/internal/web"
)

// Set at build time via -ldflags.
var (
	version    = "dev"
	agentImage = "ghcr.io/pyzyi/dockerbox-agentbox:latest"
)

const usage = `dockerbox - AI Agent in a Box

Usage:
  dockerbox web          dashboard and terminal gateway
  dockerbox broker       policy broker (holds the Docker socket)
  dockerbox egress       egress proxy for agent boxes
  dockerbox healthcheck  <http://host:port/path | tcp://host:port>
  dockerbox version
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	log := newLogger()
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	var err error
	switch os.Args[1] {
	case "web":
		err = runWeb(ctx, log)
	case "broker":
		err = runBroker(ctx, log)
	case "egress":
		err = runEgress(ctx, log)
	case "healthcheck":
		err = healthcheck(os.Args[2:])
	case "version":
		fmt.Println(version)
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	if err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func newLogger() *slog.Logger {
	level := slog.LevelInfo
	if strings.EqualFold(os.Getenv("DOCKERBOX_LOG_LEVEL"), "debug") {
		level = slog.LevelDebug
	}
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
}

// serve runs srv until ctx is cancelled, then shuts down gracefully.
func serve(ctx context.Context, srv *http.Server, log *slog.Logger) error {
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("listening", "addr", srv.Addr, "version", version)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	err := srv.Shutdown(sctx)
	if errors.Is(err, context.DeadlineExceeded) {
		return srv.Close()
	}
	return err
}

func runBroker(ctx context.Context, log *slog.Logger) error {
	cfg, err := broker.LoadConfig(agentImage)
	if err != nil {
		return err
	}
	log = log.With("svc", "broker")
	engine := dockerapi.New(cfg.DockerSocket)
	pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	err = engine.Ping(pctx)
	cancel()
	if err != nil {
		return fmt.Errorf("docker engine not reachable at %s: %w", cfg.DockerSocket, err)
	}
	checkEngine(ctx, engine, cfg, log)
	svc := broker.NewService(cfg, engine, log)
	go svc.RunReconciler(ctx, time.Minute)

	srv := broker.NewServer(svc, cfg, version, log)
	err = serve(ctx, &http.Server{
		Addr: cfg.Listen, Handler: srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
	}, log)
	return err
}

// checkEngine warns about engine versions with known isolation gaps and keeps
// CPU limits within what the host actually has.
func checkEngine(ctx context.Context, engine *dockerapi.Client, cfg *broker.Config, log *slog.Logger) {
	if v, err := engine.Version(ctx); err == nil {
		if major, _, _ := strings.Cut(v.Version, "."); atoiOr(major, 0) < 26 {
			log.Warn("Docker Engine older than 26: internal networks may forward DNS to the host resolver "+
				"(CVE-2024-29018). Update Docker for full isolation.", "version", v.Version)
		}
	}
	if info, err := engine.Info(ctx); err == nil && info.NCPU > 0 {
		if n := float64(info.NCPU); cfg.MaxCPUs > n {
			log.Info("limiting CPUs per agent to the host CPU count", "configured", cfg.MaxCPUs, "host", n)
			cfg.MaxCPUs = n
			cfg.DefaultCPUs = min(cfg.DefaultCPUs, n)
		}
	}
}

func atoiOr(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func runWeb(ctx context.Context, log *slog.Logger) error {
	e := envconf.New()
	listen := e.String("DOCKERBOX_LISTEN", ":8080")
	brokerURL := e.String("DOCKERBOX_BROKER_URL", "http://broker:8081")
	dataDir := e.String("DOCKERBOX_DATA_DIR", "/data")
	ttl := e.Duration("DOCKERBOX_SESSION_TTL", 24*time.Hour)
	secure := e.String("DOCKERBOX_SECURE_COOKIE", "auto")
	if err := e.Err(); err != nil {
		return err
	}
	if secure != "auto" && secure != "true" && secure != "false" {
		return fmt.Errorf("DOCKERBOX_SECURE_COOKIE %q must be auto, true or false", secure)
	}
	u, err := url.Parse(brokerURL)
	if err != nil || u.Scheme != "http" || u.Host == "" {
		return fmt.Errorf("DOCKERBOX_BROKER_URL %q must be http://host:port", brokerURL)
	}
	log = log.With("svc", "web")
	if os.Geteuid() == 0 {
		// Started as root (compose default) only to prepare the data directory,
		// which platforms like ZimaOS create as root:root on the host.
		if err := privdrop.OwnTree(dataDir, privdrop.UID, privdrop.GID); err != nil {
			log.Warn("could not take ownership of the data directory", "dir", dataDir, "err", err)
		}
		if err := privdrop.Drop(privdrop.UID, privdrop.GID); err != nil {
			return fmt.Errorf("drop privileges: %w", err)
		}
		log.Info("running as unprivileged user", "uid", privdrop.UID)
	}
	secrets, err := web.LoadSecrets(os.Getenv("DOCKERBOX_PASSWORD"), os.Getenv("DOCKERBOX_PASSWORD_FILE"), dataDir)
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("%w (the data directory %s must be writable by uid %d; run the web service as root so it can fix this itself, or chown the host directory)",
			err, dataDir, privdrop.UID)
	}
	if err != nil {
		return err
	}
	if secrets.Generated {
		// Printed once so the user can find it in the container log on first start.
		log.Warn("no DOCKERBOX_PASSWORD set - generated a password (stored in the data volume)", "password", secrets.Password)
	}
	auth, err := web.NewAuth(secrets.Password, secrets.Key, ttl)
	if err != nil {
		return err
	}
	settings, err := web.NewSettingsStore(dataDir)
	if err != nil {
		return err
	}
	srv := web.NewServer(web.Config{BrokerURL: u, SessionTTL: ttl, SecureCookie: secure}, auth, settings, log)
	return serve(ctx, &http.Server{
		Addr: listen, Handler: srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute,
	}, log)
}

func runEgress(ctx context.Context, log *slog.Logger) error {
	e := envconf.New()
	listen := e.String("DOCKERBOX_EGRESS_LISTEN", ":3128")
	allow := e.List("DOCKERBOX_EGRESS_ALLOW_CIDRS")
	deny := e.List("DOCKERBOX_EGRESS_DENY_CIDRS")
	maxConns := e.Int("DOCKERBOX_EGRESS_MAX_CONNECTIONS", 512)
	maxPerAgent := e.Int("DOCKERBOX_EGRESS_MAX_PER_AGENT", 64)
	idle := e.Duration("DOCKERBOX_EGRESS_IDLE_TIMEOUT", 15*time.Minute)
	var ports []int
	for _, p := range e.List("DOCKERBOX_EGRESS_ALLOWED_PORTS") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return fmt.Errorf("DOCKERBOX_EGRESS_ALLOWED_PORTS: %q is not a port", p)
		}
		ports = append(ports, n)
	}
	if err := e.Err(); err != nil {
		return err
	}
	filter, err := egress.NewFilter(allow, deny, ports)
	if err != nil {
		return err
	}
	log = log.With("svc", "egress")
	if len(allow) > 0 {
		log.Warn("egress allow-list active: agents can reach these private ranges", "cidrs", allow)
	}
	p := egress.New(egress.Options{Filter: filter, MaxConnections: maxConns, MaxPerClient: maxPerAgent, IdleTimeout: idle, Logger: log})
	return serve(ctx, &http.Server{
		Addr: listen, Handler: p,
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second,
	}, log)
}

// healthcheck lets distroless images (no curl) run Docker healthchecks.
func healthcheck(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: dockerbox healthcheck <url>")
	}
	u, err := url.Parse(args[0])
	if err != nil {
		return err
	}
	switch u.Scheme {
	case "tcp":
		c, err := net.DialTimeout("tcp", u.Host, 3*time.Second)
		if err != nil {
			return err
		}
		return c.Close()
	case "http":
		client := &http.Client{Timeout: 3 * time.Second}
		resp, err := client.Get(u.String())
		if err != nil {
			return err
		}
		resp.Body.Close()
		if resp.StatusCode >= 300 {
			return fmt.Errorf("unhealthy: %s", resp.Status)
		}
		return nil
	}
	return fmt.Errorf("unsupported scheme %q", u.Scheme)
}
