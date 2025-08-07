#!/bin/sh
set -ex

# --- STAGE 3: Create Gentx (All Validators) ---
# This script creates the genesis transaction (gentx) for a single validator.
# It requires the 'genesis-intermediate.json' from the coordinator and
# will prompt for the mnemonic phrase from Stage 1.

: "${MONIKER?MONIKER environment variable is not set}"
# The entire state, including the keyring, is expected to be in the output directory.
STATE_DIR="/output"
APP_NAME="inferenced"
CHAIN_ID="gonka-testnet-8"
KEYRING_BACKEND="file"
KEY_NAME_COLD="validator-cold"

# 1. Place the intermediate genesis file where the app can find it.
cp "$STATE_DIR/genesis-intermediate.json" "$STATE_DIR/config/genesis.json"

# 2. Recover the key using the mnemonic. This will be an interactive prompt.
# echo "You will now be prompted to enter the mnemonic phrase for your cold key from Stage 1."
# $APP_NAME keys add "$KEY_NAME_COLD" --keyring-backend "$KEYRING_BACKEND" --home "$STATE_DIR" --recover

# 3. Create the genesis transaction. You will be prompted for the keyring password.
echo "Creating genesis transaction (gentx)..."
$APP_NAME genesis gentx "$KEY_NAME_COLD" 1000000nicoin \
    --chain-id "$CHAIN_ID" \
    --keyring-backend "$KEYRING_BACKEND" \
    --home "$STATE_DIR"

# 4. Rename the gentx file for easier identification by the coordinator.
GENTX_FILE=$(ls "$STATE_DIR/config/gentx/"*.json)
mv "$GENTX_FILE" "$STATE_DIR/gentx-$MONIKER.json"


echo "---"
echo "Gentx file created: $STATE_DIR/gentx-$MONIKER.json"
echo "---"
echo "Next step: Send the generated gentx JSON file ('gentx-$MONIKER.json') back to the coordinator."
