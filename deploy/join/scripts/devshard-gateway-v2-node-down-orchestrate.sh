#!/usr/bin/env bash
# Gateway negative test: validators stopped → try create escrow + gateway chat.
#
# S1 (node only):
#   APPROVE=1 ./scripts/devshard-gateway-v2-node-down-orchestrate.sh run
# S2 (node + api):
#   APPROVE=1 ./scripts/devshard-gateway-v2-node-down-orchestrate.sh run-s2
#   APPROVE=1 HALT_SERVICES="node api" ./scripts/devshard-gateway-v2-node-down-orchestrate.sh run
#
# Env: HOSTS_FILTER=testnet-4  WAIT_FOR_POC=1  HALT_SERVICES=node|node api
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
HOST="${HOST:-decentai@xj7-5.s.filfox.io}"
SSH_PORT="${SSH_PORT:-18222}"
REMOTE_JOIN="${REMOTE_JOIN:-/srv/dai/gonka/deploy/join}"
HOSTS_FILE="${GONKA_HOSTS_FILE:-$JOIN_DIR/hosts.inventory}"
HOSTS_FILTER="${HOSTS_FILTER:-testnet-4}"
HALT_SERVICES="${HALT_SERVICES:-node}"
WAIT_FOR_POC="${WAIT_FOR_POC:-1}"
STATE_FILE="${STATE_FILE:-/tmp/devshard-gateway-v2-node-down.state}"
LOG_FILE="${LOG_FILE:-/tmp/devshard-gateway-v2-node-down.log}"

log() { echo "[node-down-gw] $*" | tee -a "$LOG_FILE"; }
die() { echo "[node-down-gw] ERROR: $*" >&2 | tee -a "$LOG_FILE"; exit 1; }

load_hosts() {
  HOST_IDS=() HOST_USERS=() HOST_HOSTS=() HOST_PORTS=() HOST_DEPLOYS=()
  while IFS='|' read -r id role user host port api deploy chain notes; do
    [[ -z "${id:-}" || "$id" =~ ^# ]] && continue
    [[ -n "$HOSTS_FILTER" ]] && ! echo "$id|$role|$chain|$notes" | grep -qi "$HOSTS_FILTER" && continue
    HOST_IDS+=("$id")
    HOST_USERS+=("$user")
    HOST_HOSTS+=("$host")
    HOST_PORTS+=("$port")
    HOST_DEPLOYS+=("$deploy")
  done < "$HOSTS_FILE"
  ((${#HOST_IDS[@]} > 0)) || die "no hosts matched filter=$HOSTS_FILTER"
}

wait_for_inference() {
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
    sleep 15
  done
}

coordinated_service() {
  local action="$1" # stop|start
  local i ssh_target
  load_hosts
  log "=== ${action} ${HALT_SERVICES} on hosts: ${HOST_IDS[*]} ==="
  for i in "${!HOST_IDS[@]}"; do
    ssh_target="${HOST_USERS[$i]}@${HOST_HOSTS[$i]}"
    (
      ssh -p "${HOST_PORTS[$i]}" -o BatchMode=yes -o ConnectTimeout=15 "$ssh_target" \
        "DEPLOY_DIR='${HOST_DEPLOYS[$i]}' SERVICES='${HALT_SERVICES}' ACTION='${action}' bash -s" <<'REMOTE'
set -euo pipefail
cd "$DEPLOY_DIR"
source ./scripts/prepare-compose-restart.sh prepare >/dev/null
# shellcheck disable=SC2086
if [[ "$ACTION" == stop ]]; then dc stop $SERVICES; else dc start $SERVICES; fi
REMOTE
      echo "${HOST_IDS[$i]}: ${action}ped"
    ) &
  done
  wait
  for i in "${!HOST_IDS[@]}"; do
    ssh_target="${HOST_USERS[$i]}@${HOST_HOSTS[$i]}"
    local status_line=""
    for svc in ${HALT_SERVICES}; do
      local st
      st="$(ssh -p "${HOST_PORTS[$i]}" -o BatchMode=yes "$ssh_target" \
        "docker inspect ${svc} --format '{{.State.Status}}' 2>/dev/null || echo missing")"
      status_line+="${svc}=${st} "
    done
    log "${HOST_IDS[$i]}: ${status_line}"
  done
}

sync_scripts() {
  scp -P "$SSH_PORT" \
    "$SCRIPT_DIR/devshard-gateway-v2-node-down-create-chat-test.sh" \
    "$SCRIPT_DIR/devshard-gateway-v2-smoke-test.sh" \
    "$HOST:${REMOTE_JOIN}/scripts/" >/dev/null
  ssh -p "$SSH_PORT" "$HOST" "chmod +x ${REMOTE_JOIN}/scripts/devshard-gateway-v2-node-down-create-chat-test.sh ${REMOTE_JOIN}/scripts/devshard-gateway-v2-smoke-test.sh"
}

run_experiment() {
  [[ "${APPROVE:-0}" == "1" ]] || die "refusing without APPROVE=1"
  : >"$LOG_FILE"
  sync_scripts
  wait_for_inference

  log "=== baseline gateway status ==="
  ssh -p "$SSH_PORT" "$HOST" "curl -fsS http://127.0.0.1:18080/v1/status | jq '{runtimes, devshards: [.devshards[]? | {id, active, phase, block_reason}]}'" | tee -a "$LOG_FILE"

  coordinated_service stop
  log "ALL_${HALT_SERVICES// /_}_DOWN at $(date -u +%Y-%m-%dT%H:%M:%SZ)"

  log "=== create escrow + gateway chat while ${HALT_SERVICES} down ==="
  ssh -p "$SSH_PORT" "$HOST" bash -s <<REMOTE | tee -a "$LOG_FILE"
set -euo pipefail
cd ${REMOTE_JOIN}
export HALT_SERVICES='${HALT_SERVICES}'
./scripts/devshard-gateway-v2-node-down-create-chat-test.sh
REMOTE

  log "=== restore nodes ==="
  coordinated_service start
  log "ALL_${HALT_SERVICES// /_}_STARTED at $(date -u +%Y-%m-%dT%H:%M:%SZ)"

  log "=== post-restore: chain reachable ==="
  ssh -p "$SSH_PORT" "$HOST" "curl -fsS http://127.0.0.1:8000/v1/epochs/latest | jq '{phase, is_confirmation_poc_active}'" | tee -a "$LOG_FILE"

  log "DONE — full log: $LOG_FILE"
}

restore_nodes() {
  [[ "${APPROVE:-0}" == "1" ]] || die "refusing without APPROVE=1"
  coordinated_service start
}

main() {
  case "${1:-run}" in
    run|test|run-s1) run_experiment ;;
    run-s2|s2) HALT_SERVICES="node api"; run_experiment ;;
    restore) restore_nodes ;;
    stop) [[ "${APPROVE:-0}" == "1" ]] || die "APPROVE=1 required"; coordinated_service stop ;;
    *) die "usage: $0 run|run-s2|restore|stop" ;;
  esac
}

main "$@"
