#!/usr/bin/env sh

set -u
set -o pipefail

###############################################################################
# Helper functions
###############################################################################
fail() { echo "ERROR: $1" >&2; [ -n "${DEBUG-}" ] && tail -f /dev/null || exit 1; }
need() { [ -z "${!1-}" ] && fail "Environment variable $1 is required"; }
kv()   { inferenced config set "$@"; }

apply_tmkms() {
  [ -z "${TMKMS_PORT-}" ] && return
  rm -f "$STATE_DIR/config/priv_validator_key.json" \
        "$STATE_DIR/data/priv_validator_state.json"
  sed -i \
    -e "s|^priv_validator_laddr =.*|priv_validator_laddr = \"tcp://0.0.0.0:${TMKMS_PORT}\"|" \
    -e "s|^priv_validator_key_file *=|# priv_validator_key_file =|" \
    -e "s|^priv_validator_state_file *=|# priv_validator_state_file =|" \
    "$STATE_DIR/config/config.toml"
}

apply_env_config() {
  [ -n "${P2P_EXTERNAL_ADDRESS-}" ] \
    && kv config p2p.external_address "$P2P_EXTERNAL_ADDRESS" --skip-validate

  inferenced set-seeds "$STATE_DIR/config/config.toml" \
                       "$SEED_NODE_RPC_URL" "$SEED_NODE_P2P_URL"

  inferenced set-statesync "$STATE_DIR/config/config.toml" \
                           "${SYNC_WITH_SNAPSHOTS:-true}"

  if [ "${SYNC_WITH_SNAPSHOTS:-true}" = "true" ]; then
    echo "Setting state-sync with snapshots"
    echo "RPC_SERVER_URL_1: $RPC_SERVER_URL_1"
    echo "RPC_SERVER_URL_2: $RPC_SERVER_URL_2"
    echo "SEED_NODE_RPC_URL: $SEED_NODE_RPC_URL"
    echo "TRUSTED_BLOCK_PERIOD: $TRUSTED_BLOCK_PERIOD"

    if [ -z "${RPC_SERVER_URL_1-}" ] || [ -z "${RPC_SERVER_URL_2-}" ]; then
        echo "Skipping state-sync RPC server configuration - RPC_SERVER_URL_1 or RPC_SERVER_URL_2 not set"
        return
    fi
    inferenced set-statesync-rpc-servers   "$STATE_DIR/config/config.toml" \
                                           "$RPC_SERVER_URL_1" "$RPC_SERVER_URL_2"
    inferenced set-statesync-trusted-block "$STATE_DIR/config/config.toml" \
                                           "$SEED_NODE_RPC_URL" "${TRUSTED_BLOCK_PERIOD:-2}"
  fi

  kv app state-sync.snapshot-interval    "${SNAPSHOT_INTERVAL:=10}"
  kv app state-sync.snapshot-keep-recent "${SNAPSHOT_KEEP_RECENT:=5}"

  for env_kv in $(env | grep '^CONFIG_'); do
    key=${env_kv%%=*}; val=${env_kv#*=}
    cfg_key=${key#CONFIG_}; cfg_key=${cfg_key//__/.}
    kv config "$cfg_key" "$val" --skip-validate
  done

  [ -f config_overrides.toml ] \
    && inferenced patch-toml "$STATE_DIR/config/config.toml" config_overrides.toml
}

###############################################################################
# Environment validation / defaults
###############################################################################
need KEY_NAME
need SEED_NODE_RPC_URL
need SEED_NODE_P2P_URL

KEYRING_BACKEND=${KEYRING_BACKEND:=test}

APP_NAME=inferenced
CHAIN_ID=${CHAIN_ID:=gonka-testnet-3}
COIN_DENOM=${COIN_DENOM:=icoin}
STATE_DIR=${STATE_DIR:=/root/.inference}

###############################################################################
# Check whether the key already exists (do before enabling 'set -e')
###############################################################################
if $APP_NAME keys show "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" \
                                   --keyring-dir "$STATE_DIR" >/dev/null 2>&1; then
  ACCOUNT_EXISTS=true
else
  ACCOUNT_EXISTS=false
fi

set -e

###############################################################################
# First-run initialisation
###############################################################################
if [ "$ACCOUNT_EXISTS" = false ]; then
  $APP_NAME init --overwrite --chain-id "$CHAIN_ID" \
                 --default-denom "$COIN_DENOM" my-node

  kv client chain-id "$CHAIN_ID"
  kv client keyring-backend "$KEYRING_BACKEND"
  kv app minimum-gas-prices "0$COIN_DENOM"

  GENESIS_FILE="$STATE_DIR/config/genesis.json"
  $APP_NAME download-genesis "$SEED_NODE_RPC_URL" "$GENESIS_FILE"

  $APP_NAME keys add "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" \
                                 --keyring-dir "$STATE_DIR"
fi

###############################################################################
# Configuration applied on every start
###############################################################################
apply_env_config
apply_tmkms

###############################################################################
# Launch
###############################################################################
cosmovisor init /usr/bin/inferenced || fail "Cosmovisor init failed"

if [ -n "${DEBUG-}" ]; then
  cosmovisor run start || fail "Failed to start inferenced"
else
  exec cosmovisor run start
fi