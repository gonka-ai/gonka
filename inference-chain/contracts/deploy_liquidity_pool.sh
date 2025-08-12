#!/bin/sh

set -e

# Configuration
APP_NAME="${APP_NAME:-inferenced}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-3}"
KEY_NAME="${KEY_NAME:-genesis}"
KEYRING_BACKEND="${KEYRING_BACKEND:-test}"
COIN_DENOM="${COIN_DENOM:-nicoin}"

# Verbose logging (set VERBOSE=1 to enable full JSON outputs)
VERBOSE="${VERBOSE:-0}"

echo "Using chain-id: $CHAIN_ID"

# Contract details
LP_DIR="$(cd "$(dirname "$0")/liquidity-pool" && pwd)"
CONTRACT_WASM="$LP_DIR/artifacts/liquidity_pool.wasm"
CONTRACT_LABEL="liquidity-pool-testnet"

# Check if WASM exists
if [ ! -f "$CONTRACT_WASM" ]; then
    echo "Error: $CONTRACT_WASM not found. Run ./build.sh first."
    exit 1
fi

echo "Deploying liquidity pool contract..."

# Store contract code
echo "Storing contract code..."
# Add delay to avoid sequence issues
sleep 2

STORE_TX=$($APP_NAME tx wasm store "$CONTRACT_WASM" \
    --from "$KEY_NAME" \
    --keyring-backend "$KEYRING_BACKEND" \
    --chain-id "$CHAIN_ID" \
    --gas auto \
    --gas-adjustment 1.3 \
    --fees "1000$COIN_DENOM" \
    --broadcast-mode sync \
    --output json \
    --yes)

if [ "$VERBOSE" = "1" ]; then
    echo "Store transaction result:"
    echo "$STORE_TX"
fi


TX_HASH=$(echo "$STORE_TX" | jq -r '.txhash')

for i in $(seq 1 30); do
    TX_QUERY=$($APP_NAME query tx "$TX_HASH" --chain-id "$CHAIN_ID" --output json 2>/dev/null || echo "")
    CODE_ID=$(echo "$TX_QUERY" | jq -r '
        .events[] | select(.type == "store_code") | .attributes[] | select(.key == "code_id") | .value
    ' 2>/dev/null | grep -E '^[0-9]+$' | head -n1)
    if [ -n "$CODE_ID" ]; then
        echo "Contract stored with code_id: $CODE_ID"
        break
    fi
    sleep 2
done

if [ -z "$CODE_ID" ]; then
    echo "Error: code_id could not be extracted after 30 tries."
    exit 1
fi

# Get gov module account address for sender
GOV_MODULE_ADDR=$($APP_NAME query auth module-accounts --chain-id "$CHAIN_ID" --output json \
  | jq -r '.accounts[] 
      | select(.value.name=="gov")
      | .value.address')
echo "Using gov module account as sender: $GOV_MODULE_ADDR"
PROPOSAL_FILE="proposal_instantiate_$CODE_ID.json"

# Get min deposit for the proposal
MIN_DEPOSIT_JSON=$($APP_NAME query gov params --chain-id "$CHAIN_ID" --output json)
MIN_DEPOSIT_AMOUNT=$(echo "$MIN_DEPOSIT_JSON" | jq -r '.params.min_deposit[] | select(.denom=="'"$COIN_DENOM"'") | .amount')

if [ -z "$MIN_DEPOSIT_AMOUNT" ]; then
  echo "ERROR: Couldn't find min_deposit for denom $COIN_DENOM"
  exit 1
fi

echo "Using minimum deposit: ${MIN_DEPOSIT_AMOUNT}${COIN_DENOM}"

# Note: total_supply uses 9 decimals for native tokens (120000000000000000000 = 120M tokens)
jq -n --arg code_id "$CODE_ID" \
      --arg contract_label "$CONTRACT_LABEL" \
      --arg authority "$GOV_MODULE_ADDR" \
      --arg instantiate_msg '{"admin": "'$GOV_MODULE_ADDR'", "daily_limit_bp": "1000", "total_supply": "120000000000000000000"}' \
      --arg deposit "${MIN_DEPOSIT_AMOUNT}${COIN_DENOM}" '
      ($code_id | tonumber) as $code_id_num |
      ($instantiate_msg | fromjson) as $instantiate_msg_obj |
      {
        "messages": [{
          "@type": "/inference.inference.MsgRegisterLiquidityPool",
          "authority": $authority,
          "codeId": $code_id,
          "label": $contract_label,
          "instantiateMsg": $instantiate_msg
        }],
        "deposit": $deposit,
        "title": "Instantiate Liquidity Pool",
        "summary": "Create the liquidity pool contract for cross-chain swaps"
      }' > "$PROPOSAL_FILE"

# Submit governance proposal  
echo "Submitting governance proposal..."

PROPOSAL_TX=$($APP_NAME tx gov submit-proposal \
    "$PROPOSAL_FILE" \
    --from "$KEY_NAME" \
    --keyring-backend "$KEYRING_BACKEND" \
    --chain-id "$CHAIN_ID" \
    --gas auto \
    --gas-adjustment 1.3 \
    --fees "1000$COIN_DENOM" \
    --broadcast-mode sync \
    --output json \
    --yes)

EXIT_CODE=$?
if [ $EXIT_CODE -ne 0 ]; then
    echo "ERROR: Proposal submission command failed with exit code $EXIT_CODE"
    echo "Raw output:"
    echo "$PROPOSAL_TX"
    exit 1
fi

if [ "$VERBOSE" = "1" ]; then
    echo "Proposal transaction result:"
    echo "$PROPOSAL_TX"
fi

# Check for errors in proposal submission
TX_CODE=$(echo "$PROPOSAL_TX" | jq -r '.code // empty')
if [ -n "$TX_CODE" ] && [ "$TX_CODE" != "0" ]; then
    echo "Error: Proposal submission failed with code $TX_CODE"
    echo "$PROPOSAL_TX" | jq
    exit 1
fi

 # Extract proposal ID with polling (wait until tx is indexed in block)
PROPOSAL_TX_HASH=$(echo "$PROPOSAL_TX" | jq -r '.txhash')
for i in $(seq 1 30); do
    TX_QUERY=$($APP_NAME query tx "$PROPOSAL_TX_HASH" --chain-id "$CHAIN_ID" --output json 2>/dev/null || echo "")
    PROPOSAL_ID=$(echo "$TX_QUERY" | jq -r '
    .events[]
        | select(.type == "submit_proposal")
        | .attributes[]
        | select(.key == "proposal_id")
        | .value
    ' 2>/dev/null | grep -E '^[0-9]+$' | head -n1)
    if [ -n "$PROPOSAL_ID" ]; then
        echo "Governance proposal submitted with ID: $PROPOSAL_ID"
        break
    fi
    sleep 2
done

if [ -z "$PROPOSAL_ID" ]; then
    echo "Governance proposal submitted but could not extract proposal ID"
fi

# Save state for other scripts
cat > transfer_state.env << EOF
export CODE_ID="$CODE_ID"
export PROPOSAL_ID="$PROPOSAL_ID"
export CONTRACT_LABEL="$CONTRACT_LABEL"
EOF

rm -f "$PROPOSAL_FILE" 