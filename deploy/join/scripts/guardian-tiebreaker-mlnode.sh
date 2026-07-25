#!/usr/bin/env bash
# Pause or unpause mlnode on any fleet host. Never touches node/tmkms/api.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/guardian-tiebreaker-config.sh"

ACTION="${1:?usage: $0 pause|unpause|status}"
SSH_PORT="${SSH_PORT:?set SSH_PORT}"
HOST_ID="${HOST_ID:-unknown}"

remote() {
  ssh -o BatchMode=yes -p "$SSH_PORT" "$SSH_HOST" "$@"
}

case "$ACTION" in
  status)
    remote "docker inspect -f '{{.State.Status}} paused={{.State.Paused}}' $MLNODE 2>/dev/null || echo missing"
    ;;
  pause)
    remote "docker inspect -f '{{.State.Paused}}' $MLNODE" | grep -q true && {
      echo "[$HOST_ID] already paused"
      exit 0
    }
    remote "docker pause $MLNODE"
    remote "docker inspect -f 'paused={{.State.Paused}} status={{.State.Status}}' $MLNODE"
    ;;
  unpause)
    remote "docker inspect -f '{{.State.Paused}}' $MLNODE" | grep -q false && {
      echo "[$HOST_ID] already running"
      exit 0
    }
    remote "docker unpause $MLNODE"
    remote "docker inspect -f 'paused={{.State.Paused}} status={{.State.Status}}' $MLNODE"
    ;;
  *)
    echo "unknown action: $ACTION" >&2
    exit 1
    ;;
esac
