#!/usr/bin/env bash
# Verify the guardian has no PoC v2 store commit for the target model at poc_start.
# A commit makes the guardian a direct member with per-model voting power (not weightless).
#
# Usage:
#   POC_START=520 GUARDIAN_PARTICIPANT=gonka1... MODEL="Qwen/..." \
#     SSH_HOST=... OBSERVER_PORT=... API_BASE=... \
#     ./guardian-tiebreaker-check-guardian-commit.sh
#
# Exit: 0 = no commit (weightless OK); 1 = commit present; 2 = query error
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/guardian-tiebreaker-config.sh"

POC_START="${POC_START:?set POC_START}"
GUARDIAN_PARTICIPANT="${GUARDIAN_PARTICIPANT:?set GUARDIAN_PARTICIPANT}"
CHAIN_API_PREFIX="${CHAIN_API_PREFIX:-${API_BASE}/chain-api/productscience/inference/inference}"

out="$(ssh -o BatchMode=yes -p "$OBSERVER_PORT" "$SSH_HOST" \
  "curl -fsS '${CHAIN_API_PREFIX}/all_poc_v2_store_commits/${POC_START}'" 2>/dev/null)" || {
  echo "ERROR: failed to query all_poc_v2_store_commits/$POC_START" >&2
  exit 2
}

count="$(echo "$out" | jq -r --arg g "$GUARDIAN_PARTICIPANT" --arg m "$MODEL" '
  [.commits[]? | select(.participant_address == $g and .model_id == $m) | .count] | first // empty' 2>/dev/null || true)"

if [[ -z "$count" ]]; then
  echo "OK guardian=$GUARDIAN_PARTICIPANT poc_start=$POC_START model=$MODEL commit=absent"
  exit 0
fi

echo "FAIL guardian=$GUARDIAN_PARTICIPANT poc_start=$POC_START model=$MODEL commit_count=$count"
exit 1
