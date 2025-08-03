#!/usr/bin/env sh
set -eu
set -x
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

filter_cw20_code() {
  input=$(cat)
  # Remove cw20_code field and its value using sed
  echo "$input" | sed -n -E '
    # If we find cw20_code, skip until the next closing brace
    /[[:space:]]*"cw20_code":[[:space:]]*"/ {
      :skip
      n
      /^[[:space:]]*}[,}]?$/! b skip
      n
    }
    # Print all other lines
    p
  '
}

###############################################################################
# Required / default environment
###############################################################################
need KEY_NAME
need SEED_NODE_RPC_URL
need SEED_NODE_P2P_URL

APP_NAME="${APP_NAME:-inferenced}"
KEYRING_BACKEND="${KEYRING_BACKEND:-test}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-5}"
COIN_DENOM="${COIN_DENOM:-icoin}"
STATE_DIR="${STATE_DIR:-/root/.inference}"

SNAPSHOT_INTERVAL="${SNAPSHOT_INTERVAL:-10}"
SNAPSHOT_KEEP_RECENT="${SNAPSHOT_KEEP_RECENT:-5}"
TRUSTED_BLOCK_PERIOD="${TRUSTED_BLOCK_PERIOD:-2}"

update_configs() {
  if [ "${REST_API_ACTIVE:-}" = true ]; then
    "$APP_NAME" patch-toml "$STATE_DIR/config/app.toml" app_overrides.toml
  else
    echo "Skipping update node config"
  fi
}

###############################################################################
# Detect first run
###############################################################################
INIT_FLAG="$STATE_DIR/.node_initialized"
if [ -f "$INIT_FLAG" ]; then
  FIRST_RUN=false
else
  FIRST_RUN=true
fi

###############################################################################
# One-time initialisation
###############################################################################
if $FIRST_RUN; then
  echo "Initialising node (first run)"
  output=$("$APP_NAME" init --overwrite --chain-id "$CHAIN_ID" \
                       --default-denom "$COIN_DENOM" my-node 2>&1)
  exit_code=$?
  if [ $exit_code -ne 0 ]; then
      echo "Error: '$APP_NAME init' failed with exit code $exit_code"
      echo "Output:"
      echo "$output"
      exit $exit_code
  fi
  echo "$output" | filter_cw20_code

  kv client chain-id "$CHAIN_ID"
  kv client keyring-backend "$KEYRING_BACKEND"
  kv app minimum-gas-prices "0${COIN_DENOM}"

  update_configs

  GENESIS_FILE="$STATE_DIR/config/genesis.json"
  output=$("$APP_NAME" download-genesis "$SEED_NODE_RPC_URL" "$GENESIS_FILE" 2>&1)
  echo "$output" | filter_cw20_code

  touch "$INIT_FLAG"
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

update_configs

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