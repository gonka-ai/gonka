#!/usr/bin/env bash
set -euo pipefail

# Simple health checks for the 3-node Filfox/Nebius testnet.
# Run from local machine:
#   SSH_KEY_PATH=~/.ssh/id_ed25519_engenious ./healthcheck.sh

if [ -f ".env" ]; then
  set -a
  # shellcheck disable=SC1091
  source ./.env
  set +a
fi

SSH_KEY_PATH="${SSH_KEY_PATH:-$HOME/.ssh/id_ed25519_engenious}"
SSH_USER="${SSH_USER:-decentai}"
DOMAIN="${DOMAIN:-${PUBLIC_DOMAIN:-xj7-5.s.filfox.io}}"

GENESIS_SSH_PORT="${GENESIS_SSH_PORT:-18220}"
JOIN1_SSH_PORT="${JOIN1_SSH_PORT:-${JOIN_1_SSH_PORT:-18225}}"
JOIN2_SSH_PORT="${JOIN2_SSH_PORT:-${JOIN_2_SSH_PORT:-18226}}"

GENESIS_API_PORT="${GENESIS_API_PORT:-19240}"
JOIN1_API_PORT="${JOIN1_API_PORT:-${JOIN_1_API_PORT:-19250}}"
JOIN2_API_PORT="${JOIN2_API_PORT:-${JOIN_2_API_PORT:-19252}}"

GENESIS_P2P_PORT="${GENESIS_P2P_PORT:-19239}"
JOIN1_P2P_PORT="${JOIN1_P2P_PORT:-19249}"
JOIN2_P2P_PORT="${JOIN2_P2P_PORT:-19251}"

CHAIN_ID="${CHAIN_ID:-gonka-testnet-3}"

SSH_COMMON=(-i "$SSH_KEY_PATH" -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=8)
FAILURES=0

print_header() {
  echo
  echo "=================================================="
  echo "$1"
  echo "=================================================="
}

check_url() {
  local name="$1"
  local url="$2"
  local out
  if out="$(curl -fsS --max-time 8 "$url" 2>/dev/null)"; then
    echo "[OK] $name -> $url"
  else
    echo "[FAIL] $name -> $url"
    FAILURES=$((FAILURES + 1))
    return 1
  fi
  # Return output in case caller needs it
  printf '%s' "$out"
}

warn() {
  echo "[WARN] $1"
}

ok() {
  echo "[OK] $1"
}

fail() {
  echo "[FAIL] $1"
  FAILURES=$((FAILURES + 1))
}

check_public_api() {
  local label="$1"
  local port="$2"
  local base="http://${DOMAIN}:${port}"

  echo
  echo "[$label] Public API checks ($base)"
  check_url "$label identity" "$base/v1/identity" >/dev/null || true
  check_url "$label participants" "$base/v1/participants" >/dev/null || true
  check_url "$label models" "$base/v1/governance/models" >/dev/null || true
  check_url "$label epoch" "$base/v1/epochs/latest" >/dev/null || true
  check_url "$label chain-rpc status" "$base/chain-rpc/status" >/dev/null || true
}

check_host() {
  local label="$1"
  local ssh_port="$2"
  local remote="${SSH_USER}@${DOMAIN}"

  echo
  echo "[$label] SSH/Docker checks ($remote:$ssh_port)"
  if ! ssh "${SSH_COMMON[@]}" -p "$ssh_port" "$remote" "echo connected >/dev/null"; then
    fail "$label SSH failed"
    return 1
  fi

  ssh "${SSH_COMMON[@]}" -p "$ssh_port" "$remote" \
    "cd /srv/dai/gonka/deploy/join 2>/dev/null && if [ -f config.env ]; then docker compose --env-file config.env ps; else docker compose ps; fi || true"

  ssh "${SSH_COMMON[@]}" -p "$ssh_port" "$remote" \
    "cd /srv/dai/gonka/deploy/join 2>/dev/null && if [ -f config.env ]; then docker compose --env-file config.env logs --tail=120 node 2>/dev/null; else docker compose logs --tail=120 node 2>/dev/null; fi | egrep -i 'panic|fatal|error|UPGRADE|needed|connection refused|current height 0 is less than upgrade height' || true"
}

get_height() {
  local url="$1"
  curl -fsS --max-time 8 "$url" 2>/dev/null \
    | python3 -c 'import json,sys; print(json.load(sys.stdin).get("result",{}).get("sync_info",{}).get("latest_block_height",""))' 2>/dev/null || true
}

check_block_growth() {
  print_header "BLOCK PRODUCTION CHECK"
  local status_url="http://${DOMAIN}:${GENESIS_API_PORT}/chain-rpc/status"
  local h1 h2
  h1="$(get_height "$status_url")"
  if [[ -z "$h1" ]]; then
    fail "Cannot read genesis block height"
    return
  fi
  echo "Height t0: $h1"
  sleep 15
  h2="$(get_height "$status_url")"
  if [[ -z "$h2" ]]; then
    fail "Cannot read genesis block height at t+15s"
    return
  fi
  echo "Height t+15s: $h2"
  if (( h2 > h1 )); then
    ok "Block height is growing"
  else
    fail "Block height is not growing"
  fi
}

check_epoch_and_participants() {
  print_header "EPOCH / PARTICIPANTS / REWARDS CHECK"
  local base="http://${DOMAIN}:${GENESIS_API_PORT}"
  local before_epoch after_epoch participants_json rewards_json

  before_epoch="$(curl -fsS --max-time 8 "$base/v1/epochs/latest" 2>/dev/null || true)"
  if [[ -z "$before_epoch" ]]; then
    fail "Cannot query /v1/epochs/latest"
  else
    ok "Fetched /v1/epochs/latest"
    echo "$before_epoch" | python3 -c 'import json,sys; d=json.load(sys.stdin); print("Epoch snapshot:", d.get("index", d.get("epoch_index", "unknown")))' 2>/dev/null || true
  fi

  participants_json="$(curl -fsS --max-time 8 "$base/v1/epochs/current/participants" 2>/dev/null || true)"
  if [[ -z "$participants_json" ]]; then
    fail "Cannot query /v1/epochs/current/participants"
  else
    ok "Fetched /v1/epochs/current/participants"
    echo "$participants_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); arr=d if isinstance(d,list) else d.get("participants",[]); print("Participants in current epoch:", len(arr))' 2>/dev/null || true
  fi

  rewards_json="$(curl -fsS --max-time 8 "$base/v1/participants" 2>/dev/null || true)"
  if [[ -z "$rewards_json" ]]; then
    fail "Cannot query /v1/participants"
  else
    ok "Fetched /v1/participants"
    echo "$rewards_json" | python3 -c 'import json,sys; d=json.load(sys.stdin); arr=d if isinstance(d,list) else d.get("participants",[]); print("Participants total:", len(arr))' 2>/dev/null || true
  fi

  sleep 5
  after_epoch="$(curl -fsS --max-time 8 "$base/v1/epochs/latest" 2>/dev/null || true)"
  if [[ -n "$before_epoch" && -n "$after_epoch" ]]; then
    if [[ "$before_epoch" != "$after_epoch" ]]; then
      warn "Epoch changed during check window (this can be normal)"
    else
      ok "Epoch endpoint stable during short check window"
    fi
  fi
}

check_poc_logs() {
  print_header "POC LOG CHECK (MLNODE)"
  local hosts=(
    "genesis:${GENESIS_SSH_PORT}"
    "join-1:${JOIN1_SSH_PORT}"
    "join-2:${JOIN2_SSH_PORT}"
  )
  local item label port remote
  for item in "${hosts[@]}"; do
    label="${item%%:*}"
    port="${item##*:}"
    remote="${SSH_USER}@${DOMAIN}"
    echo
    echo "[$label] PoC log scan"
    if ! ssh "${SSH_COMMON[@]}" -p "$port" "$remote" "echo ok >/dev/null"; then
      fail "$label SSH failed for PoC log scan"
      continue
    fi
    ssh "${SSH_COMMON[@]}" -p "$port" "$remote" \
      "docker logs join-mlnode-308-1 --tail 1000 2>/dev/null | egrep -i 'PoC /init/generate|PoC /generate|/api/v1/inference/pow/generate.*200 OK' | tail -n 20" || true
  done
}

check_node_restart_state() {
  print_header "NODE RESTART / UPGRADE STATE CHECK"
  local hosts=(
    "genesis:${GENESIS_SSH_PORT}"
    "join-1:${JOIN1_SSH_PORT}"
    "join-2:${JOIN2_SSH_PORT}"
  )
  local item label port remote state
  for item in "${hosts[@]}"; do
    label="${item%%:*}"
    port="${item##*:}"
    remote="${SSH_USER}@${DOMAIN}"
    state="$(ssh "${SSH_COMMON[@]}" -p "$port" "$remote" "docker inspect --format='{{.State.Status}} {{.State.Restarting}} {{.RestartCount}}' node 2>/dev/null" || true)"
    if [[ -z "$state" ]]; then
      fail "$label cannot inspect node container"
      continue
    fi
    echo "[$label] node state: $state"
    if echo "$state" | egrep -qi 'restarting|true'; then
      fail "$label node is restarting"
    else
      ok "$label node is not restarting"
    fi
  done
}

check_chain_id() {
  print_header "CHAIN ID CHECK"
  local status_json
  if status_json="$(curl -fsS --max-time 8 "http://${DOMAIN}:${GENESIS_API_PORT}/chain-rpc/status" 2>/dev/null)"; then
    local cid
    cid="$(printf '%s' "$status_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("result",{}).get("node_info",{}).get("network",""))' 2>/dev/null || true)"
    if [[ "$cid" == "$CHAIN_ID" ]]; then
      echo "[OK] Chain ID is $cid"
    else
      echo "[WARN] Chain ID mismatch. expected=$CHAIN_ID got=${cid:-<empty>}"
    fi
  else
    echo "[FAIL] Could not fetch genesis chain-rpc status for chain id check"
  fi
}

main() {
  print_header "PUBLIC ENDPOINT HEALTHCHECKS"
  check_public_api "genesis" "$GENESIS_API_PORT"
  check_public_api "join-1" "$JOIN1_API_PORT"
  check_public_api "join-2" "$JOIN2_API_PORT"

  check_chain_id
  check_block_growth
  check_epoch_and_participants

  print_header "HOST HEALTHCHECKS (SSH + Docker)"
  check_host "genesis" "$GENESIS_SSH_PORT"
  check_host "join-1" "$JOIN1_SSH_PORT"
  check_host "join-2" "$JOIN2_SSH_PORT"
  check_node_restart_state
  check_poc_logs

  print_header "DONE"
  if (( FAILURES > 0 )); then
    echo "Healthcheck finished with failures: $FAILURES"
    exit 1
  fi
  echo "Healthcheck finished with no hard failures."
}

main "$@"
