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
echo "Migrating Liquidity Pool on Gonka Testnet"
echo "Binary:  $APP_NAME"
echo "Key:     $KEY_NAME"
echo "=================================================="

# Default Password
PASSWORD="12345678"
NEW_CODE_ID=""
WASM_PATH=""
USE_REPO_WASM=false
PROPOSAL_ID_ARG=""
DIR="$( cd "$( dirname "${BASH_SOURCE[0]}" )" && pwd )"

# Parse named arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --code-id)
      NEW_CODE_ID="$2"
      shift 2
      ;;
    --wasm)
      WASM_PATH="$2"
      shift 2
      ;;
    --use-repo)
      USE_REPO_WASM=true
      shift
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
      echo "Usage: ./bridge-migrate-pool.sh [--code-id ID | --wasm PATH | --use-repo] [--password PASS]"
      exit 1
      ;;
  esac
done

# Function to run keys command safely (piping input)
run_keys_cmd() {
    local cmd_args="$@"
    printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME keys $cmd_args
}

get_keyring_backend "$PASSWORD" || exit 1

if [ -z "$PROPOSAL_ID_ARG" ]; then
    
    # Logic to find WASM in repo if requested
    if [ "$USE_REPO_WASM" = true ] && [ -z "$WASM_PATH" ]; then
        POTENTIAL_REPO_PATHS=("$BASE_DIR/gonka" "$DIR/.." "$DIR/../../..")
        SEARCH_PATH="inference-chain/contracts/liquidity-pool/artifacts/liquidity_pool.wasm"
        
        for rp in "${POTENTIAL_REPO_PATHS[@]}"; do
            if [ -f "$rp/$SEARCH_PATH" ]; then
                WASM_PATH="$rp/$SEARCH_PATH"
                echo "Found WASM in repo: $WASM_PATH"
                break
            fi
        done
        
        if [ -z "$WASM_PATH" ]; then
            echo "Error: Could not find $SEARCH_PATH in potential repo locations."
            exit 1
        fi
    fi

    # Upload WASM if provided
    if [ -n "$WASM_PATH" ] && [ -z "$NEW_CODE_ID" ]; then
        echo "Storing WASM contract: $WASM_PATH..."
        
        RAW_STORE_OUT=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME tx wasm store "$WASM_PATH" \
          --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
          --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" $NODE_OPTS 2>&1)
        
        STORE_TX=$(echo "$RAW_STORE_OUT" | sed -n '/{/,$p')
        TX_HASH=$(echo "$STORE_TX" | jq -r '.txhash' 2>/dev/null || echo "null")
        
        if [ "$TX_HASH" == "null" ] || [ -z "$TX_HASH" ]; then
            echo "Error: WASM store failed."
            echo "Raw output: $RAW_STORE_OUT"
            exit 1
        fi
        echo "Store TX Hash: $TX_HASH"
        
        echo "Waiting for code_id extraction..."
        NEW_CODE_ID=""
        for i in $(seq 1 15); do
            TX_QUERY=$($APP_NAME query tx "$TX_HASH" $NODE_OPTS --output json 2>/dev/null || echo "")
            JSON_QUERY=$(echo "$TX_QUERY" | sed -n '/{/,$p')
            NEW_CODE_ID=$(echo "$JSON_QUERY" | jq -r '.events[] | select(.type=="store_code") | .attributes[] | select(.key=="code_id") | .value' 2>/dev/null | head -n1)
            
            if [ -n "$NEW_CODE_ID" ] && [ "$NEW_CODE_ID" != "null" ]; then
                break
            fi
            sleep 2
        done
        
        if [ -z "$NEW_CODE_ID" ] || [ "$NEW_CODE_ID" == "null" ]; then
            echo "Error: Could not extract code_id."
            exit 1
        fi
        echo "Successfully uploaded WASM. New Code ID: $NEW_CODE_ID"
    fi

    if [ -z "$NEW_CODE_ID" ]; then
        echo "Error: Either --code-id, --wasm, or --use-repo is required."
        exit 1
    fi

    # 1. Fetch current LP contract address
    echo "Fetching current LP contract address..."
    CONTRACT_ADDR=$($APP_NAME q inference liquidity-pool --output json $NODE_OPTS | jq -r .address)

    if [ -z "$CONTRACT_ADDR" ] || [ "$CONTRACT_ADDR" == "null" ]; then
      echo "Error: No liquidity pool is currently registered in the Inference module."
      exit 1
    fi
    echo "Current LP Address: $CONTRACT_ADDR"

    # 2. Get Gov Module Address (Sender)
    ADMIN_INFO=$($APP_NAME q wasm contract "$CONTRACT_ADDR" --output json $NODE_OPTS | jq -r .contract_info.admin)
    echo "Contract Admin: $ADMIN_INFO"

    GOV_ACCOUNT_JSON=$($APP_NAME q auth module-account gov --output json $NODE_OPTS </dev/null)
    GOV_ADDR=$(echo "$GOV_ACCOUNT_JSON" | jq -r '.account.value.address // .account.base_account.address // empty')
    
    if [ "$ADMIN_INFO" != "$GOV_ADDR" ]; then
        echo "WARNING: Contract admin ($ADMIN_INFO) is NOT Governance ($GOV_ADDR)."
    fi

    # 3. Create Proposal JSON
    PROPOSAL_FILE="/tmp/proposal_migrate_pool.json"

    jq -n \
      --arg sender "$GOV_ADDR" \
      --arg contract "$CONTRACT_ADDR" \
      --arg codeId "$NEW_CODE_ID" \
      '{
        messages: [
          {
            "@type": "/cosmwasm.wasm.v1.MsgMigrateContract",
            sender: $sender,
            contract: $contract,
            code_id: $codeId,
            msg: "e30="
          }
        ],
        deposit: "25000000ngonka",
        title: ("Migrate Liquidity Pool to Code ID " + $codeId),
        summary: "Update Liquidity Pool contract code via governance migration.",
        metadata: "https://github.com/gonka-ai/gonka"
      }' > "$PROPOSAL_FILE"

    # 4. Submit Proposal
    echo "Submitting Proposal..."
    RAW_SUBMIT_OUT=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME tx gov submit-proposal "$PROPOSAL_FILE" \
      --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
      --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" $NODE_OPTS 2>&1)
    
    SUBMIT_OUT=$(echo "$RAW_SUBMIT_OUT" | sed -n '/{/,$p')
    TX_HASH=$(echo "$SUBMIT_OUT" | jq -r '.txhash' 2>/dev/null || echo "null")

    if [ "$TX_HASH" == "null" ] || [ -z "$TX_HASH" ]; then
        echo "Error: Submit-proposal failed."
        exit 1
    fi
    echo "TX Hash: $TX_HASH"
    echo "Waiting 6 seconds..."
    sleep 6

    # 5. Fetch Proposal ID
    PROPOSAL_ID=$($APP_NAME q gov proposals --output json $NODE_OPTS </dev/null | jq -r '.proposals[-1].id')
else
    PROPOSAL_ID="$PROPOSAL_ID_ARG"
fi

echo "Found Proposal ID: $PROPOSAL_ID"

if [ -z "$PROPOSAL_ID" ] || [ "$PROPOSAL_ID" == "null" ]; then
     echo "Error: Could not find proposal ID."
     exit 1
fi

# Check Status
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

# Vote
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

echo "Proposal submitted and voted. Check status in ~1 minute."
