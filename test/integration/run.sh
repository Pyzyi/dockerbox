#!/usr/bin/env bash
# Runs the integration tests against a local Docker Engine.
#   IT_IMAGE / IT_AGENT_IMAGE  images to test (default: dockerbox:dev / dockerbox-agent:dev)
#   IT_BUILD=1                 build both images first
#   IT_INTERNET=1              also check that public destinations are reachable
set -euo pipefail
cd "$(dirname "$0")/../.."

project=dockerbox-it
# Host directory for /data, like ZimaOS creates it for app volumes (owned by
# the user running the tests, root on ZimaOS). It lives in its own parent
# directory so it can be removed after web has taken it over (uid 65532).
IT_PARENT="$(mktemp -d)"
IT_DATA_DIR="$IT_PARENT/data"
mkdir "$IT_DATA_DIR"
export IT_DATA_DIR
compose=(docker compose -p "$project" -f docker-compose.yml -f test/integration/compose.override.yml)

if [[ "${IT_BUILD:-0}" == "1" ]]; then
  docker build -t "${IT_IMAGE:-dockerbox:dev}" --build-arg AGENT_IMAGE="${IT_AGENT_IMAGE:-dockerbox-agent:dev}" .
  docker build -t "${IT_AGENT_IMAGE:-dockerbox-agent:dev}" images/agent
fi

cleanup() {
  status=$?
  if [[ $status -ne 0 ]]; then "${compose[@]}" logs --no-color --tail 100 || true; fi
  # Remove any boxes the tests left behind (only objects of the "it" instance).
  docker ps -aq --filter label=dockerbox.managed=true --filter label=dockerbox.instance=it | xargs -r docker rm -f >/dev/null
  docker network ls -q --filter label=dockerbox.managed=true --filter label=dockerbox.instance=it | xargs -r docker network rm >/dev/null || true
  docker volume ls -q --filter label=dockerbox.managed=true --filter label=dockerbox.instance=it | xargs -r docker volume rm >/dev/null || true
  "${compose[@]}" down -v --remove-orphans >/dev/null 2>&1 || true
  # The data directory now belongs to uid 65532; empty it from a container so
  # this also works when the tests do not run as root (CI).
  docker run --rm -v "$IT_DATA_DIR:/d" "${IT_AGENT_IMAGE:-dockerbox-agent:dev}" \
    sh -c 'rm -rf /d/* /d/.[!.]* 2>/dev/null; true' >/dev/null 2>&1 || true
  rm -rf "$IT_PARENT" 2>/dev/null || true
  exit $status
}
trap cleanup EXIT

"${compose[@]}" up -d --wait --wait-timeout 120
IT_DATA_DIR="$IT_DATA_DIR" go test -tags integration -count=1 -v ./test/integration/
