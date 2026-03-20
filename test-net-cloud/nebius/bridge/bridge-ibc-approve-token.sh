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

echo "=================================================="
echo "Registering & Approving IBC Token on Gonka"
echo "Binary:  $APP_NAME"
echo "Key:     $KEY_NAME"
echo "=================================================="

# Default Password
PASSWORD="12345678"
IBC_DENOM=""
SYMBOL=""
NAME=""
DECIMALS="6"
COUNTERPARTY_CHAIN="injective-888" # Default
PROPOSAL_ID_ARG=""

# Parse named arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --ibc-denom)
      IBC_DENOM="$2"
      shift 2
      ;;
    --symbol)
      SYMBOL="$2"
      shift 2
      ;;
    --name)
      NAME="$2"
      shift 2
      ;;
    --decimals)
      DECIMALS="$2"
      shift 2
      ;;
    --counterparty-chain)
      COUNTERPARTY_CHAIN="$2"
      shift 2
      ;;
    --password)
      PASSWORD="$2"
      shift 2
      ;;
    --proposal)
      PROPOSAL_ID_ARG="$2"
      shift 2
      ;;
    *)
      echo "Error: Unknown option $1"
      echo "Usage: ./bridge-approve-ibc-token.sh --ibc-denom <DENOM> --symbol <SYM> --name <NAME> [--decimals <6>] [--counterparty-chain <ID>]"
      exit 1
      ;;
  esac
done

if [ -z "$PROPOSAL_ID_ARG" ]; then
    if [ -z "$IBC_DENOM" ] || [ -z "$SYMBOL" ] || [ -z "$NAME" ]; then
        echo "Error: --ibc-denom, --symbol, and --name are required."
        exit 1
    fi
fi

get_keyring_backend "$PASSWORD" || exit 1

# If PROPOSAL_ID_ARG is not set, creating new proposal
if [ -z "$PROPOSAL_ID_ARG" ]; then
    
    # 1. Get Gov Module Address
    echo "Fetching Gov Module Account Address..."
    GOV_ACCOUNT_JSON=$($APP_NAME q auth module-account gov --output json $NODE_OPTS </dev/null)
    AUTHORITY_ADDRESS=$(echo "$GOV_ACCOUNT_JSON" | jq -r '.account.value.address // .account.base_account.address // empty')

    if [ -z "$AUTHORITY_ADDRESS" ]; then
        echo "Error: Could not fetch gov module account address."
        exit 1
    fi
    echo "Authority: $AUTHORITY_ADDRESS"

    # 2. Create Proposal JSON
    PROPOSAL_FILE="/tmp/proposal_approve_token.json"
    
    # Construction of messages array
    # 1. MsgRegisterIbcTokenMetadata
    # 2. MsgApproveIbcTokenForTrading
    
    jq -n \
      --arg authority "$AUTHORITY_ADDRESS" \
      --arg chainId "$COUNTERPARTY_CHAIN" \
      --arg ibcDenom "$IBC_DENOM" \
      --arg symbol "$SYMBOL" \
      --arg name "$NAME" \
      --argjson decimals "$DECIMALS" \
      '{
        messages: [
          {
            "@type": "/inference.inference.MsgRegisterIbcTokenMetadata",
            authority: $authority,
            chainId: $chainId,
            ibcDenom: $ibcDenom,
            name: $name,
            symbol: $symbol,
            decimals: $decimals,
            overwrite: true
          },
          {
            "@type": "/inference.inference.MsgApproveIbcTokenForTrading",
            authority: $authority,
            chainId: $chainId,
            ibcDenom: $ibcDenom
          }
        ],
        deposit: "25000000ngonka",
        title: ("Register & Approve " + $symbol),
        summary: ("Registering metadata for " + $name + " and approving for trading on " + $chainId),
        metadata: "https://github.com/gonka-ai/gonka"
      }' > "$PROPOSAL_FILE"

    # 3. Submit Proposal
    echo "Submitting Proposal..."
    RAW_SUBMIT_OUT=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME tx gov submit-proposal "$PROPOSAL_FILE" \
      --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
      --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" $NODE_OPTS 2>&1 || true)
    
    SUBMIT_OUT=$(echo "$RAW_SUBMIT_OUT" | sed -n '/{/,$p')
    TX_HASH=$(echo "$SUBMIT_OUT" | jq -r '.txhash' 2>/dev/null || echo "null")

    if [ "$TX_HASH" == "null" ] || [ -z "$TX_HASH" ]; then
        echo "Error: Submit-proposal failed."
        echo "Raw output: $RAW_SUBMIT_OUT"
        exit 1
    fi
    echo "TX Hash: $TX_HASH"

    echo "Waiting 6 seconds..."
    sleep 6

    # 4. Fetch Proposal ID
    PROPOSAL_ID=$($APP_NAME q gov proposals --output json $NODE_OPTS </dev/null | jq -r '.proposals[-1].id')
else
    PROPOSAL_ID="$PROPOSAL_ID_ARG"
fi

echo "Found Proposal ID: $PROPOSAL_ID"

if [ -z "$PROPOSAL_ID" ] || [ "$PROPOSAL_ID" == "null" ]; then
     echo "Error: Could not find proposal ID."
     exit 1
fi

# Check Status logic (same as other scripts)
STATUS=$($APP_NAME q gov proposal "$PROPOSAL_ID" --output json $NODE_OPTS </dev/null | jq -r '.status')
echo "Proposal Status: $STATUS"

if [ "$STATUS" != "PROPOSAL_STATUS_VOTING_PERIOD" ] && [ "$STATUS" != "2" ]; then
    if [ "$STATUS" == "PROPOSAL_STATUS_REJECTED" ] || [ "$STATUS" == "PROPOSAL_STATUS_FAILED" ] || [ "$STATUS" == "4" ] || [ "$STATUS" == "5" ]; then
        echo "Error: Proposal $PROPOSAL_ID is already inactive/rejected (Status: $STATUS)."
        exit 1
    fi
    echo "Wait... Proposal needs to be in voting period."
    sleep 5
fi

echo "Voting YES..."
MAX_RETRIES=5
RETRY_COUNT=0
VOTE_SUCCESS=false

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    VOTE_OUT=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME tx gov vote "$PROPOSAL_ID" yes \
      --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
      --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" $NODE_OPTS 2>&1)
    
    if echo "$VOTE_OUT" | grep -q '"code":0' || echo "$VOTE_OUT" | grep -q "txhash"; then
        echo "$VOTE_OUT"
        VOTE_SUCCESS=true
        break
    else
        echo "Vote attempt $((RETRY_COUNT+1)) failed."
        RETRY_COUNT=$((RETRY_COUNT+1))
        sleep 5
    fi
done

if [ "$VOTE_SUCCESS" = true ]; then
    echo "Vote submitted successfully!"
else
    echo "Error: Failed to vote."
    exit 1
fi

echo "Done!"
