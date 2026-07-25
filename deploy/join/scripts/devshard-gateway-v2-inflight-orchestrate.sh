#!/usr/bin/env bash
# Orchestrate devshard gateway v2 in-flight long stream + coordinated node halt (S1).
#
#   APPROVE=1 ./scripts/devshard-gateway-v2-inflight-orchestrate.sh run
#
# Env: HALT_SEC=20  HALT_SERVICES=node  HOSTS_FILTER=testnet-4  WAIT_FOR_POC=1
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
HOST="${HOST:-decentai@xj7-5.s.filfox.io}"
SSH_PORT="${SSH_PORT:-18222}"
REMOTE_JOIN="${REMOTE_JOIN:-/srv/dai/gonka/deploy/join}"
HALT_SEC="${HALT_SEC:-20}"
HALT_DELAY_SEC="${HALT_DELAY_SEC:-2}"
STREAM_MAX_TOKENS="${STREAM_MAX_TOKENS:-4096}"
STREAM_TIMEOUT_SEC="${STREAM_TIMEOUT_SEC:-900}"
HALT_SERVICES="${HALT_SERVICES:-node}"
HOSTS_FILTER="${HOSTS_FILTER:-testnet-4}"
WAIT_FOR_POC="${WAIT_FOR_POC:-1}"
HALT_LOG="${HALT_LOG:-/tmp/devshard-gateway-v2-inflight-halt.log}"

log() { echo "[orchestrate-gw-v2] $*"; }
die() { echo "[orchestrate-gw-v2] ERROR: $*" >&2; exit 1; }

sync_scripts() {
  scp -P "$SSH_PORT" \
    "$SCRIPT_DIR/devshard-gateway-v2-inflight-node-halt-test.sh" \
    "$SCRIPT_DIR/devshard-gateway-v2-smoke-test.sh" \
    "$JOIN_DIR/docker-compose.devshard-gateway-v2-binary.override.yml" \
    "$HOST:${REMOTE_JOIN}/scripts/" >/dev/null 2>&1 || true
  scp -P "$SSH_PORT" \
    "$SCRIPT_DIR/devshard-gateway-v2-inflight-node-halt-test.sh" \
    "$SCRIPT_DIR/devshard-gateway-v2-smoke-test.sh" \
    "$HOST:${REMOTE_JOIN}/scripts/"
  scp -P "$SSH_PORT" \
    "$JOIN_DIR/docker-compose.devshard-gateway-v2-binary.override.yml" \
    "$HOST:${REMOTE_JOIN}/"
  ssh -p "$SSH_PORT" "$HOST" "chmod +x ${REMOTE_JOIN}/scripts/devshard-gateway-v2-inflight-node-halt-test.sh ${REMOTE_JOIN}/scripts/devshard-gateway-v2-smoke-test.sh"
  scp -P "$SSH_PORT" "$JOIN_DIR/scripts/prepare-compose-restart.sh" "$HOST:${REMOTE_JOIN}/scripts/" >/dev/null || true
  log "scripts synced"
}

wait_for_poc_finish() {
  [[ "$WAIT_FOR_POC" == "1" ]] || { log "WAIT_FOR_POC=0"; return 0; }
  log "=== waiting for Inference phase (PoC finished) ==="
  local phase conf
  while true; do
    phase="$(ssh -p "$SSH_PORT" -o BatchMode=yes "$HOST" \
      "curl -fsS http://127.0.0.1:8000/v1/epochs/latest | jq -r '.phase // \"unknown\"'" 2>/dev/null || echo unknown)"
    conf="$(ssh -p "$SSH_PORT" -o BatchMode=yes "$HOST" \
      "curl -fsS http://127.0.0.1:8000/v1/epochs/latest | jq -r '.is_confirmation_poc_active // false'" 2>/dev/null || echo true)"
    log "phase=$phase confirmation_poc=$conf"
    [[ "$phase" == "Inference" && "$conf" == "false" ]] && return 0
    sleep 8
  done
}

run_inflight_test() {
  [[ "${APPROVE:-0}" == "1" ]] || die "refusing live test without APPROVE=1"

  sync_scripts
  wait_for_poc_finish

  log "=== gateway setup + stream-start on 702111 ==="
  ssh -p "$SSH_PORT" "$HOST" bash -s <<REMOTE
set -euo pipefail
cd ${REMOTE_JOIN}
export STREAM_MAX_TOKENS=${STREAM_MAX_TOKENS}
export STREAM_TIMEOUT_SEC=${STREAM_TIMEOUT_SEC}
RUN_STEP=setup ./scripts/devshard-gateway-v2-inflight-node-halt-test.sh
RUN_STEP=stream-start ./scripts/devshard-gateway-v2-inflight-node-halt-test.sh
REMOTE

  log "=== wait for ready file (max 120s) ==="
  for i in $(seq 1 600); do
    if ssh -p "$SSH_PORT" -o BatchMode=yes "$HOST" "test -f /tmp/devshard-gateway-v2-inflight-stream.ready"; then
      log "ready:"
      ssh -p "$SSH_PORT" "$HOST" cat /tmp/devshard-gateway-v2-inflight-stream.ready
      break
    fi
    sleep 0.2
  done
  sleep "$HALT_DELAY_SEC"

  log "=== coordinated halt services=[${HALT_SERVICES}] ${HALT_SEC}s on ${HOSTS_FILTER} ==="
  "$JOIN_DIR/scripts/prepare-compose-restart.sh" coordinated-halt "$HALT_SERVICES" "$HALT_SEC" "$HOSTS_FILTER" | tee "$HALT_LOG"

  log "=== stream-wait + report ==="
  ssh -p "$SSH_PORT" "$HOST" bash -s <<'REMOTE'
set -euo pipefail
cd /srv/dai/gonka/deploy/join
RUN_STEP=stream-wait ./scripts/devshard-gateway-v2-inflight-node-halt-test.sh || true
RUN_STEP=report ./scripts/devshard-gateway-v2-inflight-node-halt-test.sh
REMOTE

  log "=== finalize + settle ==="
  ssh -p "$SSH_PORT" "$HOST" bash -s <<'REMOTE'
set -euo pipefail
cd /srv/dai/gonka/deploy/join
RUN_STEP=finalize ./scripts/devshard-gateway-v2-inflight-node-halt-test.sh
REMOTE

  log "DONE"
}

main() {
  case "${1:-run}" in
    prepare|sync) sync_scripts ;;
    run|test) run_inflight_test ;;
    *) die "usage: $0 prepare|run (run requires APPROVE=1)" ;;
  esac
}

main "$@"
