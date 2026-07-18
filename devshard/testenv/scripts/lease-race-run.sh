#!/usr/bin/env bash
# Run load + parallel lease monitor, then PASS/FAIL analysis.
#
# Prerequisites: testenv stack up (make up), validation_rate=10000 preferred,
# escrow warm on both HA replicas. See docs/validation-lease-race-manual-test.md.
#
# Usage:
#   ./scripts/lease-race-run.sh
#   MIN_LEASES=10 NON_STREAM=30 STREAM=10 ./scripts/lease-race-run.sh
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

MIN_LEASES="${MIN_LEASES:-5}"
WATCH_SECS="${WATCH_SECS:-2}"

MONITOR_LOG="$(mktemp -t lease-race-monitor.XXXXXX)"
cleanup() {
  if [[ -n "${MONITOR_PID:-}" ]] && kill -0 "${MONITOR_PID}" 2>/dev/null; then
    kill "${MONITOR_PID}" 2>/dev/null || true
    wait "${MONITOR_PID}" 2>/dev/null || true
  fi
}
trap cleanup EXIT

echo "lease-race-run: starting monitor (watch=${WATCH_SECS}s) → ${MONITOR_LOG}"
./scripts/lease-race-monitor.sh --watch "${WATCH_SECS}" --min-leases 0 >"${MONITOR_LOG}" 2>&1 &
MONITOR_PID=$!

echo "lease-race-run: driving load"
./scripts/lease-race-load.sh

# Give validators a moment to finish / SetResult.
sleep 5

echo "lease-race-run: stopping monitor for final verdict"
kill "${MONITOR_PID}" 2>/dev/null || true
wait "${MONITOR_PID}" 2>/dev/null || true
MONITOR_PID=

echo "======== monitor log (tail) ========"
tail -n 40 "${MONITOR_LOG}" || true
echo "======== final analysis ========"
./scripts/lease-race-monitor.sh --min-leases "${MIN_LEASES}"
