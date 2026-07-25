#!/usr/bin/env bash
# Devshard gateway v2 in-flight long stream during coordinated node halt (S1).
#
# Uses devshard-gateway-v2-smoke-test.sh for escrow setup/finalize/settle, then:
#   1. Start long streaming chat via gateway (:18080)
#   2. Signal ready when first stream chunk arrives
#   3. External orchestrator halts node containers on all testnet hosts
#   4. Report stream + escrow outcome
#
# Usage (on 702111):
#   ./scripts/devshard-gateway-v2-inflight-node-halt-test.sh setup
#   ./scripts/devshard-gateway-v2-inflight-node-halt-test.sh stream-start
#   ./scripts/devshard-gateway-v2-inflight-node-halt-test.sh stream-wait
#   ./scripts/devshard-gateway-v2-inflight-node-halt-test.sh report
#   ./scripts/devshard-gateway-v2-inflight-node-halt-test.sh finalize
#
# From laptop (after stream-start):
#   ./scripts/prepare-compose-restart.sh coordinated-halt node 20 testnet-4
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SMOKE="${SCRIPT_DIR}/devshard-gateway-v2-smoke-test.sh"

STATE_FILE="${STATE_FILE:-/tmp/devshard-gateway-v2-inflight.state}"
SMOKE_STATE="${SMOKE_STATE:-/tmp/devshard-gateway-v2-inflight-smoke.state}"
STREAM_LOG="${STREAM_LOG:-/tmp/devshard-gateway-v2-inflight-stream.sse}"
STREAM_META="${STREAM_META:-/tmp/devshard-gateway-v2-inflight-stream.meta}"
STREAM_PID_FILE="${STREAM_PID_FILE:-/tmp/devshard-gateway-v2-inflight-stream.pid}"
READY_FILE="${READY_FILE:-/tmp/devshard-gateway-v2-inflight-stream.ready}"
DONE_FILE="${DONE_FILE:-/tmp/devshard-gateway-v2-inflight-stream.done}"
STREAM_RUNNER="${STREAM_RUNNER:-/tmp/devshard-gateway-v2-inflight-stream-runner.sh}"

MODEL="${MODEL:-Qwen/Qwen3-4B-Instruct-2507}"
GATEWAY_BASE="${GATEWAY_BASE:-http://127.0.0.1:18080}"
STREAM_MAX_TOKENS="${STREAM_MAX_TOKENS:-4096}"
STREAM_TIMEOUT_SEC="${STREAM_TIMEOUT_SEC:-900}"
READY_TIMEOUT_SEC="${READY_TIMEOUT_SEC:-120}"
RUN_STEP="${RUN_STEP:-all}"

log() { echo "[inflight-gw-v2] $*"; }
die() { echo "[inflight-gw-v2] ERROR: $*" >&2; exit 1; }

should_run() {
  local step="$1"
  [[ "$RUN_STEP" == "all" || "$RUN_STEP" == "$step" ]]
}

save_state() {
  cat >"$STATE_FILE" <<EOF
ESCROW_ID=${ESCROW_ID:-}
SMOKE_STATE=${SMOKE_STATE}
MODEL=${MODEL}
GATEWAY_BASE=${GATEWAY_BASE}
STREAM_TOKEN=${STREAM_TOKEN:-}
STREAM_STARTED_AT=${STREAM_STARTED_AT:-}
EOF
}

load_state() {
  [[ -f "$STATE_FILE" ]] || return 0
  # shellcheck disable=SC1090
  source "$STATE_FILE"
}

setup_gateway_escrow() {
  [[ -x "$SMOKE" ]] || die "missing $SMOKE"
  rm -f "$STATE_FILE" "$SMOKE_STATE" "$READY_FILE" "$DONE_FILE" "$STREAM_LOG" "$STREAM_META" "$STREAM_PID_FILE"

  log "gateway setup (create + register)"
  STATE_FILE="$SMOKE_STATE" RUN_STEP=preflight WAIT_FOR_POC=1 "$SMOKE"
  STATE_FILE="$SMOKE_STATE" RUN_STEP=create "$SMOKE"
  STATE_FILE="$SMOKE_STATE" RUN_STEP=register STREAM_MAX_TOKENS="$STREAM_MAX_TOKENS" "$SMOKE"

  # shellcheck disable=SC1090
  source "$SMOKE_STATE"
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID missing after gateway setup"
  log "setup complete ESCROW_ID=$ESCROW_ID gateway=$GATEWAY_BASE"
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
        {role: "user", content: ("Token: " + $token + ". Stress-test long in-flight devshard gateway v2 stream during coordinated node halt.")},
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
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID not set — run setup first"

  if [[ -f "$SCRIPT_DIR/../config.devshard.env" ]]; then
    set -a
    # shellcheck disable=SC1091
    source "$SCRIPT_DIR/../config.devshard.env"
    set +a
  fi
  local api_key="${DEVSHARD_API_KEYS%%,*}"

  STREAM_TOKEN="gw-inflight-$(date +%s)-$RANDOM"
  log "starting gateway long stream token=$STREAM_TOKEN url=${GATEWAY_BASE}/v1/chat/completions max_tokens=$STREAM_MAX_TOKENS"

  rm -f "$READY_FILE" "$DONE_FILE" "$STREAM_LOG" "$STREAM_META" "$STREAM_RUNNER"
  build_stream_payload "$STREAM_TOKEN" >/tmp/devshard-gateway-v2-inflight-stream.payload.json

  cat >"$STREAM_RUNNER" <<'RUNNER'
#!/usr/bin/env bash
set -uo pipefail

STREAM_LOG="${STREAM_LOG:?}"
STREAM_META="${STREAM_META:?}"
READY_FILE="${READY_FILE:?}"
DONE_FILE="${DONE_FILE:?}"
GATEWAY_BASE="${GATEWAY_BASE:?}"
API_KEY="${API_KEY:?}"
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
    -X POST "${GATEWAY_BASE}/v1/chat/completions" \
    -H "Authorization: Bearer ${API_KEY}" \
    -H "Content-Type: application/json" \
    -d @"$PAYLOAD_FILE"
) >"$STREAM_LOG"

CURL_EXIT=$?
END_TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
END_EPOCH="$(date +%s)"
{
  echo "token=$STREAM_TOKEN"
  echo "client_path=gateway_direct"
  echo "client_url=${GATEWAY_BASE}/v1/chat/completions"
  echo "start_ts=$START_TS"
  echo "end_ts=$END_TS"
  echo "start_epoch=$START_EPOCH"
  echo "end_epoch=$END_EPOCH"
  echo "first_chunk_epoch=${FIRST_CHUNK_EPOCH:-}"
  echo "duration_sec=$((END_EPOCH - START_EPOCH))"
  echo "chunk_lines=$CHUNK_COUNT"
  echo "curl_exit=$CURL_EXIT"
  grep -q '\[DONE\]' "$STREAM_LOG" 2>/dev/null && echo "stream_complete=true" || echo "stream_complete=false"
  grep -qi 'error' "$STREAM_LOG" 2>/dev/null && echo "has_error_line=true" || echo "has_error_line=false"
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
    GATEWAY_BASE="$GATEWAY_BASE" \
    API_KEY="$api_key" \
    STREAM_TIMEOUT_SEC="$STREAM_TIMEOUT_SEC" \
    STREAM_TOKEN="$STREAM_TOKEN" \
    PAYLOAD_FILE="/tmp/devshard-gateway-v2-inflight-stream.payload.json" \
    "$STREAM_RUNNER" >/tmp/devshard-gateway-v2-inflight-stream-runner.log 2>&1 &
  stream_pid=$!
  disown "$stream_pid" 2>/dev/null || true
  echo "$stream_pid" >"$STREAM_PID_FILE"

  STREAM_STARTED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
  save_state
  log "stream pid=$stream_pid log=$STREAM_LOG"
}

wait_stream_ready() {
  local i
  for i in $(seq 1 "$READY_TIMEOUT_SEC"); do
    [[ -f "$READY_FILE" ]] && { log "stream READY (first chunk)"; cat "$READY_FILE"; return 0; }
    sleep 1
  done
  die "no first chunk within ${READY_TIMEOUT_SEC}s — see $STREAM_LOG"
}

wait_stream_done() {
  local pid=""
  [[ -f "$STREAM_PID_FILE" ]] && pid="$(cat "$STREAM_PID_FILE")"
  if [[ -n "$pid" ]] && kill -0 "$pid" 2>/dev/null; then
    log "waiting for stream pid=$pid..."
    wait "$pid" || true
  fi
  [[ -f "$DONE_FILE" ]] || die "stream did not finish — see $STREAM_LOG"
  log "stream finished"
}

report() {
  load_state
  log "=== GATEWAY INFLIGHT STREAM REPORT ==="
  [[ -f "$STREAM_META" ]] && cat "$STREAM_META" || echo "(no meta)"
  echo
  log "log bytes: $(wc -c <"$STREAM_LOG" 2>/dev/null || echo 0)"
  log "last 5 lines:"
  tail -5 "$STREAM_LOG" 2>/dev/null || true
  echo
  log "gateway devshard runtime:"
  if [[ -f "$SCRIPT_DIR/../config.devshard.env" ]]; then
    set -a
    # shellcheck disable=SC1091
    source "$SCRIPT_DIR/../config.devshard.env"
    set +a
    curl -fsS "${GATEWAY_BASE}/v1/admin/devshards" \
      -H "Authorization: Bearer ${DEVSHARD_ADMIN_API_KEY}" \
      | jq --arg id "${ESCROW_ID:-}" '.devshards[] | select(.id==$id) | .runtime | {id, phase, nonce, balance, active_requests, requests_blocked}' 2>/dev/null \
      || echo "admin status unavailable"
  fi
  echo
  log "on-chain escrow:"
  /srv/dai/inferenced query inference show-devshard-escrow "${ESCROW_ID:-0}" \
    --node http://127.0.0.1:8000/chain-rpc/ \
    --home /srv/dai/.inference -o json 2>/dev/null \
    | jq '{id: .escrow.id, settled: .escrow.settled, model: .escrow.model_id}' \
    || echo "chain query unavailable"
  echo
  log "gateway logs (escrow=${ESCROW_ID:-}?):"
  docker logs devshardctl-multi 2>&1 | grep "escrow=${ESCROW_ID:-}" \
    | grep -E 'request_fully_settled|race_completed.*winner=true|request_failed|stream_canceled' \
    | tail -8 || true
}

finalize_and_settle() {
  load_state
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID not set"
  export ESCROW_ID
  STATE_FILE="$SMOKE_STATE" RUN_STEP=finalize "$SMOKE"
  STATE_FILE="$SMOKE_STATE" RUN_STEP=settle "$SMOKE"
  log "finalize+settle complete escrow=$ESCROW_ID"
}

main() {
  if should_run setup; then setup_gateway_escrow; fi
  if should_run stream-start; then stream_start; fi
  if should_run stream-wait-ready; then wait_stream_ready; fi
  if should_run stream-wait; then wait_stream_done; fi
  if should_run report; then report; fi
  if should_run finalize; then finalize_and_settle; fi
}

main "$@"
