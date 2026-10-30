# ADR 0003: Isolated per-agent networks with an egress proxy

* Status: accepted
* Date: 2026-10-30

## Context

AI agents need Internet access (LLM APIs, package registries, git). They must not
reach the Docker host (ZimaOS web UI, SSH, SMB), devices in the home LAN, or
other agents. Prompt injection makes "the agent behaves" an invalid assumption.

### Finding during design audit

A container on a Docker network created with `internal: true` **can still reach
the host** through the bridge gateway address (traffic to the host goes through
the `INPUT` chain, which `internal` does not filter). Verified with Docker 29:
a TCP listener on the gateway IP was reachable from an internal network.

Creating the network with `com.docker.network.bridge.inhibit_ipv4=true` stops the
engine from assigning an IPv4 address to the bridge on the host. Combined with
`internal: true`, containers have **no default route and no host address** on
that segment. Verified: host, `docker0` and other networks unreachable; container
DNS for peers on the same network still works.

## Decision

* Each agent box gets its own network `dockerbox-net-<name>`
  (`internal: true`, `inhibit_ipv4=true`, IPv6 off). This also isolates agents
  from each other.
* IPv6 is switched off **inside** every box (`net.ipv6.conf.{all,default}.disable_ipv6=1`)
  whenever the kernel supports IPv6, so no link-local (`fe80::`) path to the host
  bridge can exist, independent of the Docker version. On kernels without IPv6 the
  sysctl is omitted (setting it would make the box fail to start).
* The `egress` service is attached to every agent network with the alias
  `dockerbox-egress` and to one normal bridge network for outbound traffic.
* Boxes get `HTTP_PROXY`/`HTTPS_PROXY`/`NO_PROXY` (upper and lower case) and
  `NODE_USE_ENV_PROXY=1`; apt is configured explicitly.
* The proxy resolves names itself, checks every resolved address against a
  deny-list of non-public ranges, and dials the checked address.
* `DOCKERBOX_EGRESS_ALLOW_CIDRS` can allow selected LAN targets on purpose
  (e.g. `192.168.1.50:11434` for a local LLM server; a bare address opens all ports).
* Per-box connection cap and tunnel idle timeout keep one box from starving the
  others; an optional port allow-list limits abuse.
* The broker API is protected by a random token shared only with web, so
  other processes on the compose network cannot drive it.

## Consequences

* + No iptables changes on the host, works on stock ZimaOS.
* + Agents cannot even send packets to the host or LAN; the proxy is the only exit.
* − Tools that ignore proxy variables (raw TCP, ICMP `ping`, SSH without
  `ProxyCommand`) do not work. Documented with workarounds (git over HTTPS,
  `ssh -o ProxyCommand='nc -X connect -x dockerbox-egress:3128 %h %p'`).
* − Docker's default address pools limit the number of networks (≈30 on a
  default engine). Sufficient for the target hardware; documented.
