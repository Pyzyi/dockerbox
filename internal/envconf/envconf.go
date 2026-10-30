// Package envconf reads typed configuration values from the environment and
// collects all parse errors so that misconfiguration is reported at once.
package envconf

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env reads values from the process environment.
type Env struct {
	lookup func(string) (string, bool)
	errs   []error
}

// New returns an Env backed by os.LookupEnv.
func New() *Env { return &Env{lookup: os.LookupEnv} }

// NewFromMap returns an Env backed by a map (for tests).
func NewFromMap(m map[string]string) *Env {
	return &Env{lookup: func(k string) (string, bool) { v, ok := m[k]; return v, ok }}
}

func (e *Env) get(key string) (string, bool) {
	v, ok := e.lookup(key)
	v = strings.TrimSpace(v)
	return v, ok && v != ""
}

// String returns the value of key or def.
func (e *Env) String(key, def string) string {
	if v, ok := e.get(key); ok {
		return v
	}
	return def
}

// Int returns the integer value of key or def.
func (e *Env) Int(key string, def int) int {
	v, ok := e.get(key)
	if !ok {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %q is not an integer", key, v))
		return def
	}
	return n
}

// Float returns the float value of key or def.
func (e *Env) Float(key string, def float64) float64 {
	v, ok := e.get(key)
	if !ok {
		return def
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %q is not a number", key, v))
		return def
	}
	return f
}

// Bool returns the boolean value of key or def.
func (e *Env) Bool(key string, def bool) bool {
	v, ok := e.get(key)
	if !ok {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		e.errs = append(e.errs, fmt.Errorf("%s: %q is not a boolean", key, v))
		return def
	}
	return b
}

// Duration returns the duration value of key or def.
func (e *Env) Duration(key string, def time.Duration) time.Duration {
	v, ok := e.get(key)
	if !ok {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil || d <= 0 {
		e.errs = append(e.errs, fmt.Errorf("%s: %q is not a positive duration", key, v))
		return def
	}
	return d
}

// List returns a comma-separated list value of key, or nil.
func (e *Env) List(key string) []string {
	v, ok := e.get(key)
	if !ok {
		return nil
	}
	var out []string
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

// Err returns all accumulated parse errors.
func (e *Env) Err() error { return errors.Join(e.errs...) }
