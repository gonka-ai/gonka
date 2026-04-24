#!/usr/bin/env bash
set -euo pipefail

# One-command deployment:
# 1) Upload scripts/config to all hosts
# 2) Run genesis
# 3) Wait for genesis API
# 4) Run join-1 and join-2
# 5) Run healthcheck

if [ ! -f ".env" ]; then
  echo "Missing .env. Create it from .env.example first." >&2
  exit 1
fi

set -a
# shellcheck disable=SC1091
source ./.env
set +a

SSH_KEY_PATH="${SSH_KEY_PATH:-$HOME/.ssh/id_ed25519_engenious}"
SSH_USER="${SSH_USER:-decentai}"
PUBLIC_DOMAIN="${PUBLIC_DOMAIN:-xj7-5.s.filfox.io}"
DEPLOY_DIR="${DEPLOY_DIR:-/srv/dai}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-3}"
REPO_BRANCH="${REPO_BRANCH:-origin/upgrade-v0.2.12}"
HF_HOME="${HF_HOME:-/srv/dai/cache/}"

GENESIS_SSH_PORT="${GENESIS_SSH_PORT:-18220}"
JOIN1_SSH_PORT="${JOIN1_SSH_PORT:-18225}"
JOIN2_SSH_PORT="${JOIN2_SSH_PORT:-18226}"

GENESIS_API_PORT="${GENESIS_API_PORT:-19240}"
GENESIS_P2P_PORT="${GENESIS_P2P_PORT:-19239}"
JOIN1_API_PORT="${JOIN1_API_PORT:-19250}"
JOIN1_P2P_PORT="${JOIN1_P2P_PORT:-19249}"
JOIN2_API_PORT="${JOIN2_API_PORT:-19252}"
JOIN2_P2P_PORT="${JOIN2_P2P_PORT:-19251}"
GENESIS_WAIT_SECONDS="${GENESIS_WAIT_SECONDS:-300}"
GENESIS_WAIT_INTERVAL="${GENESIS_WAIT_INTERVAL:-5}"
GENESIS_INTERNAL_API_PORT="${GENESIS_INTERNAL_API_PORT:-8000}"
JOIN1_INTERNAL_API_PORT="${JOIN1_INTERNAL_API_PORT:-8000}"
JOIN2_INTERNAL_API_PORT="${JOIN2_INTERNAL_API_PORT:-8000}"

GENESIS_INTERNAL_IP="${GENESIS_INTERNAL_IP:-172.18.114.113}"

SSH_COMMON=(-i "$SSH_KEY_PATH" -o StrictHostKeyChecking=accept-new -o BatchMode=yes)
GENESIS_HOST="${SSH_USER}@${PUBLIC_DOMAIN}"
JOIN1_HOST="${SSH_USER}@${PUBLIC_DOMAIN}"
JOIN2_HOST="${SSH_USER}@${PUBLIC_DOMAIN}"

echo "==> Uploading scripts to all nodes"
SSH_KEY_PATH="$SSH_KEY_PATH" ./prepare.sh

run_remote() {
  local host="$1"
  local port="$2"
  local cmd="$3"
  ssh "${SSH_COMMON[@]}" -p "$port" "$host" "$cmd"
}

echo "==> Running genesis launch"
run_remote "$GENESIS_HOST" "$GENESIS_SSH_PORT" \
  "cd '$DEPLOY_DIR' && \
   export PYTHONUNBUFFERED=1 TESTNET_BASE_DIR='$DEPLOY_DIR' HF_HOME='$HF_HOME' CHAIN_ID='$CHAIN_ID' API_PORT='$GENESIS_INTERNAL_API_PORT' \
   PUBLIC_URL='http://${PUBLIC_DOMAIN}:${GENESIS_API_PORT}' P2P_EXTERNAL_ADDRESS='tcp://${PUBLIC_DOMAIN}:${GENESIS_P2P_PORT}' \
   SEED_API_URL='http://${PUBLIC_DOMAIN}:${GENESIS_API_PORT}' SEED_NODE_P2P_URL='tcp://${PUBLIC_DOMAIN}:${GENESIS_P2P_PORT}' \
   SEED_NODE_RPC_URL='http://${GENESIS_INTERNAL_IP}:26657' RPC_SERVER_URL_1='http://${GENESIS_INTERNAL_IP}:26657' RPC_SERVER_URL_2='http://${GENESIS_INTERNAL_IP}:26657' && \
   python3 launch.py --mode genesis --branch '$REPO_BRANCH' --chainid '$CHAIN_ID'"

echo "==> Waiting for genesis readiness on :$GENESIS_API_PORT"
max_attempts=$((GENESIS_WAIT_SECONDS / GENESIS_WAIT_INTERVAL))
if [ "$max_attempts" -lt 1 ]; then
  max_attempts=1
fi

for i in $(seq 1 "$max_attempts"); do
  if curl -fsS "http://${PUBLIC_DOMAIN}:${GENESIS_API_PORT}/v1/identity" >/dev/null 2>&1 || \
     curl -fsS "http://${PUBLIC_DOMAIN}:${GENESIS_API_PORT}/health" >/dev/null 2>&1 || \
     curl -fsS "http://${PUBLIC_DOMAIN}:${GENESIS_API_PORT}/chain-rpc/status" >/dev/null 2>&1; then
    echo "Genesis endpoint is healthy"
    break
  fi

  if [ "$i" -eq "$max_attempts" ]; then
    echo "Genesis endpoint did not become healthy in time (${GENESIS_WAIT_SECONDS}s)" >&2
    echo "Last checks tried:" >&2
    echo "  - /v1/identity" >&2
    echo "  - /health" >&2
    echo "  - /chain-rpc/status" >&2
    echo "Collecting quick remote diagnostics..." >&2
    run_remote "$GENESIS_HOST" "$GENESIS_SSH_PORT" "cd '$DEPLOY_DIR/deploy/join' && docker compose ps" || true
    run_remote "$GENESIS_HOST" "$GENESIS_SSH_PORT" "docker logs node --tail 80" || true
    exit 1
  fi

  sleep "$GENESIS_WAIT_INTERVAL"
done

echo "==> Running join-1"
run_remote "$JOIN1_HOST" "$JOIN1_SSH_PORT" \
  "cd '$DEPLOY_DIR' && \
   export PYTHONUNBUFFERED=1 \
   export DOMAIN='$PUBLIC_DOMAIN' API_PORT='$JOIN1_INTERNAL_API_PORT' PUBLIC_API_PORT='$JOIN1_API_PORT' P2P_PORT='$JOIN1_P2P_PORT' \
   SEED_NODE_RPC_URL='http://${GENESIS_INTERNAL_IP}:26657' \
   RPC_SERVER_URL_1='http://${GENESIS_INTERNAL_IP}:26657' \
   RPC_SERVER_URL_2='http://${GENESIS_INTERNAL_IP}:26657' \
   SEED_API_URL='http://${PUBLIC_DOMAIN}:${GENESIS_API_PORT}' \
   SEED_NODE_P2P_URL='tcp://${PUBLIC_DOMAIN}:${GENESIS_P2P_PORT}' \
   CHAIN_ID='$CHAIN_ID' REPO_BRANCH='$REPO_BRANCH' TESTNET_BASE_DIR='$DEPLOY_DIR' HF_HOME='$HF_HOME' && \
   bash ./join-1.sh"

if [ "${SKIP_JOIN2:-0}" = "1" ]; then
  echo "==> Skipping join-2 (SKIP_JOIN2=1)"
else
  echo "==> Running join-2"
  run_remote "$JOIN2_HOST" "$JOIN2_SSH_PORT" \
    "cd '$DEPLOY_DIR' && \
     export PYTHONUNBUFFERED=1 \
     export DOMAIN='$PUBLIC_DOMAIN' API_PORT='$JOIN2_INTERNAL_API_PORT' PUBLIC_API_PORT='$JOIN2_API_PORT' P2P_PORT='$JOIN2_P2P_PORT' \
     SEED_NODE_RPC_URL='http://${GENESIS_INTERNAL_IP}:26657' \
     RPC_SERVER_URL_1='http://${GENESIS_INTERNAL_IP}:26657' \
     RPC_SERVER_URL_2='http://${GENESIS_INTERNAL_IP}:26657' \
     SEED_API_URL='http://${PUBLIC_DOMAIN}:${GENESIS_API_PORT}' \
     SEED_NODE_P2P_URL='tcp://${PUBLIC_DOMAIN}:${GENESIS_P2P_PORT}' \
     CHAIN_ID='$CHAIN_ID' REPO_BRANCH='$REPO_BRANCH' TESTNET_BASE_DIR='$DEPLOY_DIR' HF_HOME='$HF_HOME' && \
     bash ./join-2.sh" \
  || echo "  [warn] join-2 deploy failed (node may be unreachable), skipping"
fi

echo "==> Running health checks"
SSH_KEY_PATH="$SSH_KEY_PATH" ./healthcheck.sh

echo "==> Deploy complete"
