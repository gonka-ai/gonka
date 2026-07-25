#!/usr/bin/env bash
# Preflight checks before guardian tiebreaker orchestrator.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck disable=SC1091
source "$SCRIPT_DIR/guardian-tiebreaker-config.sh"

fail=0
ok() { echo "OK  $*"; }
warn() { echo "WARN $*"; }
bad() { echo "FAIL $*"; fail=1; }

guardian_tiebreaker_require_fleet

echo "=== guardian tiebreaker preflight ==="
echo "SSH_HOST=$SSH_HOST VICTIM=$VICTIM"
echo "GUARDIAN_VALOPER=$GUARDIAN_VALOPER"
echo

sched="$(ssh -o BatchMode=yes -p "$OBSERVER_PORT" "$SSH_HOST" \
  "API_BASE=$API_BASE bash -s" < "$SCRIPT_DIR/guardian-tiebreaker-epoch-schedule.sh")"
epoch_json="$(ssh -o BatchMode=yes -p "$OBSERVER_PORT" "$SSH_HOST" \
  "curl -fsS ${API_BASE}/v1/epochs/latest")"
cur_epoch="$(echo "$epoch_json" | jq -r '.latest_epoch.index')"
echo "$sched"
echo

next_epoch="$(echo "$sched" | awk -F= '/^next_epoch_index=/{print $2}')"
poc_start="$(echo "$sched" | awk -F= '/^poc_start=/{print $2}')"
conf="$(echo "$sched" | awk -F= '/^confirmation_poc_active=/{print $2}')"

params="$(ssh -o BatchMode=yes -p "$OBSERVER_PORT" "$SSH_HOST" \
  "docker exec node inferenced query inference params --home /root/.inference -o json")"
guardians="$(echo "$params" | jq -r '.params.genesis_guardian_params.guardian_addresses[]? // empty')"
if [[ "$guardians" == *"$GUARDIAN_VALOPER"* ]]; then
  ok "guardian_addresses includes $GUARDIAN_VALOPER"
else
  bad "guardian_addresses missing $GUARDIAN_VALOPER (run gov-propose-genesis-guardian-addresses.sh)"
  echo "  current: $(echo "$params" | jq -c '.params.genesis_guardian_params.guardian_addresses // []')"
fi

eg_ok=false
victim_ok=false
for e in "$cur_epoch" $((cur_epoch - 1)); do
  [[ "$e" -lt 0 ]] && continue
  eg="$(ssh -o BatchMode=yes -p "$OBSERVER_PORT" "$SSH_HOST" \
    "docker exec node inferenced query inference show-epoch-group-data $e --home /root/.inference -o json")"
  members="$(echo "$eg" | jq -r '.epoch_group_data.validation_weights | length')"
  tw="$(echo "$eg" | jq -r '.epoch_group_data.total_weight // "0"')"
  if [[ "$members" -ge 3 ]] && [[ "$tw" != "0" ]] && [[ "$tw" != "null" ]]; then
    ok "epoch $e has $members members total_weight=$tw"
    eg_ok=true
    victim_w="$(echo "$eg" | jq -r --arg v "$VICTIM" '.epoch_group_data.validation_weights[]? | select(.member_address==$v) | .weight // empty')"
    if [[ -n "$victim_w" ]] && [[ "$victim_w" != "0" ]]; then
      ok "victim $VICTIM weight=$victim_w (epoch $e)"
      victim_ok=true
    fi
    break
  fi
done
[[ "$eg_ok" == true ]] || bad "no recent epoch with healthy weights (checked $cur_epoch and $((cur_epoch - 1)))"
[[ "$victim_ok" == true ]] || bad "victim $VICTIM missing or zero weight in recent epoch group"

participants_json="$(ssh -o BatchMode=yes -p "$OBSERVER_PORT" "$SSH_HOST" \
  "docker exec node inferenced query inference list-participant --home /root/.inference -o json")"
for label_addr in \
  "guardian:$GUARDIAN_PARTICIPANT" \
  "preferred_victim:$PREFERRED_VICTIM_PARTICIPANT" \
  "preferred_invalid:$PREFERRED_INVALID_PARTICIPANT" \
  "preferred_valid:$PREFERRED_VALID_PARTICIPANT"; do
  label="${label_addr%%:*}"
  addr="${label_addr##*:}"
  st="$(echo "$participants_json" | jq -r --arg a "$addr" '.participant[]? | select(.index==$a) | .status // empty')"
  if [[ "$st" == "ACTIVE" ]]; then
    ok "participant $label ACTIVE on chain ($addr)"
  else
    bad "participant $label not ACTIVE on chain ($addr) status=${st:-missing}"
    echo "  hint: run $SCRIPT_DIR/guardian-tiebreaker-sync-fleet.sh after chain reset"
  fi
done

for port in "${ALL_API_PORTS[@]}"; do
  keys="$(ssh -o BatchMode=yes -p "$port" "$SSH_HOST" \
    'source /srv/dai/gonka/deploy/join/config.env; printf "%s\n" "$KEYRING_PASSWORD" | docker exec -i api inferenced keys list --keyring-backend file --home /root/.inference --list-names 2>/dev/null | paste -sd, -' || true)"
  [[ -n "$keys" ]] && ok "api keys port $port: $keys" || bad "no api keys on port $port"
done

ml="$(ssh -o BatchMode=yes -p "$GUARDIAN_SSH_PORT" "$SSH_HOST" \
  "docker inspect -f '{{.State.Status}} paused={{.State.Paused}}' $MLNODE 2>/dev/null || echo missing")"
if echo "$ml" | grep -q 'paused=true'; then
  ok "guardian mlnode paused on $GUARDIAN_HOST_ID (weightless for PoC gen)"
elif echo "$ml" | grep -q 'paused=false'; then
  warn "guardian mlnode running on $GUARDIAN_HOST_ID — pause before orchestrator for weightless guardian"
else
  warn "guardian mlnode state: $ml"
fi

if [[ "$conf" == "true" ]]; then
  warn "confirmation PoC active — pause guardian mlnode before it completes"
fi

echo "=== role resolve smoke test (local + remote snapshot) ==="
resolve_out="$(REMOTE_SNAPSHOT=1 SSH_HOST="$SSH_HOST" OBSERVER_PORT="$OBSERVER_PORT" \
  API_BASE="$API_BASE" EXPECTED_ANCHOR="$poc_start" MODEL="$MODEL" \
  GUARDIAN_PARTICIPANT="$GUARDIAN_PARTICIPANT" \
  PREFERRED_VICTIM_PARTICIPANT="$PREFERRED_VICTIM_PARTICIPANT" \
  PREFERRED_INVALID_PARTICIPANT="$PREFERRED_INVALID_PARTICIPANT" \
  PREFERRED_VALID_PARTICIPANT="$PREFERRED_VALID_PARTICIPANT" \
  FLEET_JSON="${FLEET_JSON:-}" \
  "$SCRIPT_DIR/guardian-tiebreaker-resolve-roles.sh" 2>&1)" || resolve_rc=$?
resolve_rc="${resolve_rc:-0}"
echo "$resolve_out"
if [[ "$resolve_rc" -eq 0 ]]; then
  ok "role resolve smoke test passed for anchor=$poc_start"
elif [[ "$resolve_rc" -eq 4 ]]; then
  warn "role resolve: no mining candidates (all preserved) — orchestrator will wait for next anchor"
else
  bad "role resolve smoke test failed rc=$resolve_rc (would have broken orchestrator at anchor)"
fi

echo
if [[ "$fail" -eq 0 ]]; then
  ok "preflight passed; target poc_start=$poc_start next_epoch=$next_epoch"
  echo "Orchestrator defaults: PAUSE_EARLY_BLOCKS=25, STOP_GUARDIAN_API_DURING_POC_GEN=1, guardian store-commit check before validation"
  echo "Run: $SCRIPT_DIR/guardian-tiebreaker-orchestrate.sh"
  exit 0
fi
bad "preflight failed"
exit 1
