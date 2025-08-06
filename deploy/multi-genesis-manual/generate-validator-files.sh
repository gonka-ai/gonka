#!/bin/sh
set -ex

# This script generates the necessary files for a new genesis validator.
# It should be run inside the 'inferenced' Docker container.

# --- Configuration ---
# The moniker for your validator node.
# This should be passed as an environment variable, e.g., -e MONIKER="my-node"
: "${MONIKER?MONIKER environment variable is not set}"

# The directory where output files will be saved.
# This should be a path inside the container, mounted from the host.
OUTPUT_DIR="/output"

# --- Chain and Application Details ---
APP_NAME="inferenced"
CHAIN_ID="gonka-testnet-8"
KEYRING_BACKEND="test"
KEY_NAME_COLD="validator-cold"
KEY_NAME_WARM="validator-warm"
STATE_DIR="/tmp/inference" # Temporary directory for initialization

# --- Script Logic ---

# 1. Initialize a temporary node directory
$APP_NAME init "$MONIKER" --chain-id "$CHAIN_ID" --home "$STATE_DIR"

# 2. Create the necessary keys
echo "Creating cold and warm keys..."
$APP_NAME keys add "$KEY_NAME_COLD" --keyring-backend "$KEYRING_BACKEND" --home "$STATE_DIR"
$APP_NAME keys add "$KEY_NAME_WARM" --keyring-backend "$KEYRING_BACKEND" --home "$STATE_DIR"

# 3. Create the genesis transaction (gentx)
echo "Creating genesis transaction (gentx)..."
$APP_NAME genesis gentx "$KEY_NAME_COLD" 1000000nicoin \
    --chain-id "$CHAIN_ID" \
    --keyring-backend "$KEYRING_BACKEND" \
    --home "$STATE_DIR"

# 4. Package the output files
echo "Packaging output files to $OUTPUT_DIR..."
mkdir -p "$OUTPUT_DIR/gentx"
cp "$STATE_DIR/config/gentx/"*.json "$OUTPUT_DIR/gentx/"
cp "$STATE_DIR/config/priv_validator_key.json" "$OUTPUT_DIR/"
$APP_NAME keys show "$KEY_NAME_COLD" -a --keyring-backend "$KEYRING_BACKEND" --home "$STATE_DIR" > "$OUTPUT_DIR/address.txt"

echo "---"
echo "Validator files generated successfully in $OUTPUT_DIR:"
ls -l "$OUTPUT_DIR"
echo "---"
echo "Next steps:"
echo "1. Securely send the contents of the 'gentx' directory and 'address.txt' to the chain coordinator."
echo "2. KEEP 'priv_validator_key.json' SAFE AND PRIVATE. You will need it to launch your validator node."
