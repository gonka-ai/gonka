#!/usr/bin/env bash
# fleet-pause-nodes.sh — pause/unpause the `node` container on ALL gonka-testnet-4
# hosts at (near) the same wall-clock instant, cleanly halting/resuming the chain.
#
# Why pause (not stop): `docker pause` freezes the cometbft process via the cgroup
# freezer. Block height — and therefore the epoch/PoC phase — stops advancing while
# paused, so nothing "expires" in real time. When every validator is unpaused
# together the chain resumes from the exact same height/phase. The only unsafe case
# is a NON-simultaneous pause (e.g. 1 of 4): the others keep producing blocks and the
# paused node falls behind and misses PoC/consensus. This script uses a synchronized
# wall-clock trigger so all nodes freeze within the same block.
#
# Usage:
#   ./scripts/fleet-pause-nodes.sh status            # show node container state per host
#   ./scripts/fleet-pause-nodes.sh phase             # show chain epoch/PoC phase per host
#   ./scripts/fleet-pause-nodes.sh pause [delay]     # sync-pause all (default delay 10s)
#   ./scripts/fleet-pause-nodes.sh unpause           # resume all
#
# Env overrides:
#   CONTAINER   container name to pause          (default: node)
#   SSH_HOST    ssh hostname                     (default: xj7-5.s.filfox.io)
#   SSH_USER    ssh user                         (default: decentai)
#   HOSTS       space-separated "label:ssh_port:api_port" list (default: the 4 t4 hosts)
#
# NOTE: relies on host clocks being NTP-synced (they are on these VMs). The trigger is
# an absolute epoch second computed on the laptop; each host busy-waits to that instant.
set -euo pipefail

SSH_HOST="${SSH_HOST:-xj7-5.s.filfox.io}"
SSH_USER="${SSH_USER:-decentai}"
CONTAINER="${CONTAINER:-node}"
HOSTS_DEFAULT="702111:18222:19244 702112:18221:19242 702127:18223:19246 702105:18226:19252"
read -r -a HOSTS <<< "${HOSTS:-$HOSTS_DEFAULT}"

SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=15 -o ServerAliveInterval=10)

phase_check() {
  echo "=== chain phase (per host) ==="
  for h in "${HOSTS[@]}"; do
    IFS=: read -r label sport aport <<< "$h"
    local ph
    ph=$(curl -fsS -m 8 "http://${SSH_HOST}:${aport}/v1/epochs/latest" 2>/dev/null \
      | jq -r '"phase=\(.phase) epoch=\(.latest_epoch.index) block=\(.block_height) confirm_poc=\(.is_confirmation_poc_active)"' 2>/dev/null \
      || echo "unknown (api unreachable)")
    printf "  %-8s %s\n" "$label" "$ph"
  done
}

status() {
  echo "=== ${CONTAINER} container state (per host) ==="
  for h in "${HOSTS[@]}"; do
    IFS=: read -r label sport aport <<< "$h"
    local st
    st=$(ssh -p "$sport" "${SSH_OPTS[@]}" "${SSH_USER}@${SSH_HOST}" \
      "docker inspect ${CONTAINER} --format '{{.State.Status}}' 2>/dev/null || echo missing" 2>/dev/null || echo "ssh-fail")
    printf "  %-8s %s=%s\n" "$label" "$CONTAINER" "$st"
  done
}

pause() {
  local delay="${1:-10}"
  [[ "$delay" =~ ^[0-9]+$ ]] || { echo "delay must be an integer" >&2; exit 1; }
  phase_check
  local at=$(( $(date +%s) + delay ))
  echo "=== scheduling '${CONTAINER}' PAUSE on ${#HOSTS[@]} host(s) at epoch ${at} (T-${delay}s) ==="
  local pids=()
  for h in "${HOSTS[@]}"; do
    IFS=: read -r label sport aport <<< "$h"
    ssh -p "$sport" "${SSH_OPTS[@]}" "${SSH_USER}@${SSH_HOST}" \
      "AT=${at} C=${CONTAINER} LBL=${label} bash -s" <<'REMOTE' &
set -euo pipefail
while [ "$(date +%s)" -lt "$AT" ]; do sleep 0.05; done
docker pause "$C" >/dev/null 2>&1 || true
echo "$LBL paused_at=$(date -u +%H:%M:%S.%3N) $C=$(docker inspect "$C" --format '{{.State.Status}}' 2>/dev/null || echo missing)"
REMOTE
    pids+=($!)
  done
  local rc=0
  for p in "${pids[@]}"; do wait "$p" || rc=1; done
  echo "=== verify ==="; status
  [[ "$rc" -eq 0 ]] || echo "WARN: one or more hosts reported an error above" >&2
  return "$rc"
}

unpause() {
  echo "=== UNPAUSE '${CONTAINER}' on ${#HOSTS[@]} host(s) ==="
  local pids=()
  for h in "${HOSTS[@]}"; do
    IFS=: read -r label sport aport <<< "$h"
    ssh -p "$sport" "${SSH_OPTS[@]}" "${SSH_USER}@${SSH_HOST}" \
      "docker unpause ${CONTAINER} >/dev/null 2>&1 || true; \
       echo ${label} resumed_at=\$(date -u +%H:%M:%S.%3N) ${CONTAINER}=\$(docker inspect ${CONTAINER} --format '{{.State.Status}}' 2>/dev/null || echo missing)" &
    pids+=($!)
  done
  for p in "${pids[@]}"; do wait "$p" || true; done
  echo "=== verify ==="; status
  phase_check
}

case "${1:-status}" in
  status)  status ;;
  phase)   phase_check ;;
  pause)   shift; pause "$@" ;;
  unpause) unpause ;;
  *) echo "usage: $0 {status|phase|pause [delay_secs]|unpause}" >&2; exit 1 ;;
esac
