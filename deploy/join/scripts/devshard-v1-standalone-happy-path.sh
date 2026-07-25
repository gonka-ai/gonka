#!/usr/bin/env bash
# Devshard v1 standalone happy path: create escrow → devshardctl → chat → finalize → settle.
#
# Run on a join host with versiond (e.g. 702111 / gonka-testnet-4).
# Uses /srv/dai/devshardctl-v1-release (release/v0.2.13-devshard-v1).
# See docs/devshard-standalone-v1-v2-build-and-e2e-testnet.md
#
# Usage:
#   ./scripts/devshard-v1-standalone-happy-path.sh              # full run
#   RUN_STEP=preflight ./scripts/devshard-v1-standalone-happy-path.sh
#   RUN_STEP=create ./scripts/devshard-v1-standalone-happy-path.sh
#   ESCROW_ID=3 RUN_STEP=start ./scripts/devshard-v1-standalone-happy-path.sh
#   RUN_STEP=chat|finalize|settle ./scripts/devshard-v1-standalone-happy-path.sh
#
# Optional env:
#   INFERENCED  INFERENCED_HOME  KEY_NAME  KEYRING_PASSWORD  CHAIN_ID
#   API_BASE  MODEL  AMOUNT  DEVSHARDCTL  DEVSHARD_PORT
#   STATE_FILE  SKIP_PREFLIGHT=1  SKIP_SETTLE=1
#   LONG_CHAT=1  STREAM_MAX_TOKENS=4096  STREAM_TIMEOUT_SEC=900
set -euo pipefail

if [[ -x /srv/dai/inferenced ]]; then
  INFERENCED="${INFERENCED:-/srv/dai/inferenced}"
else
  INFERENCED="${INFERENCED:-/srv/dai/inferenced}"
fi

INFERENCED_HOME="${INFERENCED_HOME:-/srv/dai/.inference}"
KEY_NAME="${KEY_NAME:-gonka-account-key}"
KEYRING_PASSWORD="${KEYRING_PASSWORD:-12345678}"
KEYRING_BACKEND="${KEYRING_BACKEND:-file}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-4}"

API_BASE="${API_BASE:-http://127.0.0.1:8000}"
NODE_RPC="${NODE_RPC:-${API_BASE}/chain-rpc/}"
CHAIN_REST="${CHAIN_REST:-${API_BASE}/chain-api}"

MODEL="${MODEL:-Qwen/Qwen3-4B-Instruct-2507}"
AMOUNT="${AMOUNT:-5000000000}"

DEVSHARDCTL="${DEVSHARDCTL:-/srv/dai/devshardctl-v1-release}"
DEVSHARD_PORT="${DEVSHARD_PORT:-18082}"

RUN_STEP="${RUN_STEP:-all}"
STATE_FILE="${STATE_FILE:-/tmp/devshard-v1-happy-path.state}"
SKIP_PREFLIGHT="${SKIP_PREFLIGHT:-0}"
SKIP_SETTLE="${SKIP_SETTLE:-0}"
LONG_CHAT="${LONG_CHAT:-0}"
STREAM_MAX_TOKENS="${STREAM_MAX_TOKENS:-4096}"
STREAM_TIMEOUT_SEC="${STREAM_TIMEOUT_SEC:-900}"

log() { echo "[devshard-v1] $*"; }
die() { echo "[devshard-v1] ERROR: $*" >&2; exit 1; }

should_run() {
  local step="$1"
  [[ "$RUN_STEP" == "all" || "$RUN_STEP" == "$step" ]]
}

save_state() {
  cat >"$STATE_FILE" <<EOF
ESCROW_ID=${ESCROW_ID:-}
TXHASH=${TXHASH:-}
MODEL=${MODEL}
AMOUNT=${AMOUNT}
DEVSHARD_PORT=${DEVSHARD_PORT}
SETTLEMENT_FILE=${SETTLEMENT_FILE:-}
SETTLEMENT_TX_FILE=${SETTLEMENT_TX_FILE:-}
EOF
}

load_state() {
  [[ -f "$STATE_FILE" ]] || return 0
  # shellcheck disable=SC1090
  source "$STATE_FILE"
}

key_address() {
  printf '%s\n' "$KEYRING_PASSWORD" | "$INFERENCED" keys show "$KEY_NAME" -a \
    --home "$INFERENCED_HOME" --keyring-backend "$KEYRING_BACKEND"
}

export_private_key() {
  printf '%s\n' "$KEYRING_PASSWORD" | "$INFERENCED" keys export "$KEY_NAME" \
    --unarmored-hex --unsafe --yes \
    --home "$INFERENCED_HOME" --keyring-backend "$KEYRING_BACKEND"
}

preflight() {
  [[ "$SKIP_PREFLIGHT" == "1" ]] && { log "SKIP_PREFLIGHT=1"; return 0; }

  [[ -x "$INFERENCED" ]] || die "inferenced not found: $INFERENCED"
  [[ -x "$DEVSHARDCTL" ]] || die "devshardctl not found: $DEVSHARDCTL"

  log "inferenced: $("$INFERENCED" version 2>/dev/null | head -1 || true)"
  log "devshardctl: $DEVSHARDCTL"

  curl -fsS "$API_BASE/devshard/v1/healthz" >/dev/null || die "v1 healthz failed"
  log "host healthz: v1 ok"

  local params
  params="$(curl -fsS "$CHAIN_REST/productscience/inference/inference/params")"
  echo "$params" | jq -e '.params.devshard_escrow_params.approved_versions[] | select(.name=="v1")' >/dev/null \
    || die "v1 not in approved_versions — run gov-propose-devshard-approved-versions.sh"

  local weights
  weights="$("$INFERENCED" query inference list-epoch-group-data \
    --node "$NODE_RPC" --home "$INFERENCED_HOME" --chain-id "$CHAIN_ID" -o json \
    | jq -r --arg m "$MODEL" '.epoch_group_data[] | select(.model_id==$m) | .validation_weights | length' \
    | head -1)"
  [[ -n "$weights" && "$weights" != "0" && "$weights" != "null" ]] \
    || die "no epoch weights for model $MODEL — wait for PoC cycle"

  local addr spendable
  addr="$(key_address)"
  spendable="$("$INFERENCED" query bank spendable-balances "$addr" \
    --node "$NODE_RPC" --home "$INFERENCED_HOME" --chain-id "$CHAIN_ID" -o json \
    | jq -r '[.balances[] | select(.denom=="ngonka") | .amount][0] // "0"')"
  log "creator=$addr spendable=${spendable}ngonka (need >= ${AMOUNT} for escrow)"
  if [[ "$spendable" -lt "$AMOUNT" ]]; then
    die "insufficient spendable ngonka — claim epoch rewards or fund account"
  fi
}

create_escrow() {
  if [[ -n "${ESCROW_ID:-}" ]]; then
    log "ESCROW_ID already set ($ESCROW_ID) — skip create"
    return 0
  fi

  local create_json=/tmp/create-escrow-v1-$$.json
  log "creating escrow amount=$AMOUNT model=$MODEL"

  printf '%s\n' "$KEYRING_PASSWORD" | "$INFERENCED" tx inference create-devshard-escrow "$AMOUNT" "$MODEL" \
    --from "$KEY_NAME" \
    --chain-id "$CHAIN_ID" \
    --home "$INFERENCED_HOME" \
    --keyring-backend "$KEYRING_BACKEND" \
    --node "$NODE_RPC" \
    --gas auto --gas-adjustment 1.5 \
    --yes --broadcast-mode sync -o json \
    | tee "$create_json" | jq '{code, txhash, raw_log}'

  local code
  code="$(jq -r '.code // empty' "$create_json")"
  [[ "$code" == "0" ]] || die "create-devshard-escrow failed (code=$code)"

  TXHASH="$(jq -r '.txhash' "$create_json")"
  ESCROW_ID=""
  for i in $(seq 1 30); do
    local tx_json
    tx_json="$("$INFERENCED" query tx "$TXHASH" \
      --node "$NODE_RPC" --home "$INFERENCED_HOME" -o json 2>/dev/null || true)"
    if [[ -n "$tx_json" ]]; then
      ESCROW_ID="$(echo "$tx_json" | jq -r '.events[]? | select(.type=="devshard_escrow_created") | .attributes[]? | select(.key=="escrow_id") | .value' | head -1)"
    fi
    if [[ -n "$ESCROW_ID" && "$ESCROW_ID" != "null" ]]; then
      break
    fi
    log "waiting for tx indexing... ($i/30)"
    sleep 2
  done
  [[ -n "$ESCROW_ID" ]] || die "could not resolve ESCROW_ID from tx $TXHASH"
  log "ESCROW_ID=$ESCROW_ID"
  save_state
}

start_client() {
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID not set — run create first or set ESCROW_ID="
  load_state
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID missing in $STATE_FILE"

  export DEVSHARD_PRIVATE_KEY
  DEVSHARD_PRIVATE_KEY="$(export_private_key)"
  export DEVSHARD_STORAGE_PATH="/tmp/devshard-test-v1-${ESCROW_ID}"

  pkill -9 -f "devshardctl.*${DEVSHARD_PORT}" 2>/dev/null || true
  sleep 1

  unset DEVSHARDS_JSON
  rm -rf "$DEVSHARD_STORAGE_PATH"
  mkdir -p "$DEVSHARD_STORAGE_PATH"

  log "starting devshardctl on :$DEVSHARD_PORT escrow=$ESCROW_ID"
  nohup env \
    DEVSHARD_ESCROW_ID="$ESCROW_ID" \
    DEVSHARD_PRIVATE_KEY="$DEVSHARD_PRIVATE_KEY" \
    DEVSHARD_CHAIN_REST="$CHAIN_REST" \
    DEVSHARD_ROUTE_PREFIX=/devshard/v1 \
    DEVSHARD_MODEL="$MODEL" \
    "$DEVSHARDCTL" \
      --escrow-id "$ESCROW_ID" \
      --chain-rest "$CHAIN_REST" \
      --port "$DEVSHARD_PORT" \
      --storage-path "$DEVSHARD_STORAGE_PATH" \
    > "/tmp/devshardctl-v1-${ESCROW_ID}.log" 2>&1 &

  sleep 4
  curl -fsS "http://127.0.0.1:${DEVSHARD_PORT}/v1/status" | jq '{escrow_id, phase, balance, nonce}'
  tail -5 "/tmp/devshardctl-v1-${ESCROW_ID}.log" || true
  save_state
}

chat() {
  [[ -n "${ESCROW_ID:-}" ]] || load_state
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID not set"

  if [[ "$LONG_CHAT" == "1" ]]; then
    chat_long_stream
    return 0
  fi

  local token chat_json http_code nonce
  token="v1-happy-$(date +%s)-$RANDOM"
  chat_json="/tmp/chat-v1-${ESCROW_ID}.json"

  log "chat token=$token"
  http_code="$(curl -sS -m 180 -w "%{http_code}" -o "$chat_json" \
    -X POST "http://127.0.0.1:${DEVSHARD_PORT}/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Say hello briefly. Token: ${token}\"}],\"max_tokens\":32}")"
  log "chat HTTP=$http_code"
  jq '{id, model, content: .choices[0].message.content, error}' "$chat_json" 2>/dev/null || cat "$chat_json"

  [[ "$http_code" == "200" ]] || die "chat failed HTTP=$http_code — see /tmp/devshardctl-v1-${ESCROW_ID}.log"

  nonce="$(curl -fsS "http://127.0.0.1:${DEVSHARD_PORT}/v1/status" | jq -r '.nonce // 0')"
  log "nonce=$nonce"
  [[ "${nonce:-0}" -ge 1 ]] || die "nonce still 0 after chat"

  grep -E 'send_failed|error' "/tmp/devshardctl-v1-${ESCROW_ID}.log" | tail -10 || true
  save_state
}

build_long_stream_payload() {
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
        {role: "user", content: ("Token: " + $token + ". We are stress-testing a long in-flight devshard v1 stream. Please generate the longest possible response.")},
        {role: "assistant", content: "Understood. I will produce a very long, detailed multi-section answer and keep writing until the token limit."},
        {role: "user", content: "Section 1: Explain distributed consensus (Raft, PBFT, Tendermint) in depth with examples."},
        {role: "user", content: "Section 2: Explain blockchain upgrade strategies (halt, live, cosmovisor, blue/green) and trade-offs."},
        {role: "user", content: "Section 3: Explain what happens to in-flight inference when validators stop but API and ML nodes stay up."},
        {role: "user", content: "Section 4: Continue with a detailed case study and do not stop early — use the full token budget."}
      ]
    }'
}

chat_long_stream() {
  local token stream_log meta http_code nonce chunk_count
  token="v1-long-$(date +%s)-$RANDOM"
  stream_log="/tmp/chat-v1-long-${ESCROW_ID}.sse"
  meta="/tmp/chat-v1-long-${ESCROW_ID}.meta"

  log "long stream chat token=$token max_tokens=$STREAM_MAX_TOKENS timeout=${STREAM_TIMEOUT_SEC}s"
  build_long_stream_payload "$token" >/tmp/chat-v1-long.payload.json

  rm -f "$stream_log" "$meta"
  chunk_count=0
  while IFS= read -r line; do
    if [[ "$line" == data:* ]]; then
      chunk_count=$((chunk_count + 1))
    fi
    echo "$line"
  done < <(
    curl -sS -N -m "$STREAM_TIMEOUT_SEC" \
      -X POST "http://127.0.0.1:${DEVSHARD_PORT}/v1/chat/completions" \
      -H "Content-Type: application/json" \
      -d @/tmp/chat-v1-long.payload.json
  ) >"$stream_log"

  local curl_exit=$?
  {
    echo "token=$token"
    echo "chunk_lines=$chunk_count"
    echo "curl_exit=$curl_exit"
    if grep -q '\[DONE\]' "$stream_log" 2>/dev/null; then
      echo "stream_complete=true"
    else
      echo "stream_complete=false"
    fi
  } >"$meta"
  cat "$meta"

  [[ "$curl_exit" -eq 0 ]] || die "long stream curl failed exit=$curl_exit — see $stream_log"
  grep -q '\[DONE\]' "$stream_log" || die "long stream missing [DONE] — see $stream_log"
  log "stream lines=$(wc -l <"$stream_log") chunks=$chunk_count"

  nonce="$(curl -fsS "http://127.0.0.1:${DEVSHARD_PORT}/v1/status" | jq -r '.nonce // 0')"
  log "nonce=$nonce"
  [[ "${nonce:-0}" -ge 1 ]] || die "nonce still 0 after long stream"

  grep -E 'send_failed|error|timeout' "/tmp/devshardctl-v1-${ESCROW_ID}.log" | tail -15 || true
  save_state
}

finalize_escrow() {
  [[ -n "${ESCROW_ID:-}" ]] || load_state
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID not set"

  SETTLEMENT_FILE="/tmp/settlement-v1-${ESCROW_ID}.json"
  log "finalize → $SETTLEMENT_FILE"

  curl -fS -X POST "http://127.0.0.1:${DEVSHARD_PORT}/v1/finalize" \
    -o "$SETTLEMENT_FILE"

  jq '{escrow_id, version, nonce, fees}' "$SETTLEMENT_FILE"

  local proto eid
  proto="$(jq -r '.version // empty' "$SETTLEMENT_FILE")"
  eid="$(jq -r '.escrow_id // empty' "$SETTLEMENT_FILE")"
  [[ "$proto" == "v1" ]] || die "expected protocol version v1 got ${proto:-<empty>}"
  [[ "$eid" == "$ESCROW_ID" ]] || die "settlement escrow_id=$eid != ESCROW_ID=$ESCROW_ID"

  pkill -f "devshardctl.*${DEVSHARD_PORT}" 2>/dev/null || true
  save_state
}

settle_on_chain() {
  [[ "$SKIP_SETTLE" == "1" ]] && { log "SKIP_SETTLE=1"; return 0; }
  [[ -n "${ESCROW_ID:-}" ]] || load_state
  SETTLEMENT_FILE="${SETTLEMENT_FILE:-/tmp/settlement-v1-${ESCROW_ID}.json}"
  [[ -f "$SETTLEMENT_FILE" ]] || die "missing $SETTLEMENT_FILE — run finalize first"
  SETTLEMENT_TX_FILE="/tmp/settlement-v1-fixed-${ESCROW_ID}.json"

  log "patch settlement field version → state_root_and_protocol_version"
  jq 'if .state_root_and_protocol_version == null then .state_root_and_protocol_version = .version else . end' \
    "$SETTLEMENT_FILE" > "$SETTLEMENT_TX_FILE"

  log "settle on chain from $SETTLEMENT_TX_FILE"
  local settle_json=/tmp/settle-v1-$$.json
  printf '%s\n' "$KEYRING_PASSWORD" | "$INFERENCED" tx inference settle-devshard-escrow "$SETTLEMENT_TX_FILE" \
    --from "$KEY_NAME" \
    --chain-id "$CHAIN_ID" \
    --home "$INFERENCED_HOME" \
    --keyring-backend "$KEYRING_BACKEND" \
    --node "$NODE_RPC" \
    --gas auto --gas-adjustment 1.5 \
    --yes --broadcast-mode sync -o json \
    | tee "$settle_json" | jq '{code, txhash, raw_log}'

  [[ "$(jq -r '.code // empty' "$settle_json")" == "0" ]] || die "settle tx failed"

  local settled=""
  for i in $(seq 1 15); do
    settled=$("$INFERENCED" query inference show-devshard-escrow "$ESCROW_ID" \
      --node "$NODE_RPC" --home "$INFERENCED_HOME" -o json \
      | jq -r '.escrow.settled // false')
    [[ "$settled" == "true" ]] && break
    sleep 2
  done

  "$INFERENCED" query inference show-devshard-escrow "$ESCROW_ID" \
    --node "$NODE_RPC" \
    --home "$INFERENCED_HOME" -o json \
    | jq '{id: .escrow.id, settled: .escrow.settled, nonce: .escrow.nonce}'
  [[ "$settled" == "true" ]] || die "escrow not settled on chain after settle tx"

  log "PASS escrow=$ESCROW_ID state=$STATE_FILE"
}

main() {
  load_state
  [[ -n "${ESCROW_ID:-}" ]] && log "loaded ESCROW_ID=$ESCROW_ID from $STATE_FILE"

  if should_run preflight; then preflight; fi
  if should_run create; then create_escrow; fi
  if should_run start; then start_client; fi
  if should_run chat; then chat; fi
  if should_run finalize; then finalize_escrow; fi
  if should_run settle; then settle_on_chain; fi

  if [[ "$RUN_STEP" == "all" ]]; then
    log "full happy path complete"
  else
    log "step '$RUN_STEP' complete"
  fi
}

main "$@"
