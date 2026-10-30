# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project uses
[Semantic Versioning](https://semver.org/).

## [1.0.6] - 2026-10-30

### Fixed

- Programs that ignore `HTTP_PROXY`/`HTTPS_PROXY` (for example the Bun-based
  rabbit agent, whose WebSocket connected directly) can now reach the
  Internet. Inside a box every name resolves to the egress proxy, which
  forwards direct connections on ports 443 and 80 by TLS server name or HTTP
  `Host`, with the same rules as before: host, LAN and other boxes stay
  unreachable. The proxy starts as root only to open these ports and then
  drops all privileges.
- Cron jobs run inside boxes. Debian's cron requires `pam_loginuid`, which a
  container cannot use, so it silently dropped every job (including
  `@reboot` entries that installers use to start agents).

Update the app (new `docker-compose.yml`), then "Re-create from image" for
existing boxes.

## [1.0.5] - 2026-10-30

### Added

- Boxes understand `systemctl`. Installers that register a systemd service
  (`systemctl enable --now ...`) now just work: the service starts at once, is
  restarted according to `Restart=`, runs as `User=` with `Environment=` /
  `EnvironmentFile=` and the box's proxy settings, and enabled services start
  again whenever the box boots. Logs: `journalctl -u <service>`. Previously
  such services never started (boxes have no systemd), so agents showed as
  offline. Existing boxes get this with "Re-create from image"; then run the
  installer again.

## [1.0.4] - 2026-10-30

### Fixed

- Console: Ctrl+V (and Ctrl+Shift+V / Cmd+V) pastes from the clipboard; it was
  sent to the box as a control character. Ctrl+C copies when text is selected
  (Ctrl+Shift+C always), otherwise it still interrupts the running program.
  Works over plain HTTP.
- Console: no tmux right-click menu any more (it covered the browser's context
  menu with "Paste"). Mouse-wheel scrolling stays. Existing boxes get this with
  "Re-create from image".

## [1.0.3] - 2026-10-30

### Fixed

- ZimaOS: "broker unreachable" - ZimaOS runs the app's containers on Docker's
  default bridge, where the service name `broker` does not resolve. The broker
  now finds its own and the dashboard's network, attaches the dashboard to its
  network if needed, and publishes its address in
  `/DATA/AppData/dockerbox/broker/broker.addr`; the dashboard connects to that
  address. CI and releases now also run all integration tests with the ZimaOS
  network layout.

## [1.0.2] - 2026-10-30

### Fixed

- ZimaOS: saving the app settings failed with "unsupported deployment entry
  .../broker.sock" because ZimaOS scans volume directories and does not accept
  socket files. web and broker now use the normal compose network again; the
  broker API is protected by a random token (created by the broker, readable
  only by root, read by web before it drops its privileges) instead of by
  network isolation.
- App data now lives in `/DATA/AppData/dockerbox/` (ZimaOS convention):
  `data` for the dashboard, `broker` for the API token.

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

[1.0.4]: https://github.com/Pyzyi/dockerbox/releases/tag/v1.0.4
[1.0.3]: https://github.com/Pyzyi/dockerbox/releases/tag/v1.0.3
[1.0.2]: https://github.com/Pyzyi/dockerbox/releases/tag/v1.0.2
[1.0.1]: https://github.com/Pyzyi/dockerbox/releases/tag/v1.0.1
[1.0.0]: https://github.com/Pyzyi/dockerbox/releases/tag/v1.0.0
