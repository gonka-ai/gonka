#!/usr/bin/env sh
set -eu
( set -o pipefail 2>/dev/null ) && set -o pipefail

###############################################################################
# Helper functions
###############################################################################
fail() {
  echo "ERROR: $1" >&2
  if [ -n "${DEBUG-}" ]; then
    tail -f /dev/null          # keep container up for inspection
  else
    exit 1
  fi
}

need() { eval ": \${$1:?Environment variable $1 is required}"; }

# treat 0 (“changed”) and 3 (“already correct”) as success
ok_rc() { [ "$1" -eq 0 ] || [ "$1" -eq 3 ]; }

run() {
  echo "CMD> $*"
  "$@"
  rc=$?
  echo "RC = $rc"
  ok_rc "$rc" || fail "'$*' failed with code $rc"
}

kv() { run "$APP_NAME" config set "$@"; }

###############################################################################
# Required / default environment
###############################################################################
need KEY_NAME
need SEED_NODE_RPC_URL
need SEED_NODE_P2P_URL

APP_NAME="${APP_NAME:-inferenced}"
KEYRING_BACKEND="${KEYRING_BACKEND:-test}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-3}"
COIN_DENOM="${COIN_DENOM:-icoin}"
STATE_DIR="${STATE_DIR:-/root/.inference}"

SNAPSHOT_INTERVAL="${SNAPSHOT_INTERVAL:-10}"
SNAPSHOT_KEEP_RECENT="${SNAPSHOT_KEEP_RECENT:-5}"
TRUSTED_BLOCK_PERIOD="${TRUSTED_BLOCK_PERIOD:-2}"

update_configs_for_explorer() {
  if [ "$WITH_EXPLORER" = true ]; then
    echo "Updating configs for enable explorer..."
    sed -i 's/^enable *= *false/enable = true/' "$STATE_DIR/config/app.toml"

    # enabled-unsafe-cors = true
    sed -i 's/^enabled-unsafe-cors *= *false/enabled-unsafe-cors = true/' "$STATE_DIR/config/app.toml"

    # cors_allowed_origins = ["*"]
    sed -i 's|^cors_allowed_origins *= *\[.*\]|cors_allowed_origins = ["*"]|' "$STATE_DIR/config/config.toml"

    # tcp://localhost:1317 → tcp://0.0.0.0:1317
    sed -i 's|tcp://localhost:1317|tcp://0.0.0.0:1317|' "$STATE_DIR/config/app.toml"
  else
    echo "Skipping config changes for explorer"
  fi
}

###############################################################################
# Detect first run
###############################################################################
if "$APP_NAME" keys show "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" \
                                    --keyring-dir "$STATE_DIR" >/dev/null 2>&1
then
  FIRST_RUN=false
else
  FIRST_RUN=true
fi

###############################################################################
# One-time initialisation
###############################################################################
if $FIRST_RUN; then
  echo "Initialising node (first run)"
  run "$APP_NAME" init --overwrite --chain-id "$CHAIN_ID" \
                       --default-denom "$COIN_DENOM" my-node

  kv client chain-id "$CHAIN_ID"
  kv client keyring-backend "$KEYRING_BACKEND"
  kv app minimum-gas-prices "0${COIN_DENOM}"

  GENESIS_FILE="$STATE_DIR/config/genesis.json"
  run "$APP_NAME" download-genesis "$SEED_NODE_RPC_URL" "$GENESIS_FILE"
    update_configs_for_explorer

    echo "Running node..."
    cosmovisor init /usr/bin/inferenced || fail "Failed to initialize cosmovisor"

  run "$APP_NAME" keys add "$KEY_NAME" \
       --keyring-backend "$KEYRING_BACKEND" --keyring-dir "$STATE_DIR"
fi

###############################################################################
# Configuration executed on every start
###############################################################################
echo "Applying configuration at container start"

# Seed / state-sync settings ---------------------------------------------------
[ -n "${P2P_EXTERNAL_ADDRESS-}" ] \
    && kv config p2p.external_address "$P2P_EXTERNAL_ADDRESS" --skip-validate

run "$APP_NAME" set-seeds "$STATE_DIR/config/config.toml" \
     "$SEED_NODE_RPC_URL" "$SEED_NODE_P2P_URL"

run "$APP_NAME" set-statesync "$STATE_DIR/config/config.toml" \
     "${SYNC_WITH_SNAPSHOTS:-false}"

if [ "${SYNC_WITH_SNAPSHOTS:-false}" = "true" ]; then
  need RPC_SERVER_URL_1
  need RPC_SERVER_URL_2
  run "$APP_NAME" set-statesync-rpc-servers "$STATE_DIR/config/config.toml" \
       "$RPC_SERVER_URL_1" "$RPC_SERVER_URL_2"
  run "$APP_NAME" set-statesync-trusted-block "$STATE_DIR/config/config.toml" \
       "$SEED_NODE_RPC_URL" "$TRUSTED_BLOCK_PERIOD"
fi

# Snapshot parameters ----------------------------------------------------------
kv app state-sync.snapshot-interval    "$SNAPSHOT_INTERVAL"
kv app state-sync.snapshot-keep-recent "$SNAPSHOT_KEEP_RECENT"
sed -Ei 's/^laddr = ".*:26657"$/laddr = "tcp:\/\/0\.0\.0\.0:26657"/g' $STATE_DIR/config/config.toml

# CONFIG_* environment overrides ----------------------------------------------
(
  env | grep '^CONFIG_' || true
) | while IFS='=' read -r raw_key raw_val; do
  key=${raw_key#CONFIG_}; key=${key//__/.}
  kv config "$key" "$raw_val" --skip-validate
done

# File-based overrides ---------------------------------------------------------
[ -f config_overrides.toml ] \
  && run "$APP_NAME" patch-toml "$STATE_DIR/config/config.toml" config_overrides.toml

# TMKMS integration ------------------------------------------------------------
if [ -n "${TMKMS_PORT-}" ]; then
  echo "Configuring TMKMS (port $TMKMS_PORT)"
  rm -f "$STATE_DIR/config/priv_validator_key.json" \
        "$STATE_DIR/data/priv_validator_state.json"

  sed -i \
    -e "s|^priv_validator_laddr =.*|priv_validator_laddr = \"tcp://0.0.0.0:${TMKMS_PORT}\"|" \
    -e "s|^priv_validator_key_file *=|# priv_validator_key_file =|" \
    -e "s|^priv_validator_state_file *=|# priv_validator_state_file =|" \
    "$STATE_DIR/config/config.toml"
fi

update_configs_for_explorer

###############################################################################
# Cosmovisor bootstrap (one-time)
###############################################################################
if [ ! -d "$STATE_DIR/cosmovisor" ]; then
  echo "Initialising cosmovisor directory"
  run cosmovisor init /usr/bin/inferenced
fi

###############################################################################
# Launch node
###############################################################################
echo "Starting node"
if [ -n "${DEBUG-}" ]; then
  cosmovisor run start || fail "Node process exited"
else
  exec cosmovisor run start
fi