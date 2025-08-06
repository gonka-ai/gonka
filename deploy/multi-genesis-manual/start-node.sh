#!/bin/sh
set -e
set -x

# This script starts a node that has already been initialized as part of
# a multi-validator genesis process. It assumes 'genesis.json' and the
# validator's private key are already present in mounted volumes.

APP_NAME="inferenced"
STATE_DIR="/root/.inference"
CONFIG_DIR="$STATE_DIR/config"
APP_CONFIG_DIR="$STATE_DIR/config"
TMKMS_DIR="/root/.tmkms"

# Default values, can be overridden by environment variables
CHAIN_ID=${CHAIN_ID:-"gonka-testnet-8"}
KEYRING_BACKEND=${KEYRING_BACKEND:-"test"}
COIN_DENOM=${COIN_DENOM:-"nicoin"}
SNAPSHOT_INTERVAL=${SNAPSHOT_INTERVAL:-1000}
SNAPSHOT_KEEP_RECENT=${SNAPSHOT_KEEP_RECENT:-5}

# ==============================================================================
# 1. Verify that necessary files exist
# ==============================================================================
if [ ! -f "$CONFIG_DIR/genesis.json" ]; then
    echo "Error: genesis.json not found in $CONFIG_DIR."
    echo "Please ensure the final genesis file is mounted correctly before starting."
    exit 1
fi

if [ ! -f "$TMKMS_DIR/priv_validator_key.json" ]; then
    echo "Error: priv_validator_key.json not found in $TMKMS_DIR."
    echo "This should be the key generated during Stage 1, mounted for tmkms."
    exit 1
fi

# ==============================================================================
# 2. Set up client, app, and config files
# ==============================================================================
# Create default config files if they don't exist, so we can modify them.
if [ ! -f "$CONFIG_DIR/config.toml" ]; then
    $APP_NAME config --home="$STATE_DIR" > /dev/null
fi
if [ ! -f "$APP_CONFIG_DIR/app.toml" ]; then
    # This is a trick to generate a default app.toml without a full init
    mv "$CONFIG_DIR" "$CONFIG_DIR.tmp"
    $APP_NAME init tmp-node --home="$STATE_DIR" > /dev/null
    mv "$STATE_DIR/config/app.toml" "$APP_CONFIG_DIR/"
    rm -rf "$STATE_DIR/config" "$STATE_DIR/data"
    mv "$CONFIG_DIR.tmp" "$CONFIG_DIR"
fi

# Set client config
$APP_NAME config set client chain-id "$CHAIN_ID" --home "$STATE_DIR"
$APP_NAME config set client keyring-backend "$KEYRING_BACKEND" --home "$STATE_DIR"

# Set app config
$APP_NAME config set app minimum-gas-prices "0$COIN_DENOM" --home "$STATE_DIR"
$APP_NAME config set app state-sync.snapshot-interval "$SNAPSHOT_INTERVAL" --home "$STATE_DIR"
$APP_NAME config set app state-sync.snapshot-keep-recent "$SNAPSHOT_KEEP_RECENT" --home "$STATE_DIR"

# Set config.toml P2P settings
if [ -n "$P2P_PERSISTENT_PEERS" ]; then
    echo "Setting persistent_peers to: $P2P_PERSISTENT_PEERS"
    sed -i.bak -e "s/^persistent_peers *=.*/persistent_peers = \"$P2P_PERSISTENT_PEERS\"/" "$CONFIG_DIR/config.toml"
else
    echo "Warning: P2P_PERSISTENT_PEERS is not set. The node may not be able to connect to peers."
fi

if [ -n "$P2P_SEEDS" ]; then
    echo "Setting seeds to: $P2P_SEEDS"
    sed -i.bak -e "s/^seeds *=.*/seeds = \"$P2P_SEEDS\"/" "$CONFIG_DIR/config.toml"
fi

sed -i.bak -e 's/^laddr = "tcp:\/\/127.0.0.1:26657"/laddr = "tcp:\/\/0.0.0.0:26657"/' "$CONFIG_DIR/config.toml"

# ==============================================================================
# 3. Configure TMKMS connection
# ==============================================================================
echo "Configuring node to connect to TMKMS..."
sed -i.bak \
    -e "s|^priv_validator_laddr =.*|priv_validator_laddr = \"tcp://0.0.0.0:26658\"|" \
    -e "s|^priv_validator_key_file *=|# priv_validator_key_file =|" \
    -e "s|^priv_validator_state_file *=|# priv_validator_state_file =|" \
    "$CONFIG_DIR/config.toml"

# ==============================================================================
# 4. Initialize and start with Cosmovisor
# ==============================================================================
if [ ! -d "$STATE_DIR/cosmovisor" ]; then
  echo "Initializing cosmovisor directory"
  cosmovisor init /usr/bin/inferenced
fi

echo "Starting node with Cosmovisor..."
exec cosmovisor run start --home "$STATE_DIR"
