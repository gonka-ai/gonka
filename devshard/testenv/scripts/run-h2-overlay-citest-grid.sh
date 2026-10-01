#!/usr/bin/env bash
# §9.1: §8.2 suite list on current versiond and versiond-router, proxy overlay,
# Connect over HTTP/2. No 0.2.15-v5 pin, no native-gRPC matrix, no lowered
# rate limits. Resume: a PASS line is not run again. FAIL is retried.
set -u
cd "$(dirname "$0")/.."

ENDPOINTS="signatures,mempool,diffs,gossip,repair,height-sync,verify-timeout,verify-error-miss,challenge-receipt,payload,chat"
OUT="../docs/grpc-transport-phase6-9.1-results.txt"
mkdir -p "$(dirname "$OUT")"
touch "$OUT"

SUITES=(
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
  citest-peerrpc-chat
  citest-peerrpc-ha-session
  citest-host-ping
)

already_passed() {
  grep -qxE "$1 PASS" "$OUT"
}

run_cell() {
  local suite="$1"
  if already_passed "H2 $suite"; then
    echo "skip H2 $suite"
    return 0
  fi
  echo "=== H2 $suite ==="
  if env -u TESTENV_VERSIOND_IMAGE -u TESTENV_VERSIOND_ROUTER_IMAGE \
    -u DEVSHARD_RPC_MSGS_PER_MIN -u DEVSHARD_RPC_MSGS_BURST \
    -u DEVSHARD_RPC_ATTACH_PER_MIN_TOTAL \
    TESTENV_PROXY_OVERLAY=1 \
    DEVSHARD_RPC_SERVER_ENABLED=true \
    DEVSHARD_RPC_ENDPOINTS="$ENDPOINTS" \
    DEVSHARD_RPC_H2_PORT=8443 \
    DEVSHARD_RPC_H2_HOST=proxy \
    DEVSHARD_RPC_H2_UPGRADE=true \
    DEVSHARD_RPC_H2_FRONT_HOST=versiond-router \
    make "$suite"; then
    grep -v "^H2 $suite " "$OUT" > "$OUT.tmp" || true
    echo "H2 $suite PASS" >> "$OUT.tmp"
    mv "$OUT.tmp" "$OUT"
  else
    grep -v "^H2 $suite " "$OUT" > "$OUT.tmp" || true
    echo "H2 $suite FAIL" >> "$OUT.tmp"
    mv "$OUT.tmp" "$OUT"
  fi
}

for suite in "${SUITES[@]}"; do
  run_cell "$suite"
done

echo "grid finished"
sort "$OUT"
