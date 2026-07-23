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
export CHAIN_ID="${CHAIN_ID:-gonka-testnet}"
export KEY_NAME="${KEY_NAME:-gonka-account-key}"
export NODE_OPTS="${NODE_OPTS:---node http://localhost:8000/chain-rpc/}"

# Port 26657 is closed on host; node is running in Docker, protecting its RPC endpoint behind proxy on port 8000.

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
echo "Registering Liquidity Pool on Gonka (Host Binary Mode)"
echo "Binary:  $APP_NAME"
echo "Key:     $KEY_NAME"
echo "Key Dir: $KEY_DIR"

# Default Password
PASSWORD="${KEYRING_PASSWORD:-12345678}"
LP_WASM_CHECKSUM="439386d561ac77a9ada298357b8b17625fcfa92b5435df8b2dfd032bfb3f0600"
CODE_ID=""
WASM_PATH=""
PROPOSAL_ID_ARG=""
USE_REPO_WASM=false

# Parse named arguments
while [[ $# -gt 0 ]]; do
  case $1 in
    --code-id)
      CODE_ID="$2"
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
      shift # past argument
      shift # past value
      ;;
    --proposal)
      PROPOSAL_ID_ARG="$2"
      shift # past argument
      shift # past value
      ;;
    *)
      echo "Error: Unknown option $1"
      echo "Usage: ssh user@host \"bash -s\" -- < script.sh [--code-id ID | --wasm PATH] [--password PASS] [--proposal ID]"
      exit 1
      ;;
  esac
done

# Validation: Either CODE_ID or WASM_PATH is required ONLY if we are creating a proposal (no PROPOSAL_ID provided)
if [ -z "$PROPOSAL_ID_ARG" ] && [ -z "$CODE_ID" ] && [ -z "$WASM_PATH" ] && [ "$USE_REPO_WASM" = false ]; then
    echo "Error: Either --code-id, --wasm, or --use-repo is required."
    echo "Usage: ssh user@host \"bash -s\" -- < script.sh [--code-id ID | --wasm PATH | --use-repo] [--password PASS]"
    exit 1
fi

# Logic to find WASM in repo if requested
if [ "$USE_REPO_WASM" = true ] && [ -z "$WASM_PATH" ]; then
    # Try to find the repo root
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
        echo "Error: Could not find $SEARCH_PATH in potential repo locations: ${POTENTIAL_REPO_PATHS[*]}"
        exit 1
    fi
fi

if [ -n "$WASM_PATH" ]; then
    echo "WASM Path: $WASM_PATH"
elif [ -n "$CODE_ID" ]; then
    echo "Code ID: $CODE_ID"
fi

if [ -n "$PROPOSAL_ID_ARG" ]; then
    echo "Resuming with Proposal ID: $PROPOSAL_ID_ARG"
fi

find_existing_lp_code_id() {
    local codes_json want
    want=$(echo "$LP_WASM_CHECKSUM" | tr '[:upper:]' '[:lower:]')
    codes_json=$($APP_NAME query wasm list-code --output json $NODE_OPTS 2>/dev/null || echo "")
    if [ -z "$codes_json" ]; then
        return 1
    fi
    echo "$codes_json" | jq -r --arg sum "$want" \
        '.code_infos[] | select((.data_hash | ascii_downcase) == $sum) | .code_id' 2>/dev/null | head -n1
}

extract_code_id_from_tx_json() {
    local json="$1"
    echo "$json" | jq -r '
        [
          (.events[]? | select(.type=="store_code") | .attributes[]? | select(.key=="code_id") | .value),
          (.events[]? | select(.type=="message") | .attributes[]? | select(.key=="code_id") | .value),
          (.logs[]?.events[]? | select(.type=="store_code") | .attributes[]? | select(.key=="code_id") | .value)
        ] | map(select(. != null and . != "")) | first // empty
    ' 2>/dev/null
}

LAST_TX_RC=0
run_tx_json() {
    local out
    set +e
    out=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | "$@" 2>&1)
    LAST_TX_RC=$?
    set -e
    printf '%s\n' "$out"
}

tx_output_has_sequence_mismatch() {
    echo "$1" | grep -qi "account sequence mismatch"
}

run_tx_json_retry() {
    local max="${1:-5}"
    shift
    local attempt=0 out tx_hash
    while [ "$attempt" -lt "$max" ]; do
        out=$(run_tx_json "$@")
        tx_hash=$(echo "$out" | sed -n '/{/,$p' | jq -r '.txhash // empty' 2>/dev/null)
        if [ -n "$tx_hash" ] && [ "$tx_hash" != "null" ]; then
            printf '%s\n' "$out"
            return 0
        fi
        if tx_output_has_sequence_mismatch "$out"; then
            attempt=$((attempt + 1))
            echo "Account sequence mismatch, retrying in 3s ($attempt/$max)..."
            sleep 3
            continue
        fi
        printf '%s\n' "$out"
        return 1
    done
    echo "Error: transaction failed after $max attempts (sequence mismatch)."
    return 1
}

# Function to run keys command safely (piping input to avoid reading script stdin)
run_keys_cmd() {
    local cmd_args="$@"
    # Pipe password (twice for safety/confirmation) to the command
    # This prevents the command from reading the script itself from stdin when run via 'bash -s'
    printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME keys $cmd_args
}

# 1. Verify Key Exists locally
echo "Checking for key '$KEY_NAME'..."

get_keyring_backend "$PASSWORD" || exit 1

# Get Key Address
MY_ADDR=$(run_keys_cmd show "$KEY_NAME" -a --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" 2>/dev/null)

if [ -z "$MY_ADDR" ]; then
    echo "Error: Could not retrieve address for key '$KEY_NAME'"
    exit 1
fi

echo "Signer Address: $MY_ADDR"

# If PROPOSAL_ID_ARG is not set, creating new proposal
if [ -z "$PROPOSAL_ID_ARG" ]; then
    
    # Optional: Upload WASM if path provided
    if [ -n "$WASM_PATH" ] && [ -z "$CODE_ID" ]; then
        EXISTING_CODE_ID="$(find_existing_lp_code_id || true)"
        if [ -n "$EXISTING_CODE_ID" ] && [ "$EXISTING_CODE_ID" != "null" ]; then
            CODE_ID="$EXISTING_CODE_ID"
            echo "Liquidity pool WASM already on chain. Reusing code ID: $CODE_ID"
        fi
    fi

    if [ -n "$WASM_PATH" ] && [ -z "$CODE_ID" ]; then
        echo "Storing WASM contract: $WASM_PATH ($(wc -c < "$WASM_PATH") bytes)..."
        
        if ! RAW_STORE_OUT=$(run_tx_json_retry 5 "$APP_NAME" tx wasm store "$WASM_PATH" \
          --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
          --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" $NODE_OPTS); then
            echo "Error: WASM store transaction failed."
            echo "Raw output from command:"
            echo "$RAW_STORE_OUT"
            exit 1
        fi
        
        STORE_TX=$(echo "$RAW_STORE_OUT" | sed -n '/{/,$p')
        TX_HASH=$(echo "$STORE_TX" | jq -r '.txhash // empty' 2>/dev/null)
        
        if [ -z "$TX_HASH" ] || [ "$TX_HASH" = "null" ]; then
            echo "Error: WASM store transaction failed (no txhash)."
            echo "Raw output from command:"
            echo "$RAW_STORE_OUT"
            exit 1
        fi
        echo "Store TX Hash: $TX_HASH"
        
        echo "Waiting for code_id extraction..."
        CODE_ID=""
        for i in $(seq 1 30); do
            TX_QUERY=$($APP_NAME query tx "$TX_HASH" $NODE_OPTS --output json 2>/dev/null || echo "")
            JSON_QUERY=$(echo "$TX_QUERY" | sed -n '/{/,$p')
            CODE_ID=$(extract_code_id_from_tx_json "$JSON_QUERY")
            if [ -n "$CODE_ID" ] && [ "$CODE_ID" != "null" ]; then
                break
            fi
            sleep 2
        done
        
        if [ -z "$CODE_ID" ] || [ "$CODE_ID" == "null" ]; then
            echo "Error: Could not extract code_id from transaction $TX_HASH"
            echo "Query the tx manually:"
            echo "  $APP_NAME query tx $TX_HASH $NODE_OPTS -o json | jq ."
            echo "Last TX query output:"
            echo "$TX_QUERY"
            exit 1
        fi
        echo "Successfully uploaded WASM. Code ID: $CODE_ID"
    fi

    # 2. Get Gov Module Address
    echo "Fetching Gov Module Account Address..."
    GOV_ACCOUNT_JSON=$($APP_NAME q auth module-account gov --output json $NODE_OPTS </dev/null)
    AUTHORITY_ADDRESS=$(echo "$GOV_ACCOUNT_JSON" | jq -r '.account.value.address // .account.base_account.address // empty')

    if [ -z "$AUTHORITY_ADDRESS" ]; then
        echo "Error: Could not fetch gov module account address. Is the node running?"
        exit 1
    fi
    echo "Authority: $AUTHORITY_ADDRESS"

    # 3. Create Proposal JSON
    PROPOSAL_FILE="/tmp/proposal_register_pool.json"
    
    # Prepare Instantiate Message (admin must be Authority for governance-owned pool)
    # Minified JSON object
    LP_MSG=$(jq -n -c --arg admin "$AUTHORITY_ADDRESS" '{admin:$admin, daily_limit_bp:"1000", total_supply:"120000000000000000", native_denom:"ngonka"}')

    # Build the proposal JSON safely. Note: codeId and instantiateMsg are STRINGS in this proto.
    jq -n \
      --arg authority "$AUTHORITY_ADDRESS" \
      --arg codeId "$CODE_ID" \
      --arg msg "$LP_MSG" \
      '{
        messages: [
          {
            "@type": "/inference.inference.MsgRegisterLiquidityPool",
            authority: $authority,
            code_id: $codeId,
            label: "liquidity-pool",
            instantiate_msg: $msg
          }
        ],
        deposit: "25000000ngonka",
        title: "Register Liquidity Pool",
        summary: "Instantiate and register the Liquidity Pool contract owned by Governance",
        metadata: "https://github.com/gonka-ai/gonka"
      }' > "$PROPOSAL_FILE"

    # 4. Submit Proposal
    echo "Submitting Proposal..."
    # Capture raw output
    if ! RAW_SUBMIT_OUT=$(run_tx_json_retry 5 "$APP_NAME" tx gov submit-proposal "$PROPOSAL_FILE" \
      --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
      --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" $NODE_OPTS); then
        echo "Error: Submit-proposal failed."
        echo "Raw output:"
        echo "$RAW_SUBMIT_OUT"
        exit 1
    fi
    
    SUBMIT_OUT=$(echo "$RAW_SUBMIT_OUT" | sed -n '/{/,$p')
    TX_HASH=$(echo "$SUBMIT_OUT" | jq -r '.txhash // empty' 2>/dev/null)

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
    # Select last proposal ID
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
        echo "Error: Proposal $PROPOSAL_ID is already inactive/rejected (Status: $STATUS). Please create a new one."
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
    if VOTE_OUT=$(run_tx_json_retry 3 "$APP_NAME" tx gov vote "$PROPOSAL_ID" yes \
      --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
      --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" $NODE_OPTS); then
        echo "$VOTE_OUT"
        VOTE_SUCCESS=true
        break
    else
        echo "Vote attempt $((RETRY_COUNT+1)) failed: $VOTE_OUT"
        if echo "$VOTE_OUT" | grep -q "inactive proposal"; then
            echo "Proposal not active for voting yet. Sleeping 5s..."
        else
            echo "Social/Network delay? Retrying in 5s..."
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
