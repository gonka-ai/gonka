#!/usr/bin/env bash
# Stop PR #1416 orchestrator and unpause victim containers.
#
# Usage:
#   ./pr1416-stop.sh
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
FLEET_ENV="${FLEET_ENV:-$JOIN_DIR/fleet.env}"
[[ -f "$FLEET_ENV" ]] || FLEET_ENV="$JOIN_DIR/../../test-net-cloud/nebius/poc-smst-e2e/fleet.env"
# shellcheck disable=SC1090
source "$FLEET_ENV"

SSH_HOST="${SSH_HOST#*@}"
SSH_USER="${SSH_USER:-decentai}"
SSH_HOST="${SSH_HOST:-xj7-5.s.filfox.io}"
MLNODE="${MLNODE:-join-mlnode-308-1}"

LOG_DIR="${PR1416_LOG_DIR:-/tmp}"
PID_FILE="${LOG_DIR}/pr1416-orchestrator.pid"
LOCK_DIR="${LOG_DIR}/pr1416-orchestrator.lockdir"

echo "Stopping local orchestrator..."
pkill -f "pr1416-negative-test-orchestrate.sh" 2>/dev/null || true
rm -f "$PID_FILE" 2>/dev/null || true
rmdir "$LOCK_DIR" 2>/dev/null || true

echo "Unpausing victim $VICTIM_HOST_ID..."
N4_CONTAINERS="${N4_PAUSE_CONTAINERS:-proxy join-inference-1 api}"
ssh -o BatchMode=yes -o ConnectTimeout=15 -p "$VICTIM_SSH_PORT" "${SSH_USER}@${SSH_HOST}" \
  "docker unpause $MLNODE $N4_CONTAINERS 2>/dev/null || true; \
   docker inspect -f '{{.Name}} paused={{.State.Paused}}' $MLNODE $N4_CONTAINERS 2>/dev/null || true"

echo "done"
