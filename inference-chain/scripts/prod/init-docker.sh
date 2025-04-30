#!/bin/sh

# Check if mandatory argument is provided
if [ -z "$KEY_NAME" ]; then
  echo "Error: KEY_NAME is required."
  exit 1
fi

if [ -z "$KEYRING_BACKEND" ]; then
  echo "KEYRING_BACKEND is not specified defaulting to test"
  KEYRING_BACKEND="test"
fi

if [ -z "$SEED_NODE_RPC_URL" ]; then
  echo "SEED_NODE_RPC_URL env var is required"
  exit 1
fi

if [ -z "$SEED_NODE_P2P_URL" ]; then
  echo "SEED_NODE_P2P_URL env var is required"
  exit 1
fi

# Display the parsed values (for debugging)
echo "Using the following arguments"
echo "KEY_NAME: $KEY_NAME"
echo "SEEDS: $SEEDS"
echo "KEYRING_BACKEND: $KEYRING_BACKEND"

APP_NAME="inferenced"
CHAIN_ID="prod-sim"
COIN_DENOM="icoin"
STATE_DIR="/root/.inference"

ACCOUNT_EXISTS=false
echo "🔍 Checking if account $KEY_NAME exists in keyring ($KEYRING_BACKEND)..."
ACCOUNT_CHECK=$($APP_NAME keys show "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" --keyring-dir "$STATE_DIR" 2>&1 || true)

set -e

echo "DEBUG LOG ACCOUNT_CHECK: $ACCOUNT_CHECK"

if echo "$ACCOUNT_CHECK" | grep -iE "is not a valid name or address|not found"; then
   echo "❌ Account $KEY_NAME does not exist!"
else
   echo "✅ Account $KEY_NAME found. Using existing account."
   ACCOUNT_EXISTS=true
fi

if [ "$ACCOUNT_EXISTS" = true ]; then
    echo "Node is already configured, skip configuration"

    if [ -n "$TKMS_PORT" ]; then
      echo "🔒 Using TMKMS: removing local consensus key and set up priv_validator_laddr to tcp://0.0.0.0:${TKMS_PORT}"

      rm -f $STATE_DIR/config/priv_validator_key.json
      rm -f $STATE_DIR/data/priv_validator_state.json

      sed -i "s|^priv_validator_laddr =.*|priv_validator_laddr = \"tcp://0.0.0.0:${TKMS_PORT}\"|"   $STATE_DIR/config/config.toml
      sed -i "s|^priv_validator_key_file *=|# priv_validator_key_file =|" "$STATE_DIR/config/config.toml"
      sed -i "s|^priv_validator_state_file *=|# priv_validator_state_file =|" "$STATE_DIR/config/config.toml"
    else
      echo "TKMS_PORT is not set, skipping"
    fi

    echo "Running node..."
    cosmovisor init /usr/bin/inferenced

    # Idle the container in the event that cosmovisor fails
    cosmovisor run start || {
    echo "Cosmovisor failed, idling the container..."
    tail -f /dev/null
    }
fi

echo "Configure node"
echo "Current directory: $(pwd)"

# Init the chain:
# I'm using prod-sim as the chain name (production simulation)
#   and icoin (intelligence coin) as the default denomination
#   and my-node as a node moniker (it doesn't have to be unique)
$APP_NAME init \
  --overwrite \
  --chain-id "$CHAIN_ID" \
  --default-denom $COIN_DENOM \
  my-nod
$APP_NAME config set client chain-id $CHAIN_ID
$APP_NAME config set client keyring-backend $KEYRING_BACKEND
$APP_NAME config set app minimum-gas-prices "0$COIN_DENOM"

# create snapshots every N blocks and keep last M snapshots
SNAPSHOT_INTERVAL=${SNAPSHOT_INTERVAL:-10}
SNAPSHOT_KEEP_RECENT=${SNAPSHOT_KEEP_RECENT:-5}
$APP_NAME config set app state-sync.snapshot-interval $SNAPSHOT_INTERVAL
$APP_NAME config set app state-sync.snapshot-keep-recent $SNAPSHOT_KEEP_RECENT
sed -Ei 's/^laddr = ".*:26657"$/laddr = "tcp:\/\/0\.0\.0\.0:26657"/g' \
  $STATE_DIR/config/config.toml

if [ -n "$P2P_EXTERNAL_ADDRESS" ]; then
  echo "Setting the external address for P2P to $P2P_EXTERNAL_ADDRESS"
  $APP_NAME config set config p2p.external_address "$P2P_EXTERNAL_ADDRESS" --skip-validate
else
  echo "P2P_EXTERNAL_ADDRESS is not set, skipping"
fi

$APP_NAME set-seeds "$STATE_DIR/config/config.toml" "$SEED_NODE_RPC_URL" "$SEED_NODE_P2P_URL"
echo "Grepping seeds =:"
grep "seeds =" $STATE_DIR/config/config.toml

# sync with snapshots?
 if [ "$SYNC_WITH_SNAPSHOTS" = "true" ]; then
     echo "Node must sync using snapshots"
TRUSTED_BLOCK_PERIOD=${TRUSTED_BLOCK_PERIOD:-2}
 $APP_NAME set-statesync "$STATE_DIR/config/config.toml" true
 $APP_NAME set-statesync-rpc-servers "$STATE_DIR/config/config.toml"  "$RPC_SERVER_URL_1" "$RPC_SERVER_URL_2"
 $APP_NAME set-statesync-trusted-block "$STATE_DIR/config/config.toml"  "$SEED_NODE_RPC_URL" "$TRUSTED_BLOCK_PERIOD"
 else
     echo "Node will sync WITHOUT snapshots"
 fi
 
echo "Setting up overrides for config.toml"
 # Process CONFIG_ environment variables
 for var in $(env | grep '^CONFIG_'); do
    # Extract key and value
    key=${var%%=*}
    value=${var#*=}
    
    # Remove CONFIG_ prefix and transform __ to .
    config_key=${key#CONFIG_}
    config_key=${config_key//__/.}
    
    echo "Setting config: $config_key = $value"
    $APP_NAME config set config "$config_key" "$value" --skip-validate
 done
# Check and apply config overrides if present
if [ -f "config_overrides.toml" ]; then
    echo "Applying config overrides from config_overrides.toml"
    $APP_NAME patch-toml "$STATE_DIR/config/config.toml" config_overrides.toml
fi

echo "Creating account for $KEY_NAME"
$APP_NAME keys add "$KEY_NAME" --keyring-backend $KEYRING_BACKEND --keyring-dir "$STATE_DIR"

# Need to join network? Or is that solely from the compose file?
GENESIS_FILE="./.inference/genesis.json"
$APP_NAME download-genesis "$SEED_NODE_RPC_URL" "$GENESIS_FILE"
cat $GENESIS_FILE
echo "Using genesis file: $GENESIS_FILE"
cp "$GENESIS_FILE" $STATE_DIR/config/genesis.json

if [ -n "$TKMS_PORT" ]; then
  echo "🔒 Using TMKMS: removing local consensus key and set up priv_validator_laddr to tcp://0.0.0.0:${TKMS_PORT}"

  rm -f $STATE_DIR/config/priv_validator_key.json
  rm -f $STATE_DIR/data/priv_validator_state.json

  sed -i "s|^priv_validator_laddr =.*|priv_validator_laddr = \"tcp://0.0.0.0:${TKMS_PORT}\"|"   $STATE_DIR/config/config.toml
  sed -i "s|^priv_validator_key_file *=|# priv_validator_key_file =|" "$STATE_DIR/config/config.toml"
  sed -i "s|^priv_validator_state_file *=|# priv_validator_state_file =|" "$STATE_DIR/config/config.toml"
else
  echo "TKMS_PORT is not set, skipping"
fi

echo "Running node..."
cosmovisor init /usr/bin/inferenced

# Idle the container in the event that cosmovisor fails
cosmovisor run start || {
  echo "Cosmovisor failed, idling the container..."
  tail -f /dev/null
}
