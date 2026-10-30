package dockerapi

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDaemon serves handler on a unix socket, like the Docker Engine.
func fakeDaemon(t *testing.T, handler http.Handler) *Client {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "docker.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go srv.Serve(l)
	t.Cleanup(func() { srv.Close() })
	return New(sock)
}

func TestErrorsAreDecoded(t *testing.T) {
	c := fakeDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/containers/missing/start":
			w.WriteHeader(404)
			io.WriteString(w, `{"message":"No such container: missing"}`)
		case "/containers/running/start":
			w.WriteHeader(304)
		case "/networks/create":
			w.WriteHeader(409)
			io.WriteString(w, "plain text conflict")
		}
	}))
	ctx := context.Background()
	err := c.ContainerStart(ctx, "missing")
	var apiErr *APIError
	if !IsNotFound(err) || !errors.As(err, &apiErr) || apiErr.Message != "No such container: missing" {
		t.Fatalf("404 not decoded: %v", err)
	}
	if err := c.ContainerStart(ctx, "running"); err != nil {
		t.Fatalf("304 on start must not be an error: %v", err)
	}
	if _, err := c.NetworkCreate(ctx, &NetworkSpec{Name: "x"}); !IsConflict(err) || !strings.Contains(err.Error(), "plain text conflict") {
		t.Fatalf("non-JSON error not decoded: %v", err)
	}
}

func TestImagePullReportsInBandErrors(t *testing.T) {
	c := fakeDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("fromImage") == "bad/image:1" {
			io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"error":"manifest unknown"}`+"\n")
			return
		}
		io.WriteString(w, `{"status":"Pulling"}`+"\n"+`{"status":"Done"}`+"\n")
	}))
	if err := c.ImagePull(context.Background(), "good/image:1"); err != nil {
		t.Fatal(err)
	}
	if err := c.ImagePull(context.Background(), "bad/image:1"); err == nil || !strings.Contains(err.Error(), "manifest unknown") {
		t.Fatalf("in-band error lost: %v", err)
	}
}

func TestHasTag(t *testing.T) {
	cases := map[string]bool{
		"debian:trixie":                      true,
		"ghcr.io/pyzyi/dockerbox-core:1.0.0": true,
		"localhost:5000/img":                 false, // the colon belongs to the registry
		"ghcr.io/pyzyi/dockerbox-core":       false,
	}
	for ref, want := range cases {
		if got := hasTag(ref); got != want {
			t.Errorf("hasTag(%q) = %v, want %v", ref, got, want)
		}
	}
}

func TestExecAttachHijacksStream(t *testing.T) {
	c := fakeDaemon(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/exec/broken/start" {
			w.WriteHeader(500)
			io.WriteString(w, `{"message":"exec failed"}`)
			return
		}
		if r.Header.Get("Upgrade") != "tcp" {
			t.Error("missing upgrade header")
		}
		io.Copy(io.Discard, r.Body) // the real daemon consumes the start options first
		conn, rw, _ := w.(http.Hijacker).Hijack()
		defer conn.Close()
		// The first output bytes arrive in the same packet as the response.
		rw.WriteString("HTTP/1.1 101 UPGRADED\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\nhello ")
		rw.Flush()
		line, _ := rw.ReadString('\n')
		rw.WriteString("echo:" + line)
		rw.Flush()
	}))
	conn, r, err := c.ExecAttach(context.Background(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	io.WriteString(conn, "ping\n")
	got, _ := bufio.NewReader(r).ReadString('\n')
	if got != "hello echo:ping\n" {
		t.Fatalf("stream = %q", got)
	}
	if _, _, err := c.ExecAttach(context.Background(), "broken"); err == nil || !strings.Contains(err.Error(), "exec failed") {
		t.Fatalf("failed exec start not reported: %v", err)
	}
}
