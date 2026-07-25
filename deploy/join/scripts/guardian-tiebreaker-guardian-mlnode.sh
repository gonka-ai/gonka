#!/usr/bin/env bash
# Pause or unpause guardian host mlnode safely. Never touches node/tmkms.
# Orchestrator may also stop the guardian api during PoC gen (see STOP_GUARDIAN_API_DURING_POC_GEN).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/guardian-tiebreaker-config.sh"

ACTION="${1:?usage: $0 pause|unpause|status}"

remote() {
  SSH_PORT="$GUARDIAN_SSH_PORT" HOST_ID="${GUARDIAN_HOST_ID}" \
    "$SCRIPT_DIR/guardian-tiebreaker-mlnode.sh" "$ACTION"
}

case "$ACTION" in
  status)
    remote
    ;;
  pause)
    remote
    ;;
  unpause)
    remote
    ;;
  *)
    echo "unknown action: $ACTION" >&2
    exit 1
    ;;
esac
