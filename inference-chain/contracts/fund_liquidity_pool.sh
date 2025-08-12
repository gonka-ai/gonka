#!/bin/sh

set -e

# Configuration
APP_NAME="${APP_NAME:-inferenced}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-3}"
KEY_NAME="${KEY_NAME:-genesis}"
KEYRING_BACKEND="${KEYRING_BACKEND:-test}"
COIN_DENOM="${COIN_DENOM:-nicoin}"

# Verbose logging (set VERBOSE=1 to enable full JSON outputs and tracing)
VERBOSE="${VERBOSE:-0}"

# Funding amount: 120M AIC = 120,000,000,000,000,000 nicoin base units
FUNDING_AMOUNT="120000000000000000"

echo "Using chain-id: $CHAIN_ID"

# Load state from previous deployment
if [ -f "transfer_state.env" ]; then
    . ./transfer_state.env
    echo "Loaded state: CODE_ID=$CODE_ID, PROPOSAL_ID=$PROPOSAL_ID"
else
    echo "Error: transfer_state.env not found. Run deploy.sh first."
    exit 1
fi

# Wait for deployment proposal to pass
echo "Waiting for deployment proposal $PROPOSAL_ID to pass..."
for i in $(seq 1 60); do
    PROPOSAL_STATUS=$($APP_NAME query gov proposal "$PROPOSAL_ID" --chain-id "$CHAIN_ID" --output json 2>/dev/null | jq -r '.proposal.status // empty')
    
    if [ "$PROPOSAL_STATUS" = "PROPOSAL_STATUS_PASSED" ]; then
        echo "Deployment proposal $PROPOSAL_ID passed!"
        sleep 10
        break
    elif [ "$PROPOSAL_STATUS" = "PROPOSAL_STATUS_REJECTED" ]; then
        echo "Deployment proposal $PROPOSAL_ID was rejected"
        exit 1
    elif [ "$PROPOSAL_STATUS" = "PROPOSAL_STATUS_FAILED" ]; then
        echo "Deployment proposal $PROPOSAL_ID failed"
        exit 1
    fi
    
    if [ $i -eq 60 ]; then
        echo "Timeout waiting for deployment proposal to pass"
        exit 1
    fi
    
    echo "Waiting for deployment proposal to pass... ($i/60) - Status: $PROPOSAL_STATUS"
    sleep 5
done

# Get the liquidity pool contract address from the singleton
echo "Getting liquidity pool contract address..."

# Retry getting the contract address (it might take a moment to be available)
for i in $(seq 1 10); do
    LIQUIDITY_POOL_ADDR=$($APP_NAME query inference liquidity-pool --chain-id "$CHAIN_ID" --output json 2>/dev/null | jq -r '.address // empty')
    
    if [ -n "$LIQUIDITY_POOL_ADDR" ] && [ "$LIQUIDITY_POOL_ADDR" != "null" ]; then
        echo "Found liquidity pool contract address: $LIQUIDITY_POOL_ADDR"
        break
    fi
    
    if [ $i -eq 10 ]; then
        echo "Error: Could not get liquidity pool address from inference module"
        echo "Make sure the contract was instantiated via governance proposal"
        exit 1
    fi
    
    echo "Waiting for contract address to be available... ($i/10)"
    sleep 3
done

# Get gov module account address for authority
GOV_MODULE_ADDR=$($APP_NAME query auth module-accounts --chain-id "$CHAIN_ID" --output json \
    | jq -r '.accounts[] 
      | select(.value.name=="gov")
      | .value.address')

if [ -z "$GOV_MODULE_ADDR" ]; then
    echo "Could not find gov module account address"
    exit 1
fi

echo "Using gov module account as authority: $GOV_MODULE_ADDR"

# Check community pool balance
echo "Checking community pool balance..."
COMMUNITY_POOL=$($APP_NAME query distribution community-pool --chain-id "$CHAIN_ID" --output json 2>/dev/null | jq -r --arg COIN_DENOM "$COIN_DENOM" '
  .pool[] | select(test($COIN_DENOM + "$")) | capture("^(?<amount>[0-9.]+)"+$COIN_DENOM+"$") | .amount // "0"'
)

if [ -z "$COMMUNITY_POOL" ] || [ "$COMMUNITY_POOL" = "0" ]; then
    echo "Community pool has insufficient funds: $COMMUNITY_POOL $COIN_DENOM"
    echo "Need: $FUNDING_AMOUNT $COIN_DENOM"
    exit 1
fi

echo "Community pool has sufficient funds: $COMMUNITY_POOL $COIN_DENOM"

# Get min deposit for the proposal
MIN_DEPOSIT_JSON=$($APP_NAME query gov params --chain-id "$CHAIN_ID" --output json)
MIN_DEPOSIT_AMOUNT=$(echo "$MIN_DEPOSIT_JSON" | jq -r '.params.min_deposit[] | select(.denom=="'"$COIN_DENOM"'") | .amount')

if [ -z "$MIN_DEPOSIT_AMOUNT" ]; then
  echo "ERROR: Couldn't find min_deposit for denom $COIN_DENOM"
  exit 1
fi

echo "Using minimum deposit: ${MIN_DEPOSIT_AMOUNT}${COIN_DENOM}"

# Create funding proposal
FUNDING_PROPOSAL_FILE="proposal_funding_$LIQUIDITY_POOL_ADDR.json"

jq -n --arg recipient "$LIQUIDITY_POOL_ADDR" \
      --arg authority "$GOV_MODULE_ADDR" \
      --arg amount "$FUNDING_AMOUNT" \
      --arg denom "$COIN_DENOM" \
      --arg deposit "${MIN_DEPOSIT_AMOUNT}${COIN_DENOM}" '
      {
        "messages": [{
          "@type": "/cosmos.distribution.v1beta1.MsgCommunityPoolSpend",
          "authority": $authority,
          "recipient": $recipient,
          "amount": [{
            "denom": $denom,
            "amount": $amount
          }]
        }],
        "deposit": $deposit,
        "title": "Fund Liquidity Pool Contract",
        "summary": "Allocate 120M AIC tokens to the liquidity pool contract for gradual sale to users"
      }' > "$FUNDING_PROPOSAL_FILE"

# Submit governance proposal
echo "Submitting funding governance proposal..."
if [ "$VERBOSE" = "1" ]; then
    set -x
fi

FUNDING_PROPOSAL_TX=$($APP_NAME tx gov submit-proposal \
    "$FUNDING_PROPOSAL_FILE" \
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
    echo "ERROR: Funding proposal submission failed with exit code $EXIT_CODE"
    echo "Raw output:"
    echo "$FUNDING_PROPOSAL_TX"
    echo "Command failed with exit code $EXIT_CODE"
    exit 1
fi

if [ "$VERBOSE" = "1" ]; then
    echo "Funding proposal transaction result:"
    echo "$FUNDING_PROPOSAL_TX"
    jq --version
    which jq
    cat "$FUNDING_PROPOSAL_FILE"
fi

# Check for errors in proposal submission
TX_CODE=$(echo "$FUNDING_PROPOSAL_TX" | jq -r '.code // empty')
if [ -n "$TX_CODE" ] && [ "$TX_CODE" != "0" ]; then
    echo "Error: Funding proposal submission failed with code $TX_CODE"
    echo "$FUNDING_PROPOSAL_TX" | jq
    exit 1
fi

# Extract funding proposal ID
FUNDING_PROPOSAL_TX_HASH=$(echo "$FUNDING_PROPOSAL_TX" | jq -r '.txhash')
for i in $(seq 1 30); do
    TX_QUERY=$($APP_NAME query tx "$FUNDING_PROPOSAL_TX_HASH" --chain-id "$CHAIN_ID" --output json 2>/dev/null || echo "")
    FUNDING_PROPOSAL_ID=$(echo "$TX_QUERY" | jq -r '
    .events[]
        | select(.type == "submit_proposal")
        | .attributes[]
        | select(.key == "proposal_id")
        | .value
    ' 2>/dev/null | grep -E '^[0-9]+$' | head -n1)
    if [ -n "$FUNDING_PROPOSAL_ID" ]; then
        echo "Funding governance proposal submitted with ID: $FUNDING_PROPOSAL_ID"
        break
    fi
    sleep 2
done

if [ -z "$FUNDING_PROPOSAL_ID" ]; then
    echo "Funding governance proposal submitted but could not extract proposal ID"
    echo "Full TX Query:"
    echo "$TX_QUERY"
    exit 1
fi

# Save state for voting
cat > transfer_state.env << EOF
export PROPOSAL_ID="$FUNDING_PROPOSAL_ID"
export LIQUIDITY_POOL_ADDR="$LIQUIDITY_POOL_ADDR"
EOF

rm -f "$FUNDING_PROPOSAL_FILE"