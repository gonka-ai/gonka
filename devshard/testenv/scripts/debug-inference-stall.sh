#!/usr/bin/env bash
# Audit a slow container E2E inference (e.g. long gap between advance nonce N and N+1).
#
# Mock inference has zero latency; gaps of ~65s are almost always devshardctl waiting
# RefusalTimeout (60s) + 5s buffer when the executor never delivers receipt+finish on SSE.
#
# Usage (stack must be up from make e2e / compose):
#   ./scripts/debug-inference-stall.sh 12
#   NONCE=12 SINCE=10m ./scripts/debug-inference-stall.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTENV_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PROJECT="${CONTAINER_E2E_PROJECT:-heightsynce2e}"
NONCE="${1:-${NONCE:-}}"
SINCE="${SINCE:-15m}"
LOKI_URL="${LOKI_URL:-http://127.0.0.1:3100}"

if [[ -z "$NONCE" ]]; then
  echo "usage: $0 <inference_nonce>" >&2
  exit 1
fi

HOST_IDX=$((NONCE % 4))
EXECUTOR="devshardd-testenv-${HOST_IDX}"

compose() {
  docker compose -f "$TESTENV_ROOT/docker-compose.yml" -p "$PROJECT" "$@"
}

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

log "━━ stall audit nonce=$NONCE executor=$EXECUTOR project=$PROJECT since=$SINCE ━━"

log "devshardctl (proxy / timeout / SendOnly):"
compose logs --timestamps --since "$SINCE" --tail=500 devshardctl 2>/dev/null \
  | grep -E "inference nonce=${NONCE}|inference ${NONCE}:|timed out|SendOnly failed|REFUSED" \
  || true

log ""
log "executor $EXECUTOR (mock engine / SSE hold):"
compose logs --timestamps --since "$SINCE" --tail=500 "$EXECUTOR" 2>/dev/null \
  | grep -E "inference_id.: ?${NONCE}|inference_id=${NONCE}|testenv inference|response hold|heightsync:" \
  || true

if curl -fsS "$LOKI_URL/ready" >/dev/null 2>&1; then
  log ""
  log "Loki (last 15m, devshardctl):"
  END_NS=$(python3 -c 'import time; print(int(time.time()*1e9))')
  START_NS=$(python3 -c 'import time; print(int((time.time()-900)*1e9))')
  Q='{service_name="devshardctl"} |~ "inference.*'"${NONCE}"'|SendOnly|timed out"'
  curl -fsSG "$LOKI_URL/loki/api/v1/query_range" --data-urlencode "query=$Q" \
    --data-urlencode "limit=50" --data-urlencode "direction=backward" \
    --data-urlencode "start=$START_NS" --data-urlencode "end=$END_NS" \
    | jq -r '.data.result[].values[][1]' 2>/dev/null | head -30 || true
fi

log ""
log "hint: gap ≈ 65s → RefusalTimeout wait; check SendOnly failed or missing MsgFinishInference in mempool"
log "hint: gap ≈ 130s → two refusal attempts before TIMEOUT_REASON_REFUSED"
