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

# Colors
GREEN='\033[0;32m'
RED='\033[0;31m'
NC='\033[0m'

echo "=================================================="
echo "Executing Trade on Liquidity Pool"
echo "Binary:  $APP_NAME"
echo "Key:     $KEY_NAME"
echo "=================================================="

# Default Settings
PASSWORD="12345678"
AMOUNT="1000000" # Default 1 unit (6 decimals)
CW20_ADDR=""
IBC_DENOM=""
LP_ADDR=""

# Parse named arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --cw20)
      CW20_ADDR="$2"
      shift 2
      ;;
    --ibc-denom)
      IBC_DENOM="$2"
      shift 2
      ;;
    --amount)
      AMOUNT="$2"
      shift 2
      ;;
    --password)
      PASSWORD="$2"
      shift 2
      ;;
    --lp-addr)
      LP_ADDR="$2"
      shift 2
      ;;
    *)
      echo "Error: Unknown option $1"
      echo "Usage: ./bridge-pool-test-trade.sh [--cw20 <ADDR> | --ibc-denom <DENOM>] [--amount <AMT>]"
      exit 1
      ;;
  esac
done

if [ -z "$CW20_ADDR" ] && [ -z "$IBC_DENOM" ]; then
    echo "Error: You must provide either --cw20 <ADDR> for wrapped trades or --ibc-denom <DENOM> for native IBC trades."
    exit 1
fi

if [ -n "$CW20_ADDR" ] && [ -n "$IBC_DENOM" ]; then
    echo "Error: Cannot provide both --cw20 and --ibc-denom. Choose one."
    exit 1
fi

# Auto-fetch LP Address
if [ -z "$LP_ADDR" ]; then
    echo "Fetching registered Liquidity Pool address..."
    LP_ADDR=$($APP_NAME q inference liquidity-pool --output json $NODE_OPTS | jq -r .address)
    
    if [ -z "$LP_ADDR" ] || [ "$LP_ADDR" == "null" ]; then
        echo -e "${RED}Error: No liquidity pool is currently registered.${NC}"
        exit 1
    fi
fi
echo -e "Liquidity Pool Address: ${GREEN}$LP_ADDR${NC}"

if [ -n "$CW20_ADDR" ]; then
    # WRAPPED TRADE (CW20)
    # Base64 of "{}"
    HOOK_MSG="e30="
    EXEC_MSG=$(jq -n \
        --arg contract "$LP_ADDR" \
        --arg amount "$AMOUNT" \
        --arg msg "$HOOK_MSG" \
        '{"send": {"contract": $contract, "amount": $amount, "msg": $msg}}')

    echo "Executing Trade: Sending $AMOUNT of CW20 ($CW20_ADDR) to LP..."
    TARGET_CONTRACT="$CW20_ADDR"
    FUNDS_FLAG=""
else
    # NATIVE IBC TRADE
    EXEC_MSG='{"purchase_with_native": {}}'
    echo "Executing Trade: Purchasing Gonka with $AMOUNT $IBC_DENOM..."
    TARGET_CONTRACT="$LP_ADDR"
    FUNDS_FLAG="--amount ${AMOUNT}${IBC_DENOM}"
fi

# Execute Tx
RAW_TX_OUT=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME tx wasm execute "$TARGET_CONTRACT" "$EXEC_MSG" \
  $FUNDS_FLAG \
  --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
  --keyring-backend file --keyring-dir "$KEY_DIR" --home "$BASE_DIR/.inference" $NODE_OPTS 2>&1)

TX_JSON=$(echo "$RAW_TX_OUT" | sed -n '/{/,$p')
TX_HASH=$(echo "$TX_JSON" | jq -r '.txhash' 2>/dev/null || echo "null")

if [ "$TX_HASH" == "null" ] || [ -z "$TX_HASH" ]; then
    echo -e "${RED}Error: Trade execution failed.${NC}"
    echo "Raw output: $RAW_TX_OUT"
    exit 1
fi

echo "Trade Tx Hash: $TX_HASH"
echo "Waiting 6 seconds for confirmation..."
sleep 6

# Check Result
echo "Checking Tx Result..."
RESULT=$($APP_NAME q tx "$TX_HASH" --output json $NODE_OPTS)
CODE=$(echo "$RESULT" | jq -r '.code')

if [ "$CODE" == "0" ]; then
    echo -e "${GREEN}SUCCESS! Trade executed successfully.${NC}"
else
    LOG=$(echo "$RESULT" | jq -r '.raw_log')
    echo -e "${RED}FAILURE (Code: $CODE)${NC}"
    echo "Log: $LOG"
    exit 1
fi
