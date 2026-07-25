#!/usr/bin/env bash
# Pre-flight checks for PR #1416 negative tests. No epoch waits, no pauses.
#
# Usage (from laptop):
#   ./pr1416-preflight.sh
#   POC_START=12760 ./pr1416-preflight.sh
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
FLEET_ENV="${FLEET_ENV:-$JOIN_DIR/fleet.env}"
[[ -f "$FLEET_ENV" ]] || FLEET_ENV="$JOIN_DIR/../../test-net-cloud/nebius/poc-smst-e2e/fleet.env"
# shellcheck disable=SC1090
source "$FLEET_ENV"

SSH_HOST="${SSH_HOST#*@}"
SSH_USER="${SSH_USER:-decentai}"
SSH_HOST="${SSH_HOST:-xj7-5.s.filfox.io}"

FAIL=0
pass() { echo "PASS $*"; }
fail() { echo "FAIL $*" >&2; FAIL=1; }

echo "=== pr1416-preflight ==="
echo "fleet=$FLEET_ENV victim=$VICTIM_HOST_ID observer=$OBSERVER_HOST_ID"

echo "--- bash -n ---"
for f in pr1416-negative-test-orchestrate.sh pr1416-run.sh pr1416-stop.sh pr1416-analyze-logs.sh; do
  if [[ -f "$SCRIPT_DIR/$f" ]] && bash -n "$SCRIPT_DIR/$f"; then
    pass "bash -n $f"
  else
    fail "bash -n $f"
  fi
done

echo "--- run host ---"
hn="$(hostname -s 2>/dev/null || hostname)"
if [[ "$hn" == "$VICTIM_HOST_ID" ]] || [[ "$hn" == *702127* ]]; then
  fail "running ON victim host $hn — use laptop"
else
  pass "not on victim host ($hn)"
fi

echo "--- SSH victim + observer ---"
for spec in "victim:$VICTIM_SSH_PORT" "observer:$OBSERVER_SSH_PORT"; do
  name="${spec%%:*}"
  p="${spec##*:}"
  if ssh -o BatchMode=yes -o ConnectTimeout=15 -p "$p" "${SSH_USER}@${SSH_HOST}" "echo ok" >/dev/null 2>&1; then
    pass "SSH $name port $p"
  else
    fail "SSH $name port $p"
  fi
done

echo "--- victim containers ---"
if out="$(ssh -o BatchMode=yes -o ConnectTimeout=15 -p "$VICTIM_SSH_PORT" "${SSH_USER}@${SSH_HOST}" \
  "docker inspect -f '{{.Name}} status={{.State.Status}} paused={{.State.Paused}}' $MLNODE api 2>&1")"; then
  echo "$out"
  if echo "$out" | grep -q 'paused=true'; then
    fail "victim has paused containers — run pr1416-stop.sh first"
  else
    pass "victim mlnode + api running"
  fi
else
  fail "victim docker inspect"
fi

echo "--- early_share_guard (GUARD_TEST_MODE=${GUARD_TEST_MODE:-enforce}) ---"
expect="${GUARD_TEST_MODE:-enforce}"
for spec in "victim:$VICTIM_SSH_PORT" "observer:$OBSERVER_SSH_PORT"; do
  name="${spec%%:*}"
  p="${spec##*:}"
  mode="$(ssh -o BatchMode=yes -o ConnectTimeout=15 -p "$p" "${SSH_USER}@${SSH_HOST}" \
    "awk '/early_share_guard:/{f=1} f&&/mode:/{print \$2; exit}' /srv/dai/gonka/deploy/join/.dapi/api-config.yaml" 2>/dev/null || echo missing)"
  echo "  $name: mode=$mode (expect $expect for victim+observer)"
  if [[ "$name" == "victim" || "$name" == "observer" ]]; then
    want="$expect"
  else
    want="observe"
  fi
  if [[ "$mode" == "$want" ]]; then
    pass "$name guard mode=$mode"
  elif [[ "$mode" == "observe" || "$mode" == "enforce" ]]; then
    fail "$name guard mode=$mode (want $want for enforce sign-off)"
  else
    fail "$name guard mode=$mode"
  fi
done

echo "--- schedule + preserved ---"
raw="$(ssh -o BatchMode=yes -o ConnectTimeout=15 -p "$VICTIM_SSH_PORT" "${SSH_USER}@${SSH_HOST}" \
  "docker exec api wget -qO- --timeout=10 http://127.0.0.1:9000/v1/epochs/latest 2>/dev/null; echo ---SPLIT---; docker exec api wget -qO- --timeout=10 http://node:1317/productscience/inference/inference/preserved_nodes_snapshot 2>/dev/null || true")"

sched_json="$(POC_OVERRIDE="${POC_START:-}" SCHED_RAW="$raw" python3 <<'PY'
import json, os, sys
raw = os.environ.get('SCHED_RAW', '')
parts = raw.split('---SPLIT---', 1)
if not parts[0].strip():
    sys.exit(1)
d = json.loads(parts[0].strip())
st = d.get('epoch_stages') or {}
nst = d.get('next_epoch_stages') or {}
phase = d.get('phase', '')
bh = int(d['block_height'])
ep = d.get('epoch_params') or {}
poc_dur = int(ep.get('poc_stage_duration') or 60)
val_delay = int(ep.get('poc_validation_delay') or 5)
ov = os.environ.get('POC_OVERRIDE', '').strip()
if ov:
    ps = int(ov)
    st = {
        'poc_start': ps,
        'poc_generation_wind_down': ps + poc_dur - 12,
        'poc_validation_start': ps + poc_dur + val_delay,
    }
elif phase == 'Inference' and nst.get('poc_start'):
    ps = int(nst['poc_start'])
    st = nst
else:
    ps = int(st.get('poc_start') or d['latest_epoch']['poc_start_block_height'])
cap = ps + max(1, poc_dur // 3)
pres = 'none'
if len(parts) > 1 and parts[1].strip():
    try:
        pj = json.loads(parts[1].strip())
        snap = pj.get('snapshot') or {}
        mp = (snap.get('model_preserved_nodes') or [{}])[0]
        pl = mp.get('participants') or [{}]
        if pl:
            pres = pl[0].get('participant_id', 'none')
    except json.JSONDecodeError:
        pres = 'unknown'
print(json.dumps({
    'block_height': bh,
    'phase': phase,
    'poc_start': ps,
    'capture_at': cap,
    'poc_generation_wind_down': int(st.get('poc_generation_wind_down') or (ps + poc_dur - 12)),
    'poc_validation_start': int(st.get('poc_validation_start') or (ps + poc_dur + val_delay)),
    'preserved_participant': pres,
    'blocks_to_poc': max(0, ps - bh),
    'blocks_to_capture': max(0, cap - bh),
}, indent=2))
PY
)"

echo "$sched_json"
pass "schedule fetched"

pres="$(echo "$sched_json" | python3 -c "import json,sys; print(json.load(sys.stdin)['preserved_participant'])")"
blocks_to_capture="$(echo "$sched_json" | python3 -c "import json,sys; print(json.load(sys.stdin)['blocks_to_capture'])")"
phase="$(echo "$sched_json" | python3 -c "import json,sys; print(json.load(sys.stdin)['phase'])")"

if [[ "$pres" == "$VICTIM_PARTICIPANT" ]]; then
  fail "victim is preserved this epoch"
else
  pass "victim not preserved (preserved=$pres)"
fi

if [[ "$blocks_to_capture" -le 0 ]] && [[ "$phase" == PoCGenerate* ]]; then
  fail "mid-PoC generate — too late for N1/N2 without --force"
else
  pass "timing ok for start (phase=$phase blocks_to_capture=$blocks_to_capture)"
fi

echo "--- no duplicate orchestrator ---"
if pgrep -f "pr1416-negative-test-orchestrate.sh" >/dev/null 2>&1; then
  fail "orchestrator already running"
else
  pass "no orchestrator running"
fi

if [[ "$FAIL" -eq 0 ]]; then
  echo "=== pr1416-preflight: ALL PASS ==="
  echo "Start: $SCRIPT_DIR/pr1416-run.sh"
  exit 0
fi
echo "=== pr1416-preflight: FAIL ==="
exit 1
