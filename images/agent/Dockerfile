# syntax=docker/dockerfile:1.7
# Agent box: a plain, lightweight Debian console. No AI agent is pre-installed;
# the user installs the agent of choice inside the box (root inside the box only).
FROM debian:trixie-slim

ARG DEBIAN_FRONTEND=noninteractive
# hadolint ignore=DL3008
RUN apt-get update \
 && apt-get install -y --no-install-recommends \
      bash-completion ca-certificates curl git jq less nano netcat-openbsd \
      openssh-client procps tini tmux unzip xz-utils \
 && rm -rf /var/lib/apt/lists/* \
 && mkdir -p /workspace

COPY rootfs/ /

# Boxes have no systemd. Installers that register a service ("systemctl enable
# --now ...") get a compatible replacement that supervises the service and
# starts it again when the box boots (see /usr/local/bin/systemctl).
RUN for t in systemctl journalctl; do \
      if [ -e /usr/bin/$t ]; then dpkg-divert --local --rename --divert /usr/bin/$t.distrib --add /usr/bin/$t; fi; \
      ln -sf /usr/local/bin/$t /usr/bin/$t; \
    done \
 && mkdir -p /etc/systemd/system /run/systemd/system

ENV LANG=C.UTF-8 \
    TERM=xterm-256color
WORKDIR /workspace

LABEL org.opencontainers.image.title="dockerbox-agent" \
      org.opencontainers.image.description="Isolated Linux console for AI agents (Dockerbox)" \
      org.opencontainers.image.source="https://github.com/Pyzyi/dockerbox" \
      org.opencontainers.image.licenses="MIT"

# tini reaps orphaned processes (tmux sessions, agent child processes).
ENTRYPOINT ["/usr/bin/tini", "--"]
CMD ["/usr/local/bin/dockerbox-agent-init"]
