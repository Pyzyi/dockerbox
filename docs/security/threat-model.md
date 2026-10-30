# Threat model

Method: STRIDE per trust boundary. Scope: Dockerbox services, agent boxes and
their interaction with the Docker host. Out of scope: security of the LLM
providers, of the host OS itself, and of software the user installs inside a box.

## Assets

| Asset | Why it matters |
|-------|----------------|
| A1 Docker host (ZimaOS) | Root on the host = all data, all other apps. |
| A2 Home LAN | NAS shares, routers, IoT, other computers. |
| A3 Agent boxes and their volumes | Code, API keys for LLM providers stored by the user. |
| A4 Dashboard credentials / session | Grants control over all boxes. |

## Actors

| Actor | Capability |
|-------|-----------|
| T1 Malicious or hijacked agent (prompt injection, malicious package) | Root **inside one box**, arbitrary code. |
| T2 Attacker in the LAN | Can reach the dashboard port. |
| T3 Malicious website opened by the user | Can make the browser send cross-site requests. |
| T4 Compromised upstream dependency or image | Code in the build or runtime. |

## Trust boundaries

```
Browser ──(B1)── web ──(B2)── broker ──(B3)── Docker Engine (host root)
                                   agent box ──(B4)── egress ──(B5)── Internet
                                   agent box ──(B6)── host / LAN / other boxes  (must be impossible)
```

## STRIDE analysis

| ID | Boundary | Threat | Mitigation | Verified by |
|----|----------|--------|------------|-------------|
| S1 | B1 | Spoofing: LAN attacker logs in | Password (env/secret, min. 12 characters, or 24-character / 144-bit random), constant-time compare, login rate limit per IPv4 address or IPv6 /64, HMAC-signed `HttpOnly` `SameSite=Strict` cookie | `internal/web/server_test.go` |
| S2 | B1 | CSRF from malicious site (T3) | `SameSite=Strict`, mandatory custom header `X-Dockerbox-CSRF` on mutating API calls, WebSocket origin check | unit tests |
| T1 | B2 | Tampering: web sends crafted Docker spec | Broker has no passthrough; builds specs from template; `policy.Validate` | `internal/broker/policy_test.go` |
| E1 | B6 | Elevation: box escapes via privileged features | No `--privileged`, capability allow-list, `no-new-privileges`, default seccomp/AppArmor, no devices, no host namespaces, no bind mounts, no Docker socket | policy tests + integration test `test/integration` |
| E2 | B6 | Elevation: kernel exploit from root in box | Residual risk of all container solutions; reduced attack surface by dropped caps and seccomp. Optional gVisor (`runsc`). | documented |
| I1 | B6 | Box reaches host services (ZimaOS UI, SSH, SMB) | Per-agent `internal` network with `inhibit_ipv4`: no host IP, no default route | integration test |
| I2 | B4/B5 | Box uses proxy to reach LAN/host/metadata | Proxy resolves and checks every IP against deny-list; dials checked IP (no DNS rebinding) | `internal/egress/filter_test.go`, `proxy_test.go` |
| I5 | B5 | Box reaches the host through the host's own **public** IP (VPS, NAT hairpinning); traffic originates from the host, so cloud firewalls/security groups may not apply | `DOCKERBOX_EGRESS_DENY_CIDRS` for the host's public addresses; documented in configuration and ZimaOS guide | `filter_test.go` (deny wins) |
| S3 | B1 | Copied session cookie stays valid after logout; open console survives logout/expiry | Logout revokes the token; console WebSockets re-verify the session every 30 s and close | `server_test.go` |
| T3 | B3 | Two Dockerbox instances on one host share or hijack each other's volumes | Instance-scoped object names; existing volumes must carry this instance's and agent's labels | `service_test.go` |
| I6 | B6 | Box reaches the host via IPv6 link-local (`fe80::`) on the bridge, which `inhibit_ipv4` does not cover | IPv6 disabled inside every box via sysctl when the kernel supports IPv6; policy allows only these two sysctls with value 1 | integration test asserts no IPv6 address in the box; `policy_test.go` |
| T4 | B1 | Appearance settings inject CSS or markup into the dashboard of every viewer | Strict server-side validation (enumerations, `#rrggbb`, ranges, unknown fields rejected), re-validated in the browser before use; values only reach CSS custom properties via CSSOM, never HTML | `settings_test.go` |
| I3 | B6 | Box attacks another box | One network per box; boxes are never on a shared segment | integration test |
| D1 | host | Box exhausts RAM/CPU/PIDs/disk logs | Memory (no swap), CPU and PID limits, log rotation | policy tests |
| D2 | B4 | Box floods proxy and starves other boxes | Global and per-box connection limits, idle timeout for tunnels, optional port allow-list | `internal/egress/proxy_test.go` |
| E4 | B1 | Dashboard (`web`) runs with privileges | Starts as root only to `chown` its own data directory (no symlink following), then drops to uid 65532 with an empty capability set before serving any request | integration test `TestWebRunsUnprivilegedAndOwnsData` (`/proc` UID/GID and `CapEff`) |
| E3 | B2 | Another container or host process on the compose network drives the broker API | Every API call needs a 256-bit token created by the broker (root-only file shared with web); constant-time check; port not published | `TestBrokerAPIRequiresToken`, integration test `TestBrokerNotReachableFromOutside` |
| R1 | all | No trace of admin actions | Structured JSON logs for create/start/stop/delete and denied egress | review |
| I4 | repo | Personal data / secrets in the public repo | `.gitignore`, no defaults containing secrets, CI secret scan (gitleaks) | CI |
| T2 | build | Supply chain (T4) | Minimal deps, lockfiles, actions pinned by SHA, CodeQL, govulncheck, Trivy image scan, SBOM + provenance on release | CI |

## Residual risks (accepted)

* **Kernel vulnerabilities**: shared kernel is inherent to containers. Mitigation is
  keeping ZimaOS updated and optionally using gVisor.
* **Broker compromise**: equals host root. Kept minimal, its API only
  accepts requests carrying the token shared with `web`.
* **Disk usage**: boxes have no disk quota (overlay2 quotas need xfs with project
  quotas). A malicious agent can fill the Docker data disk. Documented; monitor
  free space.
* **Cookies are per host name, not per port**: other web apps on the same host
  name receive the session cookie. Recommended: dedicated host name + HTTPS
  reverse proxy for remote access.
* **Shared kernel and runc**: no user namespace remapping; root in a box is uid 0
  on the host kernel. Runtime CVEs (e.g. runc CVE-2024-21626) matter; keep Docker
  and ZimaOS updated, use gVisor (`runsc`) for agents you do not trust at all.
* **`DENY_CIDRS` is static**: a changing home WAN IP must be updated manually.
* **Secrets entered into a box** (LLM API keys) are readable by the agent in that box
  by design. Users should use scoped/limited API keys.
* **Logout revocation is in memory**: after a restart of `web`, a cookie copied
  before logout is valid again until its expiry (default 24 h). Changing the
  password invalidates all sessions permanently.
* **Data exfiltration to the Internet**: an agent with Internet access can upload
  whatever it can read in its own box. Out of scope by requirement (agents need
  Internet); a domain allow-list for the proxy is on the roadmap.
