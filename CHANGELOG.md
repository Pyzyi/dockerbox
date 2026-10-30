# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [1.0.1] - 2026-10-30

### Fixed

- ZimaOS: creating an agent failed with "broker unreachable" because ZimaOS
  does not connect the services through the custom compose networks
  (`lookup broker: no such host`). `web` and `broker` now talk over a unix
  socket in a shared volume - no network involved, and the broker has no
  network port at all anymore. The compose file uses no custom networks.
- The broker also finds the egress proxy as the `egress` service of its own
  compose project when platform-specific label handling hides the
  `dockerbox.role` label.

## [1.0.0] - 2026-10-30

First public release.

- **Agent boxes:** one isolated Debian container per AI agent, root inside the
  box only; no Docker socket, no privileged mode, no host mounts; capability
  allow-list, `no-new-privileges`, memory/CPU/PID limits; persistent `/root`
  and `/workspace`; no AI agent pre-installed.
- **Network isolation:** one internal network per box without host address or
  route (`inhibit_ipv4`), IPv6 disabled inside boxes; Internet only through an
  egress proxy that denies private, host and special-purpose addresses, with
  per-box limits, idle timeout, optional LAN allow-list (`ADDR:PORT`) and port
  allow-list.
- **Policy broker:** the only component with Docker access; high-level API,
  every container spec validated against a security policy, label- and
  instance-scoped objects, robust re-create/delete, unreachable from the host
  network.
- **Dashboard:** glass design (light/dark), agent list, power button, live
  console (persistent tmux session), pop-out windows, resource usage,
  personalisation (accent, background, transparency, font size, language),
  English and German; password login, signed sessions, CSRF and origin checks,
  strict CSP; runs unprivileged (takes ownership of its data directory, then
  drops to uid 65532 without capabilities).
- **ZimaOS:** literal `docker-compose.yml` with app metadata; works with
  root-owned app data directories.
- **Quality:** unit and integration tests with real escape attempts, CodeQL,
  govulncheck, Trivy, gitleaks; automatic multi-arch releases with SBOM and
  provenance; ISO/IEC 25010 quality model, threat model and design audit.

[1.0.1]: https://github.com/Pyzyi/dockerbox/releases/tag/v1.0.1
[1.0.0]: https://github.com/Pyzyi/dockerbox/releases/tag/v1.0.0
