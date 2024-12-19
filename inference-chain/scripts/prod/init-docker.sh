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

if [ -z "$SEEDS" ]; then
  echo "Seeds not specified, SEEDS are required."
  # This needs to be set BEFORE the build to the actual seed values for the chain we want
  # the dockerfile to point to
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
sed -Ei "s/^seeds = .*$/seeds = \"$SEEDS\"/g" \
  $STATE_DIR/config/config.toml

# Create a key
$APP_NAME keys \
    --keyring-backend $KEYRING_BACKEND --keyring-dir "$STATE_DIR" \
    add "$KEY_NAME"

# Need to join network? Or is that solely from the compose file?

GENESIS_FILE="./.inference/genesis.json"
if [ ! -f "$GENESIS_FILE" ]; then
  echo "Genesis file not found at $GENESIS_FILE"
  exit 1
fi

echo "Using genesis file: $GENESIS_FILE"
cp "$GENESIS_FILE" $STATE_DIR/config/genesis.json

cosmovisor init /usr/bin/inferenced
cosmovisor run start
