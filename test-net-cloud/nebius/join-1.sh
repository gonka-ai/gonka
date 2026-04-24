#!/usr/bin/env bash
set -euo pipefail

if [ -f ".env" ]; then
  set -a
  # shellcheck disable=SC1091
  source ./.env
  set +a
fi

export KEY_NAME="${KEY_NAME:-join-1}"
export COLD_KEY_MNEMONIC="${COLD_KEY_MNEMONIC:-${JOIN1_COLD_KEY_MNEMONIC:-}}"
export DOMAIN="${DOMAIN:-${PUBLIC_DOMAIN:-xj7-5.s.filfox.io}}"
export API_PORT="${API_PORT:-${JOIN1_INTERNAL_API_PORT:-8000}}"
export PUBLIC_API_PORT="${PUBLIC_API_PORT:-${JOIN1_API_PORT:-19250}}"
export P2P_PORT="${P2P_PORT:-${JOIN1_P2P_PORT:-19249}}"
export PUBLIC_URL="http://${DOMAIN}:${PUBLIC_API_PORT}"
export P2P_EXTERNAL_ADDRESS="tcp://${DOMAIN}:${P2P_PORT}"

export SYNC_WITH_SNAPSHOTS="${SYNC_WITH_SNAPSHOTS:-false}"
export DAPI_API__POC_CALLBACK_URL="${DAPI_API__POC_CALLBACK_URL:-http://api:9100}"
export CHAIN_ID="${CHAIN_ID:-gonka-testnet-3}"
export HF_HOME="${HF_HOME:-/srv/dai/cache/}"
export TESTNET_BASE_DIR="${TESTNET_BASE_DIR:-${DEPLOY_DIR:-/srv/dai/}}"

python3 launch.py --mode join --branch "${REPO_BRANCH:-origin/testnet/main}" --chainid "$CHAIN_ID"
