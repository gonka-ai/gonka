#!/usr/bin/env bash
# Regression self-test for guardian tiebreaker scripts.
# Catches ssh-env / FLEET_JSON quoting bugs before orchestrator run.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/guardian-tiebreaker-config.sh"

guardian_tiebreaker_require_fleet

fail=0
pass() { echo "PASS $*"; }
bad() { echo "FAIL $*"; fail=1; }

echo "=== guardian tiebreaker selftest ==="

# 1) FLEET_JSON must parse when sourced from fleet.env
if [[ -z "${FLEET_JSON:-}" ]]; then
  bad "FLEET_JSON empty — run guardian-tiebreaker-sync-fleet.sh"
else
  FLEET_JSON="$FLEET_JSON" python3 - <<'PY' || bad "FLEET_JSON is invalid JSON"
import json, os
fleet = json.loads(os.environ["FLEET_JSON"])
assert len(fleet) == 4, fleet
for row in fleet:
    assert row.get("host") and row.get("port") and row.get("participant")
print(f"fleet entries={len(fleet)}")
PY
  pass "FLEET_JSON valid"
fi

# 2) ALL_API_PORTS must align with HOST_IDS (same index = same host)
if [[ ${#ALL_API_PORTS[@]} -eq ${#HOST_IDS[@]} ]]; then
  aligned=true
  if [[ -n "${FLEET_JSON:-}" ]]; then
    FLEET_JSON="$FLEET_JSON" HOST_IDS_CSV="${HOST_IDS[*]}" PORTS_CSV="${ALL_API_PORTS[*]}" python3 - <<'PY' || aligned=false
import json, os
fleet = {row["host"]: row["port"] for row in json.loads(os.environ["FLEET_JSON"])}
hosts = os.environ["HOST_IDS_CSV"].split()
ports = os.environ["PORTS_CSV"].split()
for h, p in zip(hosts, ports):
    if str(fleet.get(h)) != str(p):
        raise SystemExit(f"mismatch host {h}: array port {p} fleet port {fleet.get(h)}")
PY
  fi
  if [[ "$aligned" == true ]]; then
    pass "ALL_API_PORTS aligned with HOST_IDS"
  else
    bad "ALL_API_PORTS / HOST_IDS misaligned — re-run guardian-tiebreaker-sync-fleet.sh"
  fi
else
  bad "ALL_API_PORTS (${#ALL_API_PORTS[@]}) and HOST_IDS (${#HOST_IDS[@]}) length mismatch"
fi

# 3) Local resolve with mock snapshot + FLEET_JSON (orchestrator path)
mock='{"found":true,"snapshot":{"episode_anchor_height":"520","model_preserved_nodes":[]}}'
out="$(SNAPSHOT_JSON_OVERRIDE="$mock" EXPECTED_ANCHOR=520 \
  GUARDIAN_PARTICIPANT="$GUARDIAN_PARTICIPANT" \
  PREFERRED_VICTIM_PARTICIPANT="$PREFERRED_VICTIM_PARTICIPANT" \
  PREFERRED_INVALID_PARTICIPANT="$PREFERRED_INVALID_PARTICIPANT" \
  PREFERRED_VALID_PARTICIPANT="$PREFERRED_VALID_PARTICIPANT" \
  MODEL="$MODEL" FLEET_JSON="${FLEET_JSON:-}" \
  "$SCRIPT_DIR/guardian-tiebreaker-resolve-roles.sh" 2>&1)" || rc=$?
rc="${rc:-0}"
if [[ "$rc" -ne 0 ]]; then
  bad "mock resolve rc=$rc"
  echo "$out"
elif echo "$out" | grep -qE "env:.*No such file|command not found"; then
  bad "quoting regression in mock resolve"
  echo "$out"
else
  pass "mock resolve with FLEET_JSON"
fi

# 4) REMOTE_SNAPSHOT live fetch (orchestrator path — must not pipe script over ssh)
out="$(REMOTE_SNAPSHOT=1 SSH_HOST="$SSH_HOST" OBSERVER_PORT="$OBSERVER_PORT" \
  API_BASE="$API_BASE" EXPECTED_ANCHOR="" MODEL="$MODEL" \
  GUARDIAN_PARTICIPANT="$GUARDIAN_PARTICIPANT" \
  PREFERRED_VICTIM_PARTICIPANT="$PREFERRED_VICTIM_PARTICIPANT" \
  PREFERRED_INVALID_PARTICIPANT="$PREFERRED_INVALID_PARTICIPANT" \
  PREFERRED_VALID_PARTICIPANT="$PREFERRED_VALID_PARTICIPANT" \
  FLEET_JSON="${FLEET_JSON:-}" \
  "$SCRIPT_DIR/guardian-tiebreaker-resolve-roles.sh" 2>&1)" || rc=$?
rc="${rc:-0}"
if [[ "$rc" -ne 0 ]]; then
  bad "live REMOTE_SNAPSHOT resolve rc=$rc"
  echo "$out"
elif echo "$out" | grep -qE "env:.*No such file|command not found"; then
  bad "quoting regression in live resolve"
  echo "$out"
else
  pass "live REMOTE_SNAPSHOT resolve"
  echo "$out" | head -8
fi

# 5) Document forbidden pattern: FLEET_JSON via ssh env breaks
broken_rc=0
broken_out="$(ssh -o BatchMode=yes -p "$OBSERVER_PORT" "$SSH_HOST" \
  env FLEET_JSON="${FLEET_JSON:-[]}" bash -c 'echo ok' 2>&1)" || broken_rc=$?
if [[ "$broken_rc" -eq 0 ]] && [[ "$broken_out" == "ok" ]]; then
  pass "ssh env FLEET_JSON sanity (remote echo only)"
else
  # Expected on some shells — orchestrator must not use this path
  pass "ssh env FLEET_JSON is unsafe; orchestrator uses REMOTE_SNAPSHOT local resolve instead"
fi

echo
if [[ "$fail" -eq 0 ]]; then
  echo "=== SELFTEST OK ==="
  exit 0
fi
echo "=== SELFTEST FAILED ==="
exit 1
