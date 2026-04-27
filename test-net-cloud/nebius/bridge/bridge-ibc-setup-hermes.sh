#!/bin/bash
set -e

# Default Settings
HERMES_VERSION="v1.10.0"
HERMES_BINARY_URL="https://github.com/informalsystems/hermes/releases/download/${HERMES_VERSION}/hermes-${HERMES_VERSION}-x86_64-unknown-linux-gnu.tar.gz"
HOME_DIR="$HOME/.hermes"
CONFIG_FILE="$HOME_DIR/config.toml"

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
FUNDING_KEY="$KEY_NAME" # Default key to fund FROM

# Colors
GREEN='\033[0;32m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${BLUE}=================================================="
echo "Setting up Hermes Relayer on Gonka Testnet Node"
echo "==================================================${NC}"

# 1. Install Hermes
if ! command -v hermes &> /dev/null; then
    echo "Hermes not found. Installing ${HERMES_VERSION}..."
    curl -L "$HERMES_BINARY_URL" -o hermes.tar.gz
    tar -xzf hermes.tar.gz
    sudo mv hermes /usr/local/bin/
    rm hermes.tar.gz
    echo -e "${GREEN}Hermes installed successfully!${NC}"
else
    echo "Hermes is already installed: $(hermes version)"
fi

# 2. Configure Hermes
mkdir -p "$HOME_DIR"

if [ ! -f "$CONFIG_FILE" ]; then
    echo "Creating config.toml..."
    cat > "$CONFIG_FILE" <<EOF
[global]
log_level = 'info'

[mode.clients]
enabled = true
refresh = true
misbehaviour = false

[mode.connections]
enabled = true

[mode.channels]
enabled = true

[mode.packets]
enabled = true
clear_interval = 100
clear_on_start = true
tx_confirmation = false

[rest]
enabled = true
host = '0.0.0.0'
port = 3000

[[chains]]
id = '$GONKA_CHAIN_ID'
rpc_addr = 'http://127.0.0.1:8000/chain-rpc/'
grpc_addr = 'http://127.0.0.1:8000/chain-grpc/'
event_source = { mode = 'push', url = 'ws://127.0.0.1:8000/chain-rpc/websocket', batch_delay = '500ms' }
compat_mode = '0.37'
rpc_timeout = '10s'
account_prefix = 'gonka'
key_name = 'gonka-relayer'
store_prefix = 'ibc'
default_gas = 100000
max_gas = 10000000
gas_price = { price = 0.025, denom = 'ngonka' }
gas_multiplier = 1.1
clock_drift = '5s'
max_block_time = '30s'
trusting_period = '14days'
trust_threshold = { numerator = '1', denominator = '3' }

[[chains]]
id = '$INJ_CHAIN_ID'
rpc_addr = 'https://testnet.sentry.tm.injective.network:443'
grpc_addr = 'https://testnet.sentry.chain.grpc.injective.network:443'
event_source = { mode = 'push', url = 'wss://testnet.sentry.tm.injective.network:443/websocket', batch_delay = '500ms' }
compat_mode = '0.37'
rpc_timeout = '10s'
account_prefix = 'inj'
key_name = 'inj-relayer'
store_prefix = 'ibc'
default_gas = 100000
max_gas = 10000000
gas_price = { price = 500000000, denom = 'inj' }
gas_multiplier = 1.2
clock_drift = '5s'
max_block_time = '30s'
trusting_period = '14days'
trust_threshold = { numerator = '1', denominator = '3' }
address_type = { derivation = 'ethermint', proto_type = { pk_type = '/injective.crypto.v1beta1.ethsecp256k1.PubKey' } }
EOF
    echo -e "${GREEN}Config created at $CONFIG_FILE${NC}"
else
    echo "Config file already exists at $CONFIG_FILE. Skipping."
fi

# 3. Import Keys
echo -e "\n${BLUE}Checking Keys...${NC}"

# Gonka Relayer Key
if hermes keys list --chain $GONKA_CHAIN_ID | grep -q "gonka-relayer"; then
    echo "Key 'gonka-relayer' exists."
else
    echo "Generating NEW Gonka Relayer Key..."
    # Generate mnemonic using inferenced
    NEW_MNEMONIC=$($APP_NAME keys mnemonic 2>/dev/null)
    echo "$NEW_MNEMONIC" > "$HOME_DIR/gonka_mnemonic.txt"
    hermes keys add --chain $GONKA_CHAIN_ID --key-name gonka-relayer --mnemonic-file "$HOME_DIR/gonka_mnemonic.txt"
    rm "$HOME_DIR/gonka_mnemonic.txt"
    echo -e "${GREEN}New Gonka Relayer Key Generated!${NC}"
fi

# Injective Relayer Key
if hermes keys list --chain $INJ_CHAIN_ID | grep -q "inj-relayer"; then
    echo "Key 'inj-relayer' exists."
else
    echo "Generating NEW Injective Relayer Key..."
    # Reuse inferenced for entropy, or generate another
    INJ_MNEMONIC=$($APP_NAME keys mnemonic 2>/dev/null)
    echo "$INJ_MNEMONIC" > "$HOME_DIR/inj_mnemonic.txt"
    # Note: Injective uses eth_secp256k1, derivation path standard for cosmos-sdk usually handles it, but hermes needs explicit path sometimes.
    # We use the standard ETH path for Injective: m/44'/60'/0'/0/0
    hermes keys add --chain $INJ_CHAIN_ID --key-name inj-relayer --mnemonic-file "$HOME_DIR/inj_mnemonic.txt" --hd-path "m/44'/60'/0'/0/0"
    rm "$HOME_DIR/inj_mnemonic.txt"
    echo -e "${GREEN}New Injective Relayer Key Generated!${NC}"
fi

# 4. Fund Gonka Relayer
# Get Relayer Address
RELAYER_ADDR_LINE=$(hermes keys list --chain $GONKA_CHAIN_ID | grep 'gonka1')
RELAYER_ADDR=$(echo "$RELAYER_ADDR_LINE" | sed -E 's/.*(gonka1[a-z0-9]+).*/\1/')
echo -e "\nGonka Relayer Address: $RELAYER_ADDR"

if [ -n "$RELAYER_ADDR" ]; then
    echo "Checking Balance..."
    BALANCE=$($APP_NAME q bank balances $RELAYER_ADDR --output json $NODE_OPTS | jq -r '.balances[] | select(.denom=="ngonka").amount // 0')
    
    if [ "$BALANCE" -lt "1000000000" ]; then
        echo "Balance is low ($BALANCE ngonka). Attempting to fund from '$FUNDING_KEY'..."
        
        # Check backend
        KEYRING_BACKEND=""
        PASS_INPUT=""
        
        if printf "%s\n" "$PASS_INPUT" | $APP_NAME keys show "$FUNDING_KEY" --keyring-backend "file" --keyring-dir "$KEY_DIR" >/dev/null 2>&1; then
            KEYRING_BACKEND="file"
        elif printf "%s\n" "$PASS_INPUT" | $APP_NAME keys show "$FUNDING_KEY" --keyring-backend "test" --keyring-dir "$KEY_DIR" >/dev/null 2>&1; then
            KEYRING_BACKEND="test"
        else
            echo "Warning: Key '$FUNDING_KEY' not found in $KEY_DIR with file or test backend. Cannot auto-fund."
            echo "Please fund '$RELAYER_ADDR' manually."
            # Don't exit here, just skip funding
            KEYRING_BACKEND="none"
        fi
        
        if [ "$KEYRING_BACKEND" != "none" ]; then
             echo "Funding 1000 GONKA to Relayer..."
             
             CMD_OUTPUT=$(printf "%s\n%s\n" "$PASS_INPUT" "$PASS_INPUT" | $APP_NAME tx bank send "$FUNDING_KEY" "$RELAYER_ADDR" 1000000000000ngonka \
               --chain-id "$GONKA_CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json $NODE_OPTS --keyring-dir "$KEY_DIR" --keyring-backend "$KEYRING_BACKEND" 2>&1)
             
             TX_HASH=$(echo "$CMD_OUTPUT" | grep -oE '\{.*\}' | jq -r '.txhash' 2>/dev/null || echo "")

             if [ -n "$TX_HASH" ] && [ "$TX_HASH" != "null" ]; then
                echo -e "${GREEN}Funding transaction sent! Hash: $TX_HASH${NC}"
             else
                echo "Funding failed. Please fund '$RELAYER_ADDR' manually."
                echo "Debug: $CMD_OUTPUT"
             fi
        fi
    else
        echo "Relayer is funded ($BALANCE ngonka)."
    fi
fi

echo -e "\n${BLUE}IMPORTANT:${NC} Ensure your Injective Relayer address is funded via the Injective Testnet Faucet!"
INJ_ADDR_LINE=$(hermes keys list --chain $INJ_CHAIN_ID | grep 'inj1')
# Extract address assuming it matches format or just print lines
# Hermes list output: "inj-relayer (inj1...)"
INJ_ADDR=$(echo "$INJ_ADDR_LINE" | grep -o 'inj1[a-z0-9]*' || echo "Unknown")
echo "Injective Address: $INJ_ADDR"

echo -e "\n${GREEN}Setup Complete!${NC}"
