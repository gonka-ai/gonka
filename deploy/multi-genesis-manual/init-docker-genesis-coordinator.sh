#!/bin/sh
set -e
set -x

APP_NAME="inferenced"
CHAIN_ID="gonka-testnet-8"
COIN_DENOM="nicoin"
STATE_DIR="/root/.inference"
KEYRING_BACKEND="test"
FINAL_GENESIS_DIR="/root/final_genesis"

# ==============================================================================
# 1. Initialize the chain if it hasn't been already
# ==============================================================================
if [ ! -d "$STATE_DIR/config" ]; then
    echo "Initializing chain..."
    $APP_NAME init coordinator-node --chain-id "$CHAIN_ID" --default-denom "$COIN_DENOM" --home "$STATE_DIR"
else
    echo "Chain already initialized. Skipping init."
fi

# ==============================================================================
# 2. Add all genesis accounts from the collected addresses
# ==============================================================================
# The script will fail if the addresses directory is empty.
if [ -z "$(ls -A /root/addresses_collected)" ]; then
    echo "Error: The /root/addresses_collected directory is empty. It must contain the address files from all genesis validators."
    exit 1
fi

for addr_file in /root/addresses_collected/*; do
    ACCOUNT_ADDR=$(cat "$addr_file")
    echo "Adding genesis account: $ACCOUNT_ADDR"
    $APP_NAME genesis add-genesis-account "$ACCOUNT_ADDR" 1000000000000"$COIN_DENOM" --home "$STATE_DIR"
done

# ==============================================================================
# 3. Copy all gentx files and create the final genesis
# ==============================================================================
GENTX_TARGET_DIR="$STATE_DIR/config/gentx"
mkdir -p "$GENTX_TARGET_DIR"

if [ -z "$(ls -A /root/gentx_collected)" ]; then
    echo "Error: The /root/gentx_collected directory is empty. It must contain the gentx files from all genesis validators."
    exit 1
fi

cp /root/gentx_collected/*.json "$GENTX_TARGET_DIR/"
echo "Copied all gentx files to $GENTX_TARGET_DIR"

echo "Collecting gentxs..."
$APP_NAME genesis collect-gentxs --home "$STATE_DIR"

# ==============================================================================
# 4. Apply genesis overrides and distribute the final genesis
# ==============================================================================
if [ -f "/root/genesis_overrides.json" ]; then
    jq -s '.[0] * .[1]' "$STATE_DIR/config/genesis.json" /root/genesis_overrides.json > "$STATE_DIR/config/genesis.json.tmp"
    mv "$STATE_DIR/config/genesis.json.tmp" "$STATE_DIR/config/genesis.json"
    echo "Applied genesis overrides."
fi

# Make the final genesis file available on the host
mkdir -p "$FINAL_GENESIS_DIR"
cp "$STATE_DIR/config/genesis.json" "$FINAL_GENESIS_DIR/genesis.json"
echo "Final genesis.json has been created and is available in the mapped 'final_genesis' volume."

# ==============================================================================
# 5. Start the node
# ==============================================================================
echo "Starting coordinator node..."
$APP_NAME start --home "$STATE_DIR"
