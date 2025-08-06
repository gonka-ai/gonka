#!/bin/sh
set -e
set -x

APP_NAME="inferenced"
CHAIN_ID="gonka-testnet-8"
COIN_DENOM="nicoin"
STATE_DIR="/root/.inference"
KEYRING_BACKEND="test"
CONFIG_DIR="$STATE_DIR/config"

# ==============================================================================
# 1. Initialize the node if it hasn't been already
# ==============================================================================
# The genesis.json and priv_validator_key.json are expected to be mounted.
if [ ! -f "$CONFIG_DIR/genesis.json" ]; then
    echo "Error: genesis.json not found in $CONFIG_DIR."
    echo "Please ensure it is mounted correctly before starting."
    exit 1
fi

if [ ! -f "$CONFIG_DIR/priv_validator_key.json" ]; then
    echo "Error: priv_validator_key.json not found in $CONFIG_DIR."
    echo "This should be the key generated during Phase 1."
    exit 1
fi

# We don't run `init` because we are providing all the necessary files.
# We just need to ensure the directory structure is there.
mkdir -p "$STATE_DIR/data"

# ==============================================================================
# 2. Configure config.toml with environment variables
# ==============================================================================
# Create a default config.toml if it doesn't exist
if [ ! -f "$CONFIG_DIR/config.toml" ]; then
    $APP_NAME config --home="$STATE_DIR"
fi

if [ -n "$P2P_PERSISTENT_PEERS" ]; then
    echo "Setting persistent_peers to: $P2P_PERSISTENT_PEERS"
    sed -i.bak -e "s/^persistent_peers *=.*/persistent_peers = \"$P2P_PERSISTENT_PEERS\"/" "$CONFIG_DIR/config.toml"
else
    echo "Warning: P2P_PERSISTENT_PEERS is not set. The node may not be able to connect to the network."
fi

if [ -n "$P2P_SEEDS" ]; then
    echo "Setting seeds to: $P2P_SEEDS"
    sed -i.bak -e "s/^seeds *=.*/seeds = \"$P2P_SEEDS\"/" "$CONFIG_DIR/config.toml"
fi

# ==============================================================================
# 3. Configure TMKMS
# ==============================================================================
# Copy the private key to the tmkms directory for the tmkms container to use.
# Note: The docker-compose for the validator should mount the tmkms directory.
TMKMS_DIR="/root/.tmkms"
mkdir -p "$TMKMS_DIR"
cp "$CONFIG_DIR/priv_validator_key.json" "$TMKMS_DIR/"

# Configure the validator to connect to tmkms
sed -i.bak \
    -e "s|^priv_validator_laddr =.*|priv_validator_laddr = \"tcp://0.0.0.0:26658\"|" \
    -e "s|^priv_validator_key_file *=|# priv_validator_key_file =|" \
    -e "s|^priv_validator_state_file *=|# priv_validator_state_file =|" \
    "$CONFIG_DIR/config.toml"


# ==============================================================================
# 4. Start the node
# ==============================================================================
echo "Starting validator node..."
cosmovisor run start --home "$STATE_DIR"
