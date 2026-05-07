#!/bin/bash
set -e

# query-gonka-epoch.sh
# Queries the current epoch and group key from Gonka mainnet.
# Outputs the values needed for the Ethereum bridge contract setup.
#
# Usage:
#   ./query-gonka-epoch.sh [--node <RPC_URL>]

NODE_URL="https://node3.gonka.ai/chain-rpc/"
APP_NAME="inferenced"

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
REPO_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Try to find binary
if [ -f "$SCRIPT_DIR/inferenced" ]; then
    APP_NAME="$SCRIPT_DIR/inferenced"
elif [ -f "$REPO_ROOT/inference-chain/build/inferenced" ]; then
    APP_NAME="$REPO_ROOT/inference-chain/build/inferenced"
fi

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --node)    NODE_URL="$2"; shift 2 ;;
    --binary)  APP_NAME="$2"; shift 2 ;;
    *)
      echo "Unknown option: $1"
      echo "Usage: $0 [--node URL] [--binary PATH]"
      exit 1
      ;;
  esac
done

echo "=================================================="
echo "Query Gonka Epoch Data"
echo "=================================================="
echo "Node: $NODE_URL"
echo ""

# Get effective epoch
EPOCH_JSON=$($APP_NAME query inference show-effective-epoch \
  --node "$NODE_URL" --output json 2>/dev/null)

EPOCH_INDEX=$(echo "$EPOCH_JSON" | jq -r '.epoch_index // empty')

if [ -z "$EPOCH_INDEX" ]; then
    echo "Error: Could not query effective epoch"
    echo "$EPOCH_JSON"
    exit 1
fi

echo "Current Epoch Index: $EPOCH_INDEX"

# Get group key for the epoch
GROUP_DATA=$($APP_NAME query inference show-epoch-group-data "$EPOCH_INDEX" \
  --node "$NODE_URL" --output json 2>/dev/null)

GROUP_KEY=$(echo "$GROUP_DATA" | jq -r '.epoch_group_data.group_key // empty')

if [ -z "$GROUP_KEY" ]; then
    echo "Warning: No group key found for epoch $EPOCH_INDEX"
    echo "The epoch may still be in DKG phase."
else
    echo "Group Key (base64): $GROUP_KEY"
fi

echo ""
echo "=================================================="
echo "Use these values with the Ethereum scripts:"
echo "=================================================="
echo ""
echo "Submit epoch:"
echo "  HARDHAT_NETWORK=mainnet node submit-epoch.js <CONTRACT> $EPOCH_INDEX $GROUP_KEY"
echo ""
echo "Then enable normal operation:"
echo "  HARDHAT_NETWORK=mainnet node enable-normal-operation.js <CONTRACT>"
