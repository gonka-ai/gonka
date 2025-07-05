#!/usr/bin/env bash
set -euo pipefail

###############################################################################
# Configuration – edit these three lines if you need to.
###############################################################################
HOST="http://34.9.136.116:30000/"            # API host, e.g. "34.9.17.182:1317"
INFERENCED_BINARY="/Users/dima/cosmos/bin/inferenced"   # Path or name of your inferenced binary
REQUESTER_ADDRESS="gonka1mfyq5pe9z7eqtcx3mtysrh0g5a07969zxm6pfl"    # The extra address you want included
###############################################################################

# Nicely formatted timestamp like “5-may-14:15”
TIMESTAMP=$(date '+%-d-%b-%H:%M' | tr '[:upper:]' '[:lower:]')
OUTFILE="balances-${TIMESTAMP}.json"

# 1. Get participant addresses from the endpoint, then append REQUESTER_ADDRESS
ADDRESSES=$(curl -s "http://${HOST}/v1/epochs/current/participants" \
           | jq -r '.active_participants.participants[].index')
ADDRESSES="${ADDRESSES} ${REQUESTER_ADDRESS}"

# 2. Loop through each address, query its balance, build a JSON array
echo "["            | tee    "${OUTFILE}"
FIRST=1
for ADDR in ${ADDRESSES}; do
  BALANCE_JSON=$("${INFERENCED_BINARY}" query bank balances "${ADDR}" --output json)

  # Add commas between array elements after the first item
  if [[ ${FIRST} -eq 0 ]]; then
    echo ","        | tee -a "${OUTFILE}"
  fi
  FIRST=0

  echo "{\"address\":\"${ADDR}\",\"balance\":${BALANCE_JSON}}" \
                     | tee -a "${OUTFILE}"
done
echo "]"            | tee -a "${OUTFILE}"
