# ADR 0002: A dedicated policy broker is the only holder of the Docker socket

* Status: accepted
* Date: 2026-10-30

## Context

Whoever can call `POST /containers/create` on the Docker Engine can start a
privileged container and own the host. The web dashboard is the component most
exposed to attacks (network-facing, parses user input, renders a terminal).

## Options

1. Mount `docker.sock` into the web service. Simple, but any RCE/auth bypass in the
   web layer equals host root.
2. Generic socket proxies (e.g. endpoint allow-lists by HTTP method/path). They do
   not inspect request bodies, so `POST /containers/create` with
   `"Privileged": true` passes as soon as container creation is allowed.
3. **Own broker with a high-level API.** The broker exposes only domain
   operations (`create agent`, `start`, `stop`, `delete`, `terminal`, `stats`) and
   builds all Docker objects itself from a template. A policy check validates the
   final object before it is sent to Docker.

## Decision

Option 3. The broker:

* listens only on an `internal` Docker network shared with `web`,
* never forwards caller-provided Docker JSON,
* accepts only a name (`^[a-z0-9][a-z0-9-]{0,30}$`), memory and CPU values,
  clamped to configured maxima,
* only touches objects carrying `dockerbox.managed=true` and its own
  `dockerbox.instance` label,
* validates every container spec with `policy.Validate` (no privileged mode,
  capability allow-list, no host namespaces, no bind mounts, no devices,
  `no-new-privileges`, limits present, isolated network only).

## Consequences

* + A compromised `web` can at most create/delete hardened agent boxes.
* + The policy is unit-tested independently of Docker.
* − The broker still runs with the socket (as UID 0 without capabilities, read-only
  root filesystem). Its code size is kept small and reviewed with priority.
