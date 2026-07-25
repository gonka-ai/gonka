#!/usr/bin/env bash
# Devshard gateway v2 smoke test (join host with devshardctl-multi on :18080).
#
# Flow: preflight → create escrow → register in gateway → chat (direct + proxy) → finalize → settle
#
# Requires config.devshard.env (API keys, DEVSHARD_PRIVATE_KEY, DEVSHARD_ROUTE_PREFIX=/devshard/v2).
#
# Usage (on 702111):
#   cd /srv/dai/gonka/deploy/join
#   ./scripts/devshard-gateway-v2-smoke-test.sh
#   RUN_STEP=preflight|create|register|chat|finalize|settle ./scripts/devshard-gateway-v2-smoke-test.sh
#   ESCROW_ID=22 RUN_STEP=register ./scripts/devshard-gateway-v2-smoke-test.sh
#
# Env:
#   ESCROW_CREATOR_KEY  — tx signer (default gonka-account-key; not join config.env KEY_NAME)
#   GATEWAY_BASE  API_BASE  API_PORT  DEVSHARD_ENV_FILE
#   WAIT_FOR_POC=1  SKIP_PROXY=1  SKIP_SETTLE=1  STATE_FILE
#   FRESH=1         — reset gateway.db before register (needs gateway stop/start)
#   DEVSHARDCTL_BIN — v2 devshardctl binary for gateway (default /srv/dai/devshardctl-v2-release)
#   LONG_CHAT=1     — streaming multi-section prompt (max_tokens=STREAM_MAX_TOKENS)
#   STREAM_MAX_TOKENS=4096  STREAM_TIMEOUT_SEC=900
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR="${JOIN_DIR:-$(cd "$SCRIPT_DIR/.." && pwd)}"
DEVSHARD_ENV_FILE="${DEVSHARD_ENV_FILE:-$JOIN_DIR/config.devshard.env}"

if [[ -f "$DEVSHARD_ENV_FILE" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "$DEVSHARD_ENV_FILE"
  set +a
fi

if [[ -f "$JOIN_DIR/config.env" ]]; then
  set -a
  # shellcheck disable=SC1091
  source "$JOIN_DIR/config.env"
  set +a
fi

INFERENCED="${INFERENCED:-/srv/dai/inferenced}"
INFERENCED_HOME="${INFERENCED_HOME:-/srv/dai/.inference}"
KEY_NAME="${ESCROW_CREATOR_KEY:-gonka-account-key}"
KEYRING_PASSWORD="${KEYRING_PASSWORD:-12345678}"
KEYRING_BACKEND="${KEYRING_BACKEND:-file}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-4}"

API_PORT="${API_PORT:-8000}"
API_BASE="${API_BASE:-http://127.0.0.1:${API_PORT}}"
NODE_RPC="${NODE_RPC:-${API_BASE}/chain-rpc/}"
CHAIN_REST="${CHAIN_REST:-${API_BASE}/chain-api}"
GATEWAY_BASE="${GATEWAY_BASE:-http://127.0.0.1:18080}"

MODEL="${MODEL:-${DEVSHARD_MODEL:-Qwen/Qwen3-4B-Instruct-2507}}"
AMOUNT="${AMOUNT:-5000000000}"
DEVSHARD_ROUTE_PREFIX="${DEVSHARD_ROUTE_PREFIX:-/devshard/v2}"

RUN_STEP="${RUN_STEP:-all}"
STATE_FILE="${STATE_FILE:-/tmp/devshard-gateway-v2-smoke.state}"
WAIT_FOR_POC="${WAIT_FOR_POC:-1}"
SKIP_PROXY="${SKIP_PROXY:-0}"
SKIP_DIRECT="${SKIP_DIRECT:-0}"
SKIP_SETTLE="${SKIP_SETTLE:-0}"
FRESH="${FRESH:-0}"
DEVSHARDCTL_BIN="${DEVSHARDCTL_BIN:-/srv/dai/devshardctl-v2-release}"
GATEWAY_COMPOSE_FILES="${GATEWAY_COMPOSE_FILES:-docker-compose.devshard-gateway.yml docker-compose.devshard-gateway-v2-binary.override.yml}"
LONG_CHAT="${LONG_CHAT:-0}"
STREAM_MAX_TOKENS="${STREAM_MAX_TOKENS:-4096}"
STREAM_TIMEOUT_SEC="${STREAM_TIMEOUT_SEC:-900}"
GATEWAY_CONTAINER="${GATEWAY_CONTAINER:-devshardctl-multi}"

log() { echo "[gw-v2-smoke] $*"; }
die() { echo "[gw-v2-smoke] ERROR: $*" >&2; exit 1; }

should_run() {
  local step="$1"
  [[ "$RUN_STEP" == "all" || "$RUN_STEP" == "$step" ]]
}

admin_key() {
  [[ -n "${DEVSHARD_ADMIN_API_KEY:-}" ]] || die "DEVSHARD_ADMIN_API_KEY not set in $DEVSHARD_ENV_FILE"
  echo "$DEVSHARD_ADMIN_API_KEY"
}

api_key() {
  [[ -n "${DEVSHARD_API_KEYS:-}" ]] || die "DEVSHARD_API_KEYS not set in $DEVSHARD_ENV_FILE"
  echo "${DEVSHARD_API_KEYS%%,*}"
}

save_state() {
  cat >"$STATE_FILE" <<EOF
ESCROW_ID=${ESCROW_ID:-}
TXHASH=${TXHASH:-}
MODEL=${MODEL}
AMOUNT=${AMOUNT}
GATEWAY_BASE=${GATEWAY_BASE}
SETTLEMENT_FILE=${SETTLEMENT_FILE:-}
EOF
}

load_state() {
  [[ -f "$STATE_FILE" ]] || return 0
  # shellcheck disable=SC1090
  source "$STATE_FILE"
}

wait_for_inference() {
  [[ "$WAIT_FOR_POC" == "1" ]] || { log "WAIT_FOR_POC=0"; return 0; }
  log "waiting for Inference phase..."
  local phase conf
  for i in $(seq 1 120); do
    phase="$(curl -fsS "$API_BASE/v1/epochs/latest" | jq -r '.phase // "unknown"')"
    conf="$(curl -fsS "$API_BASE/v1/epochs/latest" | jq -r '.is_confirmation_poc_active // false')"
    log "phase=$phase confirmation_poc=$conf"
    [[ "$phase" == "Inference" && "$conf" == "false" ]] && return 0
    sleep 5
  done
  die "chain not in Inference after wait — run with WAIT_FOR_POC=0 to skip"
}

preflight() {
  [[ -x "$INFERENCED" ]] || die "inferenced not found: $INFERENCED"
  [[ -f "$DEVSHARD_ENV_FILE" ]] || die "missing $DEVSHARD_ENV_FILE"
  [[ -n "${DEVSHARD_PRIVATE_KEY:-}" ]] || die "DEVSHARD_PRIVATE_KEY not set in $DEVSHARD_ENV_FILE"

  wait_for_inference

  [[ "$DEVSHARD_ROUTE_PREFIX" == "/devshard/v2" ]] \
    || die "DEVSHARD_ROUTE_PREFIX=$DEVSHARD_ROUTE_PREFIX (expected /devshard/v2)"

  curl -fsS "$GATEWAY_BASE/v1/status" >/dev/null || die "gateway not reachable at $GATEWAY_BASE"
  curl -fsS "$API_BASE/devshard/v2/healthz" >/dev/null || die "host /devshard/v2/healthz failed"
  curl -fsS "$API_BASE/devshard-gateway/v1/status" >/dev/null || die "proxy /devshard-gateway/v1/status failed"

  log "gateway ok route=$DEVSHARD_ROUTE_PREFIX model=$MODEL"
}

ensure_v2_gateway() {
  [[ -x "$DEVSHARDCTL_BIN" ]] || die "v2 gateway binary not found: $DEVSHARDCTL_BIN"

  local mounted
  mounted="$(docker inspect devshardctl-multi --format '{{range .Mounts}}{{if eq .Destination "/usr/bin/devshardctl"}}{{.Source}}{{end}}{{end}}' 2>/dev/null || true)"
  if [[ -n "$mounted" && "$mounted" != "$DEVSHARDCTL_BIN" ]]; then
    log "gateway binary mount=$mounted (expected $DEVSHARDCTL_BIN)"
  fi

  curl -fsS "$GATEWAY_BASE/v1/status" | jq -e '.mode == "gateway"' >/dev/null \
    || die "gateway /v1/status not in gateway mode"
}

reset_gateway_state() {
  [[ "$FRESH" == "1" ]] || return 0
  log "FRESH=1 — resetting gateway.db and recreating container with v2 binary"

  local storage_dir="${JOIN_DIR}/${DEVSHARD_STORAGE_HOST_DIR:-.devshardctl}"
  local compose=(docker compose)
  local f
  for f in $GATEWAY_COMPOSE_FILES; do
    compose+=(-f "$JOIN_DIR/$f")
  done

  (cd "$JOIN_DIR" && docker stop devshardctl-multi 2>/dev/null || true)
  if [[ -f "$storage_dir/gateway.db" ]]; then
    cp "$storage_dir/gateway.db" "$storage_dir/gateway.db.bak.$(date +%s)"
    rm -f "$storage_dir/gateway.db"
  fi
  ESCROW_ID=""
  TXHASH=""
  rm -f "$STATE_FILE"

  DEVSHARDCTL_BIN="$DEVSHARDCTL_BIN" "${compose[@]}" up -d --force-recreate
  for i in $(seq 1 30); do
    curl -fsS "$GATEWAY_BASE/v1/status" >/dev/null 2>&1 && break
    sleep 1
  done
  ensure_v2_gateway
}

create_escrow() {
  if [[ -n "${ESCROW_ID:-}" ]]; then
    log "ESCROW_ID=$ESCROW_ID — skip create"
    return 0
  fi

  local create_json=/tmp/create-escrow-gw-v2-$$.json
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

  [[ "$(jq -r '.code // empty' "$create_json")" == "0" ]] || die "create-devshard-escrow failed"
  TXHASH="$(jq -r '.txhash' "$create_json")"

  ESCROW_ID=""
  for i in $(seq 1 30); do
    local tx_json
    tx_json="$("$INFERENCED" query tx "$TXHASH" \
      --node "$NODE_RPC" --home "$INFERENCED_HOME" -o json 2>/dev/null || true)"
    if [[ -n "$tx_json" ]]; then
      ESCROW_ID="$(echo "$tx_json" | jq -r '.events[]? | select(.type=="devshard_escrow_created") | .attributes[]? | select(.key=="escrow_id") | .value' | head -1)"
    fi
    [[ -n "$ESCROW_ID" && "$ESCROW_ID" != "null" ]] && break
    log "waiting for tx indexing... ($i/30)"
    sleep 2
  done
  [[ -n "$ESCROW_ID" ]] || die "could not resolve ESCROW_ID from tx $TXHASH"
  log "ESCROW_ID=$ESCROW_ID"
  save_state
}

register_gateway() {
  [[ -n "${ESCROW_ID:-}" ]] || load_state
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID not set"

  local admin reg
  admin="$(admin_key)"
  log "register escrow $ESCROW_ID in gateway"
  reg="$(curl -sS -X POST "$GATEWAY_BASE/v1/admin/devshards" \
    -H "Authorization: Bearer $admin" \
    -H "Content-Type: application/json" \
    -d "{\"id\":\"$ESCROW_ID\",\"private_key\":\"$DEVSHARD_PRIVATE_KEY\",\"model\":\"$MODEL\"}")"
  echo "$reg" | jq '{id, model, active, error: .error.message}'

  echo "$reg" | jq -e '.id == "'"$ESCROW_ID"'"' >/dev/null \
    || die "gateway register failed: $(echo "$reg" | jq -r '.error.message // .')"

  curl -sS -X POST "$GATEWAY_BASE/v1/admin/settings" \
    -H "Authorization: Bearer $admin" \
    -H "Content-Type: application/json" \
    -d '{
      "max_concurrent_requests": 512,
      "default_request_max_tokens": '"${STREAM_MAX_TOKENS:-3072}"',
      "request_max_tokens_cap": '"${STREAM_MAX_TOKENS:-4096}"',
      "model_limits": [{
        "model_id": "'"$MODEL"'",
        "access_mode": "api_key",
        "max_concurrent_requests": 512
      }]
    }' | jq '{max_concurrent_requests, model_limits: [.model_limits[]? | {model_id, access_mode}]}' 2>/dev/null || true

  sleep 2
  curl -fsS "$GATEWAY_BASE/v1/admin/devshards" -H "Authorization: Bearer $admin" \
    | jq --arg id "$ESCROW_ID" '.devshards[] | select(.id==$id) | .runtime | {id, protocol_version, phase, nonce, requests_blocked, block_reason}'

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
        {role: "user", content: ("Token: " + $token + ". Stress-test a long in-flight devshard gateway v2 stream.")},
        {role: "assistant", content: "Understood. I will produce a very long, detailed multi-section answer and keep writing until the token limit."},
        {role: "user", content: "Section 1: Explain distributed consensus (Raft, PBFT, Tendermint) in depth with examples."},
        {role: "user", content: "Section 2: Explain blockchain upgrade strategies (halt, live, cosmovisor, blue/green) and trade-offs."},
        {role: "user", content: "Section 3: Explain what happens to in-flight inference when validators stop but API and ML nodes stay up."},
        {role: "user", content: "Section 4: Continue with a detailed case study and do not stop early — use the full token budget."}
      ]
    }'
}

report_inference_serving() {
  local client_path="$1"
  local client_url="$2"
  local response_id="${3:-}"
  local stream_log="${4:-}"
  local chunk_count="${5:-0}"

  local nonce host winner outcome decision
  nonce="$(sed -n 's/.*-\([0-9][0-9]*\)$/\1/p' <<<"$response_id" | head -1)"

  if [[ -n "$nonce" ]]; then
    local logs
    logs="$(docker logs "$GATEWAY_CONTAINER" 2>&1 || true)"
    host="$(grep "escrow=${ESCROW_ID} nonce=${nonce} " <<<"$logs" \
      | grep -E 'race_completed|request_fully_settled|first_token' \
      | grep -oE 'host=[^ ]+' | tail -1 | cut -d= -f2 || true)"
    winner="$(grep "escrow=${ESCROW_ID} nonce=${nonce} " <<<"$logs" \
      | grep 'race_completed' | grep -oE 'winner=[^ ]+' | tail -1 | cut -d= -f2 || true)"
    outcome="$(grep "escrow=${ESCROW_ID} " <<<"$logs" \
      | grep 'request_fully_settled' | grep "winner_nonce=${nonce}" \
      | grep -oE 'outcome=[^ ]+' | tail -1 | cut -d= -f2 || true)"
    decision="$(grep "escrow=${ESCROW_ID} " <<<"$logs" \
      | grep 'request_fully_settled' | grep "winner_nonce=${nonce}" \
      | grep -oE 'decision=[^ ]+' | tail -1 | cut -d= -f2 || true)"
  fi

  log "=== inference serving report: $client_path ==="
  log "  client_url: $client_url"
  log "  gateway_container: $GATEWAY_CONTAINER"
  log "  host_route_prefix: $DEVSHARD_ROUTE_PREFIX (hosts: {public_api}${DEVSHARD_ROUTE_PREFIX}/sessions/${ESCROW_ID}/...)"
  log "  response_id: ${response_id:-n/a}"
  log "  winner_nonce: ${nonce:-n/a}"
  log "  winning_host: ${host:-unknown}"
  log "  race_winner_flag: ${winner:-unknown}"
  log "  outcome: ${outcome:-unknown} decision: ${decision:-unknown}"
  if [[ -n "$stream_log" ]]; then
    log "  stream_chunks: $chunk_count stream_complete=$(grep -q '\[DONE\]' "$stream_log" 2>/dev/null && echo true || echo false)"
    log "  stream_log: $stream_log"
  fi
}

run_chat_request() {
  local client_path="$1"
  local client_url="$2"
  local token="$3"
  local key="$4"
  local body http_code response_id

  if [[ "$LONG_CHAT" == "1" ]]; then
    local stream_log meta chunk_count payload
    stream_log="/tmp/gw-smoke-chat-${client_path}-${ESCROW_ID}.sse"
    meta="/tmp/gw-smoke-chat-${client_path}-${ESCROW_ID}.meta"
    payload="/tmp/gw-smoke-chat-${client_path}-${ESCROW_ID}.payload.json"
    build_long_stream_payload "$token" >"$payload"

    log "long stream via $client_path token=$token max_tokens=$STREAM_MAX_TOKENS timeout=${STREAM_TIMEOUT_SEC}s"
    rm -f "$stream_log" "$meta"
    chunk_count=0
    curl -sS -N -m "$STREAM_TIMEOUT_SEC" \
      -X POST "$client_url" \
      -H "Authorization: Bearer $key" \
      -H "Content-Type: application/json" \
      -d @"$payload" >"$stream_log" &
    local curl_pid=$!
    while kill -0 "$curl_pid" 2>/dev/null; do
      sleep 5
      chunk_count="$(grep -c '^data:' "$stream_log" 2>/dev/null || echo 0)"
      log "$client_path streaming... chunks=$chunk_count"
    done
    wait "$curl_pid"
    local curl_exit=$?
    response_id="$(grep -oE '"id"[[:space:]]*:[[:space:]]*"[^"]+"' "$stream_log" | head -1 | sed 's/.*"\([^"]*\)"$/\1/')"
    {
      echo "client_path=$client_path"
      echo "token=$token"
      echo "response_id=$response_id"
      echo "chunk_lines=$chunk_count"
      echo "curl_exit=$curl_exit"
      grep -q '\[DONE\]' "$stream_log" && echo "stream_complete=true" || echo "stream_complete=false"
    } >"$meta"
    cat "$meta"

    [[ "$curl_exit" -eq 0 ]] || die "$client_path long stream curl failed exit=$curl_exit — see $stream_log"
    grep -q '\[DONE\]' "$stream_log" || die "$client_path long stream missing [DONE] — see $stream_log"
    log "$client_path stream lines=$(wc -l <"$stream_log") chunks=$chunk_count"
    report_inference_serving "$client_path" "$client_url" "$response_id" "$stream_log" "$chunk_count"
    return 0
  fi

  body="/tmp/gw-smoke-chat-${client_path}-${ESCROW_ID}.json"
  http_code="$(curl -sS -m 180 -w "%{http_code}" -o "$body" \
    -X POST "$client_url" \
    -H "Authorization: Bearer $key" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Say hello briefly. Token: ${token}\"}],\"max_tokens\":32}")"
  log "$client_path HTTP=$http_code"
  jq '{id, content: .choices[0].message.content, error}' "$body" 2>/dev/null || cat "$body"
  [[ "$http_code" == "200" ]] || die "$client_path gateway chat failed HTTP=$http_code"
  response_id="$(jq -r '.id // empty' "$body")"
  report_inference_serving "$client_path" "$client_url" "$response_id"
}

chat_smoke() {
  [[ -n "${ESCROW_ID:-}" ]] || load_state
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID not set"

  local token key
  token="gw-v2-smoke-$(date +%s)-$RANDOM"
  key="$(api_key)"

  if [[ "$SKIP_DIRECT" != "1" ]]; then
    run_chat_request "direct" "${GATEWAY_BASE}/v1/chat/completions" "$token" "$key"
  fi

  if [[ "$SKIP_PROXY" != "1" ]]; then
    run_chat_request "proxy" "${API_BASE}/devshard-gateway/v1/chat/completions" "${token}-p" "$key"
  fi

  local nonce
  nonce="$(curl -fsS "$GATEWAY_BASE/v1/admin/devshards" -H "Authorization: Bearer $(admin_key)" \
    | jq -r --arg id "$ESCROW_ID" '.devshards[] | select(.id==$id) | .runtime.nonce // 0')"
  log "runtime nonce=$nonce"
  [[ "${nonce:-0}" -ge 1 ]] || die "nonce still 0 after chat"
  save_state
}

finalize_escrow() {
  [[ -n "${ESCROW_ID:-}" ]] || load_state
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID not set"

  SETTLEMENT_FILE="/tmp/gw-settlement-v2-${ESCROW_ID}.json"
  local finalize_url="${GATEWAY_BASE}/devshard/${ESCROW_ID}/v1/finalize"
  log "finalize → $SETTLEMENT_FILE url=$finalize_url"

  curl -fS -X POST "$finalize_url" \
    -H "Authorization: Bearer $(admin_key)" \
    -o "$SETTLEMENT_FILE"

  jq '{escrow_id, version, state_root_and_protocol_version, nonce, fees}' "$SETTLEMENT_FILE"

  local eid
  eid="$(jq -r '.escrow_id // empty' "$SETTLEMENT_FILE")"
  [[ "$eid" == "$ESCROW_ID" ]] || die "settlement escrow_id=$eid != ESCROW_ID=$ESCROW_ID"
  save_state
}

settle_on_chain() {
  [[ "$SKIP_SETTLE" == "1" ]] && { log "SKIP_SETTLE=1"; return 0; }
  [[ -n "${ESCROW_ID:-}" ]] || load_state
  SETTLEMENT_FILE="${SETTLEMENT_FILE:-/tmp/gw-settlement-v2-${ESCROW_ID}.json}"
  [[ -f "$SETTLEMENT_FILE" ]] || die "missing $SETTLEMENT_FILE"

  local tx_file fixed
  tx_file="/tmp/gw-settle-tx-${ESCROW_ID}.json"
  fixed="/tmp/gw-settlement-v2-fixed-${ESCROW_ID}.json"

  jq 'if .state_root_and_protocol_version == null then .state_root_and_protocol_version = (.version // "v1") else . end' \
    "$SETTLEMENT_FILE" > "$fixed"

  log "settle on chain from $fixed"
  printf '%s\n' "$KEYRING_PASSWORD" | "$INFERENCED" tx inference settle-devshard-escrow "$fixed" \
    --from "$KEY_NAME" \
    --chain-id "$CHAIN_ID" \
    --home "$INFERENCED_HOME" \
    --keyring-backend "$KEYRING_BACKEND" \
    --node "$NODE_RPC" \
    --gas auto --gas-adjustment 1.5 \
    --yes --broadcast-mode sync -o json \
    | tee "$tx_file" | jq '{code, txhash, raw_log}'

  [[ "$(jq -r '.code // empty' "$tx_file")" == "0" ]] || die "settle tx failed"

  local settled=""
  for i in $(seq 1 15); do
    settled="$("$INFERENCED" query inference show-devshard-escrow "$ESCROW_ID" \
      --node "$NODE_RPC" --home "$INFERENCED_HOME" -o json \
      | jq -r '.escrow.settled // false')"
    [[ "$settled" == "true" ]] && break
    sleep 2
  done

  "$INFERENCED" query inference show-devshard-escrow "$ESCROW_ID" \
    --node "$NODE_RPC" --home "$INFERENCED_HOME" -o json \
    | jq '{id: .escrow.id, settled: .escrow.settled}'
  [[ "$settled" == "true" ]] || die "escrow not settled on chain"

  log "PASS escrow=$ESCROW_ID state=$STATE_FILE"
}

main() {
  load_state
  [[ -n "${ESCROW_ID:-}" ]] && log "loaded ESCROW_ID=$ESCROW_ID from $STATE_FILE"

  if should_run preflight || [[ "$FRESH" == "1" ]]; then reset_gateway_state; fi
  if should_run preflight; then preflight; fi
  if should_run create; then create_escrow; fi
  if should_run register; then register_gateway; fi
  if should_run chat; then chat_smoke; fi
  if should_run finalize; then finalize_escrow; fi
  if should_run settle; then settle_on_chain; fi

  if [[ "$RUN_STEP" == "all" ]]; then
    log "full gateway v2 smoke test complete"
  else
    log "step '$RUN_STEP' complete"
  fi
}

main "$@"
