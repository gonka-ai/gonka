#!/usr/bin/env bash
# Orchestrate devshard v2 node-down-then-chat experiment.
#
#   APPROVE=1 ./scripts/devshard-v2-node-down-chat-orchestrate.sh run
#
# Flow: create escrow → stop all nodes → chat → start nodes → finalize/settle
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
HOST="${HOST:-decentai@xj7-5.s.filfox.io}"
SSH_PORT="${SSH_PORT:-18222}"
REMOTE_JOIN="${REMOTE_JOIN:-/srv/dai/gonka/deploy/join}"
HOSTS_FILTER="${HOSTS_FILTER:-testnet-4}"
HALT_SERVICES="${HALT_SERVICES:-node}"
WAIT_FOR_POC="${WAIT_FOR_POC:-1}"
LOG_FILE="${LOG_FILE:-/tmp/devshard-v2-node-down-chat-orchestrate.log}"
DEVSHARDCTL="${DEVSHARDCTL:-/srv/dai/release-v2-mlnode-cache/devshardctl}"
NODE_RPC="${NODE_RPC:-tcp://127.0.0.1:26657}"
KEYRING_PASSWORD="${KEYRING_PASSWORD:-12345678}"
CHAT_MAX_TOKENS="${CHAT_MAX_TOKENS:-512}"

log() { echo "[orchestrate-nodedown] $*" | tee -a "$LOG_FILE"; }
die() { echo "[orchestrate-nodedown] ERROR: $*" >&2 | tee -a "$LOG_FILE"; exit 1; }

remote_test_env() {
  cat <<EOF
export DEVSHARDCTL='${DEVSHARDCTL}'
export NODE_RPC='${NODE_RPC}'
export KEYRING_PASSWORD='${KEYRING_PASSWORD}'
export CHAT_MAX_TOKENS='${CHAT_MAX_TOKENS}'
export HAPPY_STATE='${HAPPY_STATE:-/tmp/devshard-v2-happy-path.state}'
export HALT_SERVICES='${HALT_SERVICES}'
export EXPECT_CHAT_FAIL='${EXPECT_CHAT_FAIL:-0}'
export STATE_FILE='${STATE_FILE:-/tmp/devshard-v2-node-down-chat.state}'
export CHAT_LOG='${CHAT_LOG:-/tmp/devshard-v2-node-down-chat.sse}'
export CHAT_META='${CHAT_META:-/tmp/devshard-v2-node-down-chat.meta}'
export CHAT_PID_FILE='${CHAT_PID_FILE:-/tmp/devshard-v2-node-down-chat.pid}'
export CHAT_DONE='${CHAT_DONE:-/tmp/devshard-v2-node-down-chat.done}'
export PAYLOAD_FILE='${PAYLOAD_FILE:-/tmp/devshard-v2-node-down-chat.payload.json}'
export CHAT_RUNNER_LOG='${CHAT_RUNNER_LOG:-/tmp/devshard-v2-node-down-chat-runner.log}'
EOF
}

sync_scripts() {
  scp -P "$SSH_PORT" \
    "$SCRIPT_DIR/devshard-v2-node-down-chat-test.sh" \
    "$SCRIPT_DIR/devshard-v2-standalone-happy-path.sh" \
    "$HOST:${REMOTE_JOIN}/scripts/" >/dev/null
  ssh -p "$SSH_PORT" "$HOST" "chmod +x ${REMOTE_JOIN}/scripts/devshard-v2-node-down-chat-test.sh ${REMOTE_JOIN}/scripts/devshard-v2-standalone-happy-path.sh"
  log "scripts synced"
}

wait_for_poc_finish() {
  [[ "$WAIT_FOR_POC" == "1" ]] || return 0
  log "waiting for Inference phase..."
  while true; do
    local phase conf
    phase="$(ssh -p "$SSH_PORT" -o BatchMode=yes "$HOST" \
      "curl -fsS http://127.0.0.1:8000/v1/epochs/latest | jq -r '.phase // \"unknown\"'" 2>/dev/null || echo unknown)"
    conf="$(ssh -p "$SSH_PORT" -o BatchMode=yes "$HOST" \
      "curl -fsS http://127.0.0.1:8000/v1/epochs/latest | jq -r '.is_confirmation_poc_active // false'" 2>/dev/null || echo true)"
    log "phase=$phase confirmation_poc=$conf"
    [[ "$phase" == "Inference" && "$conf" == "false" ]] && return 0
    sleep 8
  done
}

run_experiment() {
  [[ "${APPROVE:-0}" == "1" ]] || die "refusing live test without APPROVE=1"
  sync_scripts
  wait_for_poc_finish

  log "=== setup escrow on genesis ==="
  ssh -p "$SSH_PORT" "$HOST" bash -s <<REMOTE | tee -a "$LOG_FILE"
set -euo pipefail
cd ${REMOTE_JOIN}
$(remote_test_env)
RUN_STEP=setup ./scripts/devshard-v2-node-down-chat-test.sh
REMOTE

  log "=== stop ${HALT_SERVICES} on ${HOSTS_FILTER} ==="
  "$JOIN_DIR/scripts/prepare-compose-restart.sh" coordinated-stop "$HALT_SERVICES" "$HOSTS_FILTER" | tee -a "$LOG_FILE"

  log "=== chat while nodes down ==="
  ssh -p "$SSH_PORT" "$HOST" bash -s <<REMOTE | tee -a "$LOG_FILE"
set -euo pipefail
cd ${REMOTE_JOIN}
$(remote_test_env)
RUN_STEP=chat ./scripts/devshard-v2-node-down-chat-test.sh
REMOTE

  log "=== start ${HALT_SERVICES} on ${HOSTS_FILTER} ==="
  "$JOIN_DIR/scripts/prepare-compose-restart.sh" coordinated-start "$HALT_SERVICES" "$HOSTS_FILTER" | tee -a "$LOG_FILE"

  log "=== finalize + settle ==="
  ssh -p "$SSH_PORT" "$HOST" bash -s <<REMOTE | tee -a "$LOG_FILE"
set -euo pipefail
cd ${REMOTE_JOIN}
$(remote_test_env)
RUN_STEP=finalize ./scripts/devshard-v2-node-down-chat-test.sh
REMOTE

  log "DONE — see $LOG_FILE"
}

main() {
  case "${1:-run}" in
    prepare|sync) sync_scripts ;;
    run|test) run_experiment ;;
    *) die "usage: $0 prepare|run (run requires APPROVE=1)" ;;
  esac
}

main "$@"
