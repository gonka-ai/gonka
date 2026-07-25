#!/usr/bin/env bash
# Dry-run victim role swap: live snapshot + simulated preservation cases.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/guardian-tiebreaker-config.sh"
guardian_tiebreaker_require_fleet

MODEL="${MODEL:-Qwen/Qwen3-4B-Instruct-2507}"
PREF_VICTIM="$PREFERRED_VICTIM_PARTICIPANT"
PREF_INVALID="$PREFERRED_INVALID_PARTICIPANT"
PREF_VALID="$PREFERRED_VALID_PARTICIPANT"
PREFERRED_VICTIM_HOST_ID="$VICTIM_HOST_ID"

run_case() {
  local name="$1" mock="$2" expect_rc="${3:-0}"
  echo "========== CASE: $name =========="
  set +e
  out="$(SNAPSHOT_JSON_OVERRIDE="$mock" EXPECTED_ANCHOR=520 \
    GUARDIAN_PARTICIPANT="$GUARDIAN_PARTICIPANT" \
    PREFERRED_VICTIM_PARTICIPANT="$PREF_VICTIM" \
    PREFERRED_INVALID_PARTICIPANT="$PREF_INVALID" \
    PREFERRED_VALID_PARTICIPANT="$PREF_VALID" \
    MODEL="$MODEL" FLEET_JSON="$FLEET_JSON" \
    "$SCRIPT_DIR/guardian-tiebreaker-resolve-roles.sh" 2>&1)"
  rc=$?
  set -e
  echo "$out"
  echo "exit_code=$rc (expected $expect_rc)"
  if [[ "$rc" -ne "$expect_rc" ]]; then
    echo "FAIL case $name: rc $rc != $expect_rc"
    return 1
  fi
  echo "PASS case $name"
  echo
}

mock_snapshot() {
  local anchor="$1" preserved_participant="$2"
  if [[ -z "$preserved_participant" ]]; then
    python3 -c 'import json; print(json.dumps({"found":True,"snapshot":{"episode_anchor_height":"520","model_preserved_nodes":[]}}))'
  else
    python3 -c 'import json,sys; p=sys.argv[1]; print(json.dumps({"found":True,"snapshot":{"episode_anchor_height":"520","model_preserved_nodes":[{"model_id":"Qwen/Qwen3-4B-Instruct-2507","participants":[{"participant_id":p,"node_ids":["node1"]}]}]}}))' "$preserved_participant"
  fi
}

apply_orchestrator_vars() {
  local line
  unset VICTIM VICTIM_SNIP VICTIM_SSH_PORT VICTIM_HOST_ID ROLE_SWAP 2>/dev/null || true
  unset VOTE_INVALID_PORT VOTE_INVALID_KEY VOTE_INVALID_HOST_ID 2>/dev/null || true
  unset VOTE_VALID_PORT VOTE_VALID_KEY VOTE_VALID_HOST_ID 2>/dev/null || true
  while IFS= read -r line; do
    case "$line" in
      *=*)
        local key="${line%%=*}" val="${line#*=}"
        case "$key" in
          VICTIM|VICTIM_SNIP|VICTIM_SSH_PORT|VICTIM_HOST_ID|ROLE_SWAP)
            printf -v "$key" '%s' "$val"
            export "$key"
            ;;
          VOTE_INVALID_PORT|VOTE_INVALID_KEY|VOTE_INVALID_HOST_ID|VOTE_INVALID_PARTICIPANT)
            printf -v "$key" '%s' "$val"
            export "$key"
            ;;
          VOTE_VALID_PORT|VOTE_VALID_KEY|VOTE_VALID_HOST_ID|VOTE_VALID_PARTICIPANT)
            printf -v "$key" '%s' "$val"
            export "$key"
            ;;
        esac
        ;;
    esac
  done
}

echo "=== guardian tiebreaker role swap dry-run ==="
echo "preferred_victim=$PREF_VICTIM (host $PREFERRED_VICTIM_HOST_ID)"
echo

fail=0

# Case 1: nobody preserved — default roles
mock_none="$(mock_snapshot 520 '')"
run_case "no preservation (default victim mines)" "$mock_none" 0 || fail=1

# Case 2: default victim preserved — must swap
mock_victim="$(mock_snapshot 520 "$PREF_VICTIM")"
run_case "default victim preserved (swap required)" "$mock_victim" 0 || fail=1

# Verify swap assignments for case 2
echo "========== VERIFY: orchestrator var apply (victim preserved) =========="
out="$(SNAPSHOT_JSON_OVERRIDE="$mock_victim" EXPECTED_ANCHOR=520 \
  GUARDIAN_PARTICIPANT="$GUARDIAN_PARTICIPANT" \
  PREFERRED_VICTIM_PARTICIPANT="$PREF_VICTIM" \
  PREFERRED_INVALID_PARTICIPANT="$PREF_INVALID" \
  PREFERRED_VALID_PARTICIPANT="$PREF_VALID" \
  MODEL="$MODEL" FLEET_JSON="$FLEET_JSON" \
  "$SCRIPT_DIR/guardian-tiebreaker-resolve-roles.sh")"
apply_orchestrator_vars <<< "$out"
echo "ROLE_SWAP=${ROLE_SWAP:-unset}"
echo "VICTIM_HOST_ID=${VICTIM_HOST_ID:-unset} (must NOT be $PREFERRED_VICTIM_HOST_ID)"
echo "VICTIM_SSH_PORT=${VICTIM_SSH_PORT:-unset}"
echo "VOTE_INVALID_HOST_ID=${VOTE_INVALID_HOST_ID:-unset}"
echo "VOTE_VALID_HOST_ID=${VOTE_VALID_HOST_ID:-unset}"
if [[ "${ROLE_SWAP:-0}" != "1" ]]; then
  echo "FAIL ROLE_SWAP should be 1"
  fail=1
elif [[ "${VICTIM_HOST_ID:-}" == "$PREFERRED_VICTIM_HOST_ID" ]]; then
  echo "FAIL victim must not be preferred victim host when preserved"
  fail=1
elif [[ "${VOTE_VALID_HOST_ID:-}" != "$PREFERRED_VICTIM_HOST_ID" && "${VOTE_INVALID_HOST_ID:-}" != "$PREFERRED_VICTIM_HOST_ID" ]]; then
  echo "FAIL former preferred victim host should appear as a voter"
  fail=1
else
  echo "PASS swap: victim=$VICTIM_HOST_ID voters invalid=$VOTE_INVALID_HOST_ID valid=$VOTE_VALID_HOST_ID"
fi
echo

# Case 3: all non-guardian preserved — no miners
mock_all="$(FLEET_JSON="$FLEET_JSON" python3 - <<'PY'
import json, os
fleet = json.loads(os.environ["FLEET_JSON"])
parts = [h["participant"] for h in fleet if h.get("role") != "guardian"]
print(json.dumps({"found": True, "snapshot": {"episode_anchor_height": "520",
  "model_preserved_nodes": [{"model_id": "Qwen/Qwen3-4B-Instruct-2507",
    "participants": [{"participant_id": p, "node_ids": ["n1"]} for p in parts]}]}}))
PY
)"
run_case "all non-guardian preserved (no miners)" "$mock_all" 4 || fail=1

# Case 4: FLEET_JSON from fleet.env (catches ssh-env quoting regressions)
echo "========== CASE: FLEET_JSON from fleet.env =========="
rc=0
out="$(SNAPSHOT_JSON_OVERRIDE="$mock_none" EXPECTED_ANCHOR=520 \
  GUARDIAN_PARTICIPANT="$GUARDIAN_PARTICIPANT" \
  PREFERRED_VICTIM_PARTICIPANT="$PREF_VICTIM" \
  PREFERRED_INVALID_PARTICIPANT="$PREF_INVALID" \
  PREFERRED_VALID_PARTICIPANT="$PREF_VALID" \
  MODEL="$MODEL" FLEET_JSON="$FLEET_JSON" \
  "$SCRIPT_DIR/guardian-tiebreaker-resolve-roles.sh" 2>&1)" || rc=$?
rc="${rc:-0}"
echo "$out"
if [[ "$rc" -ne 0 ]]; then
  echo "FAIL FLEET_JSON case rc=$rc"
  fail=1
elif echo "$out" | grep -q 'env:.*No such file'; then
  echo "FAIL FLEET_JSON quoting regression detected"
  fail=1
else
  echo "PASS FLEET_JSON case"
fi
echo

# Case 5: live snapshot via REMOTE_SNAPSHOT (orchestrator path)
echo "========== LIVE snapshot (REMOTE_SNAPSHOT orchestrator path) =========="
ssh -o BatchMode=yes -p "$OBSERVER_PORT" "$SSH_HOST" \
  "curl -fsS ${API_BASE}/chain-api/productscience/inference/inference/preserved_nodes_snapshot | jq ." || true
echo
REMOTE_SNAPSHOT=1 SSH_HOST="$SSH_HOST" OBSERVER_PORT="$OBSERVER_PORT" \
  API_BASE="$API_BASE" EXPECTED_ANCHOR=520 \
  GUARDIAN_PARTICIPANT="$GUARDIAN_PARTICIPANT" \
  PREFERRED_VICTIM_PARTICIPANT="$PREF_VICTIM" \
  PREFERRED_INVALID_PARTICIPANT="$PREF_INVALID" \
  PREFERRED_VALID_PARTICIPANT="$PREF_VALID" \
  MODEL="$MODEL" FLEET_JSON="$FLEET_JSON" \
  "$SCRIPT_DIR/guardian-tiebreaker-resolve-roles.sh" || true
echo

if [[ "$fail" -eq 0 ]]; then
  echo "=== DRY-RUN OK ==="
  exit 0
fi
echo "=== DRY-RUN FAILED ==="
exit 1
