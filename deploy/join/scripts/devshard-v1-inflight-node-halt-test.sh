#!/usr/bin/env bash
# Devshard v1 in-flight streaming chat during coordinated node halt (S1 / S2).
#
# Uses devshard-v1-standalone-happy-path.sh for setup, then:
#   1. Start long streaming chat on standalone devshardctl (:18082)
#   2. Wait until first stream chunk arrives
#   3. Signal ready for external coordinated halt
#   4. After halt, report stream outcome and run finalize + settle
#
# Usage (on 702111):
#   ./scripts/devshard-v1-inflight-node-halt-test.sh setup
#   ./scripts/devshard-v1-inflight-node-halt-test.sh stream-start
#   ./scripts/devshard-v1-inflight-node-halt-test.sh stream-wait
#   ./scripts/devshard-v1-inflight-node-halt-test.sh report
#   ./scripts/devshard-v1-inflight-node-halt-test.sh finalize
#
# From laptop while host shows READY file:
#   ./scripts/prepare-compose-restart.sh coordinated-halt node 20 testnet-4
#   ./scripts/prepare-compose-restart.sh coordinated-halt "node api" 20 testnet-4
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HAPPY="${SCRIPT_DIR}/devshard-v1-standalone-happy-path.sh"

HAPPY_STATE="${HAPPY_STATE:-/tmp/devshard-v1-happy-path.state}"
STATE_FILE="${STATE_FILE:-/tmp/devshard-v1-inflight-node-halt.state}"
STREAM_LOG="${STREAM_LOG:-/tmp/devshard-v1-inflight-stream.sse}"
STREAM_META="${STREAM_META:-/tmp/devshard-v1-inflight-stream.meta}"
STREAM_PID_FILE="${STREAM_PID_FILE:-/tmp/devshard-v1-inflight-stream.pid}"
READY_FILE="${READY_FILE:-/tmp/devshard-v1-inflight-stream.ready}"
DONE_FILE="${DONE_FILE:-/tmp/devshard-v1-inflight-stream.done}"

MODEL="${MODEL:-Qwen/Qwen3-4B-Instruct-2507}"
DEVSHARD_PORT="${DEVSHARD_PORT:-18082}"
STREAM_MAX_TOKENS="${STREAM_MAX_TOKENS:-4096}"
STREAM_TIMEOUT_SEC="${STREAM_TIMEOUT_SEC:-900}"
READY_TIMEOUT_SEC="${READY_TIMEOUT_SEC:-120}"
STREAM_RUNNER="${STREAM_RUNNER:-/tmp/devshard-v1-inflight-stream-runner.sh}"

RUN_STEP="${RUN_STEP:-all}"

log() { echo "[inflight-v1] $*"; }
die() { echo "[inflight-v1] ERROR: $*" >&2; exit 1; }

should_run() {
  local step="$1"
  [[ "$RUN_STEP" == "all" || "$RUN_STEP" == "$step" ]]
}

save_state() {
  cat >"$STATE_FILE" <<EOF
ESCROW_ID=${ESCROW_ID:-}
HAPPY_STATE=${HAPPY_STATE:-/tmp/devshard-v1-happy-path.state}
MODEL=${MODEL}
DEVSHARD_PORT=${DEVSHARD_PORT}
STREAM_TOKEN=${STREAM_TOKEN:-}
STREAM_STARTED_AT=${STREAM_STARTED_AT:-}
EOF
}

load_state() {
  [[ -f "$STATE_FILE" ]] || return 0
  # shellcheck disable=SC1090
  source "$STATE_FILE"
}

setup_happy_path() {
  [[ -x "$HAPPY" ]] || die "missing $HAPPY"
  rm -f "$STATE_FILE" "$READY_FILE" "$DONE_FILE" "$STREAM_LOG" "$STREAM_META" "$STREAM_PID_FILE"
  rm -f "$HAPPY_STATE"
  RUN_STEP=preflight "$HAPPY"
  RUN_STEP=create "$HAPPY"
  RUN_STEP=start "$HAPPY"
  # shellcheck disable=SC1090
  source "$HAPPY_STATE"
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID missing after happy-path start"
  log "setup complete ESCROW_ID=$ESCROW_ID"
  save_state
}

build_stream_payload() {
  local token="$1"
  jq -n \
    --arg model "$MODEL" \
    --arg token "$token" \
    --argjson max_tokens "$STREAM_MAX_TOKENS" \
    '{
      model: $model,
      stream: true,
      max_tokens: $max_tokens,
      messages: [
        {role: "user", content: ("Token: " + $token + ". We are stress-testing a long in-flight devshard v1 stream during a chain node halt. Please cooperate by generating the longest possible response.")},
        {role: "assistant", content: "Understood. I will produce a very long, detailed multi-section answer and keep writing until the token limit."},
        {role: "user", content: "Section 1: Explain distributed consensus (Raft, PBFT, Tendermint) in depth with examples."},
        {role: "user", content: "Section 2: Explain blockchain upgrade strategies (halt, live, cosmovisor, blue/green) and trade-offs."},
        {role: "user", content: "Section 3: Explain what happens to in-flight inference when validators stop but API and ML nodes stay up."},
        {role: "user", content: "Section 4: Continue with a detailed case study and do not stop early — use the full token budget."}
      ]
    }'
}

stream_start() {
  load_state
  if [[ -z "${ESCROW_ID:-}" && -f "$HAPPY_STATE" ]]; then
    # shellcheck disable=SC1090
    source "$HAPPY_STATE"
  fi
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID not set — run setup first"

  log "starting long stream token will be assigned max_tokens=$STREAM_MAX_TOKENS timeout=${STREAM_TIMEOUT_SEC}s (v1: no admin API — limits from request payload)"

  rm -f "$READY_FILE" "$DONE_FILE" "$STREAM_LOG" "$STREAM_META" "$STREAM_RUNNER"
  STREAM_TOKEN="v1-inflight-$(date +%s)-$RANDOM"
  build_stream_payload "$STREAM_TOKEN" >/tmp/devshard-v1-inflight-stream.payload.json

  cat >"$STREAM_RUNNER" <<'RUNNER'
#!/usr/bin/env bash
set -uo pipefail

STREAM_LOG="${STREAM_LOG:?}"
STREAM_META="${STREAM_META:?}"
READY_FILE="${READY_FILE:?}"
DONE_FILE="${DONE_FILE:?}"
DEVSHARD_PORT="${DEVSHARD_PORT:?}"
STREAM_TIMEOUT_SEC="${STREAM_TIMEOUT_SEC:?}"
STREAM_TOKEN="${STREAM_TOKEN:?}"
PAYLOAD_FILE="${PAYLOAD_FILE:?}"

START_TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
START_EPOCH="$(date +%s)"
FIRST_CHUNK_EPOCH=""
CHUNK_COUNT=0

while IFS= read -r line; do
  if [[ -z "$FIRST_CHUNK_EPOCH" && -n "$line" ]]; then
    FIRST_CHUNK_EPOCH="$(date +%s)"
    echo "ready_at=$(date -u +%Y-%m-%dT%H:%M:%SZ)" >"$READY_FILE"
  fi
  if [[ "$line" == data:* ]]; then
    CHUNK_COUNT=$((CHUNK_COUNT + 1))
  fi
  echo "$line"
done < <(
  curl -sS -N -m "$STREAM_TIMEOUT_SEC" \
    -X POST "http://127.0.0.1:${DEVSHARD_PORT}/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d @"$PAYLOAD_FILE"
) >"$STREAM_LOG"

CURL_EXIT=$?
END_TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
END_EPOCH="$(date +%s)"
{
  echo "token=$STREAM_TOKEN"
  echo "start_ts=$START_TS"
  echo "end_ts=$END_TS"
  echo "start_epoch=$START_EPOCH"
  echo "end_epoch=$END_EPOCH"
  echo "first_chunk_epoch=${FIRST_CHUNK_EPOCH:-}"
  echo "duration_sec=$((END_EPOCH - START_EPOCH))"
  echo "chunk_lines=$CHUNK_COUNT"
  echo "curl_exit=$CURL_EXIT"
  if grep -q '\[DONE\]' "$STREAM_LOG" 2>/dev/null; then
    echo "stream_complete=true"
  else
    echo "stream_complete=false"
  fi
  if grep -qi 'error' "$STREAM_LOG" 2>/dev/null; then
    echo "has_error_line=true"
  else
    echo "has_error_line=false"
  fi
} >"$STREAM_META"
date -u +%Y-%m-%dT%H:%M:%SZ >"$DONE_FILE"
exit "$CURL_EXIT"
RUNNER
  chmod +x "$STREAM_RUNNER"

  nohup env \
    STREAM_LOG="$STREAM_LOG" \
    STREAM_META="$STREAM_META" \
    READY_FILE="$READY_FILE" \
    DONE_FILE="$DONE_FILE" \
    DEVSHARD_PORT="$DEVSHARD_PORT" \
    STREAM_TIMEOUT_SEC="$STREAM_TIMEOUT_SEC" \
    STREAM_TOKEN="$STREAM_TOKEN" \
    PAYLOAD_FILE="/tmp/devshard-v1-inflight-stream.payload.json" \
    "$STREAM_RUNNER" >/tmp/devshard-v1-inflight-stream-runner.log 2>&1 &
  stream_pid=$!
  disown "$stream_pid" 2>/dev/null || true
  echo "$stream_pid" >"$STREAM_PID_FILE"

  STREAM_STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  save_state
  log "stream pid=$stream_pid token=$STREAM_TOKEN log=$STREAM_LOG runner_log=/tmp/devshard-v1-inflight-stream-runner.log"
  log "ssh-safe: runner detached via nohup+disown"
}

wait_stream_ready() {
  local i
  for i in $(seq 1 "$READY_TIMEOUT_SEC"); do
    [[ -f "$READY_FILE" ]] && { log "stream READY (first chunk received)"; cat "$READY_FILE"; return 0; }
    sleep 1
  done
  die "stream did not produce first chunk within ${READY_TIMEOUT_SEC}s — see $STREAM_LOG"
}

wait_stream_done() {
  local pid=""
  [[ -f "$STREAM_PID_FILE" ]] && pid="$(cat "$STREAM_PID_FILE")"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    log "waiting for stream pid=$pid to finish (timeout ${STREAM_TIMEOUT_SEC}s)..."
    wait "$pid" || true
  fi
  [[ -f "$DONE_FILE" ]] || die "stream did not finish — see $STREAM_LOG"
  log "stream finished"
}

report() {
  load_state
  log "=== STREAM REPORT ==="
  [[ -f "$STREAM_META" ]] && cat "$STREAM_META" || echo "(no meta yet)"
  echo
  log "log lines: $(wc -l <"$STREAM_LOG" 2>/dev/null || echo 0)"
  log "first 5 lines:"
  head -5 "$STREAM_LOG" 2>/dev/null || true
  echo
  log "last 10 lines:"
  tail -10 "$STREAM_LOG" 2>/dev/null || true
  echo
  log "devshardctl tail:"
  [[ -n "${ESCROW_ID:-}" ]] && tail -30 "/tmp/devshardctl-v1-${ESCROW_ID}.log" 2>/dev/null | grep -iE 'stream|error|fail|halt|chain|timeout' || true
  echo
  curl -fsS "http://127.0.0.1:${DEVSHARD_PORT}/v1/status" | jq '{escrow_id, phase, balance, nonce}' 2>/dev/null || echo "status unavailable"
}

finalize_and_settle() {
  load_state
  if [[ -z "${ESCROW_ID:-}" && -f "$HAPPY_STATE" ]]; then
    # shellcheck disable=SC1090
    source "$HAPPY_STATE"
  fi
  export ESCROW_ID
  export STATE_FILE="$HAPPY_STATE"
  RUN_STEP=finalize "$HAPPY"
  RUN_STEP=settle "$HAPPY"
  log "finalize+settle complete for escrow=$ESCROW_ID"
}

main() {
  if should_run setup; then setup_happy_path; fi
  if should_run stream-start; then stream_start; fi
  if should_run stream-wait-ready; then wait_stream_ready; fi
  if should_run stream-wait; then wait_stream_done; fi
  if should_run report; then report; fi
  if should_run finalize; then finalize_and_settle; fi

  if [[ "$RUN_STEP" == "all" ]]; then
    setup_happy_path
    stream_start
    wait_stream_ready
    log "READY FOR HALT — run from laptop:"
    log "  S1: ./scripts/prepare-compose-restart.sh coordinated-halt node 20 testnet-4"
    log "  S2: ./scripts/prepare-compose-restart.sh coordinated-halt \"node api\" 20 testnet-4"
    log "Waiting 5s before halt window marker..."
    sleep 5
    log ">>> HALT WINDOW — trigger coordinated halt now <<<"
    wait_stream_done
    report
    finalize_and_settle
    log "PASS inflight v1 test (no external halt — full stream only)"
  fi
}

main "$@"
