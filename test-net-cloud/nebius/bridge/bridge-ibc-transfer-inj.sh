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


# Example Usage: ./bridge-transfer-from-injective.sh --channel channel-334 --amount 1000000 --denom inj

# Parse named arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --channel)
      INJ_CHANNEL="$2"
      shift 2
      ;;
    --amount)
      AMOUNT="$2"
      shift 2
      ;;
    --denom)
      DENOM="$2"
      shift 2
      ;;
    --receiver)
      RECEIVER="$2"
      shift 2
      ;;
    *)
      echo "Error: Unknown option $1"
      echo "Usage: ./bridge-transfer-from-injective.sh --channel <INJ_CHANNEL> [--amount <AMT>] [--denom <DENOM>] [--receiver <ADDR>]"
      exit 1
      ;;
  esac
done

if [ -z "$INJ_CHANNEL" ]; then
    echo "Error: --channel (Injective-side Channel ID) is required."
    echo "This is the channel ID on 'injective-888' that connects to 'gonka-testnet'."
    echo "If you ran create-channel.sh, it was displayed as 'Injective Chain Channel ID'."
    exit 1
fi

# Determine Receiver
if [ -z "$RECEIVER" ]; then
    echo "No --receiver specified. Fetching address for 'gonka-account-key'..."
    RECEIVER=$($APP_NAME keys show gonka-account-key -a --keyring-backend "$(get_keyring_backend "" || exit 1)" --keyring-dir "$KEY_DIR" 2>/dev/null || true)
    
    if [ -z "$RECEIVER" ]; then
        echo "Error: Could not find 'gonka-account-key' and no --receiver provided."
        exit 1
    fi
    echo "Using default receiver: $RECEIVER"
fi

echo "=================================================="
echo "Transferring Tokens from Injective -> Gonka"
echo "Src Chain:   $INJ_CHAIN_ID"
echo "Src Channel: $INJ_CHANNEL"
echo "Dst Chain:   $GONKA_CHAIN_ID"
echo "Amount:      $AMOUNT $DENOM"
echo "Receiver:    $RECEIVER"
echo "=================================================="

# Check Hermes
if ! command -v hermes &> /dev/null; then
    echo "Hermes not found. Please run ./setup-hermes.sh first."
    exit 1
fi

# Execute Transfer
# hermes tx ft-transfer --dst-chain <DST> --src-chain <SRC> --src-port transfer --src-channel <SRC_CHAN> --amount <AMT> --denom <DENOM> --receiver <RECV>
echo "Executing Hermes FT Transfer..."

hermes tx ft-transfer \
  --dst-chain "$GONKA_CHAIN_ID" \
  --src-chain "$INJ_CHAIN_ID" \
  --src-port transfer \
  --src-channel "$INJ_CHANNEL" \
  --amount "$AMOUNT" \
  --denom "$DENOM" \
  --receiver "$RECEIVER" \
  --timeout-height-offset 1000

echo ""
echo "=================================================="
echo "Transfer Initiated!"
echo "If successful, the tokens will arrive on Gonka shortly."
echo "To find the resulting IBC Denom (ibc/HASH):"
echo "  inferenced q bank balances <GONKA_ADDR>"
echo "  (Look for the 'ibc/...' denom in the list)"
echo "=================================================="
