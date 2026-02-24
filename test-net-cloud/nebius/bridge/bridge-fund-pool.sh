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

# Port 26657 is closed on host, but 8000 is open (likely proxy)
NODE_OPTS="--node http://localhost:8000/chain-rpc/"

echo "=================================================="
echo "Funding Liquidity Pool from Community Pool"
echo "Binary:  $APP_NAME"
echo "Key:     $KEY_NAME"
echo "Key Dir: $KEY_DIR"

# Defaults
PASSWORD="12345678"
# 120M GNK in base units (ngonka)
AMOUNT="120000000000000000ngonka"
PROPOSAL_ID_ARG=""

# Parse named arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --amount)
      AMOUNT="$2"
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
      echo "Usage: ssh user@host \"bash -s\" -- < script.sh [--amount AMT] [--password PASS] [--proposal ID]"
      exit 1
      ;;
  esac
done

# Function to run keys command safely
run_keys_cmd() {
    local cmd_args="$@"
    printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME keys $cmd_args
}

# 1. Verify Key Exists locally
check_key() {
    local backend=$1
    if printf "%s\n" "$PASSWORD" | $APP_NAME keys show "$KEY_NAME" --keyring-backend "$backend" --keyring-dir "$KEY_DIR" >/dev/null 2>&1; then
        return 0
    fi
    return 1
}

if check_key "file"; then
    KEYRING_BACKEND="file"
elif check_key "test"; then
    KEYRING_BACKEND="test"
else
    echo "Error: Key '$KEY_NAME' not found in $KEY_DIR"
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
    # 2. Get LP Address
    echo "Fetching Liquidity Pool address..."
    LP_ADDR=$($APP_NAME query inference liquidity-pool $NODE_OPTS --output json </dev/null | jq -r '.address' 2>/dev/null || echo "")

    if [ -z "$LP_ADDR" ] || [ "$LP_ADDR" == "null" ]; then
        echo "Error: Could not fetch liquidity pool address. Is it registered?"
        exit 1
    fi
    echo "LP Address: $LP_ADDR"

    # 3. Get Gov Module Address (Authority)
    echo "Fetching Gov Module Address..."
    GOV_ACCOUNT_JSON=$($APP_NAME q auth module-account gov --output json $NODE_OPTS </dev/null)
    AUTHORITY_ADDRESS=$(echo "$GOV_ACCOUNT_JSON" | jq -r '.account.value.address // .account.base_account.address // empty')

    if [ -z "$AUTHORITY_ADDRESS" ]; then
        echo "Error: Could not fetch gov module address."
        exit 1
    fi

    # 4. Create Proposal JSON
    PROPOSAL_FILE="/tmp/proposal_fund_pool.json"
    
    # Extract amount and denom
    VAL=$(echo "$AMOUNT" | sed 's/[^0-9]//g')
    DENOM=$(echo "$AMOUNT" | sed 's/[0-9]//g')

    jq -n \
      --arg auth "$AUTHORITY_ADDRESS" \
      --arg recipient "$LP_ADDR" \
      --arg denom "$DENOM" \
      --arg val "$VAL" \
      '{
        messages: [
          {
            "@type": "/cosmos.distribution.v1beta1.MsgCommunityPoolSpend",
            authority: $auth,
            recipient: $recipient,
            amount: [
              {
                denom: $denom,
                amount: $val
              }
            ]
          }
        ],
        deposit: "25000000ngonka",
        title: "Fund Liquidity Pool",
        summary: "Allocate tokens from the community pool to the registered Liquidity Pool contract",
        metadata: "https://github.com/gonka-ai/gonka"
      }' > "$PROPOSAL_FILE"

    # 5. Submit Proposal
    echo "Submitting Proposal..."
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

    # 6. Get Proposal ID
    PROPOSAL_ID=$($APP_NAME q gov proposals --output json $NODE_OPTS </dev/null | jq -r '.proposals[-1].id')
else
    PROPOSAL_ID="$PROPOSAL_ID_ARG"
fi

echo "Proposal ID: $PROPOSAL_ID"

if [ -z "$PROPOSAL_ID" ] || [ "$PROPOSAL_ID" == "null" ]; then
     echo "Error: Could not find proposal ID."
     exit 1
fi

# 7. Vote
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
        RETRY_COUNT=$((RETRY_COUNT+1))
        sleep 5
    fi
done

if [ "$VOTE_SUCCESS" = true ]; then
    echo "Funding proposal submitted and voted successfully!"
else
    echo "Error: Failed to vote after $MAX_RETRIES attempts."
    exit 1
fi

echo "Done!"
