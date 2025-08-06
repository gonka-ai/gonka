#!/bin/sh
set -ex

# --- STAGE 1: Key Generation ---
# This script generates all necessary validator files, including the keyring.
# It should be run inside the 'inferenced' Docker container.

: "${MONIKER?MONIKER environment variable is not set}"
# The entire state will be written to the output directory.
STATE_DIR="/output"
APP_NAME="inferenced"
CHAIN_ID="gonka-testnet-8" # This is a dummy chain-id for init
KEYRING_BACKEND="file"
KEY_NAME_COLD="validator-cold"
# Password for the file-based keyring. Can be overridden.
KEYRING_PASSWORD=${KEYRING_PASSWORD:-"password"}

# 1. Initialize the node directory directly in the output volume.
# This creates config, data, and keyring-file subdirectories.
$APP_NAME init "$MONIKER" --chain-id "$CHAIN_ID" --home "$STATE_DIR" > /dev/null

# 2. Create the cold key, piping the password to make it non-interactive.
# The key will be stored in /output/keyring-file/
printf "%s\n%s\n" "$KEYRING_PASSWORD" "$KEYRING_PASSWORD" | $APP_NAME keys add "$KEY_NAME_COLD" --keyring-backend "$KEYRING_BACKEND" --home "$STATE_DIR"

# 3. Extract the public address for the coordinator.
$APP_NAME keys show "$KEY_NAME_COLD" -a --keyring-backend "$KEYRING_BACKEND" --home "$STATE_DIR" > "$STATE_DIR/address.txt"

# 4. Copy the consensus private key to the root of the output for easier access in Stage 5.
cp "$STATE_DIR/config/priv_validator_key.json" "$STATE_DIR/"


echo "---"
echo "Key generation complete. All files are in the mounted output directory."
echo "Your public address is in: $STATE_DIR/address.txt"
echo "Your private consensus key is in: $STATE_DIR/priv_validator_key.json"
echo "---"
echo "Next step: Send the 'address.txt' file to the chain coordinator."
