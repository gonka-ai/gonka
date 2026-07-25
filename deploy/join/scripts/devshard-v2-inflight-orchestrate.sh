#!/usr/bin/env bash
# Orchestrate devshard v2 in-flight long stream + coordinated halt (S1 node / S2 node+api).
#
#   APPROVE=1 HALT_SERVICES="node api" ./scripts/devshard-v2-inflight-orchestrate.sh run-s2
#   APPROVE=1 ./scripts/devshard-v2-inflight-orchestrate.sh run          # node only (S1)
#
# Env:
#   HALT_SERVICES="node api"   — services to halt (S2)
#   HALT_SEC=30
#   HALT_DELAY_SEC=2
#   STREAM_MAX_TOKENS=4096
#   STREAM_TIMEOUT_SEC=900
#   WAIT_FOR_POC=1             — wait until Inference before test (default 1)
#   DEVSHARDCTL                — remote devshardctl path (default mlnode-cache build)
#   NODE_RPC                   — remote chain RPC (default tcp://127.0.0.1:26657)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
HOST="${HOST:-decentai@xj7-5.s.filfox.io}"
SSH_PORT="${SSH_PORT:-18222}"
REMOTE_JOIN="${REMOTE_JOIN:-/srv/dai/gonka/deploy/join}"
HALT_SEC="${HALT_SEC:-30}"
HALT_DELAY_SEC="${HALT_DELAY_SEC:-2}"
STREAM_MAX_TOKENS="${STREAM_MAX_TOKENS:-4096}"
STREAM_TIMEOUT_SEC="${STREAM_TIMEOUT_SEC:-900}"
HALT_SERVICES="${HALT_SERVICES:-node}"
HOSTS_FILTER="${HOSTS_FILTER:-testnet-4}"
WAIT_FOR_POC="${WAIT_FOR_POC:-1}"
HALT_LOG="${HALT_LOG:-/tmp/devshard-v2-inflight-halt.log}"
DEVSHARDCTL="${DEVSHARDCTL:-/srv/dai/release-v2-mlnode-cache/devshardctl}"
NODE_RPC="${NODE_RPC:-tcp://127.0.0.1:26657}"
KEYRING_PASSWORD="${KEYRING_PASSWORD:-12345678}"

log() { echo "[orchestrate] $*"; }
die() { echo "[orchestrate] ERROR: $*" >&2; exit 1; }

remote_test_env() {
  cat <<EOF
export DEVSHARDCTL='${DEVSHARDCTL}'
export NODE_RPC='${NODE_RPC}'
export KEYRING_PASSWORD='${KEYRING_PASSWORD}'
export STREAM_MAX_TOKENS='${STREAM_MAX_TOKENS}'
export STREAM_TIMEOUT_SEC='${STREAM_TIMEOUT_SEC}'
EOF
}

sync_scripts() {
  scp -P "$SSH_PORT" \
    "$SCRIPT_DIR/devshard-v2-inflight-node-halt-test.sh" \
    "$SCRIPT_DIR/devshard-v2-standalone-happy-path.sh" \
    "$JOIN_DIR/scripts/prepare-compose-restart.sh" \
    "$HOST:${REMOTE_JOIN}/scripts/" >/dev/null
  ssh -p "$SSH_PORT" "$HOST" "chmod +x ${REMOTE_JOIN}/scripts/devshard-v2-inflight-node-halt-test.sh ${REMOTE_JOIN}/scripts/devshard-v2-standalone-happy-path.sh ${REMOTE_JOIN}/scripts/prepare-compose-restart.sh"
  log "scripts synced to ${HOST}:${REMOTE_JOIN}/scripts/"
}

verify_remote_preconditions() {
  log "=== remote preflight on genesis ==="
  ssh -p "$SSH_PORT" "$HOST" bash -s <<REMOTE
set -euo pipefail
$(remote_test_env)
[[ -x "\$DEVSHARDCTL" ]] || { echo "missing devshardctl: \$DEVSHARDCTL" >&2; exit 1; }
[[ -x /srv/dai/inferenced ]] || { echo "missing inferenced" >&2; exit 1; }
curl -fsS http://127.0.0.1:8000/devshard/v2/healthz >/dev/null
phase=\$(curl -fsS http://127.0.0.1:8000/v1/epochs/latest | jq -r '.phase // "unknown"')
conf=\$(curl -fsS http://127.0.0.1:8000/v1/epochs/latest | jq -r '.is_confirmation_poc_active // false')
echo "phase=\$phase confirmation_poc=\$conf"
echo "devshardctl=\$DEVSHARDCTL"
echo "node_rpc=\$NODE_RPC"
REMOTE
}

print_ready_summary() {
  cat <<EOF

Ready for in-flight node-halt test (NOT run yet).

What changed:
  - Long multi-turn stream prompt (4 sections, max_tokens=${STREAM_MAX_TOKENS})
  - Stream runner uses nohup+disown (SSH returns immediately)
  - Halt fires ${HALT_DELAY_SEC}s after stream-start returns, or sooner if ready file seen while polling

Defaults on genesis:
  DEVSHARDCTL=${DEVSHARDCTL}
  NODE_RPC=${NODE_RPC}
  HALT_SEC=${HALT_SEC}

To run (requires approval):
  APPROVE=1 HALT_SEC=${HALT_SEC} ${JOIN_DIR}/scripts/devshard-v2-inflight-orchestrate.sh run

Manual steps (alternative):
  ssh -p ${SSH_PORT} ${HOST}
  cd ${REMOTE_JOIN}
  export DEVSHARDCTL=${DEVSHARDCTL} NODE_RPC=${NODE_RPC}
  export STREAM_MAX_TOKENS=${STREAM_MAX_TOKENS} STREAM_TIMEOUT_SEC=${STREAM_TIMEOUT_SEC}
  RUN_STEP=setup ./scripts/devshard-v2-inflight-node-halt-test.sh
  RUN_STEP=stream-start ./scripts/devshard-v2-inflight-node-halt-test.sh
  # from laptop within ~2s:
  ${JOIN_DIR}/scripts/prepare-compose-restart.sh coordinated-halt node ${HALT_SEC} ${HOSTS_FILTER}

EOF
}

report_halt_timing() {
  local meta halt_log
  meta="$(ssh -p "$SSH_PORT" "$HOST" cat /tmp/devshard-v2-inflight-stream.meta 2>/dev/null || true)"
  halt_log="${1:-$HALT_LOG}"
  log "=== halt timing analysis (informational) ==="
  echo "$meta"
  grep -E 'ALL_.*_DOWN at|ALL_.*_STARTED at' "$halt_log" 2>/dev/null || true
  python3 - "$meta" "$halt_log" <<'PY' || true
import re, sys
from datetime import datetime

meta = sys.argv[1]
halt_path = sys.argv[2]

def pick(text, key):
    m = re.search(rf"^{key}=(.+)$", text, re.M)
    return m.group(1).strip() if m else None

def parse_ts(value):
    if not value:
        return None
    return datetime.fromisoformat(value.replace("Z", "+00:00"))

start = pick(meta, "start_ts")
end = pick(meta, "end_ts")
complete = pick(meta, "stream_complete")
halt = open(halt_path).read() if halt_path else ""
down_m = re.search(r"ALL_.*_DOWN at (\S+)", halt)
up_m = re.search(r"ALL_.*_STARTED at (\S+)", halt)
down = down_m.group(1) if down_m else None
up = up_m.group(1) if up_m else None

print(f"stream_start={start} stream_end={end} halt_down={down} halt_up={up} stream_complete={complete}")

if start and end and down and up:
    start_ts = parse_ts(start)
    end_ts = parse_ts(end)
    down_ts = parse_ts(down)
    up_ts = parse_ts(up)
    halt_during_stream = start_ts <= down_ts <= end_ts
    ended_during_halt = down_ts <= end_ts <= up_ts
    print(f"halt_during_stream={'YES' if halt_during_stream else 'NO'}")
    print(f"stream_ended_during_halt={'YES' if ended_during_halt else 'NO'}")
PY
}

wait_for_poc_finish() {
  [[ "$WAIT_FOR_POC" == "1" ]] || { log "WAIT_FOR_POC=0 — skipping PoC wait"; return 0; }
  log "=== waiting for Inference phase (PoC + confirmation PoC inactive) ==="
  local phase conf start_ts
  start_ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  while true; do
    phase="$(ssh -p "$SSH_PORT" -o BatchMode=yes "$HOST" \
      "curl -fsS http://127.0.0.1:8000/v1/epochs/latest | jq -r '.phase // \"unknown\"'" 2>/dev/null || echo unknown)"
    conf="$(ssh -p "$SSH_PORT" -o BatchMode=yes "$HOST" \
      "curl -fsS http://127.0.0.1:8000/v1/epochs/latest | jq -r '.is_confirmation_poc_active // false'" 2>/dev/null || echo true)"
    log "phase=$phase confirmation_poc=$conf"
    [[ "$phase" == "Inference" && "$conf" == "false" ]] && {
      log "PoC finished (Inference, no confirmation PoC) since wait start=$start_ts"
      return 0
    }
    sleep 8
  done
}

run_inflight_test() {
  local halt_services="$1"
  [[ "${APPROVE:-0}" == "1" ]] || die "refusing to run live test without APPROVE=1"

  sync_scripts
  verify_remote_preconditions
  wait_for_poc_finish

  log "=== setup on 702111 (new escrow) ==="
  ssh -p "$SSH_PORT" "$HOST" bash -s <<REMOTE
set -euo pipefail
cd ${REMOTE_JOIN}
$(remote_test_env)
RUN_STEP=setup ./scripts/devshard-v2-inflight-node-halt-test.sh
REMOTE

  log "=== stream-start (detached; SSH should return in <3s) ==="
  T_STREAM_ARM="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  ssh -p "$SSH_PORT" "$HOST" bash -s <<REMOTE
set -euo pipefail
cd ${REMOTE_JOIN}
$(remote_test_env)
RUN_STEP=stream-start ./scripts/devshard-v2-inflight-node-halt-test.sh
REMOTE
  T_STREAM_SSH_RETURN="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  log "stream armed=$T_STREAM_ARM ssh_returned=$T_STREAM_SSH_RETURN"

  log "=== wait for ready (max 60s), min delay ${HALT_DELAY_SEC}s ==="
  T_READY=""
  for i in $(seq 1 300); do
    if ssh -p "$SSH_PORT" -o BatchMode=yes "$HOST" "test -f /tmp/devshard-v2-inflight-stream.ready"; then
      T_READY="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
      log "ready file seen at $T_READY"
      ssh -p "$SSH_PORT" "$HOST" cat /tmp/devshard-v2-inflight-stream.ready
      break
    fi
    sleep 0.2
  done
  [[ -n "$T_READY" ]] || log "WARN: ready file not seen within 60s — halting anyway after delay"

  elapsed="$(
    python3 - <<PY
import datetime as d
a=d.datetime.fromisoformat("${T_STREAM_SSH_RETURN}".replace("Z","+00:00"))
print(int((d.datetime.now(d.timezone.utc)-a).total_seconds()))
PY
  )"
  if [[ "$elapsed" -lt "$HALT_DELAY_SEC" ]]; then
    sleep "$((HALT_DELAY_SEC - elapsed))"
  fi

  log "=== coordinated halt services=[${halt_services}] ${HALT_SEC}s ==="
  T_HALT_START="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  "$JOIN_DIR/scripts/prepare-compose-restart.sh" coordinated-halt "$halt_services" "$HALT_SEC" "$HOSTS_FILTER" | tee "$HALT_LOG"
  T_HALT_END="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

  log "=== stream completion + report ==="
  ssh -p "$SSH_PORT" "$HOST" bash -s <<REMOTE
set -euo pipefail
cd ${REMOTE_JOIN}
$(remote_test_env)
RUN_STEP=stream-wait ./scripts/devshard-v2-inflight-node-halt-test.sh || true
RUN_STEP=report ./scripts/devshard-v2-inflight-node-halt-test.sh
REMOTE

  report_halt_timing "$HALT_LOG"
  echo "HALT_SERVICES=${halt_services} T_STREAM_ARM=$T_STREAM_ARM T_STREAM_SSH_RETURN=$T_STREAM_SSH_RETURN T_READY=$T_READY T_HALT_START=$T_HALT_START T_HALT_END=$T_HALT_END"

  log "=== finalize + settle ==="
  ssh -p "$SSH_PORT" "$HOST" bash -s <<REMOTE
set -euo pipefail
cd ${REMOTE_JOIN}
$(remote_test_env)
RUN_STEP=finalize ./scripts/devshard-v2-inflight-node-halt-test.sh
REMOTE

  log "DONE"
}

cmd_prepare() {
  sync_scripts
  verify_remote_preconditions
  print_ready_summary
}

cmd_run() {
  run_inflight_test "${HALT_SERVICES:-node}"
}

cmd_run_s2() {
  run_inflight_test "node api"
}

main() {
  local cmd="${1:-prepare}"
  shift || true
  case "$cmd" in
    prepare|sync)
      cmd_prepare
      ;;
    run|test|run-s1)
      cmd_run
      ;;
    run-s2|s2)
      cmd_run_s2
      ;;
    *)
      die "usage: $0 prepare|run|run-s2 (run requires APPROVE=1)"
      ;;
  esac
}

main "$@"
