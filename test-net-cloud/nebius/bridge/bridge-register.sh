#!/bin/bash
set -e

# Resolve Base Directory (Logic matches launch.py)
BASE_DIR="${TESTNET_BASE_DIR:-/srv/dai}"

# Inferenced binary path (try local first, then system)
if [ -f "$BASE_DIR/inferenced" ]; then
    APP_NAME="$BASE_DIR/inferenced"
else
    APP_NAME="inferenced"
fi

KEY_DIR="$BASE_DIR/.inference"

CHAIN_ID="gonka-testnet"
KEY_NAME="${KEY_NAME:-gonka-account-key}"
CHAIN_NAME_ID="ethereum"

# Dynamically fetch Gov Module Account Address
# Port 26657 is closed on host, but 8000 is open (likely proxy)
# Use trailing slash to avoid 301 redirect which strips port
NODE_OPTS="--node http://localhost:8000/chain-rpc/"

echo "=================================================="
echo "Registering Bridge Contract on Gonka (Host Binary Mode)"
echo "Binary:  $APP_NAME"
echo "Key:     $KEY_NAME"
echo "Key Dir: $KEY_DIR"

# Default Password
PASSWORD="12345678"
BRIDGE_ADDRESS=""
PROPOSAL_ID_ARG=""

# Parse named arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --address)
      BRIDGE_ADDRESS="$2"
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
      echo "Usage: ssh user@host \"bash -s\" -- < script.sh --address 0xYOUR_ADDRESS [--password PASS] [--proposal ID]"
      exit 1
      ;;
  esac
done

# Validation: Address is required ONLY if we are creating a proposal (no PROPOSAL_ID provided)
if [ -z "$PROPOSAL_ID_ARG" ] && [ -z "$BRIDGE_ADDRESS" ]; then
    echo "Error: --address is required for new proposals."
    echo "Usage: ssh user@host \"bash -s\" -- < script.sh --address 0xYOUR_ADDRESS [--password PASS] [--proposal ID]"
    exit 1
fi

if [ -n "$BRIDGE_ADDRESS" ]; then
    echo "Address: $BRIDGE_ADDRESS"
fi

if [ -n "$PROPOSAL_ID_ARG" ]; then
    echo "Resuming with Proposal ID: $PROPOSAL_ID_ARG"
fi

# Function to run keys command safely (piping input to avoid reading script stdin)
run_keys_cmd() {
    local cmd_args="$@"
    # Pipe password (twice for safety/confirmation) to the command
    # This prevents the command from reading the script itself from stdin when run via 'bash -s'
    printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME keys $cmd_args
}

# 1. Verify Key Exists locally
echo "Checking for key '$KEY_NAME'..."

check_key() {
    local backend=$1
    # Quiet verification
    if printf "%s\n" "$PASSWORD" | $APP_NAME keys show "$KEY_NAME" --keyring-backend "$backend" --keyring-dir "$KEY_DIR" >/dev/null 2>&1; then
        return 0
    fi
    return 1
}

if check_key "file"; then
    KEYRING_BACKEND="file"
    echo "Found key in 'file' backend."
elif check_key "test"; then
    KEYRING_BACKEND="test"
    echo "Found key in 'test' backend."
else
    echo "Error: Key '$KEY_NAME' not found in $KEY_DIR (checked file/test backends)"
    echo "Available keys (file):"
    
    # Try to list keys (piping password in case of migration prompt)
    printf "%s\n" "$PASSWORD" | $APP_NAME keys list --keyring-backend file --keyring-dir "$KEY_DIR" || echo "Listing failed."
    
    echo "Available keys (test):"
    printf "%s\n" "$PASSWORD" | $APP_NAME keys list --keyring-backend test --keyring-dir "$KEY_DIR" || echo "Listing failed."
    exit 1
fi

# Get Key Address
MY_ADDR=$(run_keys_cmd show "$KEY_NAME" -a --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" 2>/dev/null)

if [ -z "$MY_ADDR" ]; then
    echo "Error: Could not retrieve address for key '$KEY_NAME'"
    exit 1
fi

echo "Signer Address: $MY_ADDR"

# If PROPOSAL_ID_ARG is not set, creating new proposal
if [ -z "$PROPOSAL_ID_ARG" ]; then
    # 2. Get Gov Module Address
    echo "Fetching Gov Module Account Address..."
    # Using run_keys_cmd won't work for query, need direct call but query shouldn't prompt
    # Use </dev/null to be safe
    # Removing 2>/dev/null to see errors
    GOV_ACCOUNT_JSON=$($APP_NAME q auth module-account gov --output json $NODE_OPTS </dev/null)
    AUTHORITY_ADDRESS=$(echo "$GOV_ACCOUNT_JSON" | jq -r '.account.value.address // .account.base_account.address // empty')

    if [ -z "$AUTHORITY_ADDRESS" ]; then
        echo "Error: Could not fetch gov module account address. Is the node running?"
        exit 1
    fi
    echo "Authority: $AUTHORITY_ADDRESS"

    # 3. Create Proposal JSON
    PROPOSAL_FILE="/tmp/proposal_register_bridge.json"
    USDC_ADDRESS="0x1c7D4B196Cb0C7B01d743Fbc6116a902379C7238"

    jq -n \
      --arg auth "$AUTHORITY_ADDRESS" \
      --arg chain "$CHAIN_NAME_ID" \
      --arg bridge "$BRIDGE_ADDRESS" \
      --arg usdc "$USDC_ADDRESS" \
      '{
        messages: [
          {
            "@type": "/inference.inference.MsgRegisterBridgeAddresses",
            authority: $auth,
            chainName: $chain,
            addresses: [$bridge]
          },
          {
            "@type": "/inference.inference.MsgRegisterTokenMetadata",
            authority: $auth,
            chainId: $chain,
            contractAddress: $usdc,
            name: "USD Coin (Sepolia)",
            symbol: "USDC",
            decimals: 6,
            overwrite: false
          },
          {
            "@type": "/inference.inference.MsgApproveBridgeTokenForTrading",
            authority: $auth,
            chainId: $chain,
            contractAddress: $usdc
          }
        ],
        deposit: "25000000ngonka",
        title: "Register Sepolia Bridge & USDC",
        summary: "Registering the new bridge contract deployed on Sepolia and the USDC token metadata/approval",
        metadata: "https://github.com/gonka-ai/gonka"
      }' > "$PROPOSAL_FILE"

    # 4. Submit Proposal
    echo "Submitting Proposal..."
    # Capture raw output
    RAW_SUBMIT_OUT=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME tx gov submit-proposal "$PROPOSAL_FILE" \
      --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
      --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" $NODE_OPTS 2>&1)
    
    # Try to extract JSON part if there's noise
    SUBMIT_OUT=$(echo "$RAW_SUBMIT_OUT" | sed -n '/{/,$p')
    
    TX_HASH=$(echo "$SUBMIT_OUT" | jq -r '.txhash' 2>/dev/null || echo "null")

    if [ "$TX_HASH" == "null" ] || [ -z "$TX_HASH" ]; then
        echo "Error: Submit-proposal failed or output was not valid JSON."
        echo "Raw output:"
        echo "$RAW_SUBMIT_OUT"
        exit 1
    fi
    echo "TX Hash: $TX_HASH"

    echo "Waiting 6 seconds..."
    sleep 6

    # 5. Vote
    echo "Fetching Proposal ID..."
    # Removing unsupported flags. Just getting the latest proposal by index -1.
    PROPOSAL_ID=$($APP_NAME q gov proposals --output json $NODE_OPTS </dev/null | jq -r '.proposals[-1].id')
else
    PROPOSAL_ID="$PROPOSAL_ID_ARG"
fi

echo "Found Proposal ID: $PROPOSAL_ID"

if [ -z "$PROPOSAL_ID" ] || [ "$PROPOSAL_ID" == "null" ]; then
     echo "Error: Could not find proposal ID (maybe tx failed?)"
     exit 1
fi

# Check Proposal Status
STATUS=$($APP_NAME q gov proposal "$PROPOSAL_ID" --output json $NODE_OPTS </dev/null | jq -r '.status')
echo "Proposal Status: $STATUS"

if [ "$STATUS" != "PROPOSAL_STATUS_VOTING_PERIOD" ] && [ "$STATUS" != "2" ]; then
    if [ "$STATUS" == "PROPOSAL_STATUS_REJECTED" ] || [ "$STATUS" == "PROPOSAL_STATUS_FAILED" ] || [ "$STATUS" == "4" ] || [ "$STATUS" == "5" ]; then
        echo "Error: Proposal $PROPOSAL_ID is already inactive/rejected. Please create a new one."
        exit 1
    fi
    echo "Wait... Proposal $PROPOSAL_ID is in status $STATUS. Waiting for voting period..."
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
        echo "Vote attempt $((RETRY_COUNT+1)) failed: $VOTE_OUT"
        if echo "$VOTE_OUT" | grep -q "inactive proposal"; then
            echo "Proposal not active for voting yet. Sleeping 5s..."
        else
            echo "Unknown error during voting. Retrying anyway..."
        fi
        RETRY_COUNT=$((RETRY_COUNT+1))
        sleep 5
    fi
done

if [ "$VOTE_SUCCESS" = true ]; then
    echo "Vote submitted successfully!"
else
    echo "Error: Failed to vote after $MAX_RETRIES attempts."
    exit 1
fi

echo "Done!"
