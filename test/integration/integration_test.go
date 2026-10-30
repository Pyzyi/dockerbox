//go:build integration

// Package integration runs the full stack against a real Docker Engine and
// attempts to break out of an agent box. Run via test/integration/run.sh.
package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

var (
	baseURL  = env("DOCKERBOX_URL", "http://127.0.0.1:18420")
	password = env("DOCKERBOX_PASSWORD", "integration-test-password")
	// IT_INTERNET=1 additionally checks that public destinations are reachable.
	checkInternet = os.Getenv("IT_INTERNET") == "1"
	// The tests run as instance "it" (see compose.override.yml).
	boxPrefix = "dockerbox_it_agent-"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type client struct {
	t    *testing.T
	http *http.Client
}

func newClient(t *testing.T) *client {
	jar, _ := cookiejar.New(nil)
	c := &client{t: t, http: &http.Client{Jar: jar, Timeout: 10 * time.Minute}}
	if code, body := c.call("POST", "/api/login", map[string]string{"password": password}); code != 204 {
		t.Fatalf("login: %d %s", code, body)
	}
	return c
}

func (c *client) call(method, path string, body any) (int, []byte) {
	c.t.Helper()
	var rdr io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rdr = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, baseURL+path, rdr)
	req.Header.Set("X-Dockerbox-CSRF", "1")
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		c.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

// dockerExec runs a shell snippet inside a box as root and returns its output.
func dockerExec(t *testing.T, agent, script string) (string, error) {
	t.Helper()
	out, err := exec.Command("docker", "exec", boxPrefix+agent, "bash", "-c", script).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func mustExec(t *testing.T, agent, script string) string {
	t.Helper()
	out, err := dockerExec(t, agent, script)
	if err != nil {
		t.Fatalf("exec %q: %v\n%s", script, err, out)
	}
	return out
}

func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

// proxyStatus asks the egress proxy for a CONNECT tunnel from inside a box.
func proxyStatus(t *testing.T, agent, target string) string {
	t.Helper()
	script := fmt.Sprintf(`exec 3<>/dev/tcp/dockerbox-egress/3128 && printf 'CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n' >&3 && timeout 20 head -n1 <&3`, target, target)
	out, err := dockerExec(t, agent, script)
	if err != nil {
		t.Fatalf("proxy request for %s failed: %v %s", target, err, out)
	}
	return out
}

func TestAgentBoxIsolation(t *testing.T) {
	c := newClient(t)
	agents := []string{"it-alpha", "it-beta"}
	for _, a := range agents {
		c.call("DELETE", "/api/agents/"+a+"?keepData=false", nil)
		start := time.Now()
		code, body := c.call("POST", "/api/agents", map[string]any{"name": a, "memoryMB": 512, "cpus": 0.5})
		if code != 201 {
			t.Fatalf("create %s: %d %s", a, code, body)
		}
		t.Logf("created %s in %v", a, time.Since(start).Round(time.Millisecond))
	}
	t.Cleanup(func() {
		for _, a := range agents {
			c.call("DELETE", "/api/agents/"+a+"?keepData=false", nil)
		}
	})

	t.Run("container spec is hardened", func(t *testing.T) {
		var info []struct {
			HostConfig struct {
				Privileged  bool
				CapAdd      []string
				CapDrop     []string
				SecurityOpt []string
				Memory      int64
				MemorySwap  int64
				PidsLimit   int64
				Binds       []string
			}
			Mounts []struct{ Type, Source, Destination string }
		}
		if err := json.Unmarshal([]byte(docker(t, "inspect", boxPrefix+"it-alpha")), &info); err != nil {
			t.Fatal(err)
		}
		h := info[0].HostConfig
		if h.Privileged || h.Memory != 512<<20 || h.MemorySwap != h.Memory || h.PidsLimit <= 0 || len(h.Binds) > 0 {
			t.Fatalf("weak host config: %+v", h)
		}
		for _, m := range info[0].Mounts {
			if m.Type != "volume" || strings.Contains(m.Source, "docker.sock") {
				t.Fatalf("unexpected mount %+v", m)
			}
		}
	})

	t.Run("root inside, but no dangerous privileges", func(t *testing.T) {
		if uid := mustExec(t, "it-alpha", "id -u"); uid != "0" {
			t.Fatalf("agent should be root inside the box, uid=%s", uid)
		}
		status := mustExec(t, "it-alpha", "grep -E '^(CapEff|NoNewPrivs|Seccomp):' /proc/self/status")
		// CHOWN DAC_OVERRIDE FOWNER FSETID KILL SETGID SETUID SETPCAP NET_BIND_SERVICE SYS_CHROOT
		for _, want := range []string{"CapEff:\t00000000000405fb", "NoNewPrivs:\t1", "Seccomp:\t2"} {
			if !strings.Contains(status, want) {
				t.Errorf("missing %q in\n%s", want, status)
			}
		}
		attempts := map[string]string{
			"docker socket":    "test -S /var/run/docker.sock",
			"mount filesystem": "mount -t tmpfs none /mnt",
			"create device":    "mknod /tmp/sda b 8 0",
			"load module":      "test -w /proc/sys/kernel/modprobe && echo x > /proc/sys/kernel/modprobe",
			"change hostname":  "hostname evil",
			"see host procs":   "test $(ls /proc | grep -c '^[0-9]') -gt 50",
		}
		for name, script := range attempts {
			if out, err := dockerExec(t, "it-alpha", script); err == nil {
				t.Errorf("escape attempt %q unexpectedly succeeded: %s", name, out)
			}
		}
		// Normal admin work must still be possible.
		mustExec(t, "it-alpha", "useradd -m helper && chown helper /workspace && rm -rf /home/helper && userdel helper && chown root /workspace")
	})

	t.Run("network: no route to host, LAN or other boxes", func(t *testing.T) {
		betaIP := docker(t, "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", boxPrefix+"it-beta")
		targets := []string{"172.17.0.1/22", "192.168.0.1/80", "10.0.0.1/80", "1.1.1.1/443"}
		for _, ip := range strings.Fields(betaIP) {
			targets = append(targets, ip+"/22")
		}
		for _, tgt := range targets {
			script := fmt.Sprintf("timeout 3 bash -c 'echo > /dev/tcp/%s'", tgt)
			if out, err := dockerExec(t, "it-alpha", script); err == nil {
				t.Errorf("direct connection to %s succeeded: %s", tgt, out)
			}
		}
		if route := mustExec(t, "it-alpha", "awk 'NR>1 && $2==\"00000000\"' /proc/net/route"); route != "" {
			t.Errorf("box has a default route: %s", route)
		}
		// No IPv6 at all: neither global nor link-local (fe80::) addresses that
		// could reach the host bridge. /proc/net/if_inet6 is absent when the
		// kernel has no IPv6 and empty when the broker disabled it in the box.
		if v6 := mustExec(t, "it-alpha", "cat /proc/net/if_inet6 2>/dev/null || true"); v6 != "" {
			t.Errorf("box has IPv6 addresses:\n%s", v6)
		}
	})

	t.Run("egress proxy denies private targets", func(t *testing.T) {
		betaIP := strings.Fields(docker(t, "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", boxPrefix+"it-beta"))[0]
		for _, tgt := range []string{"172.17.0.1:80", "192.168.1.1:443", "169.254.169.254:80", "127.0.0.1:3128", "localhost:8080", betaIP + ":22", "web:8080", "broker:8081"} {
			if got := proxyStatus(t, "it-alpha", tgt); !strings.Contains(got, " 403 ") && !strings.Contains(got, " 502 ") {
				t.Errorf("CONNECT %s: %q, want 403/502", tgt, got)
			}
		}
		if checkInternet {
			if got := proxyStatus(t, "it-alpha", "example.com:443"); !strings.Contains(got, " 200 ") {
				t.Errorf("CONNECT example.com:443: %q, want 200", got)
			}
		}
	})

	t.Run("terminal over websocket", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		u, _ := url.Parse(baseURL)
		jar := c.http.Jar
		hdr := http.Header{}
		for _, ck := range jar.Cookies(u) {
			hdr.Add("Cookie", ck.Name+"="+ck.Value)
		}
		hdr.Set("Origin", baseURL)
		wsURL := "ws" + strings.TrimPrefix(baseURL, "http") + "/api/agents/it-alpha/terminal?rows=24&cols=80"
		ws, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr})
		if err != nil {
			t.Fatal(err)
		}
		defer ws.CloseNow()
		start := time.Now()
		if err := ws.Write(ctx, websocket.MessageBinary, []byte("echo it-$((40+2))\r")); err != nil {
			t.Fatal(err)
		}
		var seen strings.Builder
		for !strings.Contains(seen.String(), "it-42") {
			_, data, err := ws.Read(ctx)
			if err != nil {
				t.Fatalf("read: %v (seen %q)", err, seen.String())
			}
			seen.Write(data)
		}
		t.Logf("terminal round trip in %v", time.Since(start).Round(time.Millisecond))

		// cross-site WebSocket hijacking must be refused
		hdr.Set("Origin", "https://evil.example")
		if _, _, err := websocket.Dial(ctx, wsURL, &websocket.DialOptions{HTTPHeader: hdr}); err == nil {
			t.Error("cross-origin websocket accepted")
		}
	})

	t.Run("lifecycle keeps data", func(t *testing.T) {
		mustExec(t, "it-alpha", "echo persisted > /workspace/marker")
		for _, action := range []string{"stop", "start", "restart", "recreate"} {
			if code, body := c.call("POST", "/api/agents/it-alpha/"+action, nil); code != 200 {
				t.Fatalf("%s: %d %s", action, code, body)
			}
		}
		if got := mustExec(t, "it-alpha", "cat /workspace/marker"); got != "persisted" {
			t.Fatalf("data lost after recreate: %q", got)
		}
		// egress must still be reachable after recreate
		if got := proxyStatus(t, "it-alpha", "10.0.0.1:80"); !strings.Contains(got, " 403 ") {
			t.Fatalf("proxy not reachable after recreate: %q", got)
		}
	})

	t.Run("systemd services run and start on boot", func(t *testing.T) {
		mustExec(t, "it-alpha", `cat > /etc/systemd/system/it-svc.service <<'EOF'
[Service]
User=nobody
Environment="GREETING=hello box"
ExecStart=/bin/sh -c 'echo "$GREETING as $(id -un) via $HTTPS_PROXY"; exec sleep infinity'
Restart=always

[Install]
WantedBy=multi-user.target
EOF
systemctl daemon-reload && systemctl enable --now it-svc`)
		if got := mustExec(t, "it-alpha", "systemctl is-active it-svc"); got != "active" {
			t.Fatalf("service not active: %q", got)
		}
		// cron drops every job while pam_loginuid is "required" in a container
		mustExec(t, "it-alpha", "mkdir -p /etc/pam.d && printf 'session    required     pam_loginuid.so\\n' > /etc/pam.d/it-cron && systemctl restart it-svc")
		if got := mustExec(t, "it-alpha", "cat /etc/pam.d/it-cron"); !strings.Contains(got, "optional") {
			t.Fatalf("pam_loginuid not relaxed for services: %q", got)
		}
		if code, body := c.call("POST", "/api/agents/it-alpha/restart", nil); code != 200 {
			t.Fatalf("restart: %d %s", code, body)
		}
		deadline := time.Now().Add(10 * time.Second)
		for {
			out, _ := dockerExec(t, "it-alpha", "systemctl is-active it-svc && journalctl -u it-svc -n 1")
			if strings.Contains(out, "hello box as nobody via http://dockerbox-egress:3128") {
				break
			}
			if time.Now().After(deadline) {
				t.Fatalf("enabled service not started after box restart: %q", out)
			}
			time.Sleep(300 * time.Millisecond)
		}
		mustExec(t, "it-alpha", "systemctl disable --now it-svc && ! systemctl is-active -q it-svc")
	})

	t.Run("direct connections go through the egress proxy", func(t *testing.T) {
		// every name resolves to the proxy, so programs ignoring HTTP(S)_PROXY still get out
		egressIP := mustExec(t, "it-alpha", "getent ahostsv4 dockerbox-egress | awk 'NR==1{print $1}'")
		if got := mustExec(t, "it-alpha", "getent ahostsv4 os3.example.com | awk 'NR==1{print $1}'"); got != egressIP || got == "" {
			t.Fatalf("names resolve to %q, want the egress proxy %q", got, egressIP)
		}
		// same policy as the proxy: a name resolving to a private address is refused
		out, _ := dockerExec(t, "it-alpha", `exec 3<>/dev/tcp/dockerbox-egress/80 && printf 'GET / HTTP/1.1\r\nHost: dockerbox-egress\r\nConnection: close\r\n\r\n' >&3 && timeout 10 cat <&3`)
		if out != "" {
			t.Fatalf("direct HTTP to a private destination was forwarded: %q", out)
		}
		// Internet via direct HTTP and TLS, when the test host has Internet access
		if via := proxyStatus(t, "it-alpha", "example.com:443"); !strings.Contains(via, " 200 ") {
			t.Skipf("no Internet access through the proxy (%q); skipping the positive check", via)
		}
		out = mustExec(t, "it-alpha", `exec 3<>/dev/tcp/example.com/80 && printf 'HEAD / HTTP/1.1\r\nHost: example.com\r\nConnection: close\r\n\r\n' >&3 && timeout 20 head -n1 <&3`)
		if !strings.HasPrefix(out, "HTTP/1.1 ") {
			t.Fatalf("direct HTTP failed: %q", out)
		}
		if _, err := dockerExec(t, "it-alpha", "command -v curl"); err == nil {
			mustExec(t, "it-alpha", "env -u HTTPS_PROXY -u https_proxy -u HTTP_PROXY -u http_proxy curl -fsS -o /dev/null --max-time 20 https://example.com/")
		}
	})

	t.Run("idle box is cheap", func(t *testing.T) {
		out := docker(t, "stats", "--no-stream", "--format", "{{.MemUsage}}", boxPrefix+"it-beta")
		t.Logf("idle box memory: %s", out)
		// ISO 25010 resource utilisation: idle box < 5 MiB
		if used := parseSize(t, strings.SplitN(out, "/", 2)[0]); used >= 5<<20 {
			t.Fatalf("idle box uses %s, budget is 5 MiB", out)
		}
	})

	t.Run("delete removes container, network and volumes", func(t *testing.T) {
		if code, body := c.call("DELETE", "/api/agents/it-beta?keepData=false", nil); code != 204 {
			t.Fatalf("delete: %d %s", code, body)
		}
		left := docker(t, "ps", "-a", "-q", "--filter", "label=dockerbox.agent=it-beta")
		nets := docker(t, "network", "ls", "-q", "--filter", "label=dockerbox.agent=it-beta")
		vols := docker(t, "volume", "ls", "-q", "--filter", "label=dockerbox.agent=it-beta")
		if left+nets+vols != "" {
			t.Fatalf("leftovers: containers=%q networks=%q volumes=%q", left, nets, vols)
		}
	})
}

// parseSize converts docker stats sizes like "420KiB" or "1.5MiB" to bytes.
func parseSize(t *testing.T, s string) float64 {
	t.Helper()
	s = strings.TrimSpace(s)
	for _, u := range []struct {
		suffix string
		mult   float64
	}{{"GiB", 1 << 30}, {"MiB", 1 << 20}, {"KiB", 1 << 10}, {"B", 1}} {
		if v, ok := strings.CutSuffix(s, u.suffix); ok {
			var n float64
			if _, err := fmt.Sscanf(v, "%g", &n); err != nil {
				t.Fatalf("cannot parse size %q", s)
			}
			return n * u.mult
		}
	}
	t.Fatalf("cannot parse size %q", s)
	return 0
}

func TestBrokerNotReachableFromOutside(t *testing.T) {
	filter := []string{"--filter", "label=dockerbox.instance=it"}
	// The broker publishes no port.
	ports := docker(t, append([]string{"ps", "--filter", "label=dockerbox.role=broker", "--format", "{{.Ports}}"}, filter...)...)
	if strings.Contains(ports, "->") {
		t.Fatalf("broker publishes ports: %s", ports)
	}
	mounts := docker(t, append([]string{"ps", "--filter", "label=dockerbox.role=web", "--format", "{{.Mounts}}"}, filter...)...)
	if strings.Contains(mounts, "docker.sock") {
		t.Fatalf("web must not mount the docker socket: %s", mounts)
	}
	// Other processes on the network (e.g. the host) can open a connection,
	// but the API refuses every request without the token shared with web.
	ips := docker(t, append([]string{"ps", "--filter", "label=dockerbox.role=broker", "-q"}, filter...)...)
	addr := docker(t, "inspect", "-f", "{{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}", strings.Fields(ips)[0])
	client := &http.Client{Timeout: 3 * time.Second}
	for _, ip := range strings.Fields(addr) {
		for _, auth := range []string{"", "Bearer guessed"} {
			req, _ := http.NewRequest("GET", "http://"+net.JoinHostPort(ip, "8081")+"/v1/agents", nil)
			if auth != "" {
				req.Header.Set("Authorization", auth)
			}
			resp, err := client.Do(req)
			if err != nil {
				continue // not reachable at all is fine too
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusUnauthorized {
				t.Fatalf("broker API answered %d without a valid token", resp.StatusCode)
			}
		}
	}
}

// assertUnprivileged checks that the main process of a service runs as uid
// 65532 without capabilities (services that start as root must drop them).
func assertUnprivileged(t *testing.T, role string) {
	t.Helper()
	id := strings.Fields(docker(t, "ps", "-q", "--filter", "label=dockerbox.role="+role, "--filter", "label=dockerbox.instance=it"))[0]
	pid := docker(t, "inspect", "-f", "{{.State.Pid}}", id)
	status, err := os.ReadFile("/proc/" + pid + "/status")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(status), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) >= 3 && (f[0] == "Uid:" || f[0] == "Gid:"):
			// real, effective, saved, filesystem IDs
			for _, v := range f[1:] {
				if v != "65532" {
					t.Fatalf("%s process %s, want 65532 everywhere", role, line)
				}
			}
		case len(f) == 2 && f[0] == "CapEff:" && f[1] != "0000000000000000":
			t.Fatalf("%s process still has capabilities: %s", role, line)
		}
	}
}

// The egress proxy starts as root only to open ports 53/80/443.
func TestEgressRunsUnprivileged(t *testing.T) {
	assertUnprivileged(t, "egress")
}

func TestWebRunsUnprivilegedAndOwnsData(t *testing.T) {
	// The web service starts as root only to fix ownership of a root-owned
	// /data directory (ZimaOS) and must then run as uid 65532.
	assertUnprivileged(t, "web")
	dir := os.Getenv("IT_DATA_DIR")
	if dir == "" {
		t.Skip("IT_DATA_DIR not set")
	}
	// Only the directory itself: once it belongs to 65532 with mode 0700, a
	// non-root test runner cannot look inside (which is the point). That the
	// session key was written there is proven by the successful logins above.
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if owner := info.Sys().(*syscall.Stat_t).Uid; owner != 65532 {
		t.Errorf("data directory owned by %d, want 65532", owner)
	}
}
