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

WT_DIR="$(cd "$(dirname "$0")/wrapped-token" && pwd)"
CONTRACT_WASM="$WT_DIR/artifacts/wrapped_token.wasm"

if [ ! -f "$CONTRACT_WASM" ]; then
  echo "Error: $CONTRACT_WASM not found. Run $WT_DIR/build.sh first."
  exit 1
fi

echo "Storing wrapped-token wasm..."
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
  echo "$STORE_TX"
fi
TX_HASH=$(echo "$STORE_TX" | jq -r '.txhash')

# Extract code id
for i in $(seq 1 30); do
  TX_QUERY=$($APP_NAME query tx "$TX_HASH" --chain-id "$CHAIN_ID" --output json 2>/dev/null || echo "")
  CODE_ID=$(echo "$TX_QUERY" | jq -r '.events[] | select(.type == "store_code") | .attributes[] | select(.key == "code_id") | .value' 2>/dev/null | grep -E '^[0-9]+$' | head -n1)
  [ -n "$CODE_ID" ] && break
  sleep 2
done

[ -z "$CODE_ID" ] && echo "Error: code_id not found" && exit 1

echo "Submitting gov proposal to register wrapped-token code id..."
GOV_MODULE_ADDR=$($APP_NAME query auth module-accounts --chain-id "$CHAIN_ID" --output json | jq -r '.accounts[] | select(.value.name=="gov") | .value.address')
MIN_DEPOSIT_JSON=$($APP_NAME query gov params --chain-id "$CHAIN_ID" --output json)
MIN_DEPOSIT_AMOUNT=$(echo "$MIN_DEPOSIT_JSON" | jq -r '.params.min_deposit[] | select(.denom=="'"$COIN_DENOM"'") | .amount')
[ -z "$MIN_DEPOSIT_AMOUNT" ] && echo "ERROR: min deposit not found" && exit 1

PROPOSAL_FILE="proposal_register_wrapped_token_$CODE_ID.json"
jq -n --arg code_id "$CODE_ID" \
      --arg authority "$GOV_MODULE_ADDR" \
      --arg deposit "${MIN_DEPOSIT_AMOUNT}${COIN_DENOM}" '
      {
        "messages": [{
          "@type": "/inference.inference.MsgRegisterWrappedTokenContract",
          "authority": $authority,
          "codeId": $code_id
        }],
        "deposit": $deposit,
        "title": "Register Wrapped Token Code",
        "summary": "Register code id for wrapped-token instantiations"
      }' > "$PROPOSAL_FILE"

PROPOSAL_TX=$($APP_NAME tx gov submit-proposal "$PROPOSAL_FILE" \
  --from "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" --chain-id "$CHAIN_ID" \
  --gas auto --gas-adjustment 1.3 --fees "1000$COIN_DENOM" --broadcast-mode sync --output json --yes)

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

