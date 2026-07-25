#!/usr/bin/env bash
# After a PoC anchor, verify the victim is NOT preserved (legacy wrapper).
# Prefer guardian-tiebreaker-resolve-roles.sh for role swap support.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/guardian-tiebreaker-config.sh"

EXPECTED_ANCHOR="${EXPECTED_ANCHOR:-}"

out="$(REMOTE_SNAPSHOT=1 SSH_HOST="$SSH_HOST" OBSERVER_PORT="$OBSERVER_PORT" \
  API_BASE="$API_BASE" EXPECTED_ANCHOR="$EXPECTED_ANCHOR" MODEL="$MODEL" \
  GUARDIAN_PARTICIPANT="$GUARDIAN_PARTICIPANT" \
  PREFERRED_VICTIM_PARTICIPANT="$PREFERRED_VICTIM_PARTICIPANT" \
  PREFERRED_INVALID_PARTICIPANT="$PREFERRED_INVALID_PARTICIPANT" \
  PREFERRED_VALID_PARTICIPANT="$PREFERRED_VALID_PARTICIPANT" \
  FLEET_JSON="${FLEET_JSON:-}" \
  "$SCRIPT_DIR/guardian-tiebreaker-resolve-roles.sh" 2>&1)" || rc=$?

echo "$out"
rc="${rc:-0}"

if [[ "$rc" -eq 4 ]]; then
  exit 2
fi
exit "$rc"
