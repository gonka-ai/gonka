#!/bin/sh

set -e

# Configuration
APP_NAME="${APP_NAME:-inferenced}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-3}"
KEY_NAME="${KEY_NAME:-genesis}"
KEYRING_BACKEND="${KEYRING_BACKEND:-test}"

COIN_DENOM="${COIN_DENOM:-nicoin}"

PROPOSAL_ID="$1"
VOTE_OPTION="${2:-yes}"

# If no proposal ID provided, try to load from state
if [ -z "$PROPOSAL_ID" ] && [ -f "transfer_state.env" ]; then
    . ./transfer_state.env
    PROPOSAL_ID="$PROPOSAL_ID"
    if [ -n "$PROPOSAL_ID" ]; then
        echo "Using proposal ID from state: $PROPOSAL_ID"
    fi
fi

if [ -z "$PROPOSAL_ID" ]; then
    echo "Usage: $0 <proposal_id> [vote_option]"
    echo "Vote options: yes, no, abstain, no_with_veto"
    echo ""
    echo "Active proposals:"
    $APP_NAME query gov proposals --output json | jq -r '.proposals[] | select(.status == "PROPOSAL_STATUS_VOTING_PERIOD") | "\(.id): \(.title)"' 2>/dev/null || echo "No active proposals"
    exit 1
fi

echo "Waiting for proposal $PROPOSAL_ID to enter voting period..."
for i in $(seq 1 30); do
    STATUS=$($APP_NAME query gov proposal "$PROPOSAL_ID" --output json 2>/dev/null | jq -r '.proposal.status // empty')
    if [ "$STATUS" = "PROPOSAL_STATUS_VOTING_PERIOD" ]; then
        echo "Proposal is in voting period, proceeding to vote..."
        break
    fi
    sleep 2
done

if [ "$STATUS" != "PROPOSAL_STATUS_VOTING_PERIOD" ]; then
    echo "Proposal $PROPOSAL_ID is not in voting period after waiting, cannot vote"
    echo "Current status: $STATUS"
    exit 1
fi

echo "Voting $VOTE_OPTION on proposal $PROPOSAL_ID..."

VOTE_OUTPUT=$($APP_NAME tx gov vote "$PROPOSAL_ID" "$VOTE_OPTION" \
    --from "$KEY_NAME" \
    --keyring-backend "$KEYRING_BACKEND" \
    --chain-id "$CHAIN_ID" \
    --gas auto \
    --gas-adjustment 1.3 \
    --fees "1000$COIN_DENOM" \
    --yes 2>&1)
VOTE_EXIT_CODE=$?

if [ $VOTE_EXIT_CODE -ne 0 ]; then
    echo "Vote transaction failed with exit code $VOTE_EXIT_CODE"
    echo "$VOTE_OUTPUT"
    exit $VOTE_EXIT_CODE
fi

echo "Vote submitted"
echo "Check proposal status with: $APP_NAME query gov proposal $PROPOSAL_ID"