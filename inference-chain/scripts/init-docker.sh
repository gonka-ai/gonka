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

# Display the parsed values (for debugging)
echo "Using the following arguments"
echo "KEY_NAME: $KEY_NAME"
echo "SEEDS: $SEEDS"
echo "IS_GENESIS: $IS_GENESIS"
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
  --chain-id "$CHAIN_ID" \
  --default-denom $COIN_DENOM \
  my-node

$APP_NAME config set client chain-id $CHAIN_ID
$APP_NAME config set client keyring-backend $KEYRING_BACKEND
$APP_NAME config set app minimum-gas-prices "0$COIN_DENOM"
$APP_NAME config set state-sync.snapshot-interval 10
$APP_NAME config set state-sync.snapshot-keep-recent 2

# Allow access to RPC
sed -Ei 's/^laddr = ".*:26657"$/laddr = "tcp:\/\/0\.0\.0\.0:26657"/g' \
  $STATE_DIR/config/config.toml
sed -Ei "s/^seeds = .*$/seeds = \"$SEEDS\"/g" \
  $STATE_DIR/config/config.toml

# Create a key
$APP_NAME keys \
    --keyring-backend $KEYRING_BACKEND --keyring-dir "$STATE_DIR" \
    add "$KEY_NAME"

if [ "$IS_GENESIS" = true ]; then
  echo "This is a genesis node setup."
  $APP_NAME genesis add-genesis-account "$KEY_NAME" "10000000000$COIN_DENOM" --keyring-backend $KEYRING_BACKEND
  $APP_NAME genesis gentx "$KEY_NAME" "10000000$COIN_DENOM" --chain-id "$CHAIN_ID"
  $APP_NAME genesis collect-gentxs
else
  echo "To complete your setup, you need to ask someone to send you some coins. You can find your address above: \"cosmos...\""
fi
