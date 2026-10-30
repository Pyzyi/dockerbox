package web

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	sessionCookie     = "dockerbox_session"
	minPasswordLength = 12
)

// Auth verifies the dashboard password and issues stateless session tokens.
// Tokens are bound to the password: changing it invalidates all sessions.
type Auth struct {
	pwHash [32]byte
	key    []byte
	ttl    time.Duration

	mu      sync.Mutex
	revoked map[string]time.Time // signature -> expiry, filled by logout
}

// NewAuth returns an Auth for the given password and HMAC key.
func NewAuth(password string, key []byte, ttl time.Duration) (*Auth, error) {
	if len(password) < minPasswordLength {
		return nil, fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}
	if len(key) < 32 {
		return nil, errors.New("session key must be at least 32 bytes")
	}
	return &Auth{pwHash: sha256.Sum256([]byte(password)), key: key, ttl: ttl, revoked: map[string]time.Time{}}, nil
}

// CheckPassword compares in constant time.
func (a *Auth) CheckPassword(pw string) bool {
	h := sha256.Sum256([]byte(pw))
	return subtle.ConstantTimeCompare(h[:], a.pwHash[:]) == 1
}

// Issue returns a new session token valid for the configured TTL.
func (a *Auth) Issue(now time.Time) string {
	payload := "v1." + strconv.FormatInt(now.Add(a.ttl).Unix(), 10)
	return payload + "." + a.sign(payload)
}

// Verify checks a session token.
func (a *Auth) Verify(token string, now time.Time) bool {
	sig, exp, ok := a.parse(token)
	if !ok || now.Unix() >= exp {
		return false
	}
	a.mu.Lock()
	_, revoked := a.revoked[sig]
	a.mu.Unlock()
	return !revoked
}

// Revoke invalidates a token before its expiry (logout). Revocations live in
// memory; a restart also ends every open console, so this only shortens the
// remaining lifetime of a copied cookie until the next restart.
func (a *Auth) Revoke(token string, now time.Time) {
	sig, exp, ok := a.parse(token)
	if !ok {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	for s, e := range a.revoked {
		if now.Unix() >= e.Unix() {
			delete(a.revoked, s)
		}
	}
	a.revoked[sig] = time.Unix(exp, 0)
}

func (a *Auth) parse(token string) (sig string, exp int64, ok bool) {
	i := strings.LastIndexByte(token, '.')
	if i < 0 {
		return "", 0, false
	}
	payload, sig := token[:i], token[i+1:]
	if !hmac.Equal([]byte(sig), []byte(a.sign(payload))) {
		return "", 0, false
	}
	ver, expStr, ok := strings.Cut(payload, ".")
	if !ok || ver != "v1" {
		return "", 0, false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	return sig, exp, err == nil
}

func (a *Auth) sign(payload string) string {
	m := hmac.New(sha256.New, a.key)
	m.Write(a.pwHash[:])
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// loginLimiter slows down password guessing per client address.
type loginLimiter struct {
	mu       sync.Mutex
	window   time.Duration
	max      int
	failures map[string][]time.Time
}

func newLoginLimiter(maxFailures int, window time.Duration) *loginLimiter {
	return &loginLimiter{window: window, max: maxFailures, failures: map[string][]time.Time{}}
}

// Attempt reserves a login attempt for client and reports whether it may
// proceed. Checking and recording happen under one lock, so parallel requests
// cannot all slip through before the first failure is recorded. Successful
// logins call Reset.
func (l *loginLimiter) Attempt(client string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.prune(client, now)
	if len(recent) >= l.max {
		return false
	}
	l.failures[client] = append(recent, now)
	if len(l.failures) > 10000 { // bound memory under address spraying
		for k := range l.failures {
			if k != client {
				delete(l.failures, k)
				break
			}
		}
	}
	return true
}

// Reset forgets the failures of client after a successful login.
func (l *loginLimiter) Reset(client string) {
	l.mu.Lock()
	delete(l.failures, client)
	l.mu.Unlock()
}

func (l *loginLimiter) prune(client string, now time.Time) []time.Time {
	kept := l.failures[client][:0]
	for _, t := range l.failures[client] {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	if len(kept) == 0 {
		delete(l.failures, client)
		return nil
	}
	l.failures[client] = kept
	return kept
}

// Secrets resolves the password and session key. Precedence for the password:
// explicit value, password file, generated value persisted in dataDir.
type Secrets struct {
	Password  string
	Key       []byte
	Generated bool // password was generated on this start and must be shown once
}

// LoadSecrets loads or creates the password and session key.
func LoadSecrets(password, passwordFile, dataDir string) (*Secrets, error) {
	s := &Secrets{Password: password}
	if s.Password == "" && passwordFile != "" {
		b, err := os.ReadFile(passwordFile)
		if err != nil {
			return nil, fmt.Errorf("read password file: %w", err)
		}
		s.Password = strings.TrimSpace(string(b))
	}
	if s.Password == "" {
		pw, created, err := readOrCreate(filepath.Join(dataDir, "password"), func() ([]byte, error) {
			return []byte(randomString(18)), nil
		})
		if err != nil {
			return nil, fmt.Errorf("generated password: %w", err)
		}
		s.Password, s.Generated = strings.TrimSpace(string(pw)), created
	}
	key, _, err := readOrCreate(filepath.Join(dataDir, "session.key"), func() ([]byte, error) {
		return []byte(randomString(48)), nil
	})
	if err != nil {
		return nil, fmt.Errorf("session key: %w", err)
	}
	s.Key = key
	return s, nil
}

func readOrCreate(path string, gen func() ([]byte, error)) ([]byte, bool, error) {
	b, err := os.ReadFile(path)
	if err == nil && len(strings.TrimSpace(string(b))) > 0 {
		return []byte(strings.TrimSpace(string(b))), false, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, false, err
	}
	b, err = gen()
	if err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return nil, false, err
	}
	return b, true, nil
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand never fails on supported platforms
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
