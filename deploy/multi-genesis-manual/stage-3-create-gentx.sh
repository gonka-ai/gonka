#!/bin/sh
set -ex

# --- STAGE 3: Create Gentx (All Validators) ---
# This script creates the genesis transaction (gentx) for a single validator.
# It requires the 'genesis-intermediate.json' from the coordinator.

: "${MONIKER?MONIKER environment variable is not set}"
OUTPUT_DIR="/output"
STATE_DIR="/tmp/inference"
APP_NAME="inferenced"
CHAIN_ID="gonka-testnet-8"
KEYRING_BACKEND="test"
KEY_NAME_COLD="validator-cold"

# 1. Initialize a temporary node directory
$APP_NAME init "$MONIKER" --chain-id "$CHAIN_ID" --home "$STATE_DIR" > /dev/null

# 2. Place the intermediate genesis file where the app can find it
cp "$OUTPUT_DIR/genesis-intermediate.json" "$STATE_DIR/config/genesis.json"

# 3. Re-create the same cold key to sign the gentx
# (The keyring is temporary, so we need to recover the key to sign)
echo "You will now be prompted to enter the mnemonic phrase for your cold key."
$APP_NAME keys add "$KEY_NAME_COLD" --keyring-backend "$KEYRING_BACKEND" --home "$STATE_DIR" --recover

# 4. Create the genesis transaction
echo "Creating genesis transaction (gentx)..."
$APP_NAME genesis gentx "$KEY_NAME_COLD" 1000000nicoin \
    --chain-id "$CHAIN_ID" \
    --keyring-backend "$KEYRING_BACKEND" \
    --home "$STATE_DIR"

# 5. Package the gentx file for the coordinator
mkdir -p "$OUTPUT_DIR/gentx"
cp "$STATE_DIR/config/gentx/"*.json "$OUTPUT_DIR/gentx/"

echo "---"
echo "Gentx file created: $OUTPUT_DIR/gentx/"
echo "---"
echo "Next step: Send the generated gentx JSON file back to the coordinator."
