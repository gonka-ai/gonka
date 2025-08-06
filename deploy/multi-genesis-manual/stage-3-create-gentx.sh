#!/bin/sh
set -ex

# --- STAGE 3: Create Gentx (All Validators) ---
# This script creates the genesis transaction (gentx) for a single validator.
# It uses the file-based keyring created in Stage 1.

: "${MONIKER?MONIKER environment variable is not set}"
# The entire state, including the keyring, is expected to be in the output directory.
STATE_DIR="/output"
APP_NAME="inferenced"
CHAIN_ID="gonka-testnet-8"
KEYRING_BACKEND="file"
KEY_NAME_COLD="validator-cold"
# Password for the file-based keyring. Can be overridden.
KEYRING_PASSWORD=${KEYRING_PASSWORD:-"password"}


# 1. Place the intermediate genesis file where the app can find it.
# This overwrites the dummy genesis.json created in Stage 1.
cp "$STATE_DIR/genesis-intermediate.json" "$STATE_DIR/config/genesis.json"

# 2. Create the genesis transaction.
# The key is read from the file keyring in the mounted STATE_DIR.
echo "Creating genesis transaction (gentx)..."
printf "%s\n" "$KEYRING_PASSWORD" | $APP_NAME genesis gentx "$KEY_NAME_COLD" 1000000nicoin \
    --chain-id "$CHAIN_ID" \
    --keyring-backend "$KEYRING_BACKEND" \
    --home "$STATE_DIR"

# 3. Rename the gentx file for easier identification by the coordinator.
GENTX_FILE=$(ls "$STATE_DIR/config/gentx/"*.json)
mv "$GENTX_FILE" "$STATE_DIR/gentx-$MONIKER.json"


echo "---"
echo "Gentx file created: $STATE_DIR/gentx-$MONIKER.json"
echo "---"
echo "Next step: Send the generated gentx JSON file ('gentx-$MONIKER.json') back to the coordinator."
