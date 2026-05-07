#!/bin/bash
set -e

# upload-wrapped-token-wasm.sh
# Uploads the CW20 wrapped token contract to Gonka mainnet and prints the code_id.
#
# Usage:
#   ./upload-wrapped-token-wasm.sh --key <KEY_NAME> [--home <KEYRING_DIR>] [--node <RPC_URL>]
#   ./upload-wrapped-token-wasm.sh --key mykey --home ~/.inference
#
# The script auto-resolves the wasm artifact from the repo.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Defaults
CHAIN_ID="gonka-mainnet"
NODE_URL="https://node3.gonka.ai/chain-rpc/"
HOME_DIR="$HOME/.inference"
KEY_NAME=""
KEYRING_BACKEND="file"
WASM_PATH="$REPO_ROOT/inference-chain/contracts/wrapped-token/artifacts/wrapped_token.wasm"
APP_NAME="inferenced"

# Try to find binary in repo
if [ -f "$SCRIPT_DIR/inferenced" ]; then
    APP_NAME="$SCRIPT_DIR/inferenced"
elif [ -f "$REPO_ROOT/inference-chain/build/inferenced" ]; then
    APP_NAME="$REPO_ROOT/inference-chain/build/inferenced"
fi

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --key)       KEY_NAME="$2"; shift 2 ;;
    --home)      HOME_DIR="$2"; shift 2 ;;
    --node)      NODE_URL="$2"; shift 2 ;;
    --chain-id)  CHAIN_ID="$2"; shift 2 ;;
    --backend)   KEYRING_BACKEND="$2"; shift 2 ;;
    --wasm)      WASM_PATH="$2"; shift 2 ;;
    --binary)    APP_NAME="$2"; shift 2 ;;
    *)
      echo "Unknown option: $1"
      echo "Usage: $0 --key <KEY_NAME> [--home DIR] [--node URL] [--chain-id ID] [--backend file|test] [--wasm PATH] [--binary PATH]"
      exit 1
      ;;
  esac
done

if [ -z "$KEY_NAME" ]; then
    echo "Error: --key is required"
    echo "Usage: $0 --key <KEY_NAME> [--home DIR] [--node URL]"
    exit 1
fi

if [ ! -f "$WASM_PATH" ]; then
    echo "Error: Wasm artifact not found at: $WASM_PATH"
    echo "Build it first or pass --wasm <path>"
    exit 1
fi

echo "=================================================="
echo "Upload Wrapped Token CW20 Wasm to Gonka"
echo "=================================================="
echo "Binary:    $APP_NAME"
echo "Wasm:      $WASM_PATH"
echo "Key:       $KEY_NAME"
echo "Home:      $HOME_DIR"
echo "Chain ID:  $CHAIN_ID"
echo "Node:      $NODE_URL"
echo "Backend:   $KEYRING_BACKEND"
echo ""

WASM_SIZE=$(wc -c < "$WASM_PATH" | tr -d ' ')
echo "Wasm size: $WASM_SIZE bytes"
echo ""

# Upload
echo "Submitting wasm store transaction..."
RAW_OUT=$($APP_NAME tx wasm store "$WASM_PATH" \
  --from "$KEY_NAME" \
  --chain-id "$CHAIN_ID" \
  --node "$NODE_URL" \
  --home "$HOME_DIR" \
  --keyring-backend "$KEYRING_BACKEND" \
  --gas auto --gas-adjustment 1.5 \
  --yes --output json 2>&1)

# Extract JSON (skip warnings/logs)
TX_JSON=$(echo "$RAW_OUT" | sed -n '/{/,$p')
TX_HASH=$(echo "$TX_JSON" | jq -r '.txhash' 2>/dev/null || echo "")

if [ -z "$TX_HASH" ] || [ "$TX_HASH" = "null" ]; then
    echo "Error: Transaction failed."
    echo "$RAW_OUT"
    exit 1
fi

echo "TX Hash: $TX_HASH"
echo ""

# Wait for code_id
echo "Waiting for confirmation..."
CODE_ID=""
for i in $(seq 1 20); do
    sleep 3
    TX_QUERY=$($APP_NAME query tx "$TX_HASH" --node "$NODE_URL" --output json 2>/dev/null || echo "")
    JSON_QUERY=$(echo "$TX_QUERY" | sed -n '/{/,$p')

    CODE_ID=$(echo "$JSON_QUERY" | jq -r '.events[] | select(.type=="store_code") | .attributes[] | select(.key=="code_id") | .value' 2>/dev/null | head -n1)

    if [ -n "$CODE_ID" ] && [ "$CODE_ID" != "null" ]; then
        break
    fi
    echo "  attempt $i/20..."
done

if [ -z "$CODE_ID" ] || [ "$CODE_ID" = "null" ]; then
    echo "Error: Could not extract code_id from transaction $TX_HASH"
    echo "Check manually: $APP_NAME query tx $TX_HASH --node $NODE_URL --output json"
    exit 1
fi

echo ""
echo "=================================================="
echo "SUCCESS"
echo "=================================================="
echo "Code ID: $CODE_ID"
echo ""
echo "Use this in the upgrade proposal Plan.Info:"
echo "  {\"ethereum_bridge_address\":\"0x...\",\"wrapped_token_code_id\":$CODE_ID}"
