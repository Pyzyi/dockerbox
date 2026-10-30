package broker

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/pyzyi/dockerbox/internal/dockerapi"
)

// AllowedCapabilities is the complete set of Linux capabilities an agent box
// receives. It is the Docker default set minus NET_RAW, MKNOD, AUDIT_WRITE and
// SETFCAP, which agents do not need (design audit F6).
var AllowedCapabilities = []string{
	"CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL",
	"SETGID", "SETUID", "SETPCAP", "NET_BIND_SERVICE", "SYS_CHROOT",
}

// AllowedSysctls are the only kernel parameters a box spec may set, and only
// to exactly these values: they switch IPv6 off inside the box.
var AllowedSysctls = map[string]string{
	"net.ipv6.conf.all.disable_ipv6":     "1",
	"net.ipv6.conf.default.disable_ipv6": "1",
}

// AllowedRuntimes are the OCI runtimes a box may use.
var AllowedRuntimes = []string{"", "runc", "runsc", "sysbox-runc"}

// ErrPolicy wraps every policy violation.
var ErrPolicy = errors.New("policy violation")

// Validate checks a container spec against the security policy. It is the last
// line of defence before a spec reaches the Docker Engine: even if the
// template in spec.go is changed by mistake, an unsafe box is never created.
// expectedNetwork is the isolated network the box must be attached to and
// expectedVolumes the only named volumes it may mount.
func Validate(spec *dockerapi.ContainerSpec, expectedNetwork string, expectedVolumes ...string) error {
	var errs []error
	fail := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("%w: "+format, append([]any{ErrPolicy}, args...)...))
	}
	h := spec.HostConfig

	if spec.Labels[LabelManaged] != "true" || spec.Labels[LabelKind] != KindAgent {
		fail("box must carry the managed agent labels")
	}
	if h.Privileged {
		fail("privileged mode is forbidden")
	}
	if !slices.Contains(h.CapDrop, "ALL") {
		fail("capabilities must be dropped with CapDrop ALL")
	}
	for _, c := range h.CapAdd {
		if !slices.Contains(AllowedCapabilities, strings.ToUpper(strings.TrimPrefix(c, "CAP_"))) {
			fail("capability %q is not allowed", c)
		}
	}
	if !slices.Contains(h.SecurityOpt, "no-new-privileges:true") {
		fail("no-new-privileges must be set")
	}
	for _, opt := range h.SecurityOpt {
		// Docker accepts both "key=value" and the legacy "key:value" spelling.
		norm := strings.ReplaceAll(strings.ToLower(opt), "=", ":")
		if strings.Contains(norm, "unconfined") || strings.HasPrefix(norm, "label:disable") {
			fail("security option %q is not allowed", opt)
		}
	}
	if len(h.Binds) > 0 {
		fail("bind mounts are forbidden")
	}
	for _, m := range h.Mounts {
		if m.Type != "volume" || !slices.Contains(expectedVolumes, m.Source) {
			fail("mount %q must be a dockerbox-managed named volume", m.Target)
		}
	}
	if len(h.Devices) > 0 {
		fail("devices are forbidden")
	}
	if len(h.PortBindings) > 0 || h.PublishAllPorts {
		fail("published ports are forbidden")
	}
	for name, mode := range map[string]string{
		"PidMode": h.PidMode, "IpcMode": h.IpcMode, "UTSMode": h.UTSMode,
		"UsernsMode": h.UsernsMode,
	} {
		if mode == "host" || strings.HasPrefix(mode, "container:") {
			fail("%s %q is forbidden", name, mode)
		}
	}
	if h.CgroupnsMode == "host" {
		fail("CgroupnsMode host is forbidden")
	}
	for k, v := range h.Sysctls {
		if want, ok := AllowedSysctls[k]; !ok || v != want {
			fail("sysctl %s=%s is not allowed", k, v)
		}
	}
	if len(h.ExtraHosts) > 0 {
		fail("extra hosts are forbidden")
	}
	if h.NetworkMode != expectedNetwork || expectedNetwork == "" {
		fail("network mode must be the isolated agent network")
	}
	if spec.NetworkingConfig != nil {
		for n := range spec.NetworkingConfig.EndpointsConfig {
			if n != expectedNetwork {
				fail("additional network %q is forbidden", n)
			}
		}
	}
	if !slices.Contains(AllowedRuntimes, h.Runtime) {
		fail("runtime %q is not allowed", h.Runtime)
	}
	if h.Memory <= 0 || h.MemorySwap != h.Memory {
		fail("memory limit must be set and swap must not exceed it")
	}
	if h.NanoCpus <= 0 {
		fail("CPU limit must be set")
	}
	if h.PidsLimit == nil || *h.PidsLimit <= 0 {
		fail("PID limit must be set")
	}
	if h.OomKillDisable != nil && *h.OomKillDisable {
		fail("disabling the OOM killer is forbidden")
	}
	if h.LogConfig == nil || h.LogConfig.Config["max-size"] == "" {
		fail("log rotation must be configured")
	}
	return errors.Join(errs...)
}

// ValidateNetwork checks that an agent network is isolated (ADR 0003).
func ValidateNetwork(spec *dockerapi.NetworkSpec) error {
	if !spec.Internal {
		return fmt.Errorf("%w: agent network must be internal", ErrPolicy)
	}
	if spec.Options[OptInhibitIPv4] != "true" {
		return fmt.Errorf("%w: agent network must not give the host an address", ErrPolicy)
	}
	if spec.EnableIPv6 {
		return fmt.Errorf("%w: IPv6 is not supported on agent networks", ErrPolicy)
	}
	if spec.Driver != "bridge" {
		return fmt.Errorf("%w: agent network must use the bridge driver", ErrPolicy)
	}
	return nil
}
