#!/usr/bin/env bash
# Devshard v2 standalone happy path: create escrow → devshardctl → chat → finalize → settle.
#
# Run on a join host with versiond (e.g. 702111 / gonka-testnet-4).
# See docs/devshard-standalone-v1-v2-build-and-e2e-testnet.md
#
# Usage:
#   ./scripts/devshard-v2-standalone-happy-path.sh              # full run
#   RUN_STEP=preflight ./scripts/devshard-v2-standalone-happy-path.sh
#   RUN_STEP=create ./scripts/devshard-v2-standalone-happy-path.sh
#   ESCROW_ID=3 RUN_STEP=start ./scripts/devshard-v2-standalone-happy-path.sh
#   RUN_STEP=chat|finalize|settle ./scripts/devshard-v2-standalone-happy-path.sh
#
# Optional env:
#   INFERENCED  INFERENCED_HOME  KEY_NAME  KEYRING_PASSWORD  CHAIN_ID
#   API_BASE  MODEL  AMOUNT  DEVSHARDCTL  DEVSHARD_PORT  DEVSHARD_ADMIN_API_KEY
#   STATE_FILE  SKIP_PREFLIGHT=1  SKIP_SETTLE=1
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

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

DEVSHARDCTL="${DEVSHARDCTL:-/srv/dai/devshardctl-v2-release}"
DEVSHARD_PORT="${DEVSHARD_PORT:-18081}"
DEVSHARD_ADMIN_API_KEY="${DEVSHARD_ADMIN_API_KEY:-sk-admin-test}"

RUN_STEP="${RUN_STEP:-all}"
STATE_FILE="${STATE_FILE:-/tmp/devshard-v2-happy-path.state}"
SKIP_PREFLIGHT="${SKIP_PREFLIGHT:-0}"
SKIP_SETTLE="${SKIP_SETTLE:-0}"

log() { echo "[devshard-v2] $*"; }
die() { echo "[devshard-v2] ERROR: $*" >&2; exit 1; }

should_run() {
  local step="$1"
  [[ "$RUN_STEP" == "all" || "$RUN_STEP" == "$step" ]]
}

save_state() {
  # shellcheck disable=SC2064
  cat >"$STATE_FILE" <<EOF
ESCROW_ID=${ESCROW_ID:-}
TXHASH=${TXHASH:-}
MODEL=${MODEL}
AMOUNT=${AMOUNT}
DEVSHARD_PORT=${DEVSHARD_PORT}
SETTLEMENT_FILE=${SETTLEMENT_FILE:-}
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
  curl -fsS "$API_BASE/devshard/v2/healthz" >/dev/null || die "v2 healthz failed"
  log "host healthz: v1 + v2 ok"

  local params
  params="$(curl -fsS "$CHAIN_REST/productscience/inference/inference/params")"
  echo "$params" | jq -e '.params.devshard_escrow_params.approved_versions[] | select(.name=="v2")' >/dev/null \
    || die "v2 not in approved_versions — run gov-propose-devshard-approved-versions.sh"

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
    die "insufficient spendable ngonka — claim epoch rewards or fund account (UI GONKA != bank balance until claim-rewards)"
  fi
}

create_escrow() {
  if [[ -n "${ESCROW_ID:-}" ]]; then
    log "ESCROW_ID already set ($ESCROW_ID) — skip create"
    return 0
  fi

  local create_json=/tmp/create-escrow-v2-$$.json
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
  export DEVSHARD_STORAGE_DIR="/tmp/devshard-test-v2-${ESCROW_ID}"

  pkill -9 -f "devshardctl.*${DEVSHARD_PORT}" 2>/dev/null || true
  sleep 1

  unset DEVSHARDS_JSON
  rm -rf "$DEVSHARD_STORAGE_DIR"
  mkdir -p "$DEVSHARD_STORAGE_DIR"

  log "starting devshardctl on :$DEVSHARD_PORT escrow=$ESCROW_ID"
  nohup env \
    DEVSHARD_ESCROW_ID="$ESCROW_ID" \
    DEVSHARD_PRIVATE_KEY="$DEVSHARD_PRIVATE_KEY" \
    DEVSHARD_CHAIN_REST="$CHAIN_REST" \
    DEVSHARD_PUBLIC_API="$API_BASE" \
    DEVSHARD_ROUTE_PREFIX=/devshard/v2 \
    DEVSHARD_MODEL="$MODEL" \
    DEVSHARD_STORAGE_DIR="$DEVSHARD_STORAGE_DIR" \
    DEVSHARD_ADMIN_API_KEY="$DEVSHARD_ADMIN_API_KEY" \
    DEVSHARD_POC_REQUEST_MODE=relaxed \
    DEVSHARD_CAPACITY_AWARE_LIMITS=off \
    "$DEVSHARDCTL" \
      --escrow-id "$ESCROW_ID" \
      --chain-rest "$CHAIN_REST" \
      --public-api "$API_BASE" \
      --port "$DEVSHARD_PORT" \
    > "/tmp/devshardctl-v2-${ESCROW_ID}.log" 2>&1 &

  sleep 4
  local shards_line
  shards_line="$(grep 'devshards=' "/tmp/devshardctl-v2-${ESCROW_ID}.log" | tail -1 || true)"
  log "startup: ${shards_line:-<no devshards= line yet>}"
  echo "$shards_line" | grep -q 'devshards=1' || die "expected devshards=1 — unset DEVSHARDS_JSON? check log"

  curl -fsS "http://127.0.0.1:${DEVSHARD_PORT}/v1/status" | jq '{escrow_id, phase, balance, nonce}'

  curl -sS -X POST "http://127.0.0.1:${DEVSHARD_PORT}/v1/admin/settings" \
    -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" \
    -H "Content-Type: application/json" \
    -d '{
      "max_concurrent_requests": 512,
      "default_request_max_tokens": 3072,
      "request_max_tokens_cap": 4096,
      "model_limits": [{"model_id":"'"$MODEL"'","access_mode":"open"}]
    }' | jq .

  save_state
}

chat() {
  [[ -n "${ESCROW_ID:-}" ]] || load_state
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID not set"

  local token chat_json http_code nonce
  token="v2-happy-$(date +%s)-$RANDOM"
  chat_json="/tmp/chat-v2-${ESCROW_ID}.json"

  log "chat token=$token"
  http_code="$(curl -sS -m 180 -w "%{http_code}" -o "$chat_json" \
    -X POST "http://127.0.0.1:${DEVSHARD_PORT}/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Say hello briefly. Token: ${token}\"}],\"max_tokens\":32}")"
  log "chat HTTP=$http_code"
  jq '{id, model, content: .choices[0].message.content, error}' "$chat_json" 2>/dev/null || cat "$chat_json"

  [[ "$http_code" == "200" ]] || die "chat failed HTTP=$http_code — see /tmp/devshardctl-v2-${ESCROW_ID}.log"

  nonce="$(curl -fsS "http://127.0.0.1:${DEVSHARD_PORT}/v1/status" | jq -r '.nonce // 0')"
  log "nonce=$nonce"
  [[ "${nonce:-0}" -ge 1 ]] || die "nonce still 0 after chat"

  grep -E 'gateway_cache|send_failed|error' "/tmp/devshardctl-v2-${ESCROW_ID}.log" | tail -10 || true
  save_state
}

finalize_escrow() {
  [[ -n "${ESCROW_ID:-}" ]] || load_state
  [[ -n "${ESCROW_ID:-}" ]] || die "ESCROW_ID not set"

  SETTLEMENT_FILE="/tmp/settlement-v2-${ESCROW_ID}.json"
  log "finalize → $SETTLEMENT_FILE"

  curl -fS -X POST "http://127.0.0.1:${DEVSHARD_PORT}/v1/finalize" \
    -H "Authorization: Bearer $DEVSHARD_ADMIN_API_KEY" \
    -o "$SETTLEMENT_FILE"

  jq '{escrow_id, version, state_root_and_protocol_version, nonce, fees}' "$SETTLEMENT_FILE"

  local proto eid
  proto="$(jq -r '.state_root_and_protocol_version // .version // empty' "$SETTLEMENT_FILE")"
  eid="$(jq -r '.escrow_id // empty' "$SETTLEMENT_FILE")"
  [[ "$proto" == "v2" ]] || die "expected protocol version v2 got ${proto:-<empty>}"
  [[ "$eid" == "$ESCROW_ID" ]] || die "settlement escrow_id=$eid != ESCROW_ID=$ESCROW_ID"

  pkill -f "devshardctl.*${DEVSHARD_PORT}" 2>/dev/null || true
  save_state
}

settle_on_chain() {
  [[ "$SKIP_SETTLE" == "1" ]] && { log "SKIP_SETTLE=1"; return 0; }
  [[ -n "${ESCROW_ID:-}" ]] || load_state
  SETTLEMENT_FILE="${SETTLEMENT_FILE:-/tmp/settlement-v2-${ESCROW_ID}.json}"
  [[ -f "$SETTLEMENT_FILE" ]] || die "missing $SETTLEMENT_FILE — run finalize first"

  log "settle on chain from $SETTLEMENT_FILE"
  local settle_json=/tmp/settle-v2-$$.json
  printf '%s\n' "$KEYRING_PASSWORD" | "$INFERENCED" tx inference settle-devshard-escrow "$SETTLEMENT_FILE" \
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
    settled="$("$INFERENCED" query inference show-devshard-escrow "$ESCROW_ID" \
      --node "$NODE_RPC" --home "$INFERENCED_HOME" -o json \
      | jq -r '.escrow.settled // false')"
    [[ "$settled" == "true" ]] && break
    sleep 2
  done

  "$INFERENCED" query inference show-devshard-escrow "$ESCROW_ID" \
    --node "$NODE_RPC" --home "$INFERENCED_HOME" -o json \
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
