#!/bin/sh
set -ex

# --- STAGE 1: Key Generation ---
# This script generates all necessary validator files, including the keyring,
# and saves the mnemonic for non-interactive testing.

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
$APP_NAME init "$MONIKER" --chain-id "$CHAIN_ID" --home "$STATE_DIR" > /dev/null

# Force delete any old keyring to ensure we create a new one.
# This prevents errors if the script is run multiple times on the same volume.
rm -rf "$STATE_DIR/keyring-file"

# 2. Create the cold key and capture all output (stdout and stderr).
echo "Creating key and saving mnemonic for testing..."
# 2>&1 redirects stderr to stdout, so we capture the mnemonic and prevent interactive prompts.
ALL_OUTPUT=$(printf "%s\n%s\n" "$KEYRING_PASSWORD" "$KEYRING_PASSWORD" | $APP_NAME keys add "$KEY_NAME_COLD" --keyring-backend "$KEYRING_BACKEND" --home "$STATE_DIR" 2>&1)

# 3. Extract the mnemonic from the captured output and save it.
echo "$ALL_OUTPUT" | grep 'mnemonic:' | sed 's/.*mnemonic: "//' | sed 's/"$//' > "$STATE_DIR/mnemonic.txt"

# 4. Extract the public address for the coordinator. This also needs the password.
printf "%s\n" "$KEYRING_PASSWORD" | $APP_NAME keys show "$KEY_NAME_COLD" -a --keyring-backend "$KEYRING_BACKEND" --home "$STATE_DIR" > "$STATE_DIR/address.txt"

# 5. Copy the consensus private key to the root for easier access in Stage 5.
cp "$STATE_DIR/config/priv_validator_key.json" "$STATE_DIR/"


echo "---"
echo "Key generation complete."
echo "Your mnemonic phrase for testing is in: $STATE_DIR/mnemonic.txt"
echo "Your public address is in: $STATE_DIR/address.txt"
echo "---"
echo "Next step: Send the 'address.txt' file to the chain coordinator."
