#!/bin/bash
# Send a starting balance from the genesis account to a join node's signing key.
# Join APIs sign as ${KEY_NAME}-WARM, which is created after genesis and has no coins.
# PoC commits and hardware diffs are not fee-exempt, so an empty key cannot participate.
#
# Usage: ./fund-join.sh <compose-project> <key-name>
# Example: ./fund-join.sh join1 join1-WARM
set -euo pipefail

PROJECT="${1:?compose project, for example join1}"
KEY_NAME="${2:?key name inside the api container, for example join1-WARM}"
AMOUNT="${JOIN_FUND_AMOUNT:-500000000ngonka}"
FEE="${JOIN_FUND_FEE:-200000ngonka}"
CHAIN_ID="${CHAIN_ID:-gonka-mainnet}"

address=""
for _ in $(seq 1 30); do
  address="$(docker exec "${PROJECT}-api" inferenced keys show "$KEY_NAME" \
    --address --keyring-backend test --keyring-dir /root/.inference 2>/dev/null || true)"
  if [ -n "$address" ]; then
    break
  fi
  sleep 2
done
if [ -z "$address" ]; then
  echo "fund-join: key ${KEY_NAME} not found in ${PROJECT}-api" >&2
  exit 1
fi

existing="$(docker exec genesis-node inferenced query bank balances "$address" \
  --node tcp://localhost:26657 -o json | python3 -c 'import json,sys; bals=json.load(sys.stdin).get("balances") or []; print(next((b["amount"] for b in bals if b["denom"]=="ngonka"), "0"))')"
if [ "$existing" != "0" ]; then
  echo "fund-join: ${KEY_NAME} ${address} already has ${existing}ngonka"
  exit 0
fi

echo "fund-join: sending ${AMOUNT} from genesis to ${KEY_NAME} ${address}"
for attempt in 1 2 3 4 5; do
  result="$(docker exec genesis-node inferenced tx bank send genesis "$address" "$AMOUNT" \
    --keyring-backend test \
    --chain-id "$CHAIN_ID" \
    --node tcp://localhost:26657 \
    --gas 200000 \
    --fees "$FEE" \
    --yes -o json)"
  echo "$result"
  code="$(printf '%s' "$result" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("code", 1))')"
  if [ "$code" = "0" ]; then
    break
  fi
  if [ "$attempt" = "5" ]; then
    echo "fund-join: bank send to ${address} failed with code ${code}" >&2
    exit 1
  fi
  sleep 3
done
