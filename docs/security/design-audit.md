# Pre-implementation design audit

Before any code was written the design was audited for security, operability on
ZimaOS and performance on low-power hardware. This document records the findings
and how the design was changed. Implementation reviews are recorded in the pull
requests.

Audit checklist: OWASP ASVS 4.0 (L1/L2 subset relevant to a self-hosted admin UI),
CIS Docker Benchmark v1.6 section 5 (container runtime), OWASP Docker Security
Cheat Sheet, plus empirical checks against a real Docker Engine (29.x).

## Findings

| # | Severity | Finding | Resolution |
|---|----------|---------|------------|
| F1 | Critical | Initial concept "Docker inside the dashboard container" requires `--privileged`; a root agent could trivially escape (CIS 5.4). | Replaced by sibling containers via a broker ([ADR 0001](../adr/0001-sibling-containers-instead-of-docker-in-docker.md)). |
| F2 | Critical | Mounting `docker.sock` into the web UI makes every web bug a host-root bug (CIS 5.31). | Socket only in the broker, high-level API, policy validation ([ADR 0002](../adr/0002-policy-broker-holds-the-docker-socket.md)). |
| F3 | High | **Empirical:** containers on a Docker `internal` network can still connect to services on the host via the bridge gateway IP. An agent could reach the ZimaOS UI, SSH or SMB. | Per-agent networks with `inhibit_ipv4=true` → no host address, no route. Re-tested: host and `docker0` unreachable ([ADR 0003](../adr/0003-egress-proxy-and-network-isolation.md)). |
| F4 | High | A forward proxy that checks the *hostname* is vulnerable to DNS rebinding and to names resolving to private IPs. | Proxy resolves itself, filters every IP (incl. IPv4-mapped IPv6, NAT64, 6to4) and dials the checked IP. |
| F5 | High | A shared network for all agents allows agent-to-agent attacks. | One network per agent. |
| F6 | Medium | Default Docker capability set contains `NET_RAW` (ARP/ICMP spoofing) and `MKNOD`, `SETFCAP`, `AUDIT_WRITE` that agents do not need (CIS 5.3). | Explicit allow-list of 10 capabilities. |
| F7 | Medium | No memory/PID limits → one runaway agent freezes the ZimaBoard (CIS 5.10, 5.11, 5.28). | Memory, swap, CPU and PID limits enforced and clamped server-side. |
| F8 | Medium | Unbounded container logs fill the eMMC. | `json-file` with `max-size=5m`, `max-file=2` per box. |
| F9 | Medium | Generated default passwords like `changeme` in compose files are a common takeover vector. | Password from env/secret file or a 24-character (144-bit) random value generated on first start and stored in the data volume (printed once to the log). |
| F10 | Medium | Cookie auth + JSON API is CSRF-prone; WebSockets ignore SameSite in some browsers (cross-site WebSocket hijacking). | `SameSite=Strict`, custom header requirement, strict `Origin` check on WebSocket upgrade. |
| F11 | Medium | Loading xterm.js from a CDN leaks usage to third parties and enables supply-chain injection. | Vendored at build time from a lockfile; strict CSP (`script-src 'self'`). |
| F12 | Low | `latest` image tags break reproducibility and ZimaOS app store guidelines. | Compose pins versioned tags; release workflow publishes semver tags. |
| F13 | Low | Agents lose installed packages and data when the image is updated. | `/root` and `/workspace` as named volumes; "recreate" action keeps them. |
| F14 | Low | Docker SDK pulls in a large dependency tree. | Raw Engine API client ([ADR 0004](../adr/0004-go-single-binary-and-minimal-dependencies.md)). |
| F15 | Info | Services themselves should be hardened (CIS 5.12, 5.25). | All three services: `read_only`, `cap_drop: [ALL]`, `no-new-privileges`, memory/PID limits, healthchecks; web and egress run as non-root. |
| F16 | Info | Repository will be public; personal data must not leak (R8). | Generic copyright holder, no hostnames/IPs/e-mails in examples, gitleaks in CI, commits authored with no-reply identities. |

## Findings during implementation

| # | Severity | Finding | Resolution |
|---|----------|---------|------------|
| F17 | Medium | Integration test: a fixed `nofile` ulimit (65536) above the daemon's hard limit makes boxes fail to start on some hosts (`error setting rlimit type 7`). | Removed; boxes inherit the engine default. PID limit still bounds resource use. |
| F18 | Low | UI test: the (empty) terminal layer covered the power button and swallowed clicks. | `pointer-events` only on visible terminals; covered by the Playwright walkthrough. |

## Implementation review

Code review of the first implementation; all findings fixed in the same PR with
regression tests.

| # | Severity | Finding | Resolution |
|---|----------|---------|------------|
| R1 | High | Object names were not instance-scoped; a second instance could mount a kept volume of the first. | Instance-scoped names, ownership check of existing volumes before create/recreate. |
| R2 | High | Egress allowed the host's own public IP (VPS / hairpin NAT). | `DOCKERBOX_EGRESS_DENY_CIDRS`, deny wins over allow; threat I5. |
| R3 | Medium | Reconciler raced with delete and could resurrect deleted networks. | Reconciler holds the lifecycle lock and never creates networks. |
| R4 | Medium | Recreate removed the old container before the new one was running. | Old container is stopped and parked under a temporary name, restored on failure. |
| R5 | Medium | Login limiter was check-then-act (parallel guesses bypassed it). | Single atomic `Attempt` per request. |
| R6 | Low | Policy only rejected `label:disable`, not `label=disable`. | Both spellings normalised. |
| R7 | Low | Console fallback discarded bash stderr. | Explicit `command -v` chain. |
| R8 | Medium | Open consoles survived logout and session expiry. | Token revocation on logout; consoles re-verify every 30 s. |
| R9 | Low | IPv6 broker URL produced a malformed dial address. | Use `Hostname()` when adding the default port. |
| R10 | Low | Unused drain WaitGroups and `ContainerInspect` suggested guarantees that did not exist. | Removed. |

## Independent security review

A separate security review (STRIDE focus areas: box escape, egress bypass,
broker influence, auth/session/CSRF/WebSocket, XSS, Docker API path injection,
secret logging, compose settings) found **no vulnerabilities at or above
confidence 8/10**. One item below the bar was turned into a hard guarantee:

| # | Finding | Resolution |
|---|---------|------------|
| S1 | A box might reach the host via IPv6 link-local addresses on older Docker engines. | IPv6 disabled inside every box when the kernel supports it (threat I6); integration test asserts the box has no IPv6 address at all. |

The `Secure` cookie flag is only set over HTTPS by design; README and ZimaOS
guide recommend VPN/HTTPS reverse proxy for remote access.

## Pre-release review

Two independent full reviews (security audit and code quality) of the complete
code base before the first public release. No Critical or High security issues.
All findings below were fixed with tests unless marked as documented.

| # | Severity | Finding | Resolution |
|---|----------|---------|------------|
| P1 | Medium | Host-network processes could reach the unauthenticated broker API via the `control` bridge gateway. | Broker API requires a token shared only with web (see Z4); integration test calls the broker from the host and expects 401. |
| P2 | Medium | One box could exhaust all proxy slots (no per-client cap, no idle timeout). | Per-box cap, tunnel idle timeout. |
| P3 | Medium | Allow-list entries opened every port of a LAN host (e.g. the ZimaOS host itself). | `ADDR:PORT` entries, optional port allow-list, docs warn. |
| P4 | Medium | Recreate parked the old container under a name inside the agent name space; leftovers blocked recreate and kept "deleted" data. | Park name with `.`; leftovers removed by recreate/delete; volume removal errors reported. |
| P5 | Medium | Lifecycle operations were cancelled when the browser disconnected, leaving orphans. | Broker runs them detached from the request with own timeouts. |
| P6 | Medium | UI moved keyboard focus into the console every 4 s. | Focus only on selection change. |
| P7 | Medium | Release workflow had broad permissions, tag-pinned actions and no test gate. | Per-job least privilege, all actions SHA-pinned, `verify` job before publishing. |
| P8 | Low | Login limit per IPv6 address (bypass by rotating within a /64); forced logout without CSRF header; password minimum 8. | Limit per /64, CSRF header on logout, minimum 12. |
| P9 | Low | Settings storage errors returned as 400 with internal paths. | Typed validation errors; storage errors are logged 500s. |
| P10 | Low | IPv4-compatible / SIIT IPv6 forms not denied. | Added to the deny-list. |
| P11 | Low | CPU limits above the host's CPU count produced Docker errors; recreate never pulled newer images. | CPUs clamped to the host; recreate always pulls. |
| P12 | Low | Integration tests used the default instance and could interfere with a real installation. | Tests run as instance `it`. |
| P13 | Low | UI details: busy labels, delete dialog bound to selection, dialogs on session expiry, settings save on close, menu keyboard navigation, colour-only state, contrast. | Fixed. |
| P14 | Info | Disk quota, cookie scope per host name, shared kernel/runc versions, static `DENY_CIDRS`. | Documented as residual risks (threat model, README, ZimaOS guide). |

## Field test on ZimaOS

| # | Severity | Finding | Resolution |
|---|----------|---------|------------|
| Z1 | High (availability) | ZimaOS rejected `${VAR:-default}` in the compose file. | Literal compose values; CI rejects interpolation. |
| Z2 | High (availability) | ZimaOS creates volume directories as `root:root`; the non-root dashboard could not write `/data`. | Start as root with `CHOWN`/`SETUID`/`SETGID` only, take ownership of `/data`, drop to uid 65532 with no capabilities; reproduced and tested with a root-owned host directory. |

| Z3 | High (availability) | ZimaOS did not connect `web` and `broker` through the custom compose network ("broker unreachable"). | Unix socket in a shared volume instead of a network; no custom networks in the compose file; egress lookup also via compose service labels. |

| Z4 | High (availability) | ZimaOS rejects socket files inside app volumes when settings are saved. | Back to the compose network; broker API requires a 256-bit token instead; data under `/DATA/AppData/dockerbox`. |

| Z5 | High (availability) | ZimaOS runs the services on Docker's default bridge: the name `broker` does not resolve. | Broker publishes its address in the shared data directory and attaches web to its network if needed; CI runs all integration tests with this layout. |

## Performance review (ZimaBoard 2, N150, 8–16 GiB)

* Idle cost per agent box: `tini` + `sleep` ≈ 1 MiB RSS, 0 % CPU. tmux only after
  the first console is opened.
* Services: Go binaries, ≈10 MiB RSS each; no database.
* No nested container storage (DinD would duplicate image layers and run a
  second daemon per box).
* Stats polling uses Docker's `one-shot` stats mode (no 1 s blocking sample) and
  only for the agent shown on screen.
