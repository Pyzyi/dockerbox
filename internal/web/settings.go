package web

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"
)

// Settings are the user's appearance preferences. They are shared by all
// browsers of the installation and stored in the data volume. Every field is
// validated strictly because the UI turns them into CSS custom properties.
type Settings struct {
	Theme      string    `json:"theme"`      // auto | dark | light
	Accent     string    `json:"accent"`     // #rrggbb
	Wallpaper  string    `json:"wallpaper"`  // preset name or "custom"
	WallColors [4]string `json:"wallColors"` // #rrggbb, used when Wallpaper is "custom"
	Glass      int       `json:"glass"`      // transparency 0 (solid) .. 100 (clear)
	Motion     bool      `json:"motion"`     // animate the wallpaper
	FontSize   int       `json:"fontSize"`   // console font size in px
	Language   string    `json:"language"`   // auto | en | de
}

var (
	hexColorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	themes     = []string{"auto", "dark", "light"}
	wallpapers = []string{"aurora", "ocean", "sunset", "forest", "graphite", "custom"}
	languages  = []string{"auto", "en", "de"}
)

// DefaultSettings are used until the user saves their own.
func DefaultSettings() Settings {
	return Settings{
		Theme: "auto", Accent: "#0a84ff", Wallpaper: "aurora",
		WallColors: [4]string{"#5e3bd6", "#0a6cff", "#ff2d75", "#11b88a"},
		Glass:      50, Motion: true, FontSize: 13, Language: "auto",
	}
}

// ErrInvalidSettings wraps every validation failure.
var ErrInvalidSettings = errors.New("invalid settings")

// Validate rejects values that are not in the allowed sets or ranges.
func (s Settings) Validate() error {
	var errs []error
	if !slices.Contains(themes, s.Theme) {
		errs = append(errs, fmt.Errorf("theme must be one of %v", themes))
	}
	if !hexColorRE.MatchString(s.Accent) {
		errs = append(errs, errors.New("accent must be a #rrggbb colour"))
	}
	if !slices.Contains(wallpapers, s.Wallpaper) {
		errs = append(errs, fmt.Errorf("wallpaper must be one of %v", wallpapers))
	}
	for _, c := range s.WallColors {
		if !hexColorRE.MatchString(c) {
			errs = append(errs, errors.New("wallpaper colours must be #rrggbb colours"))
			break
		}
	}
	if s.Glass < 0 || s.Glass > 100 {
		errs = append(errs, errors.New("glass must be between 0 and 100"))
	}
	if s.FontSize < 10 || s.FontSize > 22 {
		errs = append(errs, errors.New("fontSize must be between 10 and 22"))
	}
	if !slices.Contains(languages, s.Language) {
		errs = append(errs, fmt.Errorf("language must be one of %v", languages))
	}
	if len(errs) == 0 {
		return nil
	}
	return fmt.Errorf("%w: %w", ErrInvalidSettings, errors.Join(errs...))
}

// SettingsStore keeps the settings in memory and persists them as JSON.
type SettingsStore struct {
	mu   sync.Mutex
	path string
	cur  Settings
}

// NewSettingsStore loads settings from dir, falling back to defaults when the
// file is missing or invalid (e.g. written by a newer version).
func NewSettingsStore(dir string) (*SettingsStore, error) {
	st := &SettingsStore{path: filepath.Join(dir, "settings.json"), cur: DefaultSettings()}
	b, err := os.ReadFile(st.path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return st, nil
	case err != nil:
		return nil, err
	}
	s := DefaultSettings() // fields missing in the file keep their defaults
	if json.Unmarshal(b, &s) == nil && s.Validate() == nil {
		st.cur = s
	}
	return st, nil
}

// Get returns the current settings.
func (st *SettingsStore) Get() Settings {
	st.mu.Lock()
	defer st.mu.Unlock()
	return st.cur
}

// Put validates and persists new settings atomically.
func (st *SettingsStore) Put(s Settings) error {
	if err := s.Validate(); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	tmp := st.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, st.path); err != nil {
		return err
	}
	st.cur = s
	return nil
}

func (s *Server) getSettings(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.settings.Get())
}

func (s *Server) putSettings(w http.ResponseWriter, r *http.Request) {
	var in Settings
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&in); err != nil {
		writeError(w, http.StatusBadRequest, "invalid settings")
		return
	}
	if err := s.settings.Put(in); err != nil {
		if errors.Is(err, ErrInvalidSettings) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		// Storage failures (write, rename, ...) must not leak paths to the client.
		s.log.Error("could not save settings", "err", err)
		writeError(w, http.StatusInternalServerError, "could not save settings")
		return
	}
	writeJSON(w, http.StatusOK, s.settings.Get())
}
