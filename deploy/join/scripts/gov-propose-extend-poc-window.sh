#!/usr/bin/env bash
# Submit governance proposal: MsgUpdateParams with longer PoC generation/exchange window.
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
: "${COIN_DENOM:=ngonka}"
: "${KEYRING_BACKEND:=file}"

NODE_CONTAINER="${NODE_CONTAINER:-node}"
NODE_RPC="${NODE_RPC:-tcp://127.0.0.1:26657}"
HOME_DIR="${HOME_DIR:-/root/.inference}"

POC_STAGE_DURATION="${POC_STAGE_DURATION:-60}"
POC_EXCHANGE_DURATION="${POC_EXCHANGE_DURATION:-10}"

APP=(docker exec "$NODE_CONTAINER" inferenced)
NODE_OPTS=(--home "$HOME_DIR" --node "$NODE_RPC" --chain-id "$CHAIN_ID")

echo "Chain: $CHAIN_ID"
echo "Proposed epoch_params: poc_stage_duration=$POC_STAGE_DURATION poc_exchange_duration=$POC_EXCHANGE_DURATION"

echo "Fetching current inference params..."
CURRENT_PARAMS="$("${APP[@]}" query inference params -o json "${NODE_OPTS[@]}" </dev/null)"
echo "$CURRENT_PARAMS" | jq '.params.epoch_params' >/dev/null

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
VOTING_PERIOD="$("${APP[@]}" query gov params -o json "${NODE_OPTS[@]}" </dev/null | jq -r '.params.voting_period')"

PATCHED_PARAMS="$(echo "$CURRENT_PARAMS" | jq \
  --argjson stage "$POC_STAGE_DURATION" \
  --argjson exchange "$POC_EXCHANGE_DURATION" \
  '.params
    | .epoch_params.poc_stage_duration = $stage
    | .epoch_params.poc_exchange_duration = $exchange')"

mkdir -p "${JOIN_DIR}/.inference"
PROPOSAL_HOST="${JOIN_DIR}/.inference/gov-proposal-extend-poc-window.json"
PROPOSAL_IN_CONTAINER="/root/.inference/gov-proposal-extend-poc-window.json"
jq -n \
  --arg auth "$GOV_AUTH" \
  --arg deposit "$DEPOSIT" \
  --argjson stage "$POC_STAGE_DURATION" \
  --argjson exchange "$POC_EXCHANGE_DURATION" \
  --argjson params "$(echo "$PATCHED_PARAMS" | jq '.params')" \
  '{
    messages: [
      {
        "@type": "/inference.inference.MsgUpdateParams",
        authority: $auth,
        params: $params
      }
    ],
    metadata: "https://github.com/gonka-ai/gonka/blob/main/proposals/governance-artifacts/extend-poc-generation-window/README.md",
    deposit: $deposit,
    title: "Extend PoC generation and exchange window",
    summary: ("Increase poc_stage_duration to " + ($stage|tostring) + " and poc_exchange_duration to " + ($exchange|tostring) + " so MLNodes have more time to upload PoC v2 artifacts to DAPI.")
  }' >"$PROPOSAL_HOST"

echo "Wrote $PROPOSAL_HOST (visible in node as $PROPOSAL_IN_CONTAINER)"
jq '{title, deposit, summary, epoch_params: .messages[0].params.epoch_params}' "$PROPOSAL_HOST"

echo "Submitting proposal..."
RAW_SUBMIT="$(printf '%s\n%s\n' "$KEYRING_PASSWORD" "$KEYRING_PASSWORD" | "${APP[@]}" tx gov submit-proposal "$PROPOSAL_IN_CONTAINER" \
  --from "$KEY_NAME" \
  --keyring-backend "$KEYRING_BACKEND" \
  --gas auto \
  --gas-adjustment 1.5 \
  --yes \
  --output json \
  "${NODE_OPTS[@]}" 2>&1)" || true

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
RAW_VOTE="$(printf '%s\n%s\n' "$KEYRING_PASSWORD" "$KEYRING_PASSWORD" | "${APP[@]}" tx gov vote "$PROPOSAL_ID" yes \
  --from "$KEY_NAME" \
  --keyring-backend "$KEYRING_BACKEND" \
  --gas auto \
  --gas-adjustment 1.5 \
  --yes \
  --output json \
  "${NODE_OPTS[@]}" 2>&1)" || true

VOTE_JSON="$(echo "$RAW_VOTE" | sed -n '/{/,$p')"
if [[ "$(echo "$VOTE_JSON" | jq -r '.code // 1')" != "0" ]]; then
  echo "Vote failed:" >&2
  echo "$RAW_VOTE" >&2
  exit 1
fi
echo "Vote tx: $(echo "$VOTE_JSON" | jq -r '.txhash')"

echo ""
echo "Voting period: $VOTING_PERIOD (sleep until tally, or ask other validators to vote yes)"
echo "Track: ${APP[*]} query gov proposal $PROPOSAL_ID -o json ${NODE_OPTS[*]}"
echo ""
echo "When status is PROPOSAL_STATUS_PASSED, verify:"
echo "  docker exec api curl -sS http://127.0.0.1:9000/v1/epochs/latest | jq '.stages'"
