package broker

import (
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/pyzyi/dockerbox/internal/envconf"
)

// Config holds the broker settings. All values come from DOCKERBOX_* env vars.
type Config struct {
	Listen          string
	TokenFile       string
	DockerSocket    string
	Instance        string
	AgentImage      string
	Runtime         string
	MaxAgents       int
	DefaultMemoryMB int
	MaxMemoryMB     int
	DefaultCPUs     float64
	MaxCPUs         float64
	PidsLimit       int64
	EgressPort      int
	// DisableIPv6 turns IPv6 off inside every box. Set automatically when the
	// kernel supports IPv6, so boxes can never use link-local (fe80::) paths to
	// the host, independent of the Docker version. On kernels without IPv6 the
	// sysctl does not exist and must not be set.
	DisableIPv6 bool
	// SelfID is the broker's own container ID prefix (its hostname); used to
	// find the egress proxy of the same compose project.
	SelfID string
}

// LoadConfig reads the broker configuration from the environment.
func LoadConfig(defaultImage string) (*Config, error) {
	e := envconf.New()
	c := &Config{
		Listen:          e.String("DOCKERBOX_BROKER_LISTEN", ":8081"),
		TokenFile:       e.String("DOCKERBOX_BROKER_TOKEN_FILE", "/run/dockerbox/broker.token"),
		DockerSocket:    e.String("DOCKERBOX_DOCKER_SOCKET", "/var/run/docker.sock"),
		Instance:        e.String("DOCKERBOX_INSTANCE", "default"),
		AgentImage:      e.String("DOCKERBOX_AGENT_IMAGE", defaultImage),
		Runtime:         e.String("DOCKERBOX_AGENT_RUNTIME", ""),
		MaxAgents:       e.Int("DOCKERBOX_MAX_AGENTS", 10),
		DefaultMemoryMB: e.Int("DOCKERBOX_DEFAULT_MEMORY_MB", 2048),
		MaxMemoryMB:     e.Int("DOCKERBOX_MAX_MEMORY_MB", 8192),
		DefaultCPUs:     e.Float("DOCKERBOX_DEFAULT_CPUS", 2),
		MaxCPUs:         e.Float("DOCKERBOX_MAX_CPUS", 4),
		PidsLimit:       int64(e.Int("DOCKERBOX_PIDS_LIMIT", 2048)),
		EgressPort:      e.Int("DOCKERBOX_EGRESS_PORT", 3128),
		DisableIPv6:     kernelHasIPv6(),
	}
	c.SelfID, _ = os.Hostname()
	if err := e.Err(); err != nil {
		return nil, err
	}
	return c, c.Validate()
}

func kernelHasIPv6() bool {
	_, err := os.Stat("/proc/sys/net/ipv6")
	return err == nil
}

// Validate checks the configuration for impossible or unsafe values.
func (c *Config) Validate() error {
	var errs []error
	if c.AgentImage == "" {
		errs = append(errs, errors.New("DOCKERBOX_AGENT_IMAGE must not be empty"))
	}
	if !ValidName(c.Instance) {
		errs = append(errs, fmt.Errorf("DOCKERBOX_INSTANCE %q must match %s", c.Instance, nameRE))
	}
	if !slices.Contains(AllowedRuntimes, c.Runtime) {
		errs = append(errs, fmt.Errorf("DOCKERBOX_AGENT_RUNTIME %q not in %v", c.Runtime, AllowedRuntimes))
	}
	if c.MaxAgents < 1 {
		errs = append(errs, errors.New("DOCKERBOX_MAX_AGENTS must be >= 1"))
	}
	if c.MaxMemoryMB < 128 || c.DefaultMemoryMB < 128 || c.DefaultMemoryMB > c.MaxMemoryMB {
		errs = append(errs, errors.New("memory limits must be >= 128 MB and default <= max"))
	}
	if c.MaxCPUs < 0.1 || c.DefaultCPUs < 0.1 || c.DefaultCPUs > c.MaxCPUs {
		errs = append(errs, errors.New("CPU limits must be >= 0.1 and default <= max"))
	}
	if c.PidsLimit < 64 {
		errs = append(errs, errors.New("DOCKERBOX_PIDS_LIMIT must be >= 64"))
	}
	if c.EgressPort < 1 || c.EgressPort > 65535 {
		errs = append(errs, errors.New("DOCKERBOX_EGRESS_PORT out of range"))
	}
	return errors.Join(errs...)
}

// clampMemory returns a memory size within [128, MaxMemoryMB]; 0 means default.
func (c *Config) clampMemory(mb int) int {
	if mb <= 0 {
		return c.DefaultMemoryMB
	}
	return max(128, min(mb, c.MaxMemoryMB))
}

// clampCPUs returns a CPU count within [0.1, MaxCPUs]; 0 means default.
func (c *Config) clampCPUs(cpus float64) float64 {
	if cpus <= 0 {
		return c.DefaultCPUs
	}
	return max(0.1, min(cpus, c.MaxCPUs))
}
