<p align="center"><img src="docs/assets/icon.svg" width="72" alt=""></p>

<h1 align="center">Dockerbox — KI-Agent in der Box</h1>

<p align="center">
KI-Agenten mit vollen Root-Rechten in leichtgewichtigen, isolierten Linux-Boxen —<br>
eine Box pro Agent, verwaltet über ein Web-Dashboard, ohne den Overhead von VMs.
</p>

<p align="center"><a href="README.md">English</a> · <a href="docs/zimaos.md">Installation auf ZimaOS</a></p>

![Dockerbox-Konsole](docs/assets/screenshot-console.png)

<p align="center">
<img src="docs/assets/screenshot-light.png" width="49%" alt="Heller Modus">
<img src="docs/assets/screenshot-settings.png" width="49%" alt="Einstellungen">
</p>

Über **Einstellungen** (Regler-Symbol oben in der Seitenleiste) lassen sich
Hell/Dunkel, Akzentfarbe, Hintergrund (auch eigene Farben), Glas-Transparenz,
Schriftgröße der Konsole und Sprache anpassen. Die Einstellungen werden auf dem
Server gespeichert und gelten auf allen Geräten.

## Worum geht es?

KI-Agenten arbeiten am besten, wenn sie Pakete installieren, Dienste starten und
das System frei verändern dürfen. Das direkt auf dem Heimserver zu erlauben ist
gefährlich, eine VM pro Agent ist für kleine Boards wie das ZimaBoard zu schwer.

Dockerbox gibt jedem Agenten eine **eigene Linux-Box**, in der er `root` ist —
die Box selbst ist aber abgeriegelt:

| In der Box | Außerhalb der Box |
|------------|-------------------|
| root, `apt`, `pip`, `npm`, Dienste, tmux | kein Docker-Socket, kein Privileged-Modus, keine Host-Mounts |
| Internet über HTTP(S)-Proxy | kein Zugriff auf Host, Heimnetz oder andere Agenten |
| `/root` und `/workspace` bleiben erhalten | harte Limits für RAM, CPU und Prozesse |

Eine Box im Leerlauf braucht **unter 1 MiB RAM**.

**Es ist kein KI-Agent vorinstalliert** — die Box ist eine schlanke Debian-Konsole.
Installiere den Agenten deiner Wahl darin (`dockerbox-help` in der Konsole zeigt Beispiele).

## Schnellstart

```bash
curl -fsSLO https://raw.githubusercontent.com/Pyzyi/dockerbox/main/docker-compose.yml
# optional: DOCKERBOX_PASSWORD (mind. 12 Zeichen) in docker-compose.yml eintragen
docker compose up -d
```

`http://<server>:8420` öffnen, anmelden, **Agent hinzufügen** klicken.

Ohne `DOCKERBOX_PASSWORD` wird beim ersten Start ein Passwort erzeugt und einmal
im Log des Containers `web` ausgegeben.

Für **ZimaOS** siehe die [ZimaOS-Anleitung](docs/zimaos.md) (auf Englisch).

## Sicherheitshinweise

* Das Dashboard ist so mächtig wie SSH-Zugang: starkes Passwort (mind. 12 Zeichen),
  Port 8420 nicht ins Internet freigeben. Für Fernzugriff VPN nutzen oder einen
  HTTPS-Reverse-Proxy mit **eigenem Hostnamen** und `DOCKERBOX_SECURE_COOKIE=true`.
* Docker Engine 26 oder neuer verwenden (ältere Versionen leiten DNS aus internen
  Netzen weiter, CVE-2024-29018).
* Boxen haben kein Festplatten-Limit: Ein amoklaufender Agent kann die Docker-Platte
  füllen. Freien Speicher im Blick behalten.
* LAN-Freigaben immer mit Port angeben (`192.168.1.50:11434`), sonst sind alle
  Dienste dieses Geräts erreichbar.
* API-Keys, die du in einer Box hinterlegst, kann der Agent in dieser Box lesen.
  Nutze eigene Keys mit Ausgabelimit.
* Container teilen sich den Kernel des Hosts. Halte ZimaOS aktuell; für stärkere
  Isolation kann gVisor (`DOCKERBOX_AGENT_RUNTIME=runsc`) genutzt werden.

Alle Details: [Architektur](docs/architecture.md), [Bedrohungsmodell](docs/security/threat-model.md),
[Konfiguration](docs/configuration.md).

## Lizenz

[MIT](LICENSE)
