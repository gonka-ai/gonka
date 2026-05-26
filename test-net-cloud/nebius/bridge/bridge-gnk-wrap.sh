#!/bin/bash
set -e

# bridge-utils.sh logic
export BASE_DIR="${TESTNET_BASE_DIR:-/srv/dai}"
export KEY_DIR="$BASE_DIR/.inference"
export CHAIN_ID="gonka-testnet"
export KEY_NAME="${KEY_NAME:-gonka-account-key}"
export NODE_OPTS="--node http://localhost:8000/chain-rpc/"

# Local and key-string support
LOCAL_MODE=false
KEY_STRING=""

# Inferenced binary path
if [ -f "./inferenced" ]; then
    export APP_NAME="./inferenced"
elif [ -f "$BASE_DIR/inferenced" ]; then
    export APP_NAME="$BASE_DIR/inferenced"
else
    # Try to find it in common local build locations
    if [ -f "./inference-chain/build/inferenced" ]; then
        export APP_NAME="./inference-chain/build/inferenced"
    elif [ -f "./build/inferenced" ]; then
        export APP_NAME="./build/inferenced"
    else
        export APP_NAME="inferenced"
    fi
fi

# Clear macOS quarantine if local
if [[ "$OSTYPE" == "darwin"* ]] && [ -f "$APP_NAME" ]; then
    xattr -d com.apple.quarantine "$APP_NAME" 2>/dev/null || true
fi

get_keyring_backend() {
    local pass=$1
    export KEYRING_BACKEND=""
    if printf "%s\n" "$pass" | $APP_NAME keys show "$KEY_NAME" --keyring-backend "file" --home "$KEY_DIR" >/dev/null 2>&1; then
        export KEYRING_BACKEND="file"
        return 0
    fi
    if printf "%s\n" "$pass" | $APP_NAME keys show "$KEY_NAME" --keyring-backend "test" --home "$KEY_DIR" >/dev/null 2>&1; then
        export KEYRING_BACKEND="test"
        return 0
    fi
    return 1
}

echo "=================================================="
echo "Wrapping GNK (Gonka -> Ethereum WGNK)"
echo "=================================================="

PASSWORD="12345678"
AMOUNT=""
DESTINATION=""
BRIDGE_ADDR=""
TARGET_CHAIN="sepolia"

while [[ $# -gt 0 ]]; do
  case $1 in
    --amount) AMOUNT="$2"; shift 2 ;;
    --destination) DESTINATION="$2"; shift 2 ;;
    --bridge) BRIDGE_ADDR="$2"; shift 2 ;;
    --chain) TARGET_CHAIN="$2"; shift 2 ;;
    --password) PASSWORD="$2"; shift 2 ;;
    --key-string) KEY_STRING="$2"; shift 2 ;;
    --key-name) KEY_NAME="$2"; shift 2 ;;
    --node) NODE_OPTS="--node $2"; shift 2 ;;
    --local) LOCAL_MODE=true; shift ;;
    *) echo "Unknown option $1"; exit 1 ;;
  esac
done

if [ "$LOCAL_MODE" = true ]; then
    export KEY_DIR="${HOME}/.inference"
    # Only reset node if it wasn't explicitly set via --node
    if [ "$NODE_OPTS" = "--node http://localhost:8000/chain-rpc/" ]; then
        export NODE_OPTS=""
    fi
fi

if [ -n "$KEY_STRING" ]; then
    echo "Importing key from string..."
    # Check if it's a mnemonic (multiple words) or hex
    if [[ "$KEY_STRING" =~ [[:space:]] ]]; then
        # Mnemonic
        printf "%s\n%s\n%s\n" "$KEY_STRING" "$PASSWORD" "$PASSWORD" | $APP_NAME keys add "$KEY_NAME" --recover --keyring-backend "test" --home "$KEY_DIR"
    else
        # Hex
        $APP_NAME keys import-hex "$KEY_NAME" "$KEY_STRING" --keyring-backend "test" --home "$KEY_DIR"
    fi
    export KEYRING_BACKEND="test"
fi

if [ -z "$AMOUNT" ] || [ -z "$DESTINATION" ] || [ -z "$BRIDGE_ADDR" ]; then
    echo "Usage: ./bridge-gnk-wrap.sh --amount <AMT_NGONKA> --destination <ETH_ADDR> --bridge <BRIDGE_ADDR> [--chain <sepolia|ethereum>] [--key-string <STR>] [--local]"
    exit 1
fi

if [ -z "$KEYRING_BACKEND" ]; then
    get_keyring_backend "$PASSWORD" || exit 1
fi

echo "Requesting bridge mint (wrap GNK)..."
printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME tx inference request-bridge-mint "$AMOUNT" "$DESTINATION" "$TARGET_CHAIN" \
  --destination-bridge-address "$BRIDGE_ADDR" \
  --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
  --keyring-backend "$KEYRING_BACKEND" --home "$KEY_DIR" $NODE_OPTS
