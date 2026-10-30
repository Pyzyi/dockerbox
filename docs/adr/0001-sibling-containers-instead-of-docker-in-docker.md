# ADR 0001: Sibling containers instead of Docker-in-Docker

* Status: accepted
* Date: 2026-10-30

## Context

The original idea was a single "dockerbox" container that itself runs Docker and
starts one nested container per AI agent ("Docker in Docker", DinD). Agents must
be `root` inside their box but must never be able to break out.

## Options

1. **Classic DinD** (`docker:dind`): requires `--privileged`. A privileged
   container has all capabilities, all devices and no seccomp/AppArmor profile.
   Any root process in it can mount the host disk or load kernel modules.
   Because the agents would run *inside* that privileged container, a breakout
   from the nested box lands in a fully privileged environment.
   **Escape-safety: none.**
2. **Rootless DinD**: still needs `--privileged` (or unconfined seccomp/AppArmor
   plus `/dev/fuse`, user-namespace tweaks) on most kernels; fragile on ZimaOS.
3. **Sysbox runtime**: makes nested Docker safe without `--privileged`, but has to
   be installed on the host. ZimaOS does not ship it and we must not require host
   modifications (R7).
4. **Sibling containers via a policy broker**: one service talks to the host
   Docker Engine and creates each agent box as a normal, hardened sibling
   container. The agent box never sees the Docker socket.

## Decision

Option 4. From the user's point of view nothing changes: the dashboard still
"creates a box per agent" and shows its console. Technically, the boxes are
siblings of the dashboard container, not children.

Sysbox and gVisor remain supported as *optional* runtimes
(`DOCKERBOX_AGENT_RUNTIME`) for users who can install them; Sysbox would even
allow Docker *inside* an agent box safely.

## Consequences

* + Escape requires a kernel or runtime vulnerability instead of a configuration
  mistake. Default seccomp/AppArmor stay active.
* + Best performance: no nested overlay filesystems, no second Docker daemon
  (DinD costs ~50–100 MiB RAM per daemon plus storage duplication).
* − The component holding the Docker socket is root-equivalent on the host.
  Mitigated by ADR 0002 (dedicated broker, no raw API passthrough).
* − Agent boxes appear as containers in the ZimaOS container list. They are
  labelled `dockerbox.managed=true` and prefixed `dockerbox-agent-`.
