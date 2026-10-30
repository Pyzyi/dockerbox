package web

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSettingsValidation(t *testing.T) {
	if err := DefaultSettings().Validate(); err != nil {
		t.Fatalf("defaults invalid: %v", err)
	}
	bad := map[string]func(*Settings){
		"css injection in accent": func(s *Settings) { s.Accent = "red;}body{display:none" },
		"named colour":            func(s *Settings) { s.Accent = "red" },
		"short hex":               func(s *Settings) { s.Accent = "#fff" },
		"url in wallpaper colour": func(s *Settings) { s.WallColors[2] = "url(https://x)" },
		"unknown wallpaper":       func(s *Settings) { s.Wallpaper = "../../etc" },
		"unknown theme":           func(s *Settings) { s.Theme = "neon" },
		"glass too high":          func(s *Settings) { s.Glass = 101 },
		"glass negative":          func(s *Settings) { s.Glass = -1 },
		"font too small":          func(s *Settings) { s.FontSize = 2 },
		"font too large":          func(s *Settings) { s.FontSize = 200 },
		"unknown language":        func(s *Settings) { s.Language = "xx" },
	}
	for name, mutate := range bad {
		s := DefaultSettings()
		mutate(&s)
		if s.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestSettingsStorePersists(t *testing.T) {
	dir := t.TempDir()
	st, err := NewSettingsStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	s := DefaultSettings()
	s.Theme, s.Accent, s.Wallpaper, s.Glass, s.FontSize = "light", "#ff375f", "sunset", 20, 15
	if err := st.Put(s); err != nil {
		t.Fatal(err)
	}
	again, _ := NewSettingsStore(dir)
	if again.Get() != s {
		t.Fatalf("not persisted: %+v", again.Get())
	}
	// A corrupt or hostile file falls back to defaults instead of reaching the UI.
	os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"accent":"red;}x{"}`), 0o600)
	fallback, _ := NewSettingsStore(dir)
	if fallback.Get() != DefaultSettings() {
		t.Fatalf("invalid file not rejected: %+v", fallback.Get())
	}
}

func TestSettingsAPI(t *testing.T) {
	srv, _ := testServer(t)
	if code := do(t, srv, "GET", "/api/settings", nil, nil); code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated settings: %d", code)
	}
	c, _ := login(t, srv, "correct horse")
	put := func(body string, hdr map[string]string) int {
		req, _ := http.NewRequest("PUT", srv.URL+"/api/settings", strings.NewReader(body))
		req.AddCookie(c)
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
	good, _ := json.Marshal(DefaultSettings())
	if code := put(string(good), nil); code != http.StatusForbidden {
		t.Fatalf("PUT without CSRF header: %d", code)
	}
	csrf := map[string]string{"X-Dockerbox-CSRF": "1"}
	if code := put(string(good), csrf); code != 200 {
		t.Fatalf("valid PUT: %d", code)
	}
	if code := put(strings.Replace(string(good), "#0a84ff", "expression(alert(1))", 1), csrf); code != http.StatusBadRequest {
		t.Fatalf("invalid colour accepted: %d", code)
	}
	if code := put(`{"theme":"dark","extra":1}`, csrf); code != http.StatusBadRequest {
		t.Fatalf("unknown field accepted: %d", code)
	}
}

func TestSettingsStorageErrorIsInternal(t *testing.T) {
	dir := t.TempDir()
	st, _ := NewSettingsStore(dir)
	// make the rename target a non-empty directory so the atomic rename fails
	os.MkdirAll(filepath.Join(dir, "settings.json", "x"), 0o700)
	err := st.Put(DefaultSettings())
	if err == nil || errors.Is(err, ErrInvalidSettings) {
		t.Fatalf("storage failure must not look like a validation error: %v", err)
	}
}
