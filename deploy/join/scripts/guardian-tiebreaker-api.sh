#!/usr/bin/env bash
# Stop or start the api container on any fleet host (for suppressing off-chain PoC validation).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/guardian-tiebreaker-config.sh"

ACTION="${1:?usage: $0 stop|start|status}"
SSH_PORT="${SSH_PORT:?set SSH_PORT}"
HOST_ID="${HOST_ID:-unknown}"
API_CONTAINER="${API_CONTAINER:-api}"

remote() {
  ssh -o BatchMode=yes -p "$SSH_PORT" "$SSH_HOST" "$@"
}

case "$ACTION" in
  status)
    remote "docker inspect -f '{{.State.Status}}' $API_CONTAINER 2>/dev/null || echo missing"
    ;;
  stop)
    remote "docker inspect -f '{{.State.Status}}' $API_CONTAINER" | grep -qEx 'exited|created' && {
      echo "[$HOST_ID] api already stopped"
      exit 0
    }
    remote "docker stop $API_CONTAINER"
    remote "docker inspect -f 'status={{.State.Status}}' $API_CONTAINER"
    ;;
  start)
    remote "docker inspect -f '{{.State.Status}}' $API_CONTAINER" | grep -q running && {
      echo "[$HOST_ID] api already running"
      exit 0
    }
    remote "docker start $API_CONTAINER"
    remote "docker inspect -f 'status={{.State.Status}}' $API_CONTAINER"
    ;;
  *)
    echo "unknown action: $ACTION" >&2
    exit 1
    ;;
esac
