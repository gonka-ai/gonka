#!/usr/bin/env bash
# Shared config for guardian tiebreaker E2E.
# Chain addresses and host-role map come from guardian-tiebreaker-fleet.env
# (generate via guardian-tiebreaker-sync-fleet.sh after chain reset).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
_fleet_env="$SCRIPT_DIR/guardian-tiebreaker-fleet.env"

# Infrastructure defaults (no on-chain addresses)
SSH_HOST="${SSH_HOST:-decentai@xj7-5.s.filfox.io}"
CHAIN_ID="${CHAIN_ID:-gonka-testnet-4}"
API_BASE="${API_BASE:-http://127.0.0.1:8000}"
MODEL="${MODEL:-Qwen/Qwen3-4B-Instruct-2507}"
MLNODE="${MLNODE:-join-mlnode-308-1}"

# Load synced fleet: participants, valoper, host IDs, ports, FLEET_JSON
if [[ -f "$_fleet_env" ]]; then
  # shellcheck disable=SC1091
  source "$_fleet_env"
fi

# Derive snip when fleet provides VICTIM but not VICTIM_SNIP
if [[ -n "${VICTIM:-}" && -z "${VICTIM_SNIP:-}" ]]; then
  VICTIM_SNIP="${VICTIM:0:13}"
fi
PREFERRED_VICTIM_PARTICIPANT="${PREFERRED_VICTIM_PARTICIPANT:-${VICTIM:-}}"

guardian_tiebreaker_require_fleet() {
  local missing=()
  local req=(
    GUARDIAN_PARTICIPANT GUARDIAN_VALOPER GUARDIAN_HOST_ID
    PREFERRED_VICTIM_PARTICIPANT PREFERRED_INVALID_PARTICIPANT PREFERRED_VALID_PARTICIPANT
    VICTIM_HOST_ID VOTE_INVALID_HOST_ID VOTE_VALID_HOST_ID
    VOTE_GUARDIAN_PORT VOTE_GUARDIAN_KEY VOTE_INVALID_PORT VOTE_INVALID_KEY
    VOTE_VALID_PORT VOTE_VALID_KEY FLEET_JSON
  )
  for v in "${req[@]}"; do
    if [[ -z "${!v:-}" ]]; then
      missing+=("$v")
    fi
  done
  if [[ ${#missing[@]} -gt 0 ]]; then
    echo "ERROR missing fleet config: ${missing[*]}" >&2
    echo "Run: $SCRIPT_DIR/guardian-tiebreaker-sync-fleet.sh" >&2
    return 1
  fi
  return 0
}
