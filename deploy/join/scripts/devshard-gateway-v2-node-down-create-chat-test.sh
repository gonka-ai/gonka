#!/usr/bin/env bash
# On 702111: while all validator nodes are stopped, try create escrow + gateway chat.
#
# Usage (normally called by devshard-gateway-v2-node-down-orchestrate.sh):
#   ./scripts/devshard-gateway-v2-node-down-create-chat-test.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# shellcheck disable=SC1091
[[ -f "$JOIN_DIR/config.devshard.env" ]] && source "$JOIN_DIR/config.devshard.env"
# shellcheck disable=SC1091
[[ -f "$JOIN_DIR/config.env" ]] && source "$JOIN_DIR/config.env"

INFERENCED="${INFERENCED:-/srv/dai/inferenced}"
INFERENCED_HOME="${INFERENCED_HOME:-/srv/dai/.inference}"
KEY_NAME="${ESCROW_CREATOR_KEY:-gonka-account-key}"
KEYRING_PASSWORD="${KEYRING_PASSWORD:-12345678}"
KEYRING_BACKEND="${KEYRING_BACKEND:-file}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-4}"
API_BASE="${API_BASE:-http://127.0.0.1:8000}"
NODE_RPC="${NODE_RPC:-${API_BASE}/chain-rpc/}"
GATEWAY_BASE="${GATEWAY_BASE:-http://127.0.0.1:18080}"
MODEL="${MODEL:-${DEVSHARD_MODEL:-Qwen/Qwen3-4B-Instruct-2507}}"
AMOUNT="${AMOUNT:-5000000000}"

REPORT="${REPORT:-/tmp/devshard-gateway-v2-node-down-report.json}"

log() { echo "[node-down-test] $*"; }

api_key() { echo "${DEVSHARD_API_KEYS%%,*}"; }
admin_key() { echo "$DEVSHARD_ADMIN_API_KEY"; }

check_services_down() {
  log "local containers: node=$(docker inspect node --format '{{.State.Status}}' 2>/dev/null || echo missing) api=$(docker inspect api --format '{{.State.Status}}' 2>/dev/null || echo missing)"
  log "public proxy chain-rpc:"
  curl -sS -m 5 -o /dev/null -w "HTTP=%{http_code}\n" "$NODE_RPC" 2>/dev/null || echo "chain-rpc unreachable"
  log "public proxy chain-api:"
  curl -sS -m 5 -o /dev/null -w "HTTP=%{http_code}\n" "${API_BASE}/chain-api/" 2>/dev/null || echo "chain-api unreachable"
  log "public proxy epochs:"
  curl -sS -m 5 -o /dev/null -w "HTTP=%{http_code}\n" "${API_BASE}/v1/epochs/latest" 2>/dev/null || echo "epochs unreachable"
  log "gateway status:"
  curl -sS -m 5 -o /dev/null -w "HTTP=%{http_code}\n" "${GATEWAY_BASE}/v1/status" 2>/dev/null || echo "gateway unreachable"
}

try_create_escrow() {
  log "=== attempt create-devshard-escrow ==="
  local out=/tmp/node-down-create-$$.json
  set +e
  printf '%s\n' "$KEYRING_PASSWORD" | "$INFERENCED" tx inference create-devshard-escrow "$AMOUNT" "$MODEL" \
    --from "$KEY_NAME" \
    --chain-id "$CHAIN_ID" \
    --home "$INFERENCED_HOME" \
    --keyring-backend "$KEYRING_BACKEND" \
    --node "$NODE_RPC" \
    --gas auto --gas-adjustment 1.5 \
    --yes --broadcast-mode sync -o json 2>&1 | tee /tmp/node-down-create-raw.txt | tail -5
  local rc=$?
  set -e
  grep '^{' /tmp/node-down-create-raw.txt 2>/dev/null | tail -1 >"$out" || true
  if [[ -s "$out" ]]; then
    jq '{code, txhash, raw_log}' "$out"
    CREATE_CODE="$(jq -r '.code // "missing"' "$out")"
    CREATE_TXHASH="$(jq -r '.txhash // empty' "$out")"
  else
    CREATE_CODE="error"
    CREATE_TXHASH=""
    log "create output (no json):"
    tail -5 /tmp/node-down-create-raw.txt || true
  fi
  CREATE_RC=$rc
  log "create exit=$rc code=${CREATE_CODE:-unknown}"
}

try_gateway_chat() {
  local escrow_id="${1:-}"
  local label="$2"
  log "=== attempt gateway chat ($label) ==="
  local key body http_code
  key="$(api_key)"
  body="/tmp/node-down-chat-${label}.json"
  set +e
  http_code="$(curl -sS -m 60 -w "%{http_code}" -o "$body" \
    -X POST "${GATEWAY_BASE}/v1/chat/completions" \
    -H "Authorization: Bearer $key" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"$MODEL\",\"messages\":[{\"role\":\"user\",\"content\":\"Node-down test ${label}. Say hi briefly.\"}],\"max_tokens\":24}")"
  local rc=$?
  set -e
  log "chat HTTP=$http_code curl_exit=$rc escrow=${escrow_id:-n/a}"
  jq '{id, content: .choices[0].message.content, error}' "$body" 2>/dev/null || head -c 500 "$body"
  CHAT_HTTP="$http_code"
}

try_register() {
  local escrow_id="$1"
  log "=== attempt register escrow $escrow_id in gateway ==="
  local reg
  reg="$(curl -sS -X POST "${GATEWAY_BASE}/v1/admin/devshards" \
    -H "Authorization: Bearer $(admin_key)" \
    -H "Content-Type: application/json" \
    -d "{\"id\":\"$escrow_id\",\"private_key\":\"$DEVSHARD_PRIVATE_KEY\",\"model\":\"$MODEL\"}")"
  echo "$reg" | jq '{id, active, error: .error.message}'
}

main() {
  log "nodes-down create+chat test started $(date -u +%Y-%m-%dT%H:%M:%SZ)"
  check_services_down

  try_create_escrow

  ESCROW_ID=""
  if [[ "${CREATE_CODE:-}" == "0" && -n "${CREATE_TXHASH:-}" ]]; then
    for i in $(seq 1 10); do
      ESCROW_ID="$("$INFERENCED" query tx "$CREATE_TXHASH" \
        --node "$NODE_RPC" --home "$INFERENCED_HOME" -o json 2>/dev/null \
        | jq -r '.events[]? | select(.type=="devshard_escrow_created") | .attributes[]? | select(.key=="escrow_id") | .value' \
        | head -1 || true)"
      [[ -n "$ESCROW_ID" && "$ESCROW_ID" != "null" ]] && break
      sleep 2
    done
    log "resolved ESCROW_ID=${ESCROW_ID:-none}"
    if [[ -n "$ESCROW_ID" ]]; then
      try_register "$ESCROW_ID"
      try_gateway_chat "$ESCROW_ID" "new-escrow-${ESCROW_ID}"
    fi
  fi

  try_gateway_chat "" "pooled-existing"

  jq -n \
    --arg ts "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
    --arg halt_services "${HALT_SERVICES:-node}" \
    --arg create_code "${CREATE_CODE:-unknown}" \
    --arg create_txhash "${CREATE_TXHASH:-}" \
    --arg escrow_id "${ESCROW_ID:-}" \
    --arg chat_http "${CHAT_HTTP:-}" \
    --argjson create_rc "${CREATE_RC:-1}" \
    '{
      timestamp: $ts,
      halt_services: $halt_services,
      create: {exit: $create_rc, code: $create_code, txhash: $create_txhash, escrow_id: $escrow_id},
      chat_pooled: {http: $chat_http}
    }' >"$REPORT"
  log "report → $REPORT"
  cat "$REPORT" | jq .
}

main "$@"
