#!/usr/bin/env bash
# Start PR #1416 negative test orchestrator (from laptop only).
#
# Usage:
#   ./pr1416-run.sh
#   POC_START=12760 ./pr1416-run.sh
#   ./pr1416-run.sh --n4-only
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
FLEET_ENV="${FLEET_ENV:-$JOIN_DIR/fleet.env}"
[[ -f "$FLEET_ENV" ]] || FLEET_ENV="$JOIN_DIR/../../test-net-cloud/nebius/poc-smst-e2e/fleet.env"

LOG_DIR="${PR1416_LOG_DIR:-/tmp}"
mkdir -p "$LOG_DIR"
NOHUP_LOG="${LOG_DIR}/pr1416-orchestrator.log"
PID_FILE="${LOG_DIR}/pr1416-orchestrator.pid"
LOCK_DIR="${LOG_DIR}/pr1416-orchestrator.lockdir"

if [[ -f "$PID_FILE" ]]; then
  oldpid="$(tr -d ' \n' <"$PID_FILE" || true)"
  if [[ -n "$oldpid" ]] && kill -0 "$oldpid" 2>/dev/null; then
    echo "orchestrator already running pid=$oldpid log=$NOHUP_LOG" >&2
    exit 1
  fi
  rm -f "$PID_FILE"
fi

if pgrep -f "pr1416-negative-test-orchestrate.sh" >/dev/null 2>&1; then
  echo "orchestrator already running:" >&2
  pgrep -af "pr1416-negative-test-orchestrate.sh" | grep -v grep || true
  exit 1
fi

if ! mkdir "$LOCK_DIR" 2>/dev/null; then
  echo "orchestrator lock held ($LOCK_DIR)" >&2
  exit 1
fi
trap 'rmdir "$LOCK_DIR" 2>/dev/null || true' EXIT

EXTRA_ARGS=("$@")

env_args=(FLEET_ENV="$FLEET_ENV")
[[ -n "${POC_START:-}" ]] && env_args+=(POC_START="$POC_START")

nohup env "${env_args[@]}" \
  "$SCRIPT_DIR/pr1416-negative-test-orchestrate.sh" ${EXTRA_ARGS+"${EXTRA_ARGS[@]}"} \
  >>"$NOHUP_LOG" 2>&1 &
PID=$!
echo "$PID" >"$PID_FILE"
echo "PID=$PID"
echo "log=$NOHUP_LOG"
echo "tail: tail -f $NOHUP_LOG"

for _ in $(seq 1 15); do
  if [[ -s "$NOHUP_LOG" ]]; then
    tail -12 "$NOHUP_LOG"
    exit 0
  fi
  sleep 1
done
echo "started; log not ready yet (PID=$PID)" >&2
