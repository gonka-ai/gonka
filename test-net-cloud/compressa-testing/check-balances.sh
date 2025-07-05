#!/usr/bin/env bash
set -euo pipefail

###############################################################################
# Configuration – edit these three lines if you need to.
###############################################################################
HOST="34.9.136.116:30000"            # API host, e.g. "34.9.17.182:1317"
INFERENCED_BINARY="kubectl -n genesis exec node-0 -- inferenced"   # inferenced cmd
if [ -z "${GONKA_ADDRESS}" ]; then
  REQUESTER_ADDRESS="gonka1mfyq5pe9z7eqtcx3mtysrh0g5a07969zxm6pfl"
  echo "Warning: GONKA_ADDRESS is not set. Using default address: $REQUESTER_ADDRESS" >&2
else
  REQUESTER_ADDRESS="$GONKA_ADDRESS"  # address to query balance for
  echo "Using GONKA_ADDRESS: $REQUESTER_ADDRESS" >&2
fi
   # extra address
###############################################################################

echo "=== balance-fetch script starting ===" >&2
echo "HOST=$HOST"                   >&2
echo "INFERENCED_BINARY=$INFERENCED_BINARY" >&2
echo "REQUESTER_ADDRESS=$REQUESTER_ADDRESS" >&2
echo "====================================" >&2

# ---------- NEW: make sure balances/ dir exists ----------
OUTDIR="balances"
mkdir -p "$OUTDIR"
# ---------------------------------------------------------

# Timestamp → “5-may-14:15”  (BSD `date` on macOS understands %-d / %-H / %-M)
TIMESTAMP=$(date '+%-d-%b-%H:%M' | tr '[:upper:]' '[:lower:]')
OUTFILE="${OUTDIR}/balances-${TIMESTAMP}.json"
echo "Output file will be: $OUTFILE" >&2

###############################################################################
# 1. Fetch participant list
###############################################################################
echo "Fetching participants …" >&2
PARTICIPANT_JSON=$(curl -sf "http://${HOST}/v1/epochs/current/participants") \
  || { echo "❌ curl failed – check HOST or network" >&2; exit 1; }

###############################################################################
# 2. Extract addresses
###############################################################################
ADDRESSES=()
while IFS= read -r line; do
  ADDRESSES+=("$line")
done < <(echo "$PARTICIPANT_JSON" | jq -r '.active_participants.participants[].index')

ADDRESSES+=("$REQUESTER_ADDRESS")                            # append requester
echo "Total addresses to query: ${#ADDRESSES[@]}" >&2

###############################################################################
# 3. Loop through addresses and query balances
###############################################################################
echo "[" | tee "$OUTFILE"        # open JSON array
FIRST=1
for ADDR in "${ADDRESSES[@]}"; do
  echo "→ Querying balance for $ADDR" >&2
  if BALANCE_JSON=$($INFERENCED_BINARY query bank balances "$ADDR" --output json); then
    [[ $FIRST -eq 0 ]] && echo "," | tee -a "$OUTFILE"
    FIRST=0
    echo "{\"address\":\"${ADDR}\",\"balance\":${BALANCE_JSON}}" | tee -a "$OUTFILE"
  else
    echo "❌ Balance query failed for $ADDR – check binary/path" >&2
    exit 1
  fi
done
echo "]" | tee -a "$OUTFILE"     # close JSON array

echo "✅ Done. Balances written to $OUTFILE" >&2
