#!/bin/sh
set -ex

# --- STAGE 1: Key Generation ---
# This script generates a new cold key and outputs the public address.
# It should be run inside the 'inferenced' Docker container.

: "${MONIKER?MONIKER environment variable is not set}"
OUTPUT_DIR="/output"
STATE_DIR="/tmp/inference"
APP_NAME="inferenced"
KEYRING_BACKEND="test"
KEY_NAME_COLD="validator-cold"

# 1. Initialize a temporary node directory to get a clean keyring
$APP_NAME init "$MONIKER" --chain-id "unused" --home "$STATE_DIR" > /dev/null

# 2. Create the cold key
$APP_NAME keys add "$KEY_NAME_COLD" --keyring-backend "$KEYRING_BACKEND" --home "$STATE_DIR"

# 3. Export the public address to the output directory
$APP_NAME keys show "$KEY_NAME_COLD" -a --keyring-backend "$KEYRING_BACKEND" --home "$STATE_DIR" > "$OUTPUT_DIR/address.txt"

# 4. VERY IMPORTANT: Export the private key for tmkms
# This file must be kept secret and will be used to launch the validator later.
cp "$STATE_DIR/config/priv_validator_key.json" "$OUTPUT_DIR/"

echo "---"
echo "Key generation complete."
echo "Your public address is in: $OUTPUT_DIR/address.txt"
echo "Your private consensus key is in: $OUTPUT_DIR/priv_validator_key.json"
echo "---"
echo "Next step: Send the 'address.txt' file to the chain coordinator."
