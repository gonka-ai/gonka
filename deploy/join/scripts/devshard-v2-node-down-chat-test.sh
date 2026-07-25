#!/usr/bin/env bash
# Devshard v2: create escrow, chat while validator nodes are stopped, finalize after restart.
#
# Run on genesis (702111). Fleet stop/start is triggered by the laptop orchestrator.
#
# Usage:
#   RUN_STEP=setup ./scripts/devshard-v2-node-down-chat-test.sh
#   RUN_STEP=chat ./scripts/devshard-v2-node-down-chat-test.sh
#   RUN_STEP=finalize ./scripts/devshard-v2-node-down-chat-test.sh
#
# Env:
#   HALT_SERVICES — comma-separated docker services expected stopped during chat (default: node)
#   EXPECT_CHAT_FAIL=1 — do not fail chat step on missing [DONE] or timeout (orchestrator restarts fleet)
#   STATE_FILE, CHAT_LOG, CHAT_META, CHAT_PID_FILE, CHAT_DONE — paths for this experiment run
#   PAYLOAD_FILE, CHAT_RUNNER_LOG — chat payload and background runner log paths
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
HAPPY="${SCRIPT_DIR}/devshard-v2-standalone-happy-path.sh"

HAPPY_STATE="${HAPPY_STATE:-/tmp/devshard-v2-happy-path.state}"
STATE_FILE="${STATE_FILE:-/tmp/devshard-v2-node-down-chat.state}"
CHAT_LOG="${CHAT_LOG:-/tmp/devshard-v2-node-down-chat.sse}"
CHAT_META="${CHAT_META:-/tmp/devshard-v2-node-down-chat.meta}"
CHAT_PID_FILE="${CHAT_PID_FILE:-/tmp/devshard-v2-node-down-chat.pid}"
CHAT_DONE="${CHAT_DONE:-/tmp/devshard-v2-node-down-chat.done}"
PAYLOAD_FILE="${PAYLOAD_FILE:-/tmp/devshard-v2-node-down-chat.payload.json}"
CHAT_RUNNER_LOG="${CHAT_RUNNER_LOG:-/tmp/devshard-v2-node-down-chat-runner.log}"

HALT_SERVICES="${HALT_SERVICES:-node}"
EXPECT_CHAT_FAIL="${EXPECT_CHAT_FAIL:-0}"

DEVSHARDCTL="${DEVSHARDCTL:-/srv/dai/release-v2-mlnode-cache/devshardctl}"
NODE_RPC="${NODE_RPC:-tcp://127.0.0.1:26657}"
DEVSHARD_PORT="${DEVSHARD_PORT:-18081}"
MODEL="${MODEL:-Qwen/Qwen3-4B-Instruct-2507}"
CHAT_MAX_TOKENS="${CHAT_MAX_TOKENS:-512}"
CHAT_TIMEOUT_SEC="${CHAT_TIMEOUT_SEC:-600}"
RUN_STEP="${RUN_STEP:-all}"

log() { echo "[node-down-chat-v2] $*"; }
die() { echo "[node-down-chat-v2] ERROR: $*" >&2; exit 1; }

should_run() {
  local step="$1"
  [[ "$RUN_STEP" == "all" || "$RUN_STEP" == "$step" ]]
}

save_state() {
  cat >"$STATE_FILE" <<EOF
ESCROW_ID=${ESCROW_ID:-}
HAPPY_STATE=${HAPPY_STATE}
MODEL=${MODEL}
DEVSHARD_PORT=${DEVSHARD_PORT}
CHAT_TOKEN=${CHAT_TOKEN:-}
EOF
}

load_state() {
  [[ -f "$STATE_FILE" ]] && source "$STATE_FILE"
  [[ -f "$HAPPY_STATE" ]] && source "$HAPPY_STATE"
}

check_halted_services() {
  local svc
  IFS=',' read -ra _halt_svcs <<< "$HALT_SERVICES"
  for svc in "${_halt_svcs[@]}"; do
    svc="${svc// /}"
    [[ -n "$svc" ]] || continue
    local st
    st="$(docker inspect "$svc" --format '{{.State.Status}}' 2>/dev/null || echo missing)"
    log "local $svc status=$st (expect not running during chat)"
    [[ "$st" != "running" ]] || log "WARN: local $svc still running"
  done
}

chat_down_message() {
  if [[ ",${HALT_SERVICES}," == *",api,"* ]]; then
    echo "Node and api are down."
  else
    echo "Nodes are down."
  fi
}

setup_escrow() {
  [[ -x "$HAPPY" ]] || die "missing $HAPPY"
  [[ -x "$DEVSHARDCTL" ]] || die "missing $DEVSHARDCTL"
  export DEVSHARDCTL NODE_RPC
  pkill -9 -f "devshardctl.*${DEVSHARD_PORT}" 2>/dev/null || true
  local outer_state="$STATE_FILE"
  rm -f "$outer_state" "$CHAT_LOG" "$CHAT_META" "$CHAT_PID_FILE" "$CHAT_DONE" "$PAYLOAD_FILE"
  rm -f "$HAPPY_STATE"
  export STATE_FILE="$HAPPY_STATE"
  RUN_STEP=preflight "$HAPPY"
  RUN_STEP=create "$HAPPY"
  RUN_STEP=start "$HAPPY"
  source "$HAPPY_STATE"
  export STATE_FILE="$outer_state"
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID missing after setup"
  curl -sS -X POST "http://127.0.0.1:${DEVSHARD_PORT}/v1/admin/settings" \
    -H "Authorization: Bearer ${DEVSHARD_ADMIN_API_KEY:-sk-admin-test}" \
    -H "Content-Type: application/json" \
    -d '{
      "max_concurrent_requests": 512,
      "default_request_max_tokens": '"$CHAT_MAX_TOKENS"',
      "request_max_tokens_cap": '"$CHAT_MAX_TOKENS"',
      "model_limits": [{"model_id":"'"$MODEL"'","access_mode":"open"}]
    }' >/dev/null
  log "setup complete ESCROW_ID=$ESCROW_ID HALT_SERVICES=$HALT_SERVICES"
  save_state
}

chat_while_nodes_down() {
  load_state
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID not set"
  check_halted_services

  local down_msg
  down_msg="$(chat_down_message)"
  CHAT_TOKEN="v2-nodedown-$(date +%s)-$RANDOM"
  rm -f "$CHAT_LOG" "$CHAT_META" "$CHAT_DONE" "$CHAT_PID_FILE"
  jq -n \
    --arg model "$MODEL" \
    --arg token "$CHAT_TOKEN" \
    --arg down "$down_msg" \
    --argjson max_tokens "$CHAT_MAX_TOKENS" \
    '{
      model: $model,
      stream: true,
      max_tokens: $max_tokens,
      messages: [
        {role: "user", content: ("Token: " + $token + ". " + $down + " Explain Tendermint consensus briefly in 3 paragraphs.")}
      ]
    }' >"$PAYLOAD_FILE"

  log "starting stream chat token=$CHAT_TOKEN max_tokens=$CHAT_MAX_TOKENS expect_fail=$EXPECT_CHAT_FAIL"
  nohup env \
    CHAT_LOG="$CHAT_LOG" \
    CHAT_META="$CHAT_META" \
    CHAT_DONE="$CHAT_DONE" \
    DEVSHARD_PORT="$DEVSHARD_PORT" \
    CHAT_TIMEOUT_SEC="$CHAT_TIMEOUT_SEC" \
    CHAT_TOKEN="$CHAT_TOKEN" \
    PAYLOAD_FILE="$PAYLOAD_FILE" \
    bash -c '
set -uo pipefail
START_TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
CHUNK_LINES=0
while IFS= read -r line; do
  [[ "$line" == data:* ]] && CHUNK_LINES=$((CHUNK_LINES + 1))
  echo "$line"
done < <(
  curl -sS -N -m "$CHAT_TIMEOUT_SEC" \
    -X POST "http://127.0.0.1:${DEVSHARD_PORT}/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d @"$PAYLOAD_FILE"
) >"$CHAT_LOG"
CURL_EXIT=$?
END_TS="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
{
  echo "token=$CHAT_TOKEN"
  echo "start_ts=$START_TS"
  echo "end_ts=$END_TS"
  echo "chunk_lines=$CHUNK_LINES"
  echo "curl_exit=$CURL_EXIT"
  if grep -q "\[DONE\]" "$CHAT_LOG" 2>/dev/null; then echo "stream_complete=true"; else echo "stream_complete=false"; fi
} >"$CHAT_META"
date -u +%Y-%m-%dT%H:%M:%SZ >"$CHAT_DONE"
exit "$CURL_EXIT"
' >"$CHAT_RUNNER_LOG" 2>&1 &
  echo $! >"$CHAT_PID_FILE"
  disown "$(cat "$CHAT_PID_FILE")" 2>/dev/null || true

  local pid i
  pid="$(cat "$CHAT_PID_FILE")"
  log "chat pid=$pid waiting up to ${CHAT_TIMEOUT_SEC}s..."
  for i in $(seq 1 "$CHAT_TIMEOUT_SEC"); do
    [[ -f "$CHAT_DONE" ]] && break
    sleep 1
  done
  if [[ ! -f "$CHAT_DONE" ]]; then
    if [[ "$EXPECT_CHAT_FAIL" == "1" ]]; then
      log "WARN: chat did not finish in time (EXPECT_CHAT_FAIL=1) — see $CHAT_RUNNER_LOG"
    else
      die "chat did not finish — see $CHAT_RUNNER_LOG"
    fi
  fi

  log "=== CHAT REPORT ==="
  cat "$CHAT_META" 2>/dev/null || true
  [[ -f "$CHAT_LOG" ]] && log "log lines: $(wc -l <"$CHAT_LOG")" || log "WARN: no CHAT_LOG at $CHAT_LOG"

  if [[ -f "$CHAT_LOG" ]] && grep -q '\[DONE\]' "$CHAT_LOG"; then
    log "stream completed with [DONE]"
  elif [[ "$EXPECT_CHAT_FAIL" == "1" ]]; then
    log "WARN: stream missing [DONE] (EXPECT_CHAT_FAIL=1)"
  else
    die "stream missing [DONE]"
  fi

  curl -fsS "http://127.0.0.1:${DEVSHARD_PORT}/v1/status" 2>/dev/null | jq '{escrow_id,nonce,balance}' || log "WARN: status query failed"
  save_state
}

finalize_settle() {
  load_state
  export ESCROW_ID DEVSHARDCTL NODE_RPC
  export STATE_FILE="$HAPPY_STATE"
  RUN_STEP=finalize "$HAPPY"
  RUN_STEP=settle "$HAPPY"
  log "PASS escrow=$ESCROW_ID finalized and settled"
}

main() {
  if should_run setup; then setup_escrow; fi
  if should_run chat; then chat_while_nodes_down; fi
  if should_run finalize; then finalize_settle; fi
}

main "$@"
