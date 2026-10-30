# Operations

## Logs

All services log JSON lines to stdout:

```bash
docker compose logs -f web broker egress
```

Useful events: `agent created`, `agent deleted`, `terminal opened`,
`login failed`, `egress denied` (an agent tried to reach a private address).

## Updating

1. Update the image tags (`DOCKERBOX_VERSION`, `DOCKERBOX_AGENT_IMAGE`) or pull the
   new `docker-compose.yml`, then `docker compose up -d`.
2. Existing boxes keep running on their old image. Use **Re-create from image** in
   the dashboard to move a box to the new image. `/root` and `/workspace` are kept;
   packages installed outside these paths must be reinstalled.

## Backup

Agent data lives in Docker volumes:

* `dockerbox-agent-<name>-home` → `/root`
* `dockerbox-agent-<name>-workspace` → `/workspace`

Back them up with your usual volume backup tool, or:

```bash
docker run --rm -v dockerbox-agent-NAME-workspace:/v -v "$PWD":/backup alpine \
  tar czf /backup/NAME-workspace.tgz -C /v .
```

## Removing everything

```bash
docker compose down -v
docker ps -aq --filter label=dockerbox.managed=true | xargs -r docker rm -f
docker network ls -q --filter label=dockerbox.managed=true | xargs -r docker network rm
docker volume ls -q --filter label=dockerbox.managed=true | xargs -r docker volume rm
```

## Troubleshooting

| Symptom | Cause / fix |
|---------|-------------|
| "egress proxy is not running" when creating an agent | The `egress` container is stopped or unhealthy; check its logs. |
| `could not find an available, non-overlapping IPv4 address pool` | Docker ran out of network address pools (each box has its own network). Remove unused networks or extend `default-address-pools` in the Docker daemon config. |
| A tool inside the box cannot connect | It probably ignores `HTTP_PROXY`/`HTTPS_PROXY`. Configure its proxy setting to `http://dockerbox-egress:3128`. ICMP (`ping`) does not work by design. |
| `403` from `dockerbox-egress` | The destination resolves to a private address. Allow it explicitly with `DOCKERBOX_EGRESS_ALLOW_CIDRS` if intended. |
| Console shows "disconnected" after a restart | It reconnects automatically within a few seconds; or press **Reconnect**. |
