#!/usr/bin/env bash
set -euo pipefail       # die on any error, unset var, or failed pipeline

###############################################################################
# Configuration – edit these three lines if you need to.
###############################################################################
HOST="localhost:1317"            # API host, e.g. "34.9.17.182:1317"
INFERENCED_BINARY="inferenced"   # Path or name of your inferenced binary
REQUESTER_ADDRESS="gonka1..."    # The extra address you want included
###############################################################################

###############################################################################
# Debug banner
###############################################################################
echo "=== balance-fetch script starting ===" >&2
echo "HOST=$HOST"                   >&2
echo "INFERENCED_BINARY=$INFERENCED_BINARY" >&2
echo "REQUESTER_ADDRESS=$REQUESTER_ADDRESS" >&2
echo "====================================" >&2

# Nicely formatted timestamp like “5-may-14:15”
TIMESTAMP=$(date '+%-d-%b-%H:%M' | tr '[:upper:]' '[:lower:]')
OUTFILE="balances-${TIMESTAMP}.json"
echo "Output file will be: $OUTFILE" >&2

###############################################################################
# 1. Get participant list
###############################################################################
echo "Fetching participants from http://$HOST/v1/epochs/current/participants ..." >&2
PARTICIPANT_JSON=$(curl -sf "http://${HOST}/v1/epochs/current/participants") \
  || { echo "❌ curl failed (check HOST or network)" >&2; exit 1; }

###############################################################################
# 2. Extract addresses and add REQUESTER_ADDRESS
###############################################################################
readarray -t API_ADDRESSES < <(echo "$PARTICIPANT_JSON" | jq -r '.active_participants.participants[].index')

echo "Found ${#API_ADDRESSES[@]} addresses in API response." >&2
ADDRESSES=("${API_ADDRESSES[@]}" "$REQUESTER_ADDRESS")
echo "Total addresses to query (including requester): ${#ADDRESSES[@]}" >&2

###############################################################################
# 3. Loop through addresses, query balances
###############################################################################
echo "[" | tee "$OUTFILE"        # begin JSON array
FIRST=1
for ADDR in "${ADDRESSES[@]}"; do
  echo "→ Querying balance for $ADDR ..." >&2
  if BALANCE_JSON=$("$INFERENCED_BINARY" query bank balances "$ADDR" --output json); then
    # Comma between JSON objects
    [[ $FIRST -eq 0 ]] && echo "," | tee -a "$OUTFILE"
    FIRST=0
    echo "{\"address\":\"${ADDR}\",\"balance\":${BALANCE_JSON}}" | tee -a "$OUTFILE"
  else
    echo "❌ Balance query failed for $ADDR (check binary/path)" >&2
    exit 1
  fi
done
echo "]" | tee -a "$OUTFILE"     # end JSON array

echo "✅ Done. Balances written to $OUTFILE" >&2
