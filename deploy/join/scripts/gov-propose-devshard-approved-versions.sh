#!/usr/bin/env bash
# Submit governance proposal: MsgUpdateParams to set devshard_escrow_params.approved_versions.
#
# Uses the host inferenced binary and genesis keyring under /srv/dai/.inference
# (same layout as bridge scripts on Nebius testnet).
#
# Default: register v1 (v0.2.13-devshard-v1 zip) and v2 (devshard/v2.0.0 zip).
#
# Usage:
#   ./scripts/gov-propose-devshard-approved-versions.sh
#
# Optional overrides:
#   INFERENCED  KEY_NAME  KEYRING_PASSWORD  INFERENCE_HOME  CHAIN_ID  NODE_RPC
#   CHAIN_API   V1_BINARY_URL V1_SHA256 V2_BINARY_URL V2_SHA256
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# Host inferenced + keyring (genesis-funded account)
if [[ -x /srv/dai/inferenced ]]; then
  INFERENCED="${INFERENCED:-/srv/dai/inferenced}"
elif [[ -x /srv/dai/inference/inferenced ]]; then
  INFERENCED="${INFERENCED:-/srv/dai/inference/inferenced}"
else
  INFERENCED="${INFERENCED:-/srv/dai/inferenced}"
fi

INFERENCE_HOME="${INFERENCE_HOME:-/srv/dai/.inference}"
KEY_NAME="${KEY_NAME:-gonka-account-key}"
KEYRING_PASSWORD="${KEYRING_PASSWORD:-12345678}"
KEYRING_BACKEND="${KEYRING_BACKEND:-file}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-4}"
NODE_RPC="${NODE_RPC:-http://127.0.0.1:8000/chain-rpc/}"
CHAIN_API="${CHAIN_API:-http://127.0.0.1:8000/chain-api}"

V1_NAME="${V1_NAME:-v1}"
V2_NAME="${V2_NAME:-v2}"
V1_BINARY_URL="${V1_BINARY_URL:-https://github.com/gonka-ai/gonka/releases/download/release%2Fv0.2.13-devshard-v1/devshardd.zip}"
V1_SHA256="${V1_SHA256:-dad6f1b97843816c0a33874b89ac403e48b54fe3aa1a0fdccb228d89d2a5594c}"
V2_BINARY_URL="${V2_BINARY_URL:-https://github.com/gonka-ai/gonka/releases/download/release%2Fdevshard%2Fv2.0.0/devshardd.zip}"
V2_SHA256="${V2_SHA256:-1a3b58bd0ac20dbb8baa68b1bf6c80516ca3c0f4d39e06160d07613ec1b1340b}"

if [[ ! -x "$INFERENCED" ]]; then
  echo "inferenced not found or not executable: $INFERENCED" >&2
  exit 1
fi
if [[ ! -d "$INFERENCE_HOME" ]]; then
  echo "inference home not found: $INFERENCE_HOME" >&2
  exit 1
fi

APP=("$INFERENCED")
KEY_OPTS=(--home "$INFERENCE_HOME" --keyring-dir "$INFERENCE_HOME" --keyring-backend "$KEYRING_BACKEND")
QUERY_OPTS=(--home "$INFERENCE_HOME" --node "$NODE_RPC" --chain-id "$CHAIN_ID")
TX_OPTS=(--home "$INFERENCE_HOME" --keyring-dir "$INFERENCE_HOME" --keyring-backend "$KEYRING_BACKEND" --node "$NODE_RPC" --chain-id "$CHAIN_ID")

echo "inferenced: $INFERENCED"
echo "home:       $INFERENCE_HOME"
echo "from:       $KEY_NAME"
echo "chain:      $CHAIN_ID"
echo "node:       $NODE_RPC"
echo ""
echo "Setting approved_versions:"
echo "  $V1_NAME  $V1_BINARY_URL"
echo "           sha256=$V1_SHA256"
echo "  $V2_NAME  $V2_BINARY_URL"
echo "           sha256=$V2_SHA256"

PROPOSER="$("${APP[@]}" keys show "$KEY_NAME" -a "${KEY_OPTS[@]}" </dev/null)"
echo "proposer:   $PROPOSER"

BALANCE="$("${APP[@]}" query bank balances "$PROPOSER" -o json "${QUERY_OPTS[@]}" </dev/null \
  | jq -r '[.balances[] | select(.denom=="ngonka") | .amount][0] // "0"')"
echo "balance:    ${BALANCE}ngonka (spendable may differ if vested)"

echo "Fetching current inference params from $CHAIN_API ..."
CURRENT_PARAMS="$(curl -fsS "$CHAIN_API/productscience/inference/inference/params")"
echo "$CURRENT_PARAMS" | jq '.params.devshard_escrow_params' >/dev/null

echo "Resolving gov module authority..."
GOV_AUTH="$("${APP[@]}" query auth module-account gov -o json "${QUERY_OPTS[@]}" </dev/null | jq -r '.account.value.address')"
if [[ -z "$GOV_AUTH" || "$GOV_AUTH" == "null" ]]; then
  echo "Failed to resolve gov module account address" >&2
  exit 1
fi
echo "Gov authority: $GOV_AUTH"

MIN_DEPOSIT_AMOUNT="$("${APP[@]}" query gov params -o json "${QUERY_OPTS[@]}" </dev/null | jq -r '.params.min_deposit[0].amount')"
MIN_DEPOSIT_DENOM="$("${APP[@]}" query gov params -o json "${QUERY_OPTS[@]}" </dev/null | jq -r '.params.min_deposit[0].denom')"
DEPOSIT="${MIN_DEPOSIT_AMOUNT}${MIN_DEPOSIT_DENOM}"
VOTING_PERIOD="$("${APP[@]}" query gov params -o json "${QUERY_OPTS[@]}" </dev/null | jq -r '.params.voting_period')"
echo "min deposit: $DEPOSIT"

PATCHED_PARAMS="$(echo "$CURRENT_PARAMS" | jq \
  --arg v1name "$V1_NAME" --arg v1url "$V1_BINARY_URL" --arg v1sha "$V1_SHA256" \
  --arg v2name "$V2_NAME" --arg v2url "$V2_BINARY_URL" --arg v2sha "$V2_SHA256" \
  '.params
    | .devshard_escrow_params.approved_versions = (
        (.devshard_escrow_params.approved_versions // [])
        | map(select(.name != $v1name and .name != $v2name))
        + [
            {name: $v1name, binary: $v1url, sha256: $v1sha},
            {name: $v2name, binary: $v2url, sha256: $v2sha}
          ]
      )
    | .devshard_escrow_params.allowed_creator_addresses = (.devshard_escrow_params.allowed_creator_addresses // [])
    | .devshard_escrow_params.max_nonce = (
        (.devshard_escrow_params.max_nonce // 0) | if . > 0 then . else 1000000 end)
    | .devshard_escrow_params.devshard_requests_enabled = (
        .devshard_escrow_params.devshard_requests_enabled // true)')"

PROPOSAL_FILE="${INFERENCE_HOME}/gov-proposal-devshard-versions.json"

jq -n \
  --arg auth "$GOV_AUTH" \
  --arg deposit "$DEPOSIT" \
  --argjson params "$PATCHED_PARAMS" \
  '{
    messages: [
      {
        "@type": "/inference.inference.MsgUpdateParams",
        authority: $auth,
        params: $params
      }
    ],
    metadata: "devshard-approved-versions",
    deposit: $deposit,
    title: "Register devshard v1 and v2 approved_versions",
    summary: "Set devshard_escrow_params.approved_versions to v1 (v0.2.13-devshard-v1) and v2 (devshard/v2.0.0) GitHub release zips"
  }' >"$PROPOSAL_FILE"

echo "Wrote $PROPOSAL_FILE"
jq '{
  title,
  deposit,
  approved_versions: .messages[0].params.devshard_escrow_params.approved_versions
}' "$PROPOSAL_FILE"

echo "Submitting proposal..."
RAW_SUBMIT="$(printf '%s\n' "$KEYRING_PASSWORD" | "${APP[@]}" tx gov submit-proposal "$PROPOSAL_FILE" \
  --from "$KEY_NAME" \
  "${TX_OPTS[@]}" \
  --gas auto \
  --gas-adjustment 1.5 \
  --yes \
  --output json 2>&1)" || true

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

PROPOSAL_ID="$("${APP[@]}" query gov proposals -o json "${QUERY_OPTS[@]}" </dev/null | jq -r '.proposals[-1].id')"
STATUS="$("${APP[@]}" query gov proposal "$PROPOSAL_ID" -o json "${QUERY_OPTS[@]}" </dev/null | jq -r '.status')"
echo "Proposal id=$PROPOSAL_ID status=$STATUS"

echo "Voting yes from $KEY_NAME..."
RAW_VOTE="$(printf '%s\n' "$KEYRING_PASSWORD" | "${APP[@]}" tx gov vote "$PROPOSAL_ID" yes \
  --from "$KEY_NAME" \
  "${TX_OPTS[@]}" \
  --gas auto \
  --gas-adjustment 1.5 \
  --yes \
  --output json 2>&1)" || true

VOTE_JSON="$(echo "$RAW_VOTE" | sed -n '/{/,$p')"
if [[ "$(echo "$VOTE_JSON" | jq -r '.code // 1')" != "0" ]]; then
  echo "Vote failed:" >&2
  echo "$RAW_VOTE" >&2
  exit 1
fi
echo "Vote tx: $(echo "$VOTE_JSON" | jq -r '.txhash')"

echo ""
echo "Voting period: $VOTING_PERIOD"
echo "Track:"
echo "  $INFERENCED query gov proposal $PROPOSAL_ID -o json --home $INFERENCE_HOME --node $NODE_RPC --chain-id $CHAIN_ID"
echo ""
echo "After PROPOSAL_STATUS_PASSED, on EACH host with versiond:"
echo "  curl -s http://127.0.0.1:9100/versions | jq ."
echo "  docker logs versiond 2>&1 | tail -30"
echo "  curl -s http://127.0.0.1:8000/devshard/v1/healthz; echo"
echo "  curl -s http://127.0.0.1:8000/devshard/v2/healthz; echo"
