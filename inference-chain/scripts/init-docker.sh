#!/bin/bash

# Initialize variables
KEY_NAME=""
SEEDS=""
IS_GENESIS=false

# Function to display usage
usage() {
  echo "Usage: $0 --key-name <key_name> [--seeds <seeds>] [--is-genesis]"
  exit 1
}

# Parse command line arguments
while [[ "$#" -gt 0 ]]; do
  case "$1" in
    --key-name)
      KEY_NAME="$2"
      shift 2
      ;;
    --seeds)
      SEEDS="$2"
      shift 2
      ;;
    --is-genesis)
      IS_GENESIS=true
      shift 1
      ;;
    *)
      echo "Unknown parameter passed: $1"
      usage
      ;;
  esac
done

# Check if mandatory argument is provided
if [ -z "$KEY_NAME" ]; then
  echo "Error: --key-name is required."
  usage
fi

# Display the parsed values (for debugging)
echo "Using the following arguments"
echo "KEY_NAME: $KEY_NAME"
echo "SEEDS: $SEEDS"
echo "IS_GENESIS: $IS_GENESIS"

exit

APP_NAME="inferenced"
CHAIN_ID="prod-sim"
COIN_DENOM="icoin"
STATE_DIR="/root/.inference"

KEY_NAME=$1
if [ -z "$KEY_NAME" ]; then
  echo "Usage: $0 <key-name> <seeds>. The key name is the name of your account key to sign transactions."
  exit 1
fi

SEEDS=$2
if [ -z "$SEEDS" ]; then
  echo "Usage: $0 <key-name> <seeds>. The seeds aren't provided. The node will be created empty."
  SEEDS=""
fi

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
$APP_NAME config set client keyring-backend file
$APP_NAME config set app minimum-gas-prices "0$COIN_DENOM"
sed -Ei 's/^laddr = ".*:26657"$/laddr = "tcp:\/\/0\.0\.0\.0:26657"/g' \
  $STATE_DIR/config/config.toml
sed -Ei "s/^seeds = .*$/seeds = \"$SEEDS\"/g" \
  $STATE_DIR/config/config.toml

# Create a key
$APP_NAME keys \
    --keyring-backend file --keyring-dir "$STATE_DIR" \
    add "$KEY_NAME"

if [ "$IS_GENESIS" = true ]; then
  echo "This is a genesis node setup."
  $APP_NAME genesis add-genesis-account alice 10000000$DENOM --keyring-backend file
  $APP_NAME genesis gentx alice 1000000$DENOM --chain-id "$CHAIN_ID"
  $APP_NAME genesis collect-gentxs
else
  echo "Copying the genesis file"
  cp /root/genesis.json $STATE_DIR/config/genesis.json
  echo "To complete your setup, you need to ask someone to send you some coins. You can find your address above: \"cosmos...\""
fi
