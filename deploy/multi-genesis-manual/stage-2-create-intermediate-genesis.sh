#!/bin/sh
set -ex

# --- STAGE 2: Create Intermediate Genesis (Coordinator Only) ---
# This script initializes a new chain and adds all collected validator
# accounts to the genesis file, creating an "intermediate" genesis.

# All paths are relative to the /data directory inside the container.
BASE_DIR="/data"
STATE_DIR="$BASE_DIR/.inference"
APP_NAME="inferenced"
CHAIN_ID="gonka-testnet-8"
COIN_DENOM="nicoin"
ADDRESSES_DIR="$BASE_DIR/addresses_collected"
OUTPUT_DIR="$BASE_DIR/intermediate_genesis_output"
GENESIS_OVERRIDES_PATH="$BASE_DIR/genesis_overrides.json"

# 1. Initialize a new chain
$APP_NAME init coordinator-node --chain-id "$CHAIN_ID" --home "$STATE_DIR"

# 2. Add all collected addresses as genesis accounts
if [ -z "$(ls -A $ADDRESSES_DIR)" ]; then
    echo "Error: The $ADDRESSES_DIR directory is empty."
    exit 1
fi

for addr_file in "$ADDRESSES_DIR"/*; do
    ACCOUNT_ADDR=$(cat "$addr_file")
    echo "Adding genesis account: $ACCOUNT_ADDR"
    $APP_NAME genesis add-genesis-account "$ACCOUNT_ADDR" 2000000"$COIN_DENOM" --home "$STATE_DIR"
done

# 3. Apply any genesis overrides
if [ -f "$GENESIS_OVERRIDES_PATH" ]; then
    jq -s '.[0] * .[1]' "$STATE_DIR/config/genesis.json" "$GENESIS_OVERRIDES_PATH" > "$STATE_DIR/config/genesis.json.tmp"
    mv "$STATE_DIR/config/genesis.json.tmp" "$STATE_DIR/config/genesis.json"
    echo "Applied genesis overrides."
fi

# 4. Copy the resulting genesis file to the output directory for distribution
mkdir -p "$OUTPUT_DIR"
cp "$STATE_DIR/config/genesis.json" "$OUTPUT_DIR/genesis-intermediate.json"

echo "---"
echo "Intermediate genesis file created: $OUTPUT_DIR/genesis-intermediate.json"
echo "---"
echo "Next step: Distribute this 'genesis-intermediate.json' file to ALL genesis validators."
