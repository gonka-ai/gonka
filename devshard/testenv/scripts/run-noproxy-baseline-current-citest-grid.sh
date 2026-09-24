#!/usr/bin/env bash
# No-proxy citest grid: pinned baseline versiond (0.2.15-v5) versus this tree.
# Phase 7 retired Echo session HTTP, so the B1-JSON and N0-JSON cells are not
# a supported mode. B1-RPC is Connect HTTP/1.1. Current-image peer RPC is
# HTTP/2 on the proxy overlay, not this grid. No proxy compose file.
# Documented in docs/scenarios.md ("No-proxy baseline versus current").
# One cell per line in ../docs/grpc-transport-phase6-8.2-results.txt
# Resume: a PASS or EXPECTED-FAIL line is not run again. FAIL is retried.
set -u
cd "$(dirname "$0")/.."

ENDPOINTS="signatures,mempool,diffs,gossip,repair,height-sync,verify-timeout,verify-error-miss,challenge-receipt,payload,chat"
B1_VERSIOND="${TESTENV_VERSIOND_IMAGE:-devshard-versiond:0.2.15-v5}"
B1_ROUTER="${TESTENV_VERSIOND_ROUTER_IMAGE:-devshard-versiond-router:0.2.15-v5}"
OUT="../docs/grpc-transport-phase6-8.2-results.txt"
mkdir -p "$(dirname "$OUT")"
touch "$OUT"

JSON_SUITES=(
  citest-stack
  citest-validation-lease-race
  citest-payload-withholding
  citest-escrow-longpoll
  citest-versiond-rolling-update
  citest-versiond-host-evacuation
  citest-versiond-warm-cutover
  citest-adversarial
  citest-force-upstream-streaming
  citest-grpc-transport
  citest-observability
  citest-height-sync
  citest-host-ping
)
RPC_SUITES=("${JSON_SUITES[@]}" citest-peerrpc-chat citest-peerrpc-ha-session)

already_settled() {
  grep -qxE "$1 (PASS|EXPECTED-FAIL)" "$OUT"
}

run_cell() {
  local cell="$1" suite="$2"
  shift 2
  if already_settled "$cell $suite"; then
    echo "skip $cell $suite"
    return 0
  fi
  echo "=== $cell $suite ==="
  if env \
    DEVSHARD_RPC_H2_PORT= \
    DEVSHARD_RPC_H2_UPGRADE= \
    "$@" \
    make "$suite"; then
    grep -v "^$cell $suite " "$OUT" > "$OUT.tmp" || true
    echo "$cell $suite PASS" >> "$OUT.tmp"
    mv "$OUT.tmp" "$OUT"
  else
    grep -v "^$cell $suite " "$OUT" > "$OUT.tmp" || true
    echo "$cell $suite FAIL" >> "$OUT.tmp"
    mv "$OUT.tmp" "$OUT"
  fi
}

for suite in "${JSON_SUITES[@]}"; do
  run_cell "B1-JSON" "$suite" \
    TESTENV_VERSIOND_IMAGE="$B1_VERSIOND" \
    TESTENV_VERSIOND_ROUTER_IMAGE="$B1_ROUTER" \
    DEVSHARD_RPC_SERVER_ENABLED=false \
    DEVSHARD_RPC_ENDPOINTS=
done

for suite in "${RPC_SUITES[@]}"; do
  run_cell "B1-RPC" "$suite" \
    TESTENV_VERSIOND_IMAGE="$B1_VERSIOND" \
    TESTENV_VERSIOND_ROUTER_IMAGE="$B1_ROUTER" \
    DEVSHARD_RPC_SERVER_ENABLED=true \
    DEVSHARD_RPC_ENDPOINTS="$ENDPOINTS"
done

for suite in "${JSON_SUITES[@]}"; do
  run_cell "N0-JSON" "$suite" \
    TESTENV_VERSIOND_IMAGE= \
    TESTENV_VERSIOND_ROUTER_IMAGE= \
    DEVSHARD_RPC_SERVER_ENABLED=false \
    DEVSHARD_RPC_ENDPOINTS=
done

# N0+RPC HTTP/1.1 is not a mode. Current versiond/router peer RPC is HTTP/2
# on the proxy overlay (plan §8.4), not Connect on :8080.

echo "grid finished"
sort "$OUT"
