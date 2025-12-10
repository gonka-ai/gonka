#!/bin/sh
set -e
set -x

KEY_NAME="${KEY_NAME:-testnet-sync}"
APP_NAME="inferenced"
CHAIN_ID="${CHAIN_ID:-gonka-mainnet}"
COIN_DENOM="ngonka"
STATE_DIR="/root/.inference"
KEYRING_BACKEND="${KEYRING_BACKEND:-test}"

echo "=========================================="
echo "Joining Gonka Public Testnet"
echo "Chain ID: $CHAIN_ID"
echo "Testnet RPC: http://185.216.21.98:26657"
echo "=========================================="

# Initialize if needed
if [ ! -f "$STATE_DIR/config/config.toml" ]; then
    echo "Initializing node for testnet sync..."
    $APP_NAME init testnet-sync-node \
      --chain-id "$CHAIN_ID" \
      --default-denom $COIN_DENOM
fi

# Configure client
$APP_NAME config set client chain-id $CHAIN_ID
$APP_NAME config set client keyring-backend $KEYRING_BACKEND
$APP_NAME config set app minimum-gas-prices "0$COIN_DENOM"

# Check genesis file
echo "Checking genesis file..."
if [ -f "$STATE_DIR/config/genesis.json" ] && [ -s "$STATE_DIR/config/genesis.json" ]; then
    echo "Genesis file found and ready"
else
    echo "Error: Genesis file not found or empty"
    echo "Please download genesis externally and mount it"
    exit 1
fi

# Configure persistent peers (using pre-known testnet peers)
echo "Setting up peer connections to testnet..."
# These peers were discovered from the public testnet
PEERS="cbc1e08e2ab0e71080308a7f375eeba49922bd90@85.234.91.199:5000,2eeb52c16e8a418dfec6a2af786df986000f7860@195.26.232.165:5000,0a38b93b1baee2dc3309dea9f7b8f82091e0a001@139.60.161.29:5000,753cda22d9fa6e6d57d8edf7734d751fa74b6250@85.234.91.38:5000"

# Configure P2P settings
if [ -n "$PEERS" ]; then
    echo "Setting persistent peers: $PEERS"
    sed -i.bak "s/^persistent_peers = .*/persistent_peers = \"$PEERS\"/" $STATE_DIR/config/config.toml
else
    echo "Warning: Could not get peers, will try to connect via seed"
    # Use testnet as seed
    sed -i.bak "s/^seeds = .*/seeds = \"\"/" $STATE_DIR/config/config.toml
fi

# Configure state sync for faster initial sync (optional - will do full sync if not configured)
# State sync can be configured externally by setting TRUST_HEIGHT and TRUST_HASH env vars
if [ -n "$TRUST_HEIGHT" ] && [ -n "$TRUST_HASH" ]; then
    echo "Configuring state sync..."
    echo "  Trust height: $TRUST_HEIGHT"
    echo "  Trust hash: $TRUST_HASH"
    
    # Enable state sync in config.toml
    sed -i.bak '/\[statesync\]/,/\[/{
        s/^enable = .*/enable = true/
        s|^rpc_servers = .*|rpc_servers = "http://185.216.21.98:26657,http://185.216.21.98:26657"|
        s/^trust_height = .*/trust_height = '$TRUST_HEIGHT'/
        s/^trust_hash = .*/trust_hash = "'$TRUST_HASH'"/
    }' $STATE_DIR/config/config.toml
    echo "State sync enabled - will sync from snapshot"
else
    echo "State sync not configured - will do full sync from genesis"
    echo "This is fine but may take 1-2 hours"
fi

# Open RPC to all interfaces
sed -i.bak 's/laddr = "tcp:\/\/127.0.0.1:26657"/laddr = "tcp:\/\/0.0.0.0:26657"/' $STATE_DIR/config/config.toml
sed -i.bak 's/laddr = "tcp:\/\/localhost:26657"/laddr = "tcp:\/\/0.0.0.0:26657"/' $STATE_DIR/config/config.toml

# Enable gRPC
sed -i.bak '/\[grpc\]/,/\[/{
    s/^enable = false/enable = true/
    s/^address = "localhost:9090"/address = "0.0.0.0:9090"/
    s/^address = "127.0.0.1:9090"/address = "0.0.0.0:9090"/
}' $STATE_DIR/config/app.toml

# Enable gRPC-web
sed -i.bak '/\[grpc-web\]/,/\[/{
    s/^enable = false/enable = true/
    s/^address = "localhost:9091"/address = "0.0.0.0:9091"/
}' $STATE_DIR/config/app.toml

# Enable REST API
sed -i.bak '/\[api\]/,/\[/{
    s/^enable = false/enable = true/
    s/^address = "tcp:\/\/localhost:1317"/address = "tcp:\/\/0.0.0.0:1317"/
}' $STATE_DIR/config/app.toml

# Create key if doesn't exist
if ! $APP_NAME keys show "$KEY_NAME" --keyring-backend $KEYRING_BACKEND --keyring-dir "$STATE_DIR" >/dev/null 2>&1; then
    echo "Creating local key for testnet sync node..."
    $APP_NAME keys add "$KEY_NAME" --keyring-backend $KEYRING_BACKEND --keyring-dir "$STATE_DIR"
fi

# Show configuration
echo "=========================================="
echo "Configuration Complete!"
echo "Chain ID: $CHAIN_ID"
echo "State Dir: $STATE_DIR"
echo "Key Name: $KEY_NAME"
echo "Peers: $(echo $PEERS | cut -d',' -f1)..."
echo "=========================================="

echo "Starting node sync with public testnet..."
$APP_NAME start --home $STATE_DIR

