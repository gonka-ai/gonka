#!/usr/bin/env bash
# Submit governance proposal: cosmos.gov.v1.MsgUpdateParams to shorten voting_period.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
cd "$JOIN_DIR"

if [[ -f config.env ]]; then
  # shellcheck disable=SC1091
  source config.env
fi

: "${KEY_NAME:?Set KEY_NAME in config.env}"
: "${KEYRING_PASSWORD:?Set KEYRING_PASSWORD in config.env}"
: "${CHAIN_ID:=gonka-testnet}"
: "${KEYRING_BACKEND:=file}"

VOTING_PERIOD="${VOTING_PERIOD:-10m0s}"
EXPEDITED_VOTING_PERIOD="${EXPEDITED_VOTING_PERIOD:-5m0s}"

NODE_CONTAINER="${NODE_CONTAINER:-node}"
NODE_RPC="${NODE_RPC:-tcp://127.0.0.1:26657}"
HOME_DIR="${HOME_DIR:-/root/.inference}"
INFERENCE_IMAGE="${INFERENCE_IMAGE:-ghcr.io/product-science/inferenced:0.2.13}"

APP=(docker exec "$NODE_CONTAINER" inferenced)
NODE_OPTS=(--home "$HOME_DIR" --node "$NODE_RPC" --chain-id "$CHAIN_ID")
TX_NODE_RPC="${TX_NODE_RPC:-tcp://node:26657}"
TX_NODE_OPTS=(--home "$HOME_DIR" --node "$TX_NODE_RPC" --chain-id "$CHAIN_ID")
TX_APP=(docker run --rm -i --network join_default \
  -v "${JOIN_DIR}/.inference:${HOME_DIR}" \
  "$INFERENCE_IMAGE" inferenced)

echo "Chain: $CHAIN_ID"
echo "Proposed gov params: voting_period=$VOTING_PERIOD expedited_voting_period=$EXPEDITED_VOTING_PERIOD"

echo "Fetching current gov params..."
CURRENT_GOV="$("${APP[@]}" query gov params -o json "${NODE_OPTS[@]}" </dev/null)"
CUR_VP="$(echo "$CURRENT_GOV" | jq -r '.params.voting_period')"
CUR_EVP="$(echo "$CURRENT_GOV" | jq -r '.params.expedited_voting_period')"
echo "Current: voting_period=$CUR_VP expedited_voting_period=$CUR_EVP"

echo "Resolving gov module authority..."
GOV_AUTH="$("${APP[@]}" query auth module-account gov -o json "${NODE_OPTS[@]}" </dev/null | jq -r '.account.value.address')"
if [[ -z "$GOV_AUTH" || "$GOV_AUTH" == "null" ]]; then
  echo "Failed to resolve gov module account address" >&2
  exit 1
fi
echo "Gov authority: $GOV_AUTH"

MIN_DEPOSIT_AMOUNT="$("${APP[@]}" query gov params -o json "${NODE_OPTS[@]}" </dev/null | jq -r '.params.min_deposit[0].amount')"
MIN_DEPOSIT_DENOM="$("${APP[@]}" query gov params -o json "${NODE_OPTS[@]}" </dev/null | jq -r '.params.min_deposit[0].denom')"
DEPOSIT="${MIN_DEPOSIT_AMOUNT}${MIN_DEPOSIT_DENOM}"
CURRENT_VOTING_PERIOD="$CUR_VP"

PATCHED_GOV="$(echo "$CURRENT_GOV" | jq \
  --arg vp "$VOTING_PERIOD" \
  --arg evp "$EXPEDITED_VOTING_PERIOD" \
  '.params
    | .voting_period = $vp
    | .expedited_voting_period = $evp')"

mkdir -p "${JOIN_DIR}/.inference"
PROPOSAL_HOST="${JOIN_DIR}/.inference/gov-proposal-shorter-voting.json"
PROPOSAL_IN_CONTAINER="/root/.inference/gov-proposal-shorter-voting.json"

jq -n \
  --arg auth "$GOV_AUTH" \
  --arg deposit "$DEPOSIT" \
  --arg vp "$VOTING_PERIOD" \
  --arg evp "$EXPEDITED_VOTING_PERIOD" \
  --argjson params "$PATCHED_GOV" \
  '{
    messages: [
      {
        "@type": "/cosmos.gov.v1.MsgUpdateParams",
        authority: $auth,
        params: $params
      }
    ],
    metadata: "shorter-gov-voting-period",
    deposit: $deposit,
    title: "Shorten governance voting period (testnet)",
    summary: ("Reduce voting_period to " + $vp + " and expedited_voting_period to " + $evp + " so allowlist and parameter proposals finish faster on testnet.")
  }' >"$PROPOSAL_HOST"

echo "Wrote $PROPOSAL_HOST (visible in node as $PROPOSAL_IN_CONTAINER)"
jq '{
  title,
  deposit,
  summary,
  voting_period: .messages[0].params.voting_period,
  expedited_voting_period: .messages[0].params.expedited_voting_period
}' "$PROPOSAL_HOST"

echo "Submitting proposal..."
echo "Pulling inference client image if needed: $INFERENCE_IMAGE"
docker pull "$INFERENCE_IMAGE" >/dev/null

RAW_SUBMIT="$(printf '%s\n' "$KEYRING_PASSWORD" | "${TX_APP[@]}" tx gov submit-proposal "$PROPOSAL_IN_CONTAINER" \
  --from "$KEY_NAME" \
  --keyring-backend "$KEYRING_BACKEND" \
  --gas auto \
  --gas-adjustment 1.5 \
  --yes \
  --output json \
  "${TX_NODE_OPTS[@]}" 2>&1)" || true

SUBMIT_JSON="$(echo "$RAW_SUBMIT" | sed -n '/{/,$p')"
TX_HASH="$(echo "$SUBMIT_JSON" | jq -r '.txhash // empty')"
CODE="$(echo "$SUBMIT_JSON" | jq -r '.code // empty')"
if [[ -z "$TX_HASH" || "$CODE" != "0" ]]; then
  echo "submit-proposal failed:" >&2
  echo "$RAW_SUBMIT" >&2
  exit 1
fi
echo "Submit tx: $TX_HASH"
sleep 8

PROPOSAL_ID="$("${APP[@]}" query gov proposals -o json "${NODE_OPTS[@]}" </dev/null | jq -r '.proposals[-1].id')"
STATUS="$("${APP[@]}" query gov proposal "$PROPOSAL_ID" -o json "${NODE_OPTS[@]}" </dev/null | jq -r '.status')"
echo "Proposal id=$PROPOSAL_ID status=$STATUS"

if [[ "$STATUS" != "PROPOSAL_STATUS_VOTING_PERIOD" && "$STATUS" != "2" ]]; then
  echo "Proposal not in voting period yet; waiting 10s..." >&2
  sleep 10
  STATUS="$("${APP[@]}" query gov proposal "$PROPOSAL_ID" -o json "${NODE_OPTS[@]}" </dev/null | jq -r '.status')"
  echo "Status now: $STATUS"
fi

echo "Voting yes from $KEY_NAME..."
RAW_VOTE="$(printf '%s\n' "$KEYRING_PASSWORD" | "${TX_APP[@]}" tx gov vote "$PROPOSAL_ID" yes \
  --from "$KEY_NAME" \
  --keyring-backend "$KEYRING_BACKEND" \
  --gas auto \
  --gas-adjustment 1.5 \
  --yes \
  --output json \
  "${TX_NODE_OPTS[@]}" 2>&1)" || true

VOTE_JSON="$(echo "$RAW_VOTE" | sed -n '/{/,$p')"
if [[ "$(echo "$VOTE_JSON" | jq -r '.code // 1')" != "0" ]]; then
  echo "Vote failed:" >&2
  echo "$RAW_VOTE" >&2
  exit 1
fi
echo "Vote tx: $(echo "$VOTE_JSON" | jq -r '.txhash')"

echo ""
echo "This proposal is voted under the CURRENT voting period: $CURRENT_VOTING_PERIOD"
echo "If it passes, new proposals will use voting_period=$VOTING_PERIOD"
echo "Track: ${APP[*]} query gov proposal $PROPOSAL_ID -o json ${NODE_OPTS[*]}"
