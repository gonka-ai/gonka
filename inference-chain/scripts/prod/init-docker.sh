#!/bin/sh
set -e

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

echo "Current directory: $(pwd)"

# Init the chain:
# I'm using prod-sim as the chain name (production simulation)
#   and icoin (intelligence coin) as the default denomination
#   and my-node as a node moniker (it doesn't have to be unique)
$APP_NAME init \
  --overwrite \
  --chain-id "$CHAIN_ID" \
  --default-denom $COIN_DENOM \
  my-node

$APP_NAME config set client chain-id $CHAIN_ID
$APP_NAME config set client keyring-backend $KEYRING_BACKEND
$APP_NAME config set app minimum-gas-prices "0$COIN_DENOM"

sed -Ei 's/^laddr = ".*:26657"$/laddr = "tcp:\/\/0\.0\.0\.0:26657"/g' \
  $STATE_DIR/config/config.toml

$APP_NAME set-seeds "$STATE_DIR/config/config.toml" "$SEED_NODE_RPC_URL" "$SEED_NODE_P2P_URL"

echo "Grepping seeds =:"
grep "seeds =" $STATE_DIR/config/config.toml

# Create a key
$APP_NAME keys \
    --keyring-backend $KEYRING_BACKEND --keyring-dir "$STATE_DIR" \
    add "$KEY_NAME"

# Need to join network? Or is that solely from the compose file?

GENESIS_FILE="./.inference/genesis.json"
$APP_NAME download-genesis "$SEED_NODE_RPC_URL" "$GENESIS_FILE"

cat $GENESIS_FILE

echo "Using genesis file: $GENESIS_FILE"
cp "$GENESIS_FILE" $STATE_DIR/config/genesis.json

cosmovisor init /usr/bin/inferenced

# Idle the container in the event that cosmovisor fails
cosmovisor run start || {
  echo "Cosmovisor failed, idling the container..."
  tail -f /dev/null
}
