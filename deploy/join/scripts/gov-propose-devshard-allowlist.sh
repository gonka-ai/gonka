#!/usr/bin/env bash
# Submit governance proposal: MsgUpdateParams to add an address to
# devshard_escrow_params.allowed_creator_addresses.
#
# On join hosts with a funded cold key at /srv/dai/.inference, set:
#   TX_KEY_NAME=gonka-account-key TX_INFERENCE_HOME=/srv/dai/.inference
# (genesis key in the node container often has no spendable balance after redeploy).
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
: "${CHAIN_ID:=gonka-testnet-4}"
: "${KEYRING_BACKEND:=file}"

NEW_ADDRESS="${NEW_ADDRESS:?Set NEW_ADDRESS to the gonka1... address to allow}"
TX_KEY_NAME="${TX_KEY_NAME:-gonka-account-key}"
TX_INFERENCE_HOME="${TX_INFERENCE_HOME:-/srv/dai/.inference}"
INFERENCE_IMAGE="${INFERENCE_IMAGE:-ghcr.io/product-science/inferenced:0.2.13}"

NODE_CONTAINER="${NODE_CONTAINER:-node}"
API_CONTAINER="${API_CONTAINER:-api}"
NODE_RPC="${NODE_RPC:-tcp://127.0.0.1:26657}"
HOME_DIR="${HOME_DIR:-/root/.inference}"
CHAIN_API_URL="${CHAIN_API_URL:-http://node:1317/productscience/inference/inference/params}"
# Chain state on testnet-4 includes DevshardEscrowParams.max_nonce (v0.2.13+). The
# join-stack node image may still ship inferenced 0.2.12, which cannot marshal
# that field. Use a matching client for gov submit/vote.

APP=(docker exec "$NODE_CONTAINER" inferenced)
NODE_OPTS=(--home "$HOME_DIR" --node "$NODE_RPC" --chain-id "$CHAIN_ID")
TX_NODE_RPC="${TX_NODE_RPC:-tcp://node:26657}"
TX_NODE_OPTS=(--home /root/.inference --node "$TX_NODE_RPC" --chain-id "$CHAIN_ID")

USE_HOST_KEYRING=0
if [[ -f "${TX_INFERENCE_HOME}/keyring-file/${TX_KEY_NAME}.info" ]]; then
  USE_HOST_KEYRING=1
fi

if [[ "$USE_HOST_KEYRING" -eq 1 ]]; then
  TX_APP=(docker run --rm -i --network join_default \
    -v "${TX_INFERENCE_HOME}:/root/.inference" \
    "$INFERENCE_IMAGE" inferenced)
  PROPOSAL_HOST="${TX_INFERENCE_HOME}/gov-proposal-devshard-allowlist.json"
  PROPOSAL_IN_CONTAINER="/root/.inference/gov-proposal-devshard-allowlist.json"
  SUBMIT_KEY="$TX_KEY_NAME"
  echo "Using host keyring: ${TX_INFERENCE_HOME} (from=${SUBMIT_KEY})"
else
  TX_APP=(docker run --rm -i --network join_default \
    -v "${JOIN_DIR}/.inference:${HOME_DIR}" \
    "$INFERENCE_IMAGE" inferenced)
  TX_NODE_OPTS=(--home "$HOME_DIR" --node "$TX_NODE_RPC" --chain-id "$CHAIN_ID")
  PROPOSAL_HOST="${JOIN_DIR}/.inference/gov-proposal-devshard-allowlist.json"
  PROPOSAL_IN_CONTAINER="/root/.inference/gov-proposal-devshard-allowlist.json"
  SUBMIT_KEY="$KEY_NAME"
  echo "Using join keyring: ${JOIN_DIR}/.inference (from=${SUBMIT_KEY})"
fi

echo "Chain: $CHAIN_ID"
echo "Adding to devshard escrow allowlist: $NEW_ADDRESS"

echo "Fetching current inference params (REST, preserves devshard_escrow_params extensions)..."
CURRENT_PARAMS="$(docker exec "$API_CONTAINER" curl -fsS "$CHAIN_API_URL")"
echo "$CURRENT_PARAMS" | jq '.params.devshard_escrow_params' >/dev/null

CURRENT_ALLOWLIST="$(echo "$CURRENT_PARAMS" | jq -r '.params.devshard_escrow_params.allowed_creator_addresses[]?')"
if echo "$CURRENT_ALLOWLIST" | grep -qxF "$NEW_ADDRESS"; then
  echo "Address already in allowed_creator_addresses; nothing to propose." >&2
  exit 0
fi

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
  --arg addr "$NEW_ADDRESS" \
  '.params
    | .devshard_escrow_params.allowed_creator_addresses += [$addr]
    | .devshard_escrow_params.allowed_creator_addresses |= unique
    | .devshard_escrow_params.approved_versions = (.devshard_escrow_params.approved_versions // [])
    | .devshard_escrow_params.max_nonce = (
        (.devshard_escrow_params.max_nonce // 0) | if . > 0 then . else 1000000 end)
    | .devshard_escrow_params.devshard_requests_enabled = (
        .devshard_escrow_params.devshard_requests_enabled // true)')"

PROPOSAL_TMP="${TMPDIR:-/tmp}/gov-proposal-devshard-allowlist.json"
jq -n \
  --arg auth "$GOV_AUTH" \
  --arg deposit "$DEPOSIT" \
  --arg addr "$NEW_ADDRESS" \
  --argjson params "$PATCHED_PARAMS" \
  '{
    messages: [
      {
        "@type": "/inference.inference.MsgUpdateParams",
        authority: $auth,
        params: $params
      }
    ],
    metadata: "devshard-allowlist",
    deposit: $deposit,
    title: "Add devshard escrow creator to allowlist",
    summary: ("Append " + $addr + " to devshard_escrow_params.allowed_creator_addresses on " + $auth)
  }' >"$PROPOSAL_TMP"

if [[ -w "$(dirname "$PROPOSAL_HOST")" ]]; then
  cp "$PROPOSAL_TMP" "$PROPOSAL_HOST"
else
  sudo install -m 0644 "$PROPOSAL_TMP" "$PROPOSAL_HOST"
fi

echo "Wrote $PROPOSAL_HOST (visible in node as $PROPOSAL_IN_CONTAINER)"
jq '{
  title,
  deposit,
  summary,
  allowed_creator_addresses: .messages[0].params.devshard_escrow_params.allowed_creator_addresses
}' "$PROPOSAL_TMP"

echo "Submitting proposal..."
echo "Pulling inference client image if needed: $INFERENCE_IMAGE"
docker pull "$INFERENCE_IMAGE" >/dev/null

RAW_SUBMIT="$(printf '%s\n' "$KEYRING_PASSWORD" | "${TX_APP[@]}" tx gov submit-proposal "$PROPOSAL_IN_CONTAINER" \
  --from "$SUBMIT_KEY" \
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

echo "Voting yes from $SUBMIT_KEY..."
RAW_VOTE="$(printf '%s\n' "$KEYRING_PASSWORD" | "${TX_APP[@]}" tx gov vote "$PROPOSAL_ID" yes \
  --from "$SUBMIT_KEY" \
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
echo "Voting period: $VOTING_PERIOD"
echo "Track: ${APP[*]} query gov proposal $PROPOSAL_ID -o json ${NODE_OPTS[*]}"
echo ""
echo "When status is PROPOSAL_STATUS_PASSED, verify allowlist:"
echo "  ${APP[*]} query inference params -o json ${NODE_OPTS[*]} | jq '.params.devshard_escrow_params.allowed_creator_addresses'"
