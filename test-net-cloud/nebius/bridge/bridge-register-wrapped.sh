#!/bin/bash
set -e

# bridge-utils.sh
# Shared utilities and environment setup for Gonka testnet bridge operations.
# NOTE: You MUST use COLD KEYS (the validator operator keys) for registration,
# as they are the ones that hold the funds required for governance deposits.

# Resolve Base Directory (Logic matches launch.py)
export BASE_DIR="${TESTNET_BASE_DIR:-/srv/dai}"
export KEY_DIR="$BASE_DIR/.inference"
export CHAIN_ID="${CHAIN_ID:-gonka-testnet}"
export KEY_NAME="${KEY_NAME:-gonka-account-key}"
export NODE_OPTS="${NODE_OPTS:---node http://localhost:8000/chain-rpc/}"

# Local and key-string support
LOCAL_MODE=false
KEY_STRING=""

# Inferenced binary path (try local first, then system)
if [ -f "$BASE_DIR/inferenced" ]; then
    export APP_NAME="$BASE_DIR/inferenced"
else
    # Try to find it in common local build locations
    if [ -f "./inference-chain/build/inferenced" ]; then
        export APP_NAME="./inference-chain/build/inferenced"
    elif [ -f "./build/inferenced" ]; then
        export APP_NAME="./build/inferenced"
    else
        export APP_NAME="inferenced"
    fi
fi

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
echo "Registering Wrapped Token CW20 on Gonka (Host Binary Mode)"
echo "Binary:  $APP_NAME"
echo "Key:     $KEY_NAME"
echo "Key Dir: $KEY_DIR"

# Default Password
PASSWORD="${KEYRING_PASSWORD:-12345678}"
WRAPPED_TOKEN_CHECKSUM="d9598eb08e9379b81dea6474a12566671338561b5394ec9d5c019a8995d02e9d"
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
    --password) PASSWORD="$2"; shift 2 ;;
    --proposal) PROPOSAL_ID_ARG="$2"; shift 2 ;;
    --key-string) KEY_STRING="$2"; shift 2 ;;
    --local) LOCAL_MODE=true; shift ;;
    *)
      echo "Error: Unknown option $1"
      echo "Usage: ssh user@host \"bash -s\" -- < script.sh [--code-id ID | --wasm PATH] [--password PASS] [--proposal ID] [--key-string STR] [--local]"
      exit 1
      ;;
  esac
done

if [ "$LOCAL_MODE" = true ]; then
    export KEY_DIR="${HOME}/.inference"
    export NODE_OPTS=""
fi

if [ -n "$KEY_STRING" ]; then
    echo "Importing key from string..."
    printf "%s\n%s\n%s\n" "$KEY_STRING" "$PASSWORD" "$PASSWORD" | $APP_NAME keys add "$KEY_NAME" --recover --keyring-backend "test" --home "$KEY_DIR"
    export KEYRING_BACKEND="test"
fi

# Validation
if [ -z "$PROPOSAL_ID_ARG" ] && [ -z "$CODE_ID" ] && [ -z "$WASM_PATH" ] && [ "$USE_REPO_WASM" = false ]; then
    echo "Error: Either --code-id, --wasm, or --use-repo is required."
    echo "Usage: ssh user@host \"bash -s\" -- < script.sh [--code-id ID | --wasm PATH | --use-repo] [--password PASS]"
    exit 1
fi

# Logic to find WASM in repo if requested
if [ "$USE_REPO_WASM" = true ] && [ -z "$WASM_PATH" ]; then
    # Try to find the repo root
    # Standard locations: $BASE_DIR/gonka or current directory's parent (if script run from within repo)
    POTENTIAL_REPO_PATHS=("$BASE_DIR/gonka" "$DIR/.." "$DIR/../../..")
    SEARCH_PATH="inference-chain/contracts/wrapped-token/artifacts/wrapped_token.wasm"
    
    for rp in "${POTENTIAL_REPO_PATHS[@]}"; do
        if [ -f "$rp/$SEARCH_PATH" ]; then
            WASM_PATH="$rp/$SEARCH_PATH"
            echo "Found WASM in repo: $WASM_PATH"
            break
        fi
    done
    
    if [ -z "$WASM_PATH" ]; then
        echo "Error: Could not find $SEARCH_PATH in potential repo locations: ${POTENTIAL_REPO_PATHS[*]}"
        echo "Build it with: cd inference-chain/contracts/wrapped-token && ./build.sh"
        exit 1
    fi
fi

if [ -n "$WASM_PATH" ] && [ ! -s "$WASM_PATH" ]; then
    echo "Error: WASM file is missing or empty: $WASM_PATH"
    echo "Build it with: cd inference-chain/contracts/wrapped-token && ./build.sh"
    exit 1
fi

find_existing_wrapped_code_id() {
    local codes_json want
    want=$(echo "$WRAPPED_TOKEN_CHECKSUM" | tr '[:upper:]' '[:lower:]')
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

# Function to run keys command safely
run_keys_cmd() {
    local cmd_args="$@"
    printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME keys $cmd_args
}

get_keyring_backend "$PASSWORD" || exit 1

# Get Key Address
MY_ADDR=$(run_keys_cmd show "$KEY_NAME" -a --keyring-backend "$KEYRING_BACKEND" --home "$KEY_DIR" 2>/dev/null)

if [ -z "$MY_ADDR" ]; then
    echo "Error: Could not retrieve address for key '$KEY_NAME'"
    exit 1
fi

echo "Signer Address: $MY_ADDR"

# If PROPOSAL_ID_ARG is not set, creating new proposal
if [ -z "$PROPOSAL_ID_ARG" ]; then
    
    # Optional: Upload WASM if path provided
    if [ -n "$WASM_PATH" ] && [ -z "$CODE_ID" ]; then
        EXISTING_CODE_ID="$(find_existing_wrapped_code_id || true)"
        if [ -n "$EXISTING_CODE_ID" ] && [ "$EXISTING_CODE_ID" != "null" ]; then
            CODE_ID="$EXISTING_CODE_ID"
            echo "Wrapped token WASM already on chain. Reusing code ID: $CODE_ID"
        fi
    fi

    if [ -n "$WASM_PATH" ] && [ -z "$CODE_ID" ]; then
        echo "Storing WASM contract: $WASM_PATH ($(wc -c < "$WASM_PATH") bytes)..."
        
        if ! RAW_STORE_OUT=$(run_tx_json_retry 5 "$APP_NAME" tx wasm store "$WASM_PATH" \
          --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
          --keyring-backend "$KEYRING_BACKEND" --home "$KEY_DIR" $NODE_OPTS); then
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
        
        if [ -z "$CODE_ID" ] || [ "$CODE_ID" = "null" ]; then
            echo "Error: Could not extract code_id from transaction $TX_HASH"
            echo "Query the tx manually:"
            echo "  $APP_NAME query tx $TX_HASH $NODE_OPTS -o json | jq ."
            echo "Last TX query output:"
            echo "$TX_QUERY"
            exit 1
        fi
        echo "Successfully uploaded WASM. Code ID: $CODE_ID"
    fi

    # 2. Get Gov Module Address (Authority)
    echo "Fetching Gov Module Account Address..."
    GOV_ACCOUNT_JSON=$($APP_NAME q auth module-account gov --output json $NODE_OPTS </dev/null)
    AUTHORITY_ADDRESS=$(echo "$GOV_ACCOUNT_JSON" | jq -r '.account.value.address // .account.base_account.address // empty')

    if [ -z "$AUTHORITY_ADDRESS" ]; then
        echo "Error: Could not fetch gov module account address."
        exit 1
    fi

    # 3. Create Proposal JSON
    PROPOSAL_FILE="/tmp/proposal_register_wrapped.json"
    
    # Use jq to build the proposal JSON safely
    jq -n \
      --arg authority "$AUTHORITY_ADDRESS" \
      --argjson codeId "$CODE_ID" \
      '{
        messages: [
          {
            "@type": "/inference.inference.MsgRegisterWrappedTokenContract",
            authority: $authority,
            codeId: $codeId
          }
        ],
        deposit: "25000000ngonka",
        title: "Register Wrapped Token Contract",
        summary: "Register the code ID for future wrapped token instantiations",
        metadata: "https://github.com/gonka-ai/gonka"
      }' > "$PROPOSAL_FILE"

    # 4. Submit Proposal
    echo "Submitting Proposal..."
    if ! SUBMIT_OUT=$(run_tx_json_retry 5 "$APP_NAME" tx gov submit-proposal "$PROPOSAL_FILE" \
      --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
      --keyring-backend "$KEYRING_BACKEND" --home "$KEY_DIR" $NODE_OPTS); then
        echo "Error: submit-proposal failed."
        echo "$SUBMIT_OUT"
        exit 1
    fi
    echo "$SUBMIT_OUT" > submit_wrapped_output.json

    TX_HASH=$(echo "$SUBMIT_OUT" | sed -n '/{/,$p' | jq -r '.txhash // empty' 2>/dev/null)
    if [ -z "$TX_HASH" ] || [ "$TX_HASH" = "null" ]; then
        echo "Error: submit-proposal did not return a txhash."
        echo "$SUBMIT_OUT"
        exit 1
    fi
    echo "TX Hash: $TX_HASH"

    echo "Waiting 6 seconds..."
    sleep 6

    # 5. Get Proposal ID
    PROPOSAL_ID=$($APP_NAME q gov proposals --output json $NODE_OPTS </dev/null | jq -r '.proposals[-1].id')
else
    PROPOSAL_ID="$PROPOSAL_ID_ARG"
fi

echo "Proposal ID: $PROPOSAL_ID"

if [ -z "$PROPOSAL_ID" ] || [ "$PROPOSAL_ID" == "null" ]; then
     echo "Error: Could not find proposal ID."
     exit 1
fi

# 6. Vote
echo "Voting YES..."
MAX_RETRIES=5
RETRY_COUNT=0
VOTE_SUCCESS=false

while [ $RETRY_COUNT -lt $MAX_RETRIES ]; do
    if VOTE_OUT=$(run_tx_json_retry 3 "$APP_NAME" tx gov vote "$PROPOSAL_ID" yes \
      --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
      --keyring-backend "$KEYRING_BACKEND" --home "$KEY_DIR" $NODE_OPTS); then
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
    echo "Vote submitted successfully!"
else
    echo "Error: Failed to vote after $MAX_RETRIES attempts."
    exit 1
fi

echo "Done!"
