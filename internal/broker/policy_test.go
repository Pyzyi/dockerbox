package broker

import (
	"errors"
	"testing"

	"github.com/pyzyi/dockerbox/internal/dockerapi"
)

func testConfig() *Config {
	return &Config{
		Instance: "default", AgentImage: "example/agent:1", MaxAgents: 3,
		DefaultMemoryMB: 1024, MaxMemoryMB: 4096, DefaultCPUs: 1, MaxCPUs: 2,
		PidsLimit: 1024, EgressPort: 3128,
	}
}

func TestGeneratedSpecPassesPolicy(t *testing.T) {
	cfg := testConfig()
	spec := cfg.ContainerSpec("alice", 1024, 1.5)
	home, ws := cfg.VolumeNames("alice")
	if err := Validate(spec, cfg.NetworkName("alice"), home, ws); err != nil {
		t.Fatalf("generated spec must be valid: %v", err)
	}
	if err := ValidateNetwork(cfg.NetworkSpec("alice")); err != nil {
		t.Fatalf("generated network must be valid: %v", err)
	}
}

func TestPolicyRejectsUnsafeSpecs(t *testing.T) {
	pids := int64(10)
	yes := true
	cases := map[string]func(s *dockerapi.ContainerSpec){
		"privileged":  func(s *dockerapi.ContainerSpec) { s.HostConfig.Privileged = true },
		"sys_admin":   func(s *dockerapi.ContainerSpec) { s.HostConfig.CapAdd = append(s.HostConfig.CapAdd, "SYS_ADMIN") },
		"cap_prefix":  func(s *dockerapi.ContainerSpec) { s.HostConfig.CapAdd = append(s.HostConfig.CapAdd, "CAP_NET_ADMIN") },
		"no cap drop": func(s *dockerapi.ContainerSpec) { s.HostConfig.CapDrop = nil },
		"no nnp":      func(s *dockerapi.ContainerSpec) { s.HostConfig.SecurityOpt = nil },
		"seccomp unconfined": func(s *dockerapi.ContainerSpec) {
			s.HostConfig.SecurityOpt = append(s.HostConfig.SecurityOpt, "seccomp=unconfined")
		},
		"apparmor off": func(s *dockerapi.ContainerSpec) {
			s.HostConfig.SecurityOpt = append(s.HostConfig.SecurityOpt, "apparmor=unconfined")
		},
		"docker socket": func(s *dockerapi.ContainerSpec) {
			s.HostConfig.Binds = []string{"/var/run/docker.sock:/var/run/docker.sock"}
		},
		"bind mount": func(s *dockerapi.ContainerSpec) {
			s.HostConfig.Mounts = append(s.HostConfig.Mounts, dockerapi.Mount{Type: "bind", Source: "/", Target: "/host"})
		},
		"foreign volume": func(s *dockerapi.ContainerSpec) {
			s.HostConfig.Mounts = append(s.HostConfig.Mounts, dockerapi.Mount{Type: "volume", Source: "other-app-data", Target: "/x"})
		},
		"device": func(s *dockerapi.ContainerSpec) {
			s.HostConfig.Devices = []any{map[string]string{"PathOnHost": "/dev/sda"}}
		},
		"ports":         func(s *dockerapi.ContainerSpec) { s.HostConfig.PortBindings = map[string]any{"22/tcp": nil} },
		"publish all":   func(s *dockerapi.ContainerSpec) { s.HostConfig.PublishAllPorts = true },
		"pid host":      func(s *dockerapi.ContainerSpec) { s.HostConfig.PidMode = "host" },
		"ipc host":      func(s *dockerapi.ContainerSpec) { s.HostConfig.IpcMode = "host" },
		"uts host":      func(s *dockerapi.ContainerSpec) { s.HostConfig.UTSMode = "host" },
		"userns host":   func(s *dockerapi.ContainerSpec) { s.HostConfig.UsernsMode = "host" },
		"pid container": func(s *dockerapi.ContainerSpec) { s.HostConfig.PidMode = "container:dockerbox-broker" },
		"cgroupns host": func(s *dockerapi.ContainerSpec) { s.HostConfig.CgroupnsMode = "host" },
		"sysctl":        func(s *dockerapi.ContainerSpec) { s.HostConfig.Sysctls = map[string]string{"net.ipv4.ip_forward": "1"} },
		"extra hosts": func(s *dockerapi.ContainerSpec) {
			s.HostConfig.ExtraHosts = []string{"host.docker.internal:host-gateway"}
		},
		"host network":   func(s *dockerapi.ContainerSpec) { s.HostConfig.NetworkMode = "host" },
		"bridge network": func(s *dockerapi.ContainerSpec) { s.HostConfig.NetworkMode = "bridge" },
		"second network": func(s *dockerapi.ContainerSpec) {
			s.NetworkingConfig.EndpointsConfig["bridge"] = dockerapi.EndpointSettings{}
		},
		"unknown runtime": func(s *dockerapi.ContainerSpec) { s.HostConfig.Runtime = "nvidia-unsafe" },
		"no memory":       func(s *dockerapi.ContainerSpec) { s.HostConfig.Memory = 0 },
		"extra swap":      func(s *dockerapi.ContainerSpec) { s.HostConfig.MemorySwap = -1 },
		"no cpu":          func(s *dockerapi.ContainerSpec) { s.HostConfig.NanoCpus = 0 },
		"no pids":         func(s *dockerapi.ContainerSpec) { s.HostConfig.PidsLimit = nil },
		"zero pids":       func(s *dockerapi.ContainerSpec) { pids = 0; s.HostConfig.PidsLimit = &pids },
		"oom kill off":    func(s *dockerapi.ContainerSpec) { s.HostConfig.OomKillDisable = &yes },
		"no log rotation": func(s *dockerapi.ContainerSpec) { s.HostConfig.LogConfig = nil },
		"unmanaged":       func(s *dockerapi.ContainerSpec) { delete(s.Labels, LabelManaged) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig()
			spec := cfg.ContainerSpec("alice", 1024, 1)
			mutate(spec)
			home, ws := cfg.VolumeNames("alice")
			err := Validate(spec, cfg.NetworkName("alice"), home, ws)
			if !errors.Is(err, ErrPolicy) {
				t.Fatalf("expected policy violation, got %v", err)
			}
		})
	}
}

func TestIPv6IsDisabledWhenKernelSupportsIt(t *testing.T) {
	cfg := testConfig()
	cfg.DisableIPv6 = true
	spec := cfg.ContainerSpec("alice", 1024, 1)
	if spec.HostConfig.Sysctls["net.ipv6.conf.all.disable_ipv6"] != "1" ||
		spec.HostConfig.Sysctls["net.ipv6.conf.default.disable_ipv6"] != "1" {
		t.Fatalf("IPv6 not disabled: %v", spec.HostConfig.Sysctls)
	}
	home, ws := cfg.VolumeNames("alice")
	if err := Validate(spec, cfg.NetworkName("alice"), home, ws); err != nil {
		t.Fatalf("IPv6 sysctls must pass the policy: %v", err)
	}
	spec.HostConfig.Sysctls["net.ipv6.conf.all.disable_ipv6"] = "0"
	if err := Validate(spec, cfg.NetworkName("alice"), home, ws); !errors.Is(err, ErrPolicy) {
		t.Fatal("re-enabling IPv6 must be rejected")
	}
	cfg.DisableIPv6 = false
	if s := cfg.ContainerSpec("alice", 1024, 1); len(s.HostConfig.Sysctls) != 0 {
		t.Fatal("no sysctls may be set on kernels without IPv6")
	}
}

func TestPolicyRejectsUnisolatedNetworks(t *testing.T) {
	cases := map[string]func(n *dockerapi.NetworkSpec){
		"not internal":  func(n *dockerapi.NetworkSpec) { n.Internal = false },
		"host has ip":   func(n *dockerapi.NetworkSpec) { delete(n.Options, OptInhibitIPv4) },
		"ipv6":          func(n *dockerapi.NetworkSpec) { n.EnableIPv6 = true },
		"macvlan":       func(n *dockerapi.NetworkSpec) { n.Driver = "macvlan" },
		"inhibit false": func(n *dockerapi.NetworkSpec) { n.Options[OptInhibitIPv4] = "false" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			n := testConfig().NetworkSpec("alice")
			mutate(n)
			if err := ValidateNetwork(n); !errors.Is(err, ErrPolicy) {
				t.Fatalf("expected policy violation, got %v", err)
			}
		})
	}
}

func TestValidName(t *testing.T) {
	good := []string{"a", "agent-1", "0x", "abcdefghijklmnopqrstuvwxyz01234"}
	bad := []string{"", "-a", "A", "a_b", "a.b", "a/b", "../x", "abcdefghijklmnopqrstuvwxyz012345", "a b"}
	for _, n := range good {
		if !ValidName(n) {
			t.Errorf("%q should be valid", n)
		}
	}
	for _, n := range bad {
		if ValidName(n) {
			t.Errorf("%q should be invalid", n)
		}
	}
}

func TestClamp(t *testing.T) {
	c := testConfig()
	if got := c.clampMemory(0); got != 1024 {
		t.Errorf("default memory = %d", got)
	}
	if got := c.clampMemory(1 << 20); got != 4096 {
		t.Errorf("max memory = %d", got)
	}
	if got := c.clampMemory(1); got != 128 {
		t.Errorf("min memory = %d", got)
	}
	if got := c.clampCPUs(64); got != 2 {
		t.Errorf("max cpus = %v", got)
	}
	if got := c.clampCPUs(-1); got != 1 {
		t.Errorf("default cpus = %v", got)
	}
}

func TestConfigValidate(t *testing.T) {
	c := testConfig()
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	c.Runtime = "kata-unknown"
	c.Instance = "Bad Instance"
	c.DefaultMemoryMB = 99999
	if err := c.Validate(); err == nil {
		t.Fatal("expected errors")
	}
}
