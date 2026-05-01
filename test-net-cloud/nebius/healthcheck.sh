#!/usr/bin/env bash
set -euo pipefail

# Flexible health checks for Nebius testnets.
# Run from local machine:
#   SSH_KEY_PATH=~/.ssh/id_ed25519_engenious ./healthcheck.sh
#   HEALTHCHECK_NODES_FILE=./healthcheck-nodes.csv ./healthcheck.sh
#
# Supported node inventory formats:
#   CSV header or JSON object keys:
#     label,role,ssh_host,ssh_port,api_host,api_port,ssh_user,compose_dir,node_container,mlnode_container,enabled
#   JSON can be either a list of node objects or {"nodes":[...]}.
#   If no file is provided, .env values for genesis/join-1/join-2 are used.

if [ -f ".env" ]; then
  set -a
  # shellcheck disable=SC1091
  source ./.env
  set +a
fi

SSH_KEY_PATH="${SSH_KEY_PATH:-$HOME/.ssh/id_ed25519_engenious}"
SSH_USER="${SSH_USER:-decentai}"
DEFAULT_DOMAIN="${DOMAIN:-${PUBLIC_DOMAIN:-xj7-5.s.filfox.io}}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-3}"
HEALTHCHECK_NODES_FILE="${HEALTHCHECK_NODES_FILE:-${HOSTS_FILE:-${NODES_FILE:-}}}"
DEFAULT_DEPLOY_DIR="${DEPLOY_DIR:-/srv/dai}"
DEFAULT_COMPOSE_DIR="${HEALTHCHECK_COMPOSE_DIR:-${DEFAULT_DEPLOY_DIR}/gonka/deploy/join}"
DEFAULT_NODE_CONTAINER="${HEALTHCHECK_NODE_CONTAINER:-node}"
DEFAULT_MLNODE_CONTAINER="${HEALTHCHECK_MLNODE_CONTAINER:-join-mlnode-308-1}"

GENESIS_SSH_PORT="${GENESIS_SSH_PORT:-18220}"
JOIN1_SSH_PORT="${JOIN1_SSH_PORT:-${JOIN_1_SSH_PORT:-18225}}"
JOIN2_SSH_PORT="${JOIN2_SSH_PORT:-${JOIN_2_SSH_PORT:-18219}}"

GENESIS_API_PORT="${GENESIS_API_PORT:-19240}"
JOIN1_API_PORT="${JOIN1_API_PORT:-${JOIN_1_API_PORT:-19250}}"
JOIN2_API_PORT="${JOIN2_API_PORT:-${JOIN_2_API_PORT:-19238}}"

SSH_COMMON=(-i "$SSH_KEY_PATH" -o BatchMode=yes -o StrictHostKeyChecking=accept-new -o ConnectTimeout=8)
FAILURES=0
GENESIS_INDEX=-1

declare -a NODE_LABELS=()
declare -a NODE_ROLES=()
declare -a NODE_SSH_USERS=()
declare -a NODE_SSH_HOSTS=()
declare -a NODE_SSH_PORTS=()
declare -a NODE_API_HOSTS=()
declare -a NODE_API_PORTS=()
declare -a NODE_COMPOSE_DIRS=()
declare -a NODE_NODE_CONTAINERS=()
declare -a NODE_MLNODE_CONTAINERS=()

print_header() {
  echo
  echo "=================================================="
  echo "$1"
  echo "=================================================="
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
  printf '%s' "$out"
}

add_node() {
  NODE_LABELS+=("$1")
  NODE_ROLES+=("$2")
  NODE_SSH_USERS+=("$3")
  NODE_SSH_HOSTS+=("$4")
  NODE_SSH_PORTS+=("$5")
  NODE_API_HOSTS+=("$6")
  NODE_API_PORTS+=("$7")
  NODE_COMPOSE_DIRS+=("$8")
  NODE_NODE_CONTAINERS+=("$9")
  NODE_MLNODE_CONTAINERS+=("${10}")
}

load_nodes_from_inventory() {
  local parsed label role ssh_user ssh_host ssh_port api_host api_port compose_dir node_container mlnode_container

  if [[ -z "$HEALTHCHECK_NODES_FILE" ]]; then
    return 1
  fi
  if [[ ! -f "$HEALTHCHECK_NODES_FILE" ]]; then
    echo "Inventory file not found: $HEALTHCHECK_NODES_FILE" >&2
    exit 1
  fi

  parsed="$(python3 - "$HEALTHCHECK_NODES_FILE" "$SSH_USER" "$DEFAULT_DOMAIN" "$DEFAULT_COMPOSE_DIR" "$DEFAULT_NODE_CONTAINER" "$DEFAULT_MLNODE_CONTAINER" <<'PY'
import csv
import json
import sys
from pathlib import Path
from urllib.parse import urlparse

path = Path(sys.argv[1])
default_user = sys.argv[2]
default_domain = sys.argv[3]
default_compose_dir = sys.argv[4]
default_node_container = sys.argv[5]
default_mlnode_container = sys.argv[6]

if not path.exists():
    raise SystemExit(f"Inventory file not found: {path}")


def pick(item, *keys):
    for key in keys:
        value = item.get(key)
        if value is None:
            continue
        text = str(value).strip()
        if text:
            return text
    return ""


def normalize_host(value):
    text = str(value or "").strip()
    if not text:
        return ""
    if "://" in text:
        parsed = urlparse(text)
        return (parsed.hostname or parsed.netloc or parsed.path).strip().strip("/")
    return text.split("/", 1)[0].strip()


def parse_url(value):
    text = str(value or "").strip()
    if not text:
        return "", ""
    if "://" not in text:
        text = f"http://{text}"
    parsed = urlparse(text)
    return (parsed.hostname or "").strip(), str(parsed.port or "")


def is_enabled(value):
    text = str(value or "true").strip().lower()
    return text not in {"0", "false", "no", "off", "disabled"}


def normalize(item, index):
    label = pick(item, "label", "name") or f"node-{index + 1}"
    role = pick(item, "role") or ("genesis" if label == "genesis" else "join")
    ssh_user = pick(item, "ssh_user") or default_user

    api_url_host, api_url_port = parse_url(pick(item, "api_url", "public_url", "url"))
    ssh_host = normalize_host(pick(item, "ssh_host", "host")) or normalize_host(pick(item, "domain", "public_host")) or api_url_host
    api_host = normalize_host(pick(item, "api_host")) or normalize_host(pick(item, "public_host", "domain")) or api_url_host or ssh_host or default_domain

    ssh_port = pick(item, "ssh_port", "port") or "22"
    api_port = pick(item, "api_port", "public_api_port") or api_url_port
    compose_dir = pick(item, "compose_dir", "deploy_dir") or default_compose_dir
    node_container = pick(item, "node_container") or default_node_container
    mlnode_container = pick(item, "mlnode_container") or default_mlnode_container

    if not is_enabled(pick(item, "enabled")):
        return None
    if not ssh_host and not api_host:
        raise SystemExit(f"Node #{index + 1} is missing both ssh_host and api_host")

    return [
        label,
        role,
        ssh_user,
        ssh_host,
        ssh_port,
        api_host,
        api_port,
        compose_dir,
        node_container,
        mlnode_container,
    ]


suffix = path.suffix.lower()
if suffix == ".json":
    with path.open() as fh:
        data = json.load(fh)
    if isinstance(data, list):
        items = data
    elif isinstance(data, dict) and isinstance(data.get("nodes"), list):
        items = data["nodes"]
    else:
        raise SystemExit("JSON inventory must be a list or an object with a 'nodes' list")
else:
    with path.open(newline="") as fh:
        reader = csv.DictReader(fh)
        if not reader.fieldnames:
            raise SystemExit("CSV inventory must include a header row")
        items = list(reader)

for index, item in enumerate(items):
    normalized = normalize(item, index)
    if normalized is None:
        continue
    print("\t".join(normalized))
PY
)"

  while IFS=$'\t' read -r label role ssh_user ssh_host ssh_port api_host api_port compose_dir node_container mlnode_container; do
    [[ -z "${label}${role}${ssh_user}${ssh_host}${ssh_port}${api_host}${api_port}${compose_dir}${node_container}${mlnode_container}" ]] && continue
    add_node "$label" "$role" "$ssh_user" "$ssh_host" "$ssh_port" "$api_host" "$api_port" "$compose_dir" "$node_container" "$mlnode_container"
  done <<< "$parsed"

  return 0
}

load_legacy_nodes() {
  local genesis_ssh_host genesis_ssh_port genesis_api_host
  local join1_ssh_host join1_ssh_port join1_api_host
  local join2_ssh_host join2_ssh_port join2_api_host

  genesis_ssh_host="${GENESIS_INTERNAL_IP:-$DEFAULT_DOMAIN}"
  genesis_ssh_port="${GENESIS_HEALTHCHECK_SSH_PORT:-$GENESIS_SSH_PORT}"
  genesis_api_host="${GENESIS_INTERNAL_IP:-$DEFAULT_DOMAIN}"

  join1_ssh_host="${JOIN1_INTERNAL_IP:-$DEFAULT_DOMAIN}"
  join1_ssh_port="${JOIN1_HEALTHCHECK_SSH_PORT:-$JOIN1_SSH_PORT}"
  join1_api_host="${JOIN1_INTERNAL_IP:-$DEFAULT_DOMAIN}"

  join2_ssh_host="$DEFAULT_DOMAIN"
  join2_ssh_port="${JOIN2_HEALTHCHECK_SSH_PORT:-$JOIN2_SSH_PORT}"
  join2_api_host="$DEFAULT_DOMAIN"

  add_node "genesis" "genesis" "$SSH_USER" "$genesis_ssh_host" "$genesis_ssh_port" "$genesis_api_host" "$GENESIS_API_PORT" "$DEFAULT_COMPOSE_DIR" "$DEFAULT_NODE_CONTAINER" "$DEFAULT_MLNODE_CONTAINER"
  add_node "join-1" "join" "$SSH_USER" "$join1_ssh_host" "$join1_ssh_port" "$join1_api_host" "$JOIN1_API_PORT" "$DEFAULT_COMPOSE_DIR" "$DEFAULT_NODE_CONTAINER" "$DEFAULT_MLNODE_CONTAINER"
  add_node "join-2" "join" "$SSH_USER" "$join2_ssh_host" "$join2_ssh_port" "$join2_api_host" "$JOIN2_API_PORT" "$DEFAULT_COMPOSE_DIR" "$DEFAULT_NODE_CONTAINER" "$DEFAULT_MLNODE_CONTAINER"
}

load_nodes() {
  if ! load_nodes_from_inventory; then
    load_legacy_nodes
  fi

  if (( ${#NODE_LABELS[@]} == 0 )); then
    echo "No nodes configured for healthcheck." >&2
    exit 1
  fi

  local idx
  for idx in "${!NODE_LABELS[@]}"; do
    if [[ "${NODE_ROLES[$idx]}" == "genesis" || "${NODE_LABELS[$idx]}" == "genesis" ]]; then
      GENESIS_INDEX="$idx"
      break
    fi
  done

  if (( GENESIS_INDEX < 0 )); then
    GENESIS_INDEX=0
    warn "No node marked as genesis; using ${NODE_LABELS[0]} for chain-level checks"
  fi
}

print_node_inventory() {
  print_header "LOADED NODE INVENTORY"
  local idx api_display ssh_display
  for idx in "${!NODE_LABELS[@]}"; do
    ssh_display="<disabled>"
    api_display="<disabled>"
    if [[ -n "${NODE_SSH_HOSTS[$idx]}" ]]; then
      ssh_display="${NODE_SSH_USERS[$idx]}@${NODE_SSH_HOSTS[$idx]}:${NODE_SSH_PORTS[$idx]}"
    fi
    if [[ -n "${NODE_API_HOSTS[$idx]}" && -n "${NODE_API_PORTS[$idx]}" ]]; then
      api_display="http://${NODE_API_HOSTS[$idx]}:${NODE_API_PORTS[$idx]}"
    fi
    echo "[${NODE_LABELS[$idx]}] role=${NODE_ROLES[$idx]} ssh=${ssh_display} api=${api_display}"
  done
}

check_public_api() {
  local label="$1"
  local api_host="$2"
  local api_port="$3"
  local base

  if [[ -z "$api_host" || -z "$api_port" ]]; then
    warn "$label has no public API endpoint configured; skipping API checks"
    return 0
  fi

  base="http://${api_host}:${api_port}"
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
  local ssh_user="$2"
  local ssh_host="$3"
  local ssh_port="$4"
  local compose_dir="$5"
  local node_container="$6"
  local remote

  if [[ -z "$ssh_host" || -z "$ssh_port" ]]; then
    warn "$label has no SSH target configured; skipping host checks"
    return 0
  fi

  remote="${ssh_user}@${ssh_host}"
  echo
  echo "[$label] SSH/Docker checks ($remote:$ssh_port)"
  if ! ssh "${SSH_COMMON[@]}" -p "$ssh_port" "$remote" "echo connected >/dev/null"; then
    fail "$label SSH failed"
    return 1
  fi

  ssh "${SSH_COMMON[@]}" -p "$ssh_port" "$remote" \
    "cd '$compose_dir' 2>/dev/null && if [ -f config.env ]; then docker compose --env-file config.env ps; else docker compose ps; fi || true"

  ssh "${SSH_COMMON[@]}" -p "$ssh_port" "$remote" \
    "cd '$compose_dir' 2>/dev/null && if [ -f config.env ]; then docker compose --env-file config.env logs --tail=120 '$node_container' 2>/dev/null; else docker compose logs --tail=120 '$node_container' 2>/dev/null; fi | grep -Ei 'panic|fatal|error|UPGRADE|needed|connection refused|current height 0 is less than upgrade height' || true"
}

get_height() {
  local url="$1"
  curl -fsS --max-time 8 "$url" 2>/dev/null \
    | python3 -c 'import json,sys; print(json.load(sys.stdin).get("result",{}).get("sync_info",{}).get("latest_block_height",""))' 2>/dev/null || true
}

check_block_growth() {
  print_header "BLOCK PRODUCTION CHECK"
  local status_url="http://${NODE_API_HOSTS[$GENESIS_INDEX]}:${NODE_API_PORTS[$GENESIS_INDEX]}/chain-rpc/status"
  local h1 h2

  if [[ -z "${NODE_API_HOSTS[$GENESIS_INDEX]}" || -z "${NODE_API_PORTS[$GENESIS_INDEX]}" ]]; then
    fail "Genesis node has no API endpoint configured for block growth check"
    return
  fi

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
  local base="http://${NODE_API_HOSTS[$GENESIS_INDEX]}:${NODE_API_PORTS[$GENESIS_INDEX]}"
  local before_epoch after_epoch participants_json rewards_json

  if [[ -z "${NODE_API_HOSTS[$GENESIS_INDEX]}" || -z "${NODE_API_PORTS[$GENESIS_INDEX]}" ]]; then
    fail "Genesis node has no API endpoint configured for epoch checks"
    return
  fi

  before_epoch="$(curl -fsS --max-time 8 "$base/v1/epochs/latest" 2>/dev/null || true)"
  if [[ -z "$before_epoch" ]]; then
    fail "Cannot query /v1/epochs/latest"
  else
    ok "Fetched /v1/epochs/latest"
    echo "$before_epoch" | python3 -c 'import json,sys; d=json.load(sys.stdin); print("Epoch snapshot:", d.get("index", d.get("epoch_index", "unknown")))' 2>/dev/null || true
  fi

  participants_json="$(curl -fsS --max-time 8 "$base/v1/epochs/current/participants" 2>/dev/null || true)"
  if [[ -z "$participants_json" ]]; then
    warn "Cannot query /v1/epochs/current/participants (expected before first PoC cycle)"
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

check_inference_and_escrow_readiness() {
  print_header "INFERENCE & ESCROW READINESS (non-blocking)"
  local base model http_code participants_json weighted_count total_count

  if [[ -z "${NODE_API_HOSTS[$GENESIS_INDEX]}" || -z "${NODE_API_PORTS[$GENESIS_INDEX]}" ]]; then
    warn "No genesis API endpoint; skipping inference/escrow readiness checks"
    return 0
  fi

  base="http://${NODE_API_HOSTS[$GENESIS_INDEX]}:${NODE_API_PORTS[$GENESIS_INDEX]}"

  # --- Escrow readiness: check if participants have validation weights ---
  # create-devshard-escrow requires validation_weights in the current epoch group.
  # If weights are absent, the tx fails with "epoch group data not found".
  participants_json="$(curl -fsS --max-time 8 "$base/v1/epochs/current/participants" 2>/dev/null || true)"
  if [[ -z "$participants_json" ]]; then
    warn "Escrow readiness: cannot fetch /v1/epochs/current/participants"
  else
    read -r weighted_count total_count < <(
      echo "$participants_json" | python3 -c '
import json, sys
d = json.load(sys.stdin)
arr = d if isinstance(d, list) else d.get("participants", [])
weighted = sum(1 for p in arr if int(p.get("weight", 0) or p.get("validation_weight", 0) or 0) > 0)
print(weighted, len(arr))
' 2>/dev/null || echo "0 0"
    )
    if (( weighted_count > 0 )); then
      ok "Escrow readiness: $weighted_count/$total_count participants have validation weights — escrow creation should work"
    else
      warn "Escrow readiness: 0/$total_count participants have validation weights yet — epoch group not ready (normal before first PoC completes)"
    fi
  fi

  # --- Inference readiness: quick POST to /v1/chat/completions ---
  # 200/402/429 = endpoint is live and routing inference; 5xx/timeout = not ready.
  model="$(curl -fsS --max-time 8 "$base/v1/governance/models" 2>/dev/null \
    | python3 -c 'import json,sys; ms=json.load(sys.stdin); ms=ms if isinstance(ms,list) else ms.get("models",[]); print(ms[0]["id"] if ms else "")' 2>/dev/null || true)"
  if [[ -z "$model" ]]; then
    warn "Inference readiness: no governance models found; skipping inference probe"
    return 0
  fi

  http_code="$(curl -s -o /dev/null -w "%{http_code}" --max-time 10 \
    -X POST "$base/v1/chat/completions" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"$model\",\"messages\":[{\"role\":\"user\",\"content\":\"hi\"}],\"max_tokens\":1}" \
    2>/dev/null || echo "000")"

  case "$http_code" in
    200|401|402|429)
      ok "Inference readiness: $base responded $http_code to chat/completions (endpoint live, model=$model)" ;;
    000)
      warn "Inference readiness: connection failed to $base/v1/chat/completions" ;;
    *)
      warn "Inference readiness: $base responded HTTP $http_code to chat/completions (may not be ready yet)" ;;
  esac
}

check_poc_logs() {
  print_header "POC LOG CHECK (MLNODE)"
  local idx label ssh_user ssh_host ssh_port remote mlnode_container

  for idx in "${!NODE_LABELS[@]}"; do
    label="${NODE_LABELS[$idx]}"
    ssh_user="${NODE_SSH_USERS[$idx]}"
    ssh_host="${NODE_SSH_HOSTS[$idx]}"
    ssh_port="${NODE_SSH_PORTS[$idx]}"
    mlnode_container="${NODE_MLNODE_CONTAINERS[$idx]}"

    echo
    echo "[$label] PoC log scan"
    if [[ -z "$ssh_host" || -z "$ssh_port" ]]; then
      warn "$label has no SSH target configured; skipping PoC log scan"
      continue
    fi

    remote="${ssh_user}@${ssh_host}"
    if ! ssh "${SSH_COMMON[@]}" -p "$ssh_port" "$remote" "echo ok >/dev/null"; then
      fail "$label SSH failed for PoC log scan"
      continue
    fi
    ssh "${SSH_COMMON[@]}" -p "$ssh_port" "$remote" \
      "docker logs '$mlnode_container' --tail 1000 2>/dev/null | grep -Ei 'PoC /init/generate|PoC /generate|/api/v1/inference/pow/generate.*200 OK' | tail -n 20" || true
  done
}

check_node_restart_state() {
  print_header "NODE RESTART / UPGRADE STATE CHECK"
  local idx label ssh_user ssh_host ssh_port remote state node_container

  for idx in "${!NODE_LABELS[@]}"; do
    label="${NODE_LABELS[$idx]}"
    ssh_user="${NODE_SSH_USERS[$idx]}"
    ssh_host="${NODE_SSH_HOSTS[$idx]}"
    ssh_port="${NODE_SSH_PORTS[$idx]}"
    node_container="${NODE_NODE_CONTAINERS[$idx]}"

    if [[ -z "$ssh_host" || -z "$ssh_port" ]]; then
      warn "$label has no SSH target configured; skipping restart state check"
      continue
    fi

    remote="${ssh_user}@${ssh_host}"
    state="$(ssh "${SSH_COMMON[@]}" -p "$ssh_port" "$remote" "docker inspect --format='{{.State.Status}} {{.State.Restarting}} {{.RestartCount}}' '$node_container' 2>/dev/null" || true)"
    if [[ -z "$state" ]]; then
      fail "$label cannot inspect node container"
      continue
    fi
    echo "[$label] node state: $state"
    if echo "$state" | grep -Eqi 'restarting|true'; then
      fail "$label node is restarting"
    else
      ok "$label node is not restarting"
    fi
  done
}

check_chain_id() {
  print_header "CHAIN ID CHECK"
  local status_json cid

  if [[ -z "${NODE_API_HOSTS[$GENESIS_INDEX]}" || -z "${NODE_API_PORTS[$GENESIS_INDEX]}" ]]; then
    fail "Genesis node has no API endpoint configured for chain-id check"
    return
  fi

  if status_json="$(curl -fsS --max-time 8 "http://${NODE_API_HOSTS[$GENESIS_INDEX]}:${NODE_API_PORTS[$GENESIS_INDEX]}/chain-rpc/status" 2>/dev/null)"; then
    cid="$(printf '%s' "$status_json" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("result",{}).get("node_info",{}).get("network",""))' 2>/dev/null || true)"
    if [[ "$cid" == "$CHAIN_ID" ]]; then
      echo "[OK] Chain ID is $cid"
    else
      echo "[WARN] Chain ID mismatch. expected=$CHAIN_ID got=${cid:-<empty>}"
    fi
  else
    fail "Could not fetch genesis chain-rpc status for chain id check"
  fi
}

main() {
  local idx

  load_nodes
  print_node_inventory

  print_header "PUBLIC ENDPOINT HEALTHCHECKS"
  for idx in "${!NODE_LABELS[@]}"; do
    check_public_api "${NODE_LABELS[$idx]}" "${NODE_API_HOSTS[$idx]}" "${NODE_API_PORTS[$idx]}"
  done

  check_chain_id
  check_block_growth
  check_epoch_and_participants
  check_inference_and_escrow_readiness

  print_header "HOST HEALTHCHECKS (SSH + Docker)"
  for idx in "${!NODE_LABELS[@]}"; do
    check_host "${NODE_LABELS[$idx]}" "${NODE_SSH_USERS[$idx]}" "${NODE_SSH_HOSTS[$idx]}" "${NODE_SSH_PORTS[$idx]}" "${NODE_COMPOSE_DIRS[$idx]}" "${NODE_NODE_CONTAINERS[$idx]}"
  done
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
