#!/bin/sh
set -ex

# --- STAGE 4: Assemble Final Genesis (Coordinator Only) ---
# This script collects all gentx files and injects them into the
# intermediate genesis file, producing the final launch-ready genesis.

STATE_DIR="/root/.inference"
APP_NAME="inferenced"
GENTX_DIR_COLLECTED="/root/gentx_collected"
INTERMEDIATE_GENESIS_DIR="/root/intermediate_genesis"
FINAL_GENESIS_DIR="/root/final_genesis"

# 1. Place the intermediate genesis file in the config directory
cp "$INTERMEDIATE_GENESIS_DIR/genesis-intermediate.json" "$STATE_DIR/config/genesis.json"

# 2. Copy all collected gentx files into the config/gentx directory
GENTX_TARGET_DIR="$STATE_DIR/config/gentx"
mkdir -p "$GENTX_TARGET_DIR"

if [ -z "$(ls -A $GENTX_DIR_COLLECTED)" ]; then
    echo "Error: The $GENTX_DIR_COLLECTED directory is empty."
    exit 1
fi

cp "$GENTX_DIR_COLLECTED"/*.json "$GENTX_TARGET_DIR/"
echo "Copied all gentx files to $GENTX_TARGET_DIR"

# 3. Collect the gentxs to produce the final genesis file
echo "Collecting gentxs..."
$APP_NAME genesis collect-gentxs --home "$STATE_DIR"

# 4. Copy the final genesis file to the output directory for distribution
mkdir -p "$FINAL_GENESIS_DIR"
cp "$STATE_DIR/config/genesis.json" "$FINAL_GENESIS_DIR/genesis-final.json"

echo "---"
echo "Final genesis file created: $FINAL_GENESIS_DIR/genesis-final.json"
echo "---"
echo "Next step: Distribute this 'genesis-final.json' file to ALL genesis validators for launch."
echo "The coordinator can now start their node using this final genesis."
