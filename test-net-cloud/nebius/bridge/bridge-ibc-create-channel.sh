#!/bin/bash
set -e

# bridge-utils.sh
# Shared utilities and environment setup for Gonka testnet bridge operations.

# Resolve Base Directory (Logic matches launch.py)
export BASE_DIR="${TESTNET_BASE_DIR:-/srv/dai}"

# Inferenced binary path (try local first, then system)
if [ -f "$BASE_DIR/inferenced" ]; then
    export APP_NAME="$BASE_DIR/inferenced"
else
    export APP_NAME="inferenced"
fi

export KEY_DIR="$BASE_DIR/.inference"
export CHAIN_ID="gonka-testnet"
export KEY_NAME="${KEY_NAME:-gonka-account-key}"

# Port 26657 is closed on host; node is running in Docker, protecting its RPC endpoint behind proxy on port 8000.
export NODE_OPTS="--node http://localhost:8000/chain-rpc/"

# Function to verify key exists and determine its backend
# Usage:
#   if get_keyring_backend "12345678"; then
#       echo "Found in $KEYRING_BACKEND"
#   else
#       exit 1
#   fi
get_keyring_backend() {
    local pass=$1
    export KEYRING_BACKEND=""
    
    # Try 'file' backend first
    if printf "%s\n" "$pass" | $APP_NAME keys show "$KEY_NAME" --keyring-backend "file" --keyring-dir "$KEY_DIR" >/dev/null 2>&1; then
        export KEYRING_BACKEND="file"
        echo "Found key '$KEY_NAME' in 'file' backend."
        return 0
    fi
    
    # Try 'test' backend second
    if printf "%s\n" "$pass" | $APP_NAME keys show "$KEY_NAME" --keyring-backend "test" --keyring-dir "$KEY_DIR" >/dev/null 2>&1; then
        export KEYRING_BACKEND="test"
        echo "Found key '$KEY_NAME' in 'test' backend."
        return 0
    fi
    
    echo "Error: Key '$KEY_NAME' not found in $KEY_DIR (checked both 'file' and 'test' backends)."
    return 1
}
INJ_CHAIN_ID="injective-888"

# Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
RED='\033[0;31m'
NC='\033[0m'

# Check if Hermes is installed
if ! command -v hermes &> /dev/null; then
    echo "Hermes not found. Please run ./setup-hermes.sh first."
    exit 1
fi

echo -e "${BLUE}=================================================="
echo "Creating IBC Connection & Channel"
echo "==================================================${NC}"

# 1. Check for Existing Connection
echo "Checking for existing connections..."
CONNECTIONS=$(hermes query connections --chain $GONKA_CHAIN_ID | grep -oE "connection-[0-9]+" || echo "")

if [ -z "$CONNECTIONS" ]; then
    echo "No existing IBC connection found. Creating Connection..."
    echo -e "${GREEN}Step 1: Create Connection ($GONKA_CHAIN_ID <-> $INJ_CHAIN_ID)${NC}"
    
    # Create Connection
    hermes create connection --a-chain "$GONKA_CHAIN_ID" --b-chain "$INJ_CHAIN_ID"
    
    echo "Connection created. Re-querying ID..."
    CONNECTIONS=$(hermes query connections --chain $GONKA_CHAIN_ID | grep -oE "connection-[0-9]+" | tail -n1 || echo "")
else
    echo "Found existing connection(s):"
    echo "$CONNECTIONS"
fi

# Extract Connection ID
CONN_ID=$(echo "$CONNECTIONS" | head -n1)

if [ -z "$CONN_ID" ]; then
    echo -e "${RED}Error: Could not determine Connection ID after creation/query.${NC}"
    exit 1
fi

echo -e "${GREEN}Using Connection ID: $CONN_ID${NC}"

# 2. Check for Existing Channel
echo "Checking for existing channels on connection $CONN_ID..."
CHANNELS_QUERY=$(hermes query channels --chain $GONKA_CHAIN_ID --show-counterparty 2>/dev/null || echo "")
MY_CHANNEL=$(echo "$CHANNELS_QUERY" | grep "$CONN_ID" | tail -n1 || echo "")

if [ -z "$MY_CHANNEL" ]; then
    echo -e "\n${GREEN}Step 2: Create Channel on $CONN_ID${NC}"

    # Capture output to extract IDs
    CHANNEL_OUT=$(hermes create channel --a-chain "$GONKA_CHAIN_ID" \
      --a-connection "$CONN_ID" \
      --a-port transfer \
      --b-port transfer 2>&1)

    echo -e "\n${GREEN}Channel Creation Complete!${NC}"
else
    echo -e "\n${GREEN}Channel already exists on $CONN_ID!${NC}"
fi

# Attempt to extract Channel IDs
# Standard Hermes output format varies, but usually contains:
# "ChannelId("channel-X")" ...
# or JSON output if configured (but we didn't use --output json)

# Query channels to be sure
echo "Querying active channels..."
CHANNELS_QUERY=$(hermes query channels --chain $GONKA_CHAIN_ID --show-counterparty)

# Filter for our connection
MY_CHANNEL=$(echo "$CHANNELS_QUERY" | grep "$CONN_ID" | tail -n1)

if [ -n "$MY_CHANNEL" ]; then
    GONKA_CHANNEL=$(echo "$MY_CHANNEL" | grep -o 'channel-[0-9]*' | head -n1)
    # The output usually looks like: Channel: channel-0 (Port: transfer) <-> Counterparty: channel-X (Port: transfer)
    # So second channel-something is counterparty
    COUNTERPARTY_CHANNEL=$(echo "$MY_CHANNEL" | grep -o 'channel-[0-9]*' | sed -n '2p')
    
    echo -e "${BLUE}=================================================="
    echo "       IMPORTANT: RECORD THESE CHANNEL IDS"
    echo "==================================================${NC}"
    echo -e "Gonka Chain ($GONKA_CHAIN_ID):     ${GREEN}$GONKA_CHANNEL${NC}"
    echo -e "Injective Chain ($INJ_CHAIN_ID):   ${GREEN}$COUNTERPARTY_CHANNEL${NC}"
    echo -e "Connection ID:                     $CONN_ID"
    echo -e "${BLUE}==================================================${NC}"
else
    echo -e "${RED}Could not auto-detect new channel IDs. Please check manually:${NC}"
    echo "hermes query channels --chain $GONKA_CHAIN_ID --show-counterparty"
fi
