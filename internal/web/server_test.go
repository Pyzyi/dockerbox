package web

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

var testKey = []byte(strings.Repeat("k", 32))

func TestAuthTokens(t *testing.T) {
	a, err := NewAuth("correct horse", testKey, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0)
	tok := a.Issue(now)
	if !a.Verify(tok, now.Add(59*time.Minute)) {
		t.Fatal("fresh token rejected")
	}
	if a.Verify(tok, now.Add(61*time.Minute)) {
		t.Fatal("expired token accepted")
	}
	if a.Verify(strings.Replace(tok, "v1.", "v1.9", 1), now) {
		t.Fatal("tampered token accepted")
	}
	other, _ := NewAuth("another password", testKey, time.Hour)
	if other.Verify(tok, now) {
		t.Fatal("token must be invalid after password change")
	}
	if _, err := NewAuth("short", testKey, time.Hour); err == nil {
		t.Fatal("short password accepted")
	}
}

func TestLoginLimiter(t *testing.T) {
	l := newLoginLimiter(3, time.Minute)
	now := time.Now()
	for range 3 {
		if !l.Attempt("a", now) {
			t.Fatal("blocked too early")
		}
	}
	if l.Attempt("a", now) {
		t.Fatal("not blocked after max attempts")
	}
	if !l.Attempt("b", now) {
		t.Fatal("other client blocked")
	}
	if !l.Attempt("a", now.Add(2*time.Minute)) {
		t.Fatal("not unblocked after window")
	}
	l.Reset("a")
	if !l.Attempt("a", now.Add(2*time.Minute)) {
		t.Fatal("reset did not clear attempts")
	}
}

func TestLoadSecretsGeneratesAndPersists(t *testing.T) {
	dir := t.TempDir()
	s1, err := LoadSecrets("", "", dir)
	if err != nil || !s1.Generated || len(s1.Password) < 20 {
		t.Fatalf("generation failed: %+v %v", s1, err)
	}
	st, _ := os.Stat(filepath.Join(dir, "password"))
	if st.Mode().Perm() != 0o600 {
		t.Errorf("password file mode %v", st.Mode())
	}
	s2, _ := LoadSecrets("", "", dir)
	if s2.Generated || s2.Password != s1.Password || string(s2.Key) != string(s1.Key) {
		t.Fatal("secrets not persisted")
	}
	pf := filepath.Join(dir, "pw")
	os.WriteFile(pf, []byte("from-file-secret\n"), 0o600)
	s3, _ := LoadSecrets("", pf, dir)
	if s3.Password != "from-file-secret" {
		t.Fatalf("password file ignored: %q", s3.Password)
	}
	s4, _ := LoadSecrets("explicit-pass", pf, dir)
	if s4.Password != "explicit-pass" {
		t.Fatal("explicit password must win")
	}
}

// testServer wires the web server to a fake broker that records requests.
func testServer(t *testing.T) (*httptest.Server, *[]string) {
	t.Helper()
	var calls []string
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Method+" "+r.URL.RequestURI())
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `[]`)
	}))
	t.Cleanup(broker.Close)
	u, _ := url.Parse(broker.URL)
	a, _ := NewAuth("correct horse", testKey, time.Hour)
	st, _ := NewSettingsStore(t.TempDir())
	s := NewServer(Config{BrokerURL: u, SessionTTL: time.Hour, SecureCookie: "false"}, a, st, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv, &calls
}

func login(t *testing.T, srv *httptest.Server, pw string) (*http.Cookie, int) {
	resp, err := http.Post(srv.URL+"/api/login", "application/json", strings.NewReader(`{"password":"`+pw+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == sessionCookie {
			return c, resp.StatusCode
		}
	}
	return nil, resp.StatusCode
}

func do(t *testing.T, srv *httptest.Server, method, path string, c *http.Cookie, hdr map[string]string) int {
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(`{"name":"alice"}`))
	if c != nil {
		req.AddCookie(c)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func TestAPIRequiresSessionAndCSRF(t *testing.T) {
	srv, calls := testServer(t)
	if code := do(t, srv, "GET", "/api/agents", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated: %d", code)
	}
	if _, code := login(t, srv, "wrong password"); code != http.StatusUnauthorized {
		t.Fatalf("wrong password: %d", code)
	}
	c, code := login(t, srv, "correct horse")
	if c == nil || code != http.StatusNoContent || !c.HttpOnly || c.SameSite != http.SameSiteStrictMode {
		t.Fatalf("login failed or insecure cookie: %+v %d", c, code)
	}
	if code := do(t, srv, "GET", "/api/agents", c, nil); code != 200 {
		t.Fatalf("authenticated GET: %d", code)
	}
	if code := do(t, srv, "POST", "/api/agents", c, nil); code != http.StatusForbidden {
		t.Fatalf("POST without CSRF header: %d", code)
	}
	csrf := map[string]string{"X-Dockerbox-CSRF": "1"}
	if code := do(t, srv, "POST", "/api/agents", c, csrf); code != 200 {
		t.Fatalf("POST with CSRF header: %d", code)
	}
	evil := map[string]string{"X-Dockerbox-CSRF": "1", "Origin": "https://evil.example"}
	if code := do(t, srv, "POST", "/api/agents", c, evil); code != http.StatusForbidden {
		t.Fatalf("cross-origin POST: %d", code)
	}
	if code := do(t, srv, "POST", "/api/agents/alice/format-disk", c, csrf); code != http.StatusNotFound {
		t.Fatalf("unknown action: %d", code)
	}
	if code := do(t, srv, "DELETE", "/api/agents/..%2fetc", c, csrf); code != http.StatusBadRequest && code != http.StatusNotFound {
		t.Fatalf("path traversal name: %d", code)
	}
	if code := do(t, srv, "DELETE", "/api/agents/alice?keepData=false&force=1", c, csrf); code != 200 {
		t.Fatalf("delete: %d", code)
	}
	want := []string{"GET /v1/agents", "POST /v1/agents", "DELETE /v1/agents/alice?keepData=false"}
	if strings.Join(*calls, "|") != strings.Join(want, "|") {
		t.Fatalf("broker calls = %v, want %v", *calls, want)
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv, _ := testServer(t)
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	csp := resp.Header.Get("Content-Security-Policy")
	if !strings.Contains(csp, "script-src 'self'") || !strings.Contains(csp, "frame-ancestors 'none'") {
		t.Fatalf("weak CSP: %q", csp)
	}
	if resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatal("missing nosniff")
	}
}

func TestLoginRateLimit(t *testing.T) {
	srv, _ := testServer(t)
	for range 10 {
		login(t, srv, "wrong password")
	}
	if _, code := login(t, srv, "correct horse"); code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 after repeated failures, got %d", code)
	}
}

func TestLoginLimiterIsAtomic(t *testing.T) {
	l := newLoginLimiter(10, time.Minute)
	var wg sync.WaitGroup
	var allowed atomic.Int32
	for range 500 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.Attempt("x", time.Now()) {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if allowed.Load() != 10 {
		t.Fatalf("%d parallel attempts passed, want 10", allowed.Load())
	}
}

func TestLogoutRevokesSession(t *testing.T) {
	srv, _ := testServer(t)
	c, _ := login(t, srv, "correct horse")
	if code := do(t, srv, "GET", "/api/agents", c, nil); code != 200 {
		t.Fatalf("before logout: %d", code)
	}
	if code := do(t, srv, "POST", "/api/logout", c, nil); code != http.StatusForbidden {
		t.Fatalf("cross-site forced logout must be rejected: %d", code)
	}
	if code := do(t, srv, "POST", "/api/logout", c, map[string]string{"X-Dockerbox-CSRF": "1"}); code != http.StatusNoContent {
		t.Fatalf("logout: %d", code)
	}
	if code := do(t, srv, "GET", "/api/agents", c, nil); code != http.StatusUnauthorized {
		t.Fatalf("copied cookie still valid after logout: %d", code)
	}
}

func TestClientAddrGroupsIPv6Networks(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "[2001:db8:1:2:aaaa::1]:1234"
	a := clientAddr(r)
	r.RemoteAddr = "[2001:db8:1:2:bbbb::9]:4321"
	if b := clientAddr(r); a != b {
		t.Fatalf("addresses of one /64 must share a limit: %s vs %s", a, b)
	}
	r.RemoteAddr = "192.0.2.7:80"
	if got := clientAddr(r); got != "192.0.2.7" {
		t.Fatalf("ipv4 key = %s", got)
	}
}

func TestTruncateKeepsUTF8Valid(t *testing.T) {
	s := strings.Repeat("ä", 100) // 2 bytes per rune
	got := truncate(s, 123)
	if len(got) > 123 || !utf8.ValidString(got) {
		t.Fatalf("invalid truncation: %d bytes, valid=%v", len(got), utf8.ValidString(got))
	}
}
