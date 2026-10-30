package broker

import (
	"fmt"
	"maps"
	"regexp"

	"github.com/pyzyi/dockerbox/internal/dockerapi"
)

// Labels and names shared by all Docker objects Dockerbox creates.
const (
	ObjectPrefix   = "dockerbox"
	LabelManaged   = "dockerbox.managed"
	LabelInstance  = "dockerbox.instance"
	LabelKind      = "dockerbox.kind"
	LabelAgent     = "dockerbox.agent"
	LabelRole      = "dockerbox.role"
	KindAgent      = "agent"
	KindNetwork    = "network"
	KindVolume     = "volume"
	RoleEgress     = "egress"
	EgressAlias    = "dockerbox-egress"
	OptInhibitIPv4 = "com.docker.network.bridge.inhibit_ipv4"
)

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,30}$`)

// ValidName reports whether name is an acceptable agent name. Names end up in
// container, network and volume names, so the alphabet is deliberately narrow.
func ValidName(name string) bool { return nameRE.MatchString(name) }

// prefix scopes Docker object names to the instance so that several
// installations on one host never share containers, networks or volumes. The
// default instance keeps the short "dockerbox-" names. Instance names cannot
// contain "_", so "dockerbox_<instance>_" cannot collide with other prefixes.
func (c *Config) prefix() string {
	if c.Instance == "default" {
		return ObjectPrefix + "-"
	}
	return ObjectPrefix + "_" + c.Instance + "_"
}

// ContainerName returns the Docker container name of an agent box.
func (c *Config) ContainerName(agent string) string { return c.prefix() + "agent-" + agent }

// NetworkName returns the isolated network name of an agent box.
func (c *Config) NetworkName(agent string) string { return c.prefix() + "net-" + agent }

// VolumeNames returns the names of the home and workspace volumes.
func (c *Config) VolumeNames(agent string) (home, workspace string) {
	base := c.prefix() + "agent-" + agent
	return base + "-home", base + "-workspace"
}

func (c *Config) labels(kind, agent string) map[string]string {
	return map[string]string{
		LabelManaged:  "true",
		LabelInstance: c.Instance,
		LabelKind:     kind,
		LabelAgent:    agent,
	}
}

// managedFilter returns label filters matching objects of this instance.
func (c *Config) managedFilter(kind string) []string {
	return []string{LabelManaged + "=true", LabelInstance + "=" + c.Instance, LabelKind + "=" + kind}
}

func (c *Config) isManaged(labels map[string]string, kind string) bool {
	return labels[LabelManaged] == "true" && labels[LabelInstance] == c.Instance && labels[LabelKind] == kind
}

// NetworkSpec builds the isolated network of an agent box.
func (c *Config) NetworkSpec(agent string) *dockerapi.NetworkSpec {
	return &dockerapi.NetworkSpec{
		Name:     c.NetworkName(agent),
		Driver:   "bridge",
		Internal: true,
		Options: map[string]string{
			OptInhibitIPv4: "true",
			"com.docker.network.bridge.enable_ip_masquerade": "false",
		},
		Labels: c.labels(KindNetwork, agent),
	}
}

// ContainerSpec builds the hardened container spec of an agent box.
func (c *Config) ContainerSpec(agent string, memoryMB int, cpus float64) *dockerapi.ContainerSpec {
	network := c.NetworkName(agent)
	home, workspace := c.VolumeNames(agent)
	mem := int64(memoryMB) << 20
	pids := c.PidsLimit
	stop := 10
	proxy := fmt.Sprintf("http://%s:%d", EgressAlias, c.EgressPort)
	noProxy := "localhost,127.0.0.1,::1"

	var sysctls map[string]string
	if c.DisableIPv6 {
		sysctls = maps.Clone(AllowedSysctls)
	}
	volLabels := &dockerapi.VolumeOptions{Labels: c.labels(KindVolume, agent)}
	return &dockerapi.ContainerSpec{
		Image:    c.AgentImage,
		Hostname: agent,
		Env: []string{
			"DOCKERBOX_AGENT=" + agent,
			"HTTP_PROXY=" + proxy, "HTTPS_PROXY=" + proxy,
			"http_proxy=" + proxy, "https_proxy=" + proxy,
			"NO_PROXY=" + noProxy, "no_proxy=" + noProxy,
			"NODE_USE_ENV_PROXY=1",
			"TERM=xterm-256color", "LANG=C.UTF-8",
		},
		Labels:      c.labels(KindAgent, agent),
		WorkingDir:  "/workspace",
		StopTimeout: &stop,
		HostConfig: dockerapi.HostConfig{
			NetworkMode: network,
			CapDrop:     []string{"ALL"},
			CapAdd:      append([]string(nil), AllowedCapabilities...),
			SecurityOpt: []string{"no-new-privileges:true"},
			Mounts: []dockerapi.Mount{
				{Type: "volume", Source: home, Target: "/root", VolumeOptions: volLabels},
				{Type: "volume", Source: workspace, Target: "/workspace", VolumeOptions: volLabels},
			},
			Sysctls:    sysctls,
			Runtime:    c.Runtime,
			Memory:     mem,
			MemorySwap: mem,
			NanoCpus:   int64(cpus * 1e9),
			PidsLimit:  &pids,
			ShmSize:    64 << 20,
			// No explicit nofile ulimit: values above the daemon's hard limit make
			// the box fail to start on some hosts; the engine default is sufficient.
			// Boxes come back after a reboot unless the user stopped them.
			RestartPolicy: dockerapi.RestartPolicy{Name: "unless-stopped"},
			LogConfig: &dockerapi.LogConfig{
				Type:   "json-file",
				Config: map[string]string{"max-size": "5m", "max-file": "2"},
			},
		},
		NetworkingConfig: &dockerapi.NetworkingConfig{
			EndpointsConfig: map[string]dockerapi.EndpointSettings{network: {}},
		},
	}
}

// TerminalExec is the command run for an interactive console: attach to (or
// create) the persistent tmux session, falling back to a login shell if the
// user removed tmux.
func TerminalExec(rows, cols int) *dockerapi.ExecSpec {
	spec := &dockerapi.ExecSpec{
		AttachStdin: true, AttachStdout: true, AttachStderr: true, Tty: true,
		Cmd: []string{"/bin/sh", "-c",
			"if command -v tmux >/dev/null 2>&1; then exec tmux new-session -A -s main; fi; " +
				"if command -v bash >/dev/null 2>&1; then exec bash -l; fi; exec sh -l"},
		Env:        []string{"TERM=xterm-256color", "LANG=C.UTF-8"},
		User:       "root",
		WorkingDir: "/workspace",
	}
	if rows > 0 && cols > 0 {
		spec.ConsoleSize = &[2]int{rows, cols}
	}
	return spec
}
