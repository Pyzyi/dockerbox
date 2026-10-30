# Configuration

All settings are environment variables of the three services. The published
`docker-compose.yml` contains literal values (ZimaOS does not support
`${VAR:-default}` interpolation): edit them in the file or in the ZimaOS app
settings.

## Compose file

| Setting | Default | Where |
|---------|---------|-------|
| Dashboard port | `8420` | `web` → `ports` → `published` |
| Image version | `VERSION` | `image:` of all three services and `DOCKERBOX_AGENT_IMAGE` of `broker` |
| Time zone | `UTC` | `web` → `TZ` |
| Data directories | `/DATA/AppData/dockerbox/{data,broker}` | bind mounts of `web` and `broker` |

## `web`

| Variable | Default | Description |
|----------|---------|-------------|
| `DOCKERBOX_PASSWORD` | – | Dashboard password, at least 12 characters. |
| `DOCKERBOX_PASSWORD_FILE` | – | Read the password from a file (Docker secrets). |
| | | If neither is set, a 24-character password is generated, stored in `/data/password` and logged once. |
| `DOCKERBOX_SESSION_TTL` | `24h` | Session lifetime (Go duration, e.g. `8h`). Changing the password invalidates all sessions; logging out revokes the session and closes its open consoles within 30 s. |
| `DOCKERBOX_SECURE_COOKIE` | `auto` | `auto` sets the `Secure` flag when served over HTTPS (also via `X-Forwarded-Proto`); `true`/`false` to force. |
| `DOCKERBOX_BROKER_URL` | `http://broker:8081` | Broker address (compose service name). |
| `DOCKERBOX_BROKER_TOKEN_FILE` | `/run/dockerbox/broker.token` | API token created by the broker; web reads it at start. |
| `DOCKERBOX_BROKER_ADDR_FILE` | `/run/dockerbox/broker.addr` | Address the broker publishes (needed on ZimaOS, where service names do not resolve); used instead of `DOCKERBOX_BROKER_URL` when present. |
| `DOCKERBOX_LISTEN` | `:8080` | Listen address inside the container. |
| `DOCKERBOX_DATA_DIR` | `/data` | Where the generated password and session key are stored. |

## `broker`

| Variable | Default | Description |
|----------|---------|-------------|
| `DOCKERBOX_AGENT_IMAGE` | `ghcr.io/pyzyi/dockerbox-agentbox:<version>` | Image for new boxes. Pulled automatically. |
| `DOCKERBOX_AGENT_RUNTIME` | empty (`runc`) | `runsc` (gVisor) or `sysbox-runc`, only if installed on the host. |
| `DOCKERBOX_MAX_AGENTS` | `10` | Maximum number of boxes. |
| `DOCKERBOX_DEFAULT_MEMORY_MB` | `2048` | Default memory per box. |
| `DOCKERBOX_MAX_MEMORY_MB` | `8192` | Upper limit per box (requests are clamped). |
| `DOCKERBOX_DEFAULT_CPUS` | `2` | Default CPU cores per box. |
| `DOCKERBOX_MAX_CPUS` | `4` | Upper limit per box. |
| `DOCKERBOX_PIDS_LIMIT` | `2048` | Maximum number of processes per box. |
| `DOCKERBOX_INSTANCE` | `default` | Separates several Dockerbox installations on one host (labels and object names, e.g. `dockerbox_lab_agent-x`). Must match the `dockerbox.instance` label of the `egress` service. |
| `DOCKERBOX_EGRESS_PORT` | `3128` | Port of the egress proxy as seen from the boxes. |
| `DOCKERBOX_DOCKER_SOCKET` | `/var/run/docker.sock` | Docker Engine socket. |
| `DOCKERBOX_BROKER_LISTEN` | `:8081` | Broker listen address (not published). Every API call needs the token from `DOCKERBOX_BROKER_TOKEN_FILE`. |

`DOCKERBOX_MAX_CPUS` / `DOCKERBOX_DEFAULT_CPUS` are lowered automatically to the
host's CPU count. The broker logs a warning on Docker Engine versions older than 26.

To run a second installation on the same host, change `DOCKERBOX_INSTANCE` **and**
the `dockerbox.instance` labels of all three services in your compose file, plus
the compose project name and port.

## `egress`

| Variable | Default | Description |
|----------|---------|-------------|
| `DOCKERBOX_EGRESS_ALLOW_CIDRS` | empty | Comma-separated private targets boxes may reach. Use `ADDR:PORT` (`192.168.1.50:11434`, `[fd00::5]:8080`) to open one service; a bare address or CIDR opens **every port** of those hosts (never list the Dockerbox host itself that way: boxes could reach its admin UI, SSH, SMB, …). |
| `DOCKERBOX_EGRESS_ALLOWED_PORTS` | empty (all) | Comma-separated destination ports boxes may use, e.g. `80,443,22`. Reduces abuse (spam, scanning) from your IP. |
| `DOCKERBOX_EGRESS_MAX_PER_AGENT` | `64` | Concurrent proxy connections per box, so one box cannot starve the others. |
| `DOCKERBOX_EGRESS_IDLE_TIMEOUT` | `15m` | Tunnels without traffic in either direction are closed after this time. |
| `DOCKERBOX_EGRESS_DENY_CIDRS` | empty | Comma-separated **public** addresses boxes must not reach. Set this to the server's own public/WAN IP if it has one (VPS, router with NAT hairpinning), otherwise agents could reach services the server publishes on that IP. Wins over the allow-list. |
| `DOCKERBOX_EGRESS_TRANSPARENT` | `true` | Forward direct connections of programs that ignore the proxy settings (DNS on port 53, TLS on 443, HTTP on 80; needs the `cap_add` of the published compose file). `false` turns it off; such programs then cannot connect. |
| `DOCKERBOX_EGRESS_MAX_CONNECTIONS` | `512` | Concurrent connections through the proxy. |
| `DOCKERBOX_EGRESS_LISTEN` | `:3128` | Listen address. |

## Appearance settings

Colours, background, glass transparency, console font size and language are set
in the dashboard (**Settings**) and stored in `/data/settings.json` in the `web`
data volume. They apply to every browser. All values are validated on the server
(enumerations, `#rrggbb` colours, numeric ranges); an invalid file falls back to
the defaults.

## All services

| Variable | Default | Description |
|----------|---------|-------------|
| `DOCKERBOX_LOG_LEVEL` | `info` | `debug` for verbose logs. Logs are JSON on stdout. |
