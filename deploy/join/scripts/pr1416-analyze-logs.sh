#!/usr/bin/env bash
# Analyze PR #1416 guard logs for a PoC stage across observers.
#
# Usage:
#   STAGE=12400 ./pr1416-analyze-logs.sh
#   STAGE=12400 VICTIM=gonka17da4... ./pr1416-analyze-logs.sh
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

STAGE="${STAGE:?set STAGE=poc_start_block_height}"
VICTIM="${VICTIM:-$VICTIM_PARTICIPANT}"

analyze() {
  local port="$1" host_id="$2"
  echo "==================== $host_id (port $port) ===================="
  ssh -o BatchMode=yes -o ConnectTimeout=20 -p "$port" "${SSH_USER}@${SSH_HOST}" "
    echo '--- capture ---'
    docker logs api 2>&1 | grep 'EarlyShareGuard: captured early checkpoints' | grep 'stage=$STAGE' | tail -2
    echo '--- N1/N2 ---'
    docker logs api 2>&1 | grep 'low early share miss' | grep '$VICTIM' | grep 'stage=$STAGE' || echo '(none)'
    echo '--- N4 ---'
    docker logs api 2>&1 | grep '$VICTIM' | grep -E 'guard retry \(enforce\)|would retry|guard retry|proof fetch/verify failed' | grep -E 'stage=$STAGE|pocStageStartBlockHeight=$STAGE' || \
      docker logs api 2>&1 | grep '$VICTIM' | grep -E 'guard retry \(enforce\)|would retry|guard retry|proof fetch/verify failed' | tail -8 || echo '(none)'
    echo '--- validation ---'
    docker logs api 2>&1 | grep 'pocStageStartBlockHeight=$STAGE' | grep -E 'starting validation|validation complete|failed' | tail -5
  "
}

analyze "$OBSERVER_SSH_PORT" "$OBSERVER_HOST_ID"
analyze "$SECONDARY_OBSERVER_SSH_PORT" "${SECONDARY_OBSERVER_HOST_ID:-702105}"
analyze "$GENESIS_SSH_PORT" "${GENESIS_HOST_ID:-702111}"
