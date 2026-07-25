#!/usr/bin/env bash
# PR #1416 negative test orchestrator — N1/N2 (MLNode pause) + N4 (API pause)
#
# Run from a laptop with SSH to the fleet. Do NOT run on the victim host.
# Victim = attack target (702127). Observers keep early_share_guard observe/enforce.
#
# Usage:
#   ./pr1416-preflight.sh
#   ./pr1416-run.sh
#   POC_START=12760 ./pr1416-negative-test-orchestrate.sh
#   ./pr1416-negative-test-orchestrate.sh --n4-only
#   ./pr1416-negative-test-orchestrate.sh --n1n2-only
#   ./pr1416-negative-test-orchestrate.sh --force   # join late; skip N1/N2 if past capture
#
# N1/N2 and N4 are independent: both run when enabled; failure of one does not skip the other.
# N4 pauses proxy + join-inference-1 + api (the :19246 public proof path), not api alone.
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

VICTIM_PORT="${VICTIM_SSH_PORT:?}"
OBSERVER_PORT="${OBSERVER_SSH_PORT:?}"
MLNODE="${MLNODE:-join-mlnode-308-1}"
MLNODE_PAUSE_SECS="${MLNODE_PAUSE_SECS:-100}"
N4_PAUSE_CONTAINERS="${N4_PAUSE_CONTAINERS:-proxy join-inference-1 api}"
GUARD_TEST_MODE="${GUARD_TEST_MODE:-enforce}"
LOG_FILE="${LOG_FILE:-/tmp/pr1416-negative-test-$(date +%Y%m%d-%H%M%S).log}"

RUN_N1N2=1
RUN_N4=1
FORCE_LATE=0
SKIP_HOST_CHECK=0
N1N2_RAN=0
N4_RAN=0
N1N2_ERR=0
N4_ERR=0

for arg in "$@"; do
  case "$arg" in
    --n1n2-only) RUN_N4=0 ;;
    --n4-only) RUN_N1N2=0 ;;
    --force) FORCE_LATE=1 ;;
    --skip-host-check) SKIP_HOST_CHECK=1 ;;
  esac
done

victim_ssh() { ssh -o BatchMode=yes -o ConnectTimeout=20 -p "$VICTIM_PORT" "${SSH_USER}@${SSH_HOST}" "$@"; }
observer_ssh() { ssh -o BatchMode=yes -o ConnectTimeout=20 -p "$OBSERVER_PORT" "${SSH_USER}@${SSH_HOST}" "$@"; }

log() { echo "[$(date -u +%H:%M:%S)] $*" | tee -a "$LOG_FILE" >&2; }

MLNODE_PAUSED=0
N4_PAUSED=0

mlnode_unpause() {
  [[ "$MLNODE_PAUSED" -eq 1 ]] || return 0
  victim_ssh "docker unpause $MLNODE 2>/dev/null || true" || true
  MLNODE_PAUSED=0
  log "N1/N2: MLNode unpaused"
}

n4_unpause() {
  [[ "$N4_PAUSED" -eq 1 ]] || return 0
  victim_ssh "for c in $N4_PAUSE_CONTAINERS; do docker unpause \$c 2>/dev/null || true; done" || true
  N4_PAUSED=0
  log "N4: unpaused $N4_PAUSE_CONTAINERS"
}

n4_pause() {
  victim_ssh "for c in $N4_PAUSE_CONTAINERS; do docker pause \$c; done; \
    for c in $N4_PAUSE_CONTAINERS; do echo \$c paused=\$(docker inspect -f '{{.State.Paused}}' \$c 2>/dev/null || echo missing); done"
}

cleanup() {
  local rc=$?
  if [[ "$rc" -ne 0 ]]; then
    log "cleanup: exit rc=$rc — unpausing victim containers"
  fi
  mlnode_unpause || true
  n4_unpause || true
}
trap cleanup EXIT INT TERM

check_run_host() {
  [[ "$SKIP_HOST_CHECK" -eq 1 ]] && return 0
  local hn
  hn="$(hostname -s 2>/dev/null || hostname)"
  if [[ "$hn" == "$VICTIM_HOST_ID" ]] || [[ "$hn" == *702127* ]]; then
    log "ERROR: run from laptop, not victim host $VICTIM_HOST_ID (SSH loopback fails)"
    exit 1
  fi
}

# Returns JSON schedule via victim docker exec (avoids :19244 rate limits).
fetch_schedule() {
  local poc_override="${1:-}"
  victim_ssh "docker exec api wget -qO- --timeout=10 http://127.0.0.1:9000/v1/epochs/latest 2>/dev/null; echo ---SPLIT---; docker exec api wget -qO- --timeout=10 http://node:1317/productscience/inference/inference/preserved_nodes_snapshot 2>/dev/null || true" \
    | POC_OVERRIDE="$poc_override" VICTIM_PARTICIPANT="$VICTIM_PARTICIPANT" python3 -c "
import json, os, sys

raw = sys.stdin.read()
parts = raw.split('---SPLIT---', 1)
epoch_raw = parts[0].strip()
pres_raw = parts[1].strip() if len(parts) > 1 else ''
poc_override = os.environ.get('POC_OVERRIDE', '').strip()

if not epoch_raw:
    print('{}')
    sys.exit(1)

d = json.loads(epoch_raw)
st = d.get('epoch_stages') or {}
nst = d.get('next_epoch_stages') or {}
phase = d.get('phase', '')
bh = int(d['block_height'])
le = d.get('latest_epoch') or {}
ep = d.get('epoch_params') or {}

poc_dur = int(ep.get('poc_stage_duration') or 60)
val_delay = int(ep.get('poc_validation_delay') or 5)
val_dur = int(ep.get('poc_validation_duration') or 40)

if poc_override:
    poc_start = int(poc_override)
    st = {
        'poc_start': poc_start,
        'poc_generation_wind_down': poc_start + poc_dur - 12,
        'poc_generation_end': poc_start + poc_dur,
        'poc_validation_start': poc_start + poc_dur + val_delay,
        'poc_validation_end': poc_start + poc_dur + val_delay + val_dur,
    }
elif phase == 'Inference' and nst.get('poc_start'):
    poc_start = int(nst['poc_start'])
    st = nst
else:
    poc_start = int(st.get('poc_start') or le.get('poc_start_block_height') or 0)

capture_at = poc_start + max(1, poc_dur // 3)

pres_participant = 'none'
pres_anchor = '?'
if pres_raw:
    try:
        pj = json.loads(pres_raw)
        snap = pj.get('snapshot') or {}
        pres_anchor = str(snap.get('episode_anchor_height', '?'))
        mp = (snap.get('model_preserved_nodes') or [{}])[0]
        pl = mp.get('participants') or [{}]
        if pl:
            pres_participant = pl[0].get('participant_id', 'none')
    except json.JSONDecodeError:
        pres_participant = 'unknown'

print(json.dumps({
    'block_height': bh,
    'phase': phase,
    'poc_start': poc_start,
    'capture_at': capture_at,
    'poc_generation_wind_down': int(st.get('poc_generation_wind_down') or (poc_start + poc_dur - 12)),
    'poc_generation_end': int(st.get('poc_generation_end') or (poc_start + poc_dur)),
    'poc_validation_start': int(st.get('poc_validation_start') or (poc_start + poc_dur + val_delay)),
    'poc_validation_end': int(st.get('poc_validation_end') or (poc_start + poc_dur + val_delay + val_dur)),
    'preserved_participant': pres_participant,
    'preserved_anchor': pres_anchor,
}))
"
}

get_block_phase() {
  local out raw_host
  for raw_host in victim observer; do
    if [[ "$raw_host" == victim ]]; then
      out="$(victim_ssh "docker exec api wget -qO- --timeout=8 http://127.0.0.1:9000/v1/epochs/latest 2>/dev/null" \
        | python3 -c "import json,sys; raw=sys.stdin.read().strip();
if not raw: sys.exit(2)
d=json.loads(raw); print(d['block_height'], d.get('phase',''))" 2>/dev/null)" && { echo "$out"; return 0; }
    else
      out="$(observer_ssh "docker exec api wget -qO- --timeout=8 http://127.0.0.1:9000/v1/epochs/latest 2>/dev/null" \
        | python3 -c "import json,sys; raw=sys.stdin.read().strip();
if not raw: sys.exit(2)
d=json.loads(raw); print(d['block_height'], d.get('phase',''))" 2>/dev/null)" && { echo "$out"; return 0; }
    fi
  done
  return 1
}

wait_block_ge() {
  local target="$1" label="$2"
  local block phase
  while true; do
    if read -r block phase <<< "$(get_block_phase)"; then
      log "wait $label: block=$block phase=$phase target=$target"
      if [[ "$block" -ge "$target" ]]; then
        echo "$block"
        return 0
      fi
    else
      log "wait $label: epochs/latest unavailable, retrying..."
    fi
    sleep 2
  done
}

wait_phase_or_block() {
  local want_phase="$1" min_block="$2" label="$3"
  local block phase
  while true; do
    if read -r block phase <<< "$(get_block_phase)"; then
      log "wait $label: block=$block phase=$phase want_phase=$want_phase min_block=$min_block"
      if [[ "$phase" == "$want_phase" ]] || [[ "$block" -ge "$min_block" ]]; then
        printf '%s %s\n' "$block" "$phase"
        return 0
      fi
    else
      log "wait $label: epochs/latest unavailable, retrying..."
    fi
    sleep 1
  done
}

run_n1n2() {
  N1N2_RAN=1
  set +e
  if [[ "$CUR_BLOCK" -gt "$((POC_START + 2))" ]] && [[ "$FORCE_LATE" -eq 0 ]]; then
    if [[ "$CUR_BLOCK" -ge "$CAPTURE_AT" ]]; then
      log "SKIP N1/N2: block $CUR_BLOCK already past capture $CAPTURE_AT (continuing to N4 if enabled)"
      return 0
    else
      log "WARN: joining late (block=$CUR_BLOCK poc_start=$POC_START) — N1/N2 may be unreliable"
    fi
  fi

  if [[ "$CUR_BLOCK" -lt "$POC_START" ]]; then
    log "N1/N2: waiting for PoC generate (block $POC_START)..."
    wait_block_ge "$POC_START" "poc_generate" >/dev/null
  fi

  log "N1/N2: pausing MLNode $MLNODE on victim (hold through capture at $CAPTURE_AT)..."
  if ! victim_ssh "docker pause $MLNODE && docker inspect -f 'paused={{.State.Paused}}' $MLNODE"; then
    log "WARN: N1/N2: mlnode pause failed — continuing"
    N1N2_ERR=1
    set -e
    return 0
  fi
  MLNODE_PAUSED=1

  local unpause_at=$((CAPTURE_AT + 2))
  local pause_deadline=$(( $(date +%s) + MLNODE_PAUSE_SECS + 30 ))
  while true; do
    local now block phase
    now=$(date +%s)
    if read -r block phase <<< "$(get_block_phase)"; then
      log "N1/N2 hold: block=$block capture=$CAPTURE_AT unpause_at=$unpause_at"
      if [[ "$block" -ge "$unpause_at" ]]; then
        break
      fi
    fi
    if [[ "$now" -ge "$pause_deadline" ]]; then
      log "WARN: N1/N2 pause deadline reached — unpausing anyway"
      break
    fi
    sleep 2
  done
  mlnode_unpause
  set -e
}

run_n4() {
  N4_RAN=1
  set +e
  log "N4: waiting for PoCValidate (block >= $VAL_START)..."
  local n4_block n4_phase
  read -r n4_block n4_phase <<< "$(wait_phase_or_block "PoCValidate" "$VAL_START" "n4_validate")"
  log "N4: phase=$n4_phase block=$n4_block — pausing $N4_PAUSE_CONTAINERS (public proof path)"
  if ! n4_pause; then
    log "WARN: N4: container pause failed — continuing to log analysis"
    N4_ERR=1
    set -e
    return 0
  fi
  N4_PAUSED=1

  while true; do
    local block phase
    if read -r block phase <<< "$(get_block_phase)"; then
      log "N4 hold: block=$block phase=$phase val_end=$VAL_END"
      if [[ "$block" -ge "$VAL_END" ]] || [[ "$phase" == "Inference" ]]; then
        break
      fi
    else
      log "N4 hold: epochs/latest unavailable, retrying..."
    fi
    sleep 2
  done
  n4_unpause
  set -e
}

check_run_host

if [[ -z "${POC_START:-}" ]]; then
  SCHED="$(fetch_schedule "")" || { log "ERROR: fetch_schedule failed"; exit 1; }
  POC_START="$(echo "$SCHED" | python3 -c "import json,sys; print(json.load(sys.stdin)['poc_start'])")"
  log "auto poc_start=$POC_START"
else
  SCHED="$(fetch_schedule "$POC_START")" || { log "ERROR: fetch_schedule failed"; exit 1; }
fi

CAPTURE_AT="$(echo "$SCHED" | python3 -c "import json,sys; print(json.load(sys.stdin)['capture_at'])")"
WIND_DOWN="$(echo "$SCHED" | python3 -c "import json,sys; print(json.load(sys.stdin)['poc_generation_wind_down'])")"
VAL_START="$(echo "$SCHED" | python3 -c "import json,sys; print(json.load(sys.stdin)['poc_validation_start'])")"
VAL_END="$(echo "$SCHED" | python3 -c "import json,sys; print(json.load(sys.stdin)['poc_validation_end'])")"
CUR_BLOCK="$(echo "$SCHED" | python3 -c "import json,sys; print(json.load(sys.stdin)['block_height'])")"
CUR_PHASE="$(echo "$SCHED" | python3 -c "import json,sys; print(json.load(sys.stdin)['phase'])")"
PRES="$(echo "$SCHED" | python3 -c "import json,sys; print(json.load(sys.stdin)['preserved_participant'])")"
PRES_ANCHOR="$(echo "$SCHED" | python3 -c "import json,sys; print(json.load(sys.stdin)['preserved_anchor'])")"

log "=== PR1416 negative test ==="
log "victim=$VICTIM_HOST_ID ($VICTIM_PARTICIPANT) port=$VICTIM_PORT"
log "observer=$OBSERVER_HOST_ID port=$OBSERVER_PORT"
log "poc_start=$POC_START capture=$CAPTURE_AT wind_down=$WIND_DOWN val_start=$VAL_START val_end=$VAL_END"
log "current block=$CUR_BLOCK phase=$CUR_PHASE"
log "preserved=$PRES anchor=$PRES_ANCHOR"
log "tests: N1/N2=$RUN_N1N2 N4=$RUN_N4 guard_mode=$GUARD_TEST_MODE mlnode_pause=${MLNODE_PAUSE_SECS}s n4_pause=[$N4_PAUSE_CONTAINERS] n4_hold=until_val_end=$VAL_END"

if [[ "$PRES" == "$VICTIM_PARTICIPANT" ]]; then
  log "ABORT: victim is preserved this epoch — cannot attack"
  exit 2
fi

if [[ "$PRES" == "$OBSERVER_PARTICIPANT" ]]; then
  log "WARN: primary observer $OBSERVER_HOST_ID is preserved — grep ${SECONDARY_OBSERVER_HOST_ID:-702105} too"
fi

# --- N1/N2 (independent) ---
if [[ "$RUN_N1N2" -eq 1 ]]; then
  run_n1n2
fi

# --- N4 (independent; runs even if N1/N2 skipped or errored) ---
if [[ "$RUN_N4" -eq 1 ]]; then
  run_n4
fi

if [[ "$RUN_N4" -eq 0 ]] || [[ "$N4_RAN" -eq 0 ]]; then
  log "Waiting for validation end (block $VAL_END)..."
  wait_block_ge "$VAL_END" "validate_end" >/dev/null
fi
sleep 5

STAGE="$POC_START"
VICTIM="$VICTIM_PARTICIPANT"
log "=== Log analysis ==="

analyze_observer() {
  local port="$1" host_id="$2"
  log "--- observer $host_id (port $port) ---"
  ssh -o BatchMode=yes -o ConnectTimeout=20 -p "$port" "${SSH_USER}@${SSH_HOST}" "
    echo '--- capture ---'
    docker logs api 2>&1 | grep 'EarlyShareGuard: captured early checkpoints' | grep 'stage=$STAGE' | tail -2
    echo '--- N1/N2 (low early share miss) ---'
    docker logs api 2>&1 | grep 'low early share miss' | grep '$VICTIM' | grep 'stage=$STAGE' || echo '(none)'
    echo '--- N4 (retry / proof fetch fail) ---'
    docker logs api 2>&1 | grep '$VICTIM' | grep -E 'guard retry \(enforce\)|would retry|guard retry|proof fetch/verify failed' | grep -E 'stage=$STAGE|pocStageStartBlockHeight=$STAGE' || \
      docker logs api 2>&1 | grep '$VICTIM' | grep -E 'guard retry \(enforce\)|would retry|guard retry|proof fetch/verify failed' | tail -8 || echo '(none)'
    echo '--- validation ---'
    docker logs api 2>&1 | grep 'pocStageStartBlockHeight=$STAGE' | grep -E 'starting validation|validation complete|failed' | tail -5
  " | tee -a "$LOG_FILE"
}

analyze_observer "$OBSERVER_PORT" "$OBSERVER_HOST_ID"
if [[ -n "${SECONDARY_OBSERVER_SSH_PORT:-}" ]]; then
  analyze_observer "$SECONDARY_OBSERVER_SSH_PORT" "${SECONDARY_OBSERVER_HOST_ID:-secondary}"
fi

# Pass/fail summary (enforce vs observe log patterns)
N1N2_PASS=0
N4_PASS=0
if grep -q "low early share miss" "$LOG_FILE" && grep "low early share miss" "$LOG_FILE" | grep -q "$VICTIM"; then
  if [[ "$GUARD_TEST_MODE" == "enforce" ]]; then
    if grep "low early share miss" "$LOG_FILE" | grep -q "enforcing=true"; then
      N1N2_PASS=1
    fi
  else
    N1N2_PASS=1
  fi
fi
if [[ "$GUARD_TEST_MODE" == "enforce" ]]; then
  if grep -qE "guard retry \(enforce\)|proof fetch/verify failed" "$LOG_FILE" && \
     grep -E "guard retry \(enforce\)|proof fetch/verify failed" "$LOG_FILE" | grep -q "$VICTIM"; then
    N4_PASS=1
  fi
else
  if grep -qE "would retry|guard retry|proof fetch/verify failed" "$LOG_FILE" && \
     grep -E "would retry|guard retry|proof fetch/verify failed" "$LOG_FILE" | grep -q "$VICTIM"; then
    N4_PASS=1
  fi
fi

log "=== RESULTS stage=$STAGE guard_mode=$GUARD_TEST_MODE ==="
if [[ "$N1N2_RAN" -eq 1 ]]; then
  if [[ "$GUARD_TEST_MODE" == "enforce" ]]; then
    log "N1/N2: $([ "$N1N2_PASS" -eq 1 ] && echo PASS || echo FAIL)$([ "$N1N2_ERR" -eq 1 ] && echo ' (attack error)') (expect: low early share miss, enforcing=true, wouldVoteNo=false)"
  else
    log "N1/N2: $([ "$N1N2_PASS" -eq 1 ] && echo PASS || echo FAIL)$([ "$N1N2_ERR" -eq 1 ] && echo ' (attack error)') (expect: low early share miss for $VICTIM)"
  fi
elif [[ "$RUN_N1N2" -eq 1 ]]; then
  log "N1/N2: SKIPPED"
fi
if [[ "$N4_RAN" -eq 1 ]]; then
  if [[ "$GUARD_TEST_MODE" == "enforce" ]]; then
    log "N4:    $([ "$N4_PASS" -eq 1 ] && echo PASS || echo FAIL)$([ "$N4_ERR" -eq 1 ] && echo ' (attack error)') (expect: guard retry (enforce) or proof fetch/verify failed for $VICTIM)"
  else
    log "N4:    $([ "$N4_PASS" -eq 1 ] && echo PASS || echo FAIL)$([ "$N4_ERR" -eq 1 ] && echo ' (attack error)') (expect: would retry or proof fetch/verify failed for $VICTIM)"
  fi
elif [[ "$RUN_N4" -eq 1 ]]; then
  log "N4:    SKIPPED"
fi
log "DONE log=$LOG_FILE"

rc=0
if [[ "$N1N2_RAN" -eq 1 && "$N1N2_PASS" -eq 0 ]]; then rc=3; fi
if [[ "$N4_RAN" -eq 1 && "$N4_PASS" -eq 0 ]]; then
  [[ "$rc" -eq 3 ]] && rc=5 || rc=4
fi
exit "$rc"
