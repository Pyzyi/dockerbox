# Installing on ZimaOS

Tested target: ZimaBoard 2 (Intel N150) with ZimaOS. Any ZimaOS/CasaOS device
with `amd64` or `arm64` works the same way.

## 1. Install the app

1. Open the ZimaOS dashboard → **App Store** → **Custom Install** (the `+` icon) →
   **Import**.
2. Paste the content of [`docker-compose.yml`](../docker-compose.yml) (or upload
   the file) and confirm.
3. In the settings of the `web` service, set **`DOCKERBOX_PASSWORD`** to a long
   password (min. 12 characters). Optional — if you leave it empty, see step 3.
   The web port (default `8420`) can be changed there as well.
4. Install. ZimaOS pulls the ~17 MB service image and starts three containers:
   `web`, `broker` and `egress`.

## 2. Open the dashboard

Click the Dockerbox icon or open `http://<zimaos-address>:8420`.

## 3. Generated password

If no password was set, open the app's **Logs** (container `web`) and look for the
line `generated a password`. The password is stored in the app's data volume and
stays the same across restarts. To change it later, set `DOCKERBOX_PASSWORD` in the
app settings.

## 4. Create your first agent

Click **Add agent**, choose a name, memory and CPU cores. The first box downloads
the agent image (~120 MB) once; later boxes start in under a second.

Inside the console you are `root`. Install the agent you want, for example:

```bash
apt-get update && apt-get install -y nodejs npm python3 python3-pip
# then follow the installation instructions of your agent
```

Run `dockerbox-help` for tips (proxy, SSH, persistence).

## Resource planning for a ZimaBoard 2

| RAM | Recommendation |
|-----|----------------|
| 8 GB | 2–3 agents with 1–2 GB each |
| 16 GB | 4–6 agents with 2 GB each |

Stopped boxes use no RAM. Running but idle boxes use < 1 MiB plus whatever the
agent itself keeps in memory. Adjust `DOCKERBOX_MAX_MEMORY_MB` to protect ZimaOS
itself.

## What you will see in the ZimaOS container list

Agent boxes are normal containers named `dockerbox-agent-<name>`. Manage them in
the Dockerbox dashboard, not in ZimaOS, so that their isolated network and egress
attachment stay consistent.

## Uninstalling

Uninstalling the app removes `web`, `broker` and `egress`, **not** the agent
boxes (they are created at runtime). Delete the agents in the dashboard first, or
see [operations.md](operations.md#removing-everything).

## Public IP of the server

ZimaOS at home normally has a private LAN address, which boxes cannot reach. If
your router forwards ports to the server (NAT hairpinning) or the server has a
public IP, add that public IP to `DOCKERBOX_EGRESS_DENY_CIDRS` on the `egress`
service, e.g. `203.0.113.10/32`.

## Local LLM in the LAN

Boxes cannot reach the LAN by default. To allow one service, e.g. an Ollama server
at `192.168.1.50`, set `DOCKERBOX_EGRESS_ALLOW_CIDRS=192.168.1.50:11434` on the
`egress` service. Agents then reach it through the proxy (`http://192.168.1.50:11434`).
Always include the port: a bare address opens every service of that host — if
Ollama runs on the ZimaOS box itself, that would include the ZimaOS UI and SSH.

## Security checklist

* Docker Engine 26 or newer (the broker logs a warning otherwise).
* A strong dashboard password; after the first start with a generated password,
  set your own via `DOCKERBOX_PASSWORD` (the generated one was printed in the log).
* Do not forward port 8420 on your router. For remote access use a VPN, or an
  HTTPS reverse proxy on its own hostname with `DOCKERBOX_SECURE_COOKIE=true`.
* Give agents dedicated API keys with spending limits — keys inside a box are
  readable by the agent.
* Watch free disk space: boxes have no disk quota.
