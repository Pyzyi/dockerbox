# ADR 0004: Go single binary, minimal dependencies, no frontend framework

* Status: accepted
* Date: 2026-10-30

## Context

Target hardware (ZimaBoard 2: Intel N150, 8–16 GiB RAM, eMMC) should spend its
resources on the agents, not on the management layer. The project is meant to be
published, so supply-chain surface and maintainability matter.

## Decision

* One Go module, one binary `dockerbox` with sub-commands `web`, `broker`,
  `egress`, `healthcheck`. One container image for all three services.
* Docker Engine API is used directly over the unix socket (≈300 lines) instead of
  the Docker SDK (hundreds of transitive packages).
* Only third-party Go dependency: `github.com/coder/websocket`.
* Frontend: plain HTML/CSS/ES modules; xterm.js and its fit addon are copied from
  npm at build time (pinned via `package-lock.json`), never loaded from a CDN.
* Runtime image: `gcr.io/distroless/static-debian12:nonroot`.

## Consequences

* + ~10 MiB RSS per service, image ≈ 15 MiB, fast cold start.
* + Small SBOM, easy to audit; all versions are pinned (Go, npm lockfile, base images, actions by SHA).
* − Docker API types are maintained by hand (only the fields used).
