#!/usr/bin/env bash
# Print guardian tiebreaker block schedule from live /v1/epochs/latest.
# Run on guardian host or anywhere with API access.
set -euo pipefail

API_BASE="${API_BASE:-http://127.0.0.1:8000}"
PAUSE_EARLY_BLOCKS="${PAUSE_EARLY_BLOCKS:-2}"
UNPAUSE_AFTER_VAL_END="${UNPAUSE_AFTER_VAL_END:-5}"

json="$(curl -fsS "${API_BASE}/v1/epochs/latest")"
python3 - <<'PY' "$json" "$PAUSE_EARLY_BLOCKS" "$UNPAUSE_AFTER_VAL_END"
import json, sys
d = json.loads(sys.argv[1])
pause_early = int(sys.argv[2])
unpause_after = int(sys.argv[3])
bh = int(d["block_height"])
ns = d["next_epoch_stages"]
ps = int(ns["poc_start"])
poc_gen_end = int(ns["poc_generation_end"])
val_start = int(ns["poc_validation_start"])
val_end = int(ns["poc_validation_end"])
set_new = int(ns["set_new_validators"])
pause_at = ps - pause_early
unpause_at = val_end + unpause_after
conf_active = bool(d.get("is_confirmation_poc_active"))
print(f"height_now={bh}")
print(f"phase_now={d.get('phase')}")
print(f"confirmation_poc_active={str(conf_active).lower()}")
print(f"next_epoch_index={ns['epoch_index']}")
print(f"poc_start={ps}")
print(f"poc_generation_end={poc_gen_end}")
print(f"pause_mlnode_at={pause_at}")
print(f"poc_validation_start={val_start}")
print(f"poc_validation_end={val_end}")
print(f"vote_window_start={val_start + 1}")
print(f"vote_window_end={val_end}")
print(f"unpause_mlnode_at={unpause_at}")
print(f"set_new_validators={set_new}")
print(f"next_poc_after_this={int(d['next_epoch_stages']['next_poc_start'])}")
print(f"blocks_to_pause={pause_at - bh}")
print(f"blocks_to_vote_start={val_start + 1 - bh}")
print(f"blocks_to_set_new={set_new - bh}")
if conf_active:
    print("warning=confirmation_poc_active_pause_guardian_mlnode_now_or_before_it_ends")
elif bh >= ps:
    print("warning=already_past_poc_start_schedule_targets_next_epoch_only")
PY
