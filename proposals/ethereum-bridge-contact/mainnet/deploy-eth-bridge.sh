#!/bin/bash
set -e

# deploy-eth-bridge.sh
# Deploys the BridgeContract to Ethereum mainnet, sets the genesis epoch,
# and enables normal operation — all in one go.
#
# Usage:
#   ./deploy-eth-bridge.sh [--epoch <INDEX>] [--group-key <BASE64>] [--network mainnet|sepolia]
#
# If --epoch and --group-key are not provided, queries Gonka mainnet automatically.
#
# Requires:
#   - .env with MAINNET_RPC_URL + PRIVATE_KEY (or LEDGER_ADDRESS)
#   - npm install already done in proposals/ethereum-bridge-contact/

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

NETWORK="mainnet"
EPOCH_INDEX=""
GROUP_KEY=""
SKIP_DEPLOY=false
CONTRACT_ADDRESS=""

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --epoch)          EPOCH_INDEX="$2"; shift 2 ;;
    --group-key)      GROUP_KEY="$2"; shift 2 ;;
    --network)        NETWORK="$2"; shift 2 ;;
    --contract)       CONTRACT_ADDRESS="$2"; SKIP_DEPLOY=true; shift 2 ;;
    *)
      echo "Unknown option: $1"
      echo "Usage: $0 [--epoch INDEX] [--group-key BASE64] [--network mainnet|sepolia] [--contract ADDRESS]"
      exit 1
      ;;
  esac
done

echo "=================================================="
echo "Ethereum Bridge Contract Setup"
echo "=================================================="
echo "Network:  $NETWORK"
echo "Project:  $PROJECT_DIR"
echo ""

# Auto-query Gonka epoch if not provided
if [ -z "$EPOCH_INDEX" ] || [ -z "$GROUP_KEY" ]; then
    echo "Querying current Gonka epoch..."
    
    if [ -f "$SCRIPT_DIR/query-gonka-epoch.sh" ]; then
        # Reuse our query script's logic inline
        APP_NAME="inferenced"
        NODE_URL="https://node3.gonka.ai/chain-rpc/"
        
        if [ -f "$SCRIPT_DIR/inferenced" ]; then
            APP_NAME="$SCRIPT_DIR/inferenced"
        fi
        
        EPOCH_JSON=$($APP_NAME query inference show-effective-epoch \
          --node "$NODE_URL" --output json 2>/dev/null || echo "{}")
        
        EPOCH_INDEX=$(echo "$EPOCH_JSON" | jq -r '.epoch_index // empty')
        
        if [ -z "$EPOCH_INDEX" ]; then
            echo "Error: Could not query Gonka epoch. Provide --epoch and --group-key manually."
            exit 1
        fi
        
        GROUP_DATA=$($APP_NAME query inference show-epoch-group-data "$EPOCH_INDEX" \
          --node "$NODE_URL" --output json 2>/dev/null || echo "{}")
        
        GROUP_KEY=$(echo "$GROUP_DATA" | jq -r '.epoch_group_data.group_key // empty')
        
        if [ -z "$GROUP_KEY" ]; then
            echo "Error: No group key for epoch $EPOCH_INDEX. Provide --group-key manually."
            exit 1
        fi
    else
        echo "Error: Could not auto-query. Provide --epoch and --group-key."
        exit 1
    fi
fi

echo "Epoch Index: $EPOCH_INDEX"
echo "Group Key:   ${GROUP_KEY:0:20}..."
echo ""

cd "$PROJECT_DIR"

# Step 1: Deploy (unless --contract was provided)
if [ "$SKIP_DEPLOY" = false ]; then
    echo "=== Step 1/3: Deploy BridgeContract ==="
    DEPLOY_OUT=$(HARDHAT_NETWORK="$NETWORK" node deploy.js 2>&1)
    echo "$DEPLOY_OUT"
    
    # Extract contract address from output
    CONTRACT_ADDRESS=$(echo "$DEPLOY_OUT" | grep -o '0x[0-9a-fA-F]\{40\}' | tail -1)
    
    if [ -z "$CONTRACT_ADDRESS" ]; then
        echo "Error: Could not extract contract address from deploy output."
        exit 1
    fi
    echo ""
    echo "Contract deployed: $CONTRACT_ADDRESS"
else
    echo "=== Skipping deploy, using existing contract: $CONTRACT_ADDRESS ==="
fi

echo ""

# Step 2: Submit genesis epoch
echo "=== Step 2/3: Submit Genesis Epoch ==="
HARDHAT_NETWORK="$NETWORK" node submit-epoch.js \
  "$CONTRACT_ADDRESS" \
  "$EPOCH_INDEX" \
  "$GROUP_KEY"

echo ""

# Step 3: Enable normal operation
echo "=== Step 3/3: Enable Normal Operation ==="
HARDHAT_NETWORK="$NETWORK" node enable-normal-operation.js \
  "$CONTRACT_ADDRESS"

echo ""
echo "=================================================="
echo "COMPLETE"
echo "=================================================="
echo ""
echo "Contract Address: $CONTRACT_ADDRESS"
echo "Genesis Epoch:    $EPOCH_INDEX"
echo "State:            NORMAL_OPERATION"
echo ""
echo "Use this in the upgrade proposal Plan.Info:"
echo "  {\"ethereum_bridge_address\":\"$CONTRACT_ADDRESS\",\"wrapped_token_code_id\":<CODE_ID>}"
echo ""
echo "To transfer ownership to a multisig:"
echo "  HARDHAT_NETWORK=$NETWORK node transfer-ownership.js $CONTRACT_ADDRESS <MULTISIG_ADDRESS>"
