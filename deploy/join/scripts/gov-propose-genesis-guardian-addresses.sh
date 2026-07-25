#!/usr/bin/env bash
# Submit governance proposal: MsgUpdateParams to set
# params.genesis_guardian_params.guardian_addresses (gonkavaloper… operator addresses).
# "genesis_guardian" is the chain parameter name — NOT "must be the genesis validator host".
# For testnet-4 tiebreaker: set 702112's valoper (gonkavaloper1qge046…), not 702111 genesis.
#
# After a v0.2.14 chain upgrade, submit with a matching inferenced client (cosmovisor 2.14
# binary in the node container). Host /srv/dai/inferenced 0.2.13 cannot submit MsgUpdateParams.
#
# Uses the host inferenced binary and keyring under /srv/dai/.inference
# (same layout as gov-propose-devshard-approved-versions.sh).
#
# Guardian tiebreaker also requires genesis_only_params.genesis_guardian_enabled=true
# (set at genesis; not changed by this proposal). Preflight reads chain export from
# CHAIN_DATA_HOME (default: deploy/join/.inference mounted in the node container).
#
# Usage:
#   GUARDIAN_VALOPERATORS=gonkavaloper1abc...,gonkavaloper1def... \
#     ./scripts/gov-propose-genesis-guardian-addresses.sh
#
# List validators:
#   /srv/dai/inferenced query staking validators \
#     --home /srv/dai/.inference --node http://127.0.0.1:8000/chain-rpc/ \
#     --chain-id gonka-testnet-4 -o json \
#     | jq -r '.validators[] | "\(.description.moniker)\t\(.operator_address)"'
#
# Optional overrides:
#   INFERENCED  INFERENCE_HOME  CHAIN_DATA_HOME  GOV_KEY_NAME  KEYRING_PASSWORD
#   DEPLOY_DIR  CHAIN_ID  NODE_RPC  CHAIN_API  GUARDIAN_VALOPERATORS
#   FORCE=1  GENESIS_PREFLIGHT=1  (optional: run slow export check)
#
# Run from deploy/join or /srv/dai — uses GOV_KEY_NAME (default gonka-account-key),
# not config.env KEY_NAME (often "genesis" for the node operator key).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -n "${DEPLOY_DIR:-}" && -f "$DEPLOY_DIR/config.env" ]]; then
  JOIN_DIR="$DEPLOY_DIR"
elif [[ -f "$SCRIPT_DIR/../config.env" ]]; then
  JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
elif [[ -d /srv/dai/gonka/deploy/join ]]; then
  JOIN_DIR=/srv/dai/gonka/deploy/join
else
  JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
fi

if [[ -f "$JOIN_DIR/config.env" ]]; then
  # shellcheck disable=SC1091
  source "$JOIN_DIR/config.env"
fi

if [[ -x /srv/dai/inferenced ]]; then
  INFERENCED="${INFERENCED:-/srv/dai/inferenced}"
elif [[ -x /srv/dai/inference/inferenced ]]; then
  INFERENCED="${INFERENCED:-/srv/dai/inference/inferenced}"
else
  INFERENCED="${INFERENCED:-/srv/dai/inferenced}"
fi

INFERENCE_HOME="${INFERENCE_HOME:-${INFERENCED_HOME:-/srv/dai/.inference}}"
if [[ -n "${CHAIN_DATA_HOME:-}" ]]; then
  :
elif [[ -d "$JOIN_DIR/.inference/data" ]]; then
  CHAIN_DATA_HOME="$JOIN_DIR/.inference"
elif [[ -d /srv/dai/gonka/deploy/join/.inference/data ]]; then
  CHAIN_DATA_HOME=/srv/dai/gonka/deploy/join/.inference
else
  CHAIN_DATA_HOME="$JOIN_DIR/.inference"
fi
# Funded gov proposer in /srv/dai/.inference — not the join-stack KEY_NAME (genesis).
GOV_KEY_NAME="${GOV_KEY_NAME:-gonka-account-key}"
KEYRING_PASSWORD="${KEYRING_PASSWORD:?Set KEYRING_PASSWORD}"
KEYRING_BACKEND="${KEYRING_BACKEND:-file}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-4}"
NODE_RPC="${NODE_RPC:-http://127.0.0.1:${API_PORT:-8000}/chain-rpc/}"
CHAIN_API="${CHAIN_API:-http://127.0.0.1:${API_PORT:-8000}/chain-api}"
: "${GUARDIAN_VALOPERATORS:?Set GUARDIAN_VALOPERATORS to comma-separated gonkavaloper… addresses}"

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

with_keyring() {
  printf '%s\n' "$KEYRING_PASSWORD" | "${APP[@]}" "$@"
}

IFS=',' read -r -a NEW_GUARDIANS <<< "$GUARDIAN_VALOPERATORS"
if [[ "${#NEW_GUARDIANS[@]}" -eq 0 ]]; then
  echo "GUARDIAN_VALOPERATORS is empty" >&2
  exit 1
fi

echo "inferenced:       $INFERENCED"
echo "home (keyring):   $INFERENCE_HOME"
echo "deploy dir:       $JOIN_DIR"
echo "chain data home:  $CHAIN_DATA_HOME"
echo "from:             $GOV_KEY_NAME"
echo "chain:            $CHAIN_ID"
echo "node:             $NODE_RPC"
echo "Proposed genesis guardian operator addresses:"
printf '  %s\n' "${NEW_GUARDIANS[@]}"
echo ""

if ! PROPOSER="$(with_keyring keys show "$GOV_KEY_NAME" -a "${KEY_OPTS[@]}" 2>/dev/null)"; then
  echo "Key '$GOV_KEY_NAME' not found under $INFERENCE_HOME (or wrong KEYRING_PASSWORD)" >&2
  echo "Available keys:" >&2
  with_keyring keys list "${KEY_OPTS[@]}" 2>/dev/null >&2 || true
  echo "Set GOV_KEY_NAME to a funded key in $INFERENCE_HOME" >&2
  exit 1
fi
echo "proposer:         $PROPOSER"

BALANCE="$("${APP[@]}" query bank balances "$PROPOSER" -o json "${QUERY_OPTS[@]}" </dev/null \
  | jq -r '[.balances[] | select(.denom=="ngonka") | .amount][0] // "0"')"
echo "balance:          ${BALANCE}ngonka"
echo ""

echo "Preflight: genesis_only_params.genesis_guardian_enabled (must be true for tiebreaker)..."
GENESIS_ONLY_ENABLED="unknown"
if [[ "${GENESIS_PREFLIGHT:-0}" == "1" ]]; then
  echo "  Running chain export (slow; may fail while node is running)..."
  if GENESIS_ONLY_ENABLED="$(
    timeout 120 "${APP[@]}" export --home "$CHAIN_DATA_HOME" 2>/dev/null \
      | jq -r '.app_state.inference.genesis_only_params.genesis_guardian_enabled // "missing"'
  )"; then
    echo "  genesis_guardian_enabled=$GENESIS_ONLY_ENABLED (export --home $CHAIN_DATA_HOME)"
  else
    echo "  export failed or timed out; treating genesis_guardian_enabled as unknown" >&2
    GENESIS_ONLY_ENABLED="unknown"
  fi
else
  echo "  Skipping export (default). Full export is slow and often fails on a live node."
  echo "  Set GENESIS_PREFLIGHT=1 to attempt export, or FORCE=1 to submit anyway."
fi

if [[ "$GENESIS_ONLY_ENABLED" != "true" ]]; then
  echo "WARNING: guardian tiebreaker may stay off without genesis_only_params.genesis_guardian_enabled=true." >&2
  echo "This proposal only sets params.genesis_guardian_params.guardian_addresses." >&2
  if [[ "${FORCE:-0}" != "1" ]]; then
    echo "Re-run with FORCE=1 to submit anyway." >&2
    exit 1
  fi
  echo "FORCE=1 set; continuing."
fi

echo "Fetching current inference params from $CHAIN_API ..."
CURRENT_PARAMS="$(curl -fsS "$CHAIN_API/productscience/inference/inference/params")"
echo "$CURRENT_PARAMS" | jq '.params.genesis_guardian_params' >/dev/null

CURRENT_GUARDIANS="$(echo "$CURRENT_PARAMS" | jq -r '.params.genesis_guardian_params.guardian_addresses[]?')"
for g in "${NEW_GUARDIANS[@]}"; do
  if echo "$CURRENT_GUARDIANS" | grep -qxF "$g"; then
    echo "Already a guardian: $g"
  fi
done

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

GUARDIANS_JSON="$(printf '%s\n' "${NEW_GUARDIANS[@]}" | jq -R . | jq -s 'unique')"
PATCHED_PARAMS="$(echo "$CURRENT_PARAMS" | jq \
  --argjson guardians "$GUARDIANS_JSON" \
  '.params
    | .genesis_guardian_params = (.genesis_guardian_params // {})
    | .genesis_guardian_params.guardian_addresses = $guardians
    | .genesis_guardian_params.network_maturity_threshold = (
        .genesis_guardian_params.network_maturity_threshold // "2000000")
    | .genesis_guardian_params.network_maturity_min_height = (
        .genesis_guardian_params.network_maturity_min_height // "0")')"

PROPOSAL_FILE="${INFERENCE_HOME}/gov-proposal-genesis-guardian-addresses.json"

jq -n \
  --arg auth "$GOV_AUTH" \
  --arg deposit "$DEPOSIT" \
  --argjson guardians "$GUARDIANS_JSON" \
  --argjson params "$PATCHED_PARAMS" \
  '{
    messages: [
      {
        "@type": "/inference.inference.MsgUpdateParams",
        authority: $auth,
        params: $params
      }
    ],
    metadata: "genesis-guardian-addresses",
    deposit: $deposit,
    title: "Set genesis guardian operator addresses",
    summary: ("Set genesis_guardian_params.guardian_addresses to " + ($guardians | join(", ")))
  }' >"$PROPOSAL_FILE"

echo "Wrote $PROPOSAL_FILE"
jq '{
  title,
  deposit,
  summary,
  guardian_addresses: .messages[0].params.genesis_guardian_params.guardian_addresses
}' "$PROPOSAL_FILE"

echo "Submitting proposal..."
RAW_SUBMIT="$(with_keyring tx gov submit-proposal "$PROPOSAL_FILE" \
  --from "$GOV_KEY_NAME" \
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

echo "Voting yes from $GOV_KEY_NAME..."
RAW_VOTE="$(with_keyring tx gov vote "$PROPOSAL_ID" yes \
  --from "$GOV_KEY_NAME" \
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
echo "Vote YES from the other validators, then verify:"
echo "  curl -sS $CHAIN_API/productscience/inference/inference/params \\"
echo "    | jq '.params.genesis_guardian_params.guardian_addresses'"
echo ""
echo "Track:"
echo "  $INFERENCED query gov proposal $PROPOSAL_ID -o json --home $INFERENCE_HOME --node $NODE_RPC --chain-id $CHAIN_ID"
