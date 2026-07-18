#!/usr/bin/env bash
# Drive gateway chat load to produce finished inferences (validation candidates).
#
# Usage (stack already up; escrow warm preferred):
#   ./scripts/lease-race-load.sh
#   GATEWAY=http://127.0.0.1:8081 NON_STREAM=40 STREAM=20 ./scripts/lease-race-load.sh
set -euo pipefail

GATEWAY="${GATEWAY:-http://127.0.0.1:8081}"
KEY="${KEY:-${TESTENV_ADMIN_API_KEY:-testenv-citest-admin}}"
MODEL="${MODEL:-test-model}"
NON_STREAM="${NON_STREAM:-40}"
STREAM="${STREAM:-20}"

echo "lease-race-load: gateway=${GATEWAY} model=${MODEL} non_stream=${NON_STREAM} stream=${STREAM}"

for i in $(seq 1 "${NON_STREAM}"); do
  code="$(curl -sS -o /tmp/lease-race-ns.json -w "%{http_code}" -X POST "${GATEWAY}/v1/chat/completions" \
    -H "Authorization: Bearer ${KEY}" \
    -H "Content-Type: application/json" \
    -d "{\"model\":\"${MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"lease race non-stream ${i}\"}],\"max_tokens\":32,\"stream\":false}" \
    || true)"
  echo "non-stream ${i}: HTTP ${code}"
done

for i in $(seq 1 "${STREAM}"); do
  code="$(curl -sS -o /tmp/lease-race-st.txt -w "%{http_code}" -N -X POST "${GATEWAY}/v1/chat/completions" \
    -H "Authorization: Bearer ${KEY}" \
    -H "Content-Type: application/json" \
    -H "Accept: text/event-stream" \
    -d "{\"model\":\"${MODEL}\",\"messages\":[{\"role\":\"user\",\"content\":\"lease race stream ${i}\"}],\"max_tokens\":32,\"stream\":true}" \
    || true)"
  echo "stream ${i}: HTTP ${code}"
done

echo "lease-race-load: done"
