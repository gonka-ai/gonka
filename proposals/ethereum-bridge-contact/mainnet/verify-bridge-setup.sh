#!/bin/bash
set -e

# verify-bridge-setup.sh
# Verifies both sides of the bridge are configured correctly.
#
# Usage:
#   ./verify-bridge-setup.sh --contract <ETH_ADDRESS> [--node <GONKA_RPC>] [--network mainnet]

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
PROJECT_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

CONTRACT_ADDRESS=""
NODE_URL="https://node3.gonka.ai/chain-rpc/"
NETWORK="mainnet"
APP_NAME="inferenced"

if [ -f "$SCRIPT_DIR/inferenced" ]; then
    APP_NAME="$SCRIPT_DIR/inferenced"
fi

# Parse arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --contract)  CONTRACT_ADDRESS="$2"; shift 2 ;;
    --node)      NODE_URL="$2"; shift 2 ;;
    --network)   NETWORK="$2"; shift 2 ;;
    --binary)    APP_NAME="$2"; shift 2 ;;
    *)
      echo "Unknown option: $1"
      echo "Usage: $0 --contract <ETH_ADDRESS> [--node URL] [--network mainnet|sepolia]"
      exit 1
      ;;
  esac
done

if [ -z "$CONTRACT_ADDRESS" ]; then
    echo "Error: --contract is required"
    exit 1
fi

echo "=================================================="
echo "Verify Bridge Setup"
echo "=================================================="
echo ""

# --- Ethereum side ---
echo "--- Ethereum Contract ($NETWORK) ---"
cd "$PROJECT_DIR"
HARDHAT_NETWORK="$NETWORK" node get-contract-state.js "$CONTRACT_ADDRESS" 2>&1 || true
echo ""

# --- Gonka side ---
echo "--- Gonka Chain ---"
echo ""

echo "Bridge Contract Addresses:"
$APP_NAME query inference bridge-contract-addresses \
  --node "$NODE_URL" --output json 2>/dev/null | jq '.' || echo "  (none registered yet)"
echo ""

echo "Wrapped Token Code ID:"
$APP_NAME query inference wrapped-token-code-id \
  --node "$NODE_URL" --output json 2>/dev/null | jq '.' || echo "  (not registered yet)"
echo ""

echo "Bridge Token Metadata:"
$APP_NAME query inference bridge-token-metadata \
  --node "$NODE_URL" --output json 2>/dev/null | jq '.' || echo "  (none registered yet)"
echo ""

echo "Current Epoch:"
$APP_NAME query inference show-effective-epoch \
  --node "$NODE_URL" --output json 2>/dev/null | jq '.epoch_index' || echo "  (unknown)"
echo ""

echo "=================================================="
echo "Verification complete."
echo "=================================================="
