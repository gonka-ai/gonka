#!/usr/bin/env bash
# Resolve victim / invalid / valid voter roles from the preserved-node snapshot.
# If the preferred victim is preserved, pick another non-preserved miner as victim
# and move the preserved host into the voter pool (manual votes still work).
#
# Prints shell assignments (eval-friendly) on stdout.
#
# Env:
#   REMOTE_SNAPSHOT=1  fetch snapshot via SSH_HOST + OBSERVER_PORT (laptop workflow)
#   SNAPSHOT_JSON_OVERRIDE  skip fetch (dry-run / tests)
#   FLEET_JSON + participant vars from guardian-tiebreaker-fleet.env (required)
#
# Exit codes:
#   0  roles resolved
#   1  query/parse error
#   4  no mining candidate (all non-guardian hosts preserved)
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/guardian-tiebreaker-config.sh"
guardian_tiebreaker_require_fleet

API_BASE="${API_BASE:-http://127.0.0.1:8000}"
EXPECTED_ANCHOR="${EXPECTED_ANCHOR:-}"
MODEL="${MODEL:-Qwen/Qwen3-4B-Instruct-2507}"
REMOTE_SNAPSHOT="${REMOTE_SNAPSHOT:-0}"
SSH_HOST="${SSH_HOST:-}"
OBSERVER_PORT="${OBSERVER_PORT:-}"
SNAPSHOT_JSON_OVERRIDE="${SNAPSHOT_JSON_OVERRIDE:-}"

PRESERVED_SNAPSHOT_PATH="/chain-api/productscience/inference/inference/preserved_nodes_snapshot"

fetch_snapshot() {
  if [[ -n "$SNAPSHOT_JSON_OVERRIDE" ]]; then
    printf '%s' "$SNAPSHOT_JSON_OVERRIDE"
    return 0
  fi
  if [[ "$REMOTE_SNAPSHOT" == "1" ]]; then
    [[ -n "$SSH_HOST" && -n "$OBSERVER_PORT" ]] || {
      echo "ERROR REMOTE_SNAPSHOT=1 requires SSH_HOST and OBSERVER_PORT" >&2
      return 1
    }
    ssh -o BatchMode=yes -p "$OBSERVER_PORT" "$SSH_HOST" \
      "curl -fsS '${API_BASE}${PRESERVED_SNAPSHOT_PATH}'"
    return 0
  fi
  curl -fsS "${API_BASE}${PRESERVED_SNAPSHOT_PATH}"
}

json="$(fetch_snapshot)"

python3 - <<'PY' "$json" "$MODEL" "$EXPECTED_ANCHOR" "$GUARDIAN_PARTICIPANT" \
  "$PREFERRED_VICTIM_PARTICIPANT" "$PREFERRED_INVALID_PARTICIPANT" "$PREFERRED_VALID_PARTICIPANT" "$FLEET_JSON"
import json, sys

raw = json.loads(sys.argv[1])
model = sys.argv[2]
expected_anchor = sys.argv[3]
guardian = sys.argv[4]
pref_victim = sys.argv[5]
pref_invalid = sys.argv[6]
pref_valid = sys.argv[7]
fleet_json = sys.argv[8]

if not fleet_json.strip():
    print("ERROR FLEET_JSON is required (run guardian-tiebreaker-sync-fleet.sh)", file=sys.stderr)
    sys.exit(1)
fleet = json.loads(fleet_json)

found = bool(raw.get("found"))
snap = raw.get("snapshot") or {}
anchor = snap.get("episode_anchor_height", "")
models = snap.get("model_preserved_nodes") or []

if expected_anchor and anchor and str(anchor) != str(expected_anchor):
    print(f"WARN snapshot anchor={anchor} expected={expected_anchor} (stale or not yet updated)", file=sys.stderr)
    if not found:
        sys.exit(1)

preserved = set()
for entry in models:
    if entry.get("model_id") != model:
        continue
    for p in entry.get("participants") or []:
        pid = p.get("participant_id") or p.get("participantId") or ""
        if pid:
            preserved.add(pid)

non_guardian = [h for h in fleet if h.get("role") != "guardian" and h.get("participant") != guardian]
miners = [h for h in non_guardian if h["participant"] not in preserved]

def snip(addr: str) -> str:
    return addr[:13] if len(addr) >= 13 else addr

def pick_victim():
    for h in non_guardian:
        if h["participant"] == pref_victim and h in miners:
            return h
    miners_sorted = sorted(miners, key=lambda h: h["host"])
    return miners_sorted[0] if miners_sorted else None

def pick_voter(candidates, preferred):
    for h in candidates:
        if h["participant"] == preferred:
            return h
    return sorted(candidates, key=lambda h: h["host"])[0] if candidates else None

print(f"anchor={anchor}")
print(f"found={str(found).lower()}")
print(f"model={model}")
if preserved:
    print(f"preserved_participants={','.join(sorted(preserved))}")
else:
    print("preserved_participants=")

if not miners:
    print("ERROR no mining candidate — all non-guardian hosts are preserved", file=sys.stderr)
    sys.exit(4)

victim = pick_victim()
remaining = [h for h in non_guardian if h["host"] != victim["host"]]
invalid = pick_voter(remaining, pref_invalid)
remaining2 = [h for h in remaining if h["host"] != invalid["host"]]
valid = pick_voter(remaining2, pref_valid)

role_swap = victim["participant"] != pref_victim
print(f"ROLE_SWAP={1 if role_swap else 0}")
print(f"miner_candidates={','.join(h['participant'] for h in miners)}")
print(f"VICTIM={victim['participant']}")
print(f"VICTIM_SNIP={snip(victim['participant'])}")
print(f"VICTIM_SSH_PORT={victim['port']}")
print(f"VICTIM_HOST_ID={victim['host']}")
print(f"VOTE_INVALID_PORT={invalid['port']}")
print(f"VOTE_INVALID_KEY={invalid['key']}")
print(f"VOTE_INVALID_HOST_ID={invalid['host']}")
print(f"VOTE_INVALID_PARTICIPANT={invalid['participant']}")
print(f"VOTE_VALID_PORT={valid['port']}")
print(f"VOTE_VALID_KEY={valid['key']}")
print(f"VOTE_VALID_HOST_ID={valid['host']}")
print(f"VOTE_VALID_PARTICIPANT={valid['participant']}")

if role_swap:
    print(f"NOTE preferred victim {pref_victim} preserved; mining victim -> {victim['host']} ({victim['participant']})")
    if pref_victim in preserved:
        print(f"NOTE former victim {pref_victim} is a voter this episode (preserved hosts can submit manual votes)")
else:
    print(f"NOTE victim {victim['host']} ({victim['participant']}) will mine")
PY
