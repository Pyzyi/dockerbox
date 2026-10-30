# shellcheck shell=sh
# Show a short hint once per tmux session.
if [ -n "${PS1:-}" ] && [ -z "${DOCKERBOX_HINT_SHOWN:-}" ]; then
  export DOCKERBOX_HINT_SHOWN=1
  printf '\033[2mDockerbox box "%s" - you are root here. Run dockerbox-help for tips.\033[0m\n' "${DOCKERBOX_AGENT:-$(hostname)}"
fi
