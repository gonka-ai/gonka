#!/usr/bin/env bash
#
# Run multiple chat-completions through subnetctl and verify refusal-timeout behaviour
# by stopping the Docker Compose participant that owns the executor slot for each
# inference. Intended for the local testenv stack (mock-server + participants + subnetctl).
#
# Prerequisites:
#   - curl, jq
#   - docker compose (for participant stop/start when not using --no-docker)
#   - testenv up: from subnet/testenv: `docker compose up -d` (or make up)
#
# Usage:
#   ./scripts/subnetctl_timeout_check.sh
#   SUBNETCTL_URL=http://127.0.0.1:8081 ./scripts/subnetctl_timeout_check.sh
#   RUNS=3 REFUSAL_MAX_SECONDS=150 ./scripts/subnetctl_timeout_check.sh
#
# Environment:
#   SUBNETCTL_URL        Base URL of subnetctl (default http://127.0.0.1:8081)
#   TESTENV_DIR          Directory containing docker-compose.yml (default: parent of this script)
#   COMPOSE_FILE         Override compose file path
#   NUM_SLOTS            Group size for slot math (default 16; must match mock escrow)
#   RUNS                 How many refusal-timeout inferences to run (default 3)
#   REFUSAL_MAX_SECONDS  curl --max-time; refusal uses RefusalTimeout (60s) + buffer (~5s) + votes
#   --no-docker          Only call subnetctl; do not stop containers (manual fault injection)
#   --skip-happy         Skip the quick success check (all hosts must be up)
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTENV_DIR="${TESTENV_DIR:-$(cd "${SCRIPT_DIR}/.." && pwd)}"
COMPOSE_FILE="${COMPOSE_FILE:-${TESTENV_DIR}/docker-compose.yml}"
SUBNETCTL_URL="${SUBNETCTL_URL:-http://127.0.0.1:8081}"
NUM_SLOTS="${NUM_SLOTS:-16}"
RUNS="${RUNS:-3}"
REFUSAL_MAX_SECONDS="${REFUSAL_MAX_SECONDS:-150}"

USE_DOCKER=1
SKIP_HAPPY=0
while [[ $# -gt 0 ]]; do
  case "$1" in
    --no-docker) USE_DOCKER=0 ;;
    --skip-happy) SKIP_HAPPY=1 ;;
    -h|--help)
      sed -n '2,30p' "$0"
      exit 0
      ;;
    *)
      echo "unknown arg: $1" >&2
      exit 1
      ;;
  esac
  shift
done

need() { command -v "$1" >/dev/null 2>&1 || { echo "missing required command: $1" >&2; exit 1; }; }
need curl
need jq

# Map executor slot index (0..NUM_SLOTS-1) to compose service name for the default
# gencompose layout: 16 slots, 10 participants, slot s -> participant-$((s % 10)).
participant_for_slot() {
  local slot="$1"
  local n=$((slot % 10))
  echo "participant-${n}"
}

json_chat_body() {
  local model="${1:-Qwen/Qwen2.5-7B-Instruct}"
  jq -nc --arg m "$model" \
    '{model:$m, stream:false, max_tokens:32,
      messages:[{role:"user", content:"timeout-check ping"}]}'
}

http_get_status() {
  curl -fsS "${SUBNETCTL_URL}/v1/status"
}

# Next inference uses nonce (current + 1); executor slot is that id mod NUM_SLOTS.
executor_slot_for_next_inference() {
  local nonce
  nonce="$(http_get_status | jq -r '.nonce')"
  echo $(( (nonce + 1) % NUM_SLOTS ))
}

chat_completion() {
  local outfile="$1"
  local maxt="${2:-$REFUSAL_MAX_SECONDS}"
  curl -sS -o "$outfile" -w '%{http_code}' \
    --max-time "${maxt}" \
    -H 'Content-Type: application/json' \
    -X POST "${SUBNETCTL_URL}/v1/chat/completions" \
    -d "$(json_chat_body)"
}

assert_refusal_timeout_response() {
  local file="$1"
  local code="$2"
  if [[ "$code" != "502" ]]; then
    echo "expected HTTP 502, got $code. Body:" >&2
    cat "$file" >&2
    return 1
  fi
  # Body may be text/plain with JSON inside (http.Error); match substrings.
  if ! grep -q 'timed out' "$file" || ! grep -q 'REFUSED' "$file"; then
    echo "expected error body to mention timed out and REFUSED; got:" >&2
    cat "$file" >&2
    return 1
  fi
}

assert_success_response() {
  local file="$1"
  local code="$2"
  if [[ "$code" != "200" ]]; then
    echo "expected HTTP 200, got $code. Body:" >&2
    cat "$file" >&2
    return 1
  fi
  if ! jq -e . >/dev/null 2>&1 <"$file"; then
    echo "expected JSON body:" >&2
    cat "$file" >&2
    return 1
  fi
}

compose() {
  (cd "$TESTENV_DIR" && docker compose -f "$COMPOSE_FILE" "$@")
}

stop_participant() {
  local svc="$1"
  if [[ "$USE_DOCKER" -eq 1 ]]; then
    echo "Stopping ${svc} (executor unreachable)..."
    compose stop "$svc"
  fi
}

start_participant() {
  local svc="$1"
  if [[ "$USE_DOCKER" -eq 1 ]]; then
    echo "Starting ${svc}..."
    compose start "$svc"
  fi
}

echo "subnetctl: ${SUBNETCTL_URL}"
echo "compose:   ${COMPOSE_FILE} (docker control: $([[ "$USE_DOCKER" -eq 1 ]] && echo on || echo off))"

if [[ "$USE_DOCKER" -eq 1 ]] && [[ ! -f "$COMPOSE_FILE" ]]; then
  echo "compose file not found: $COMPOSE_FILE" >&2
  exit 1
fi

tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

if [[ "$SKIP_HAPPY" -eq 0 ]]; then
  echo ""
  echo "=== Happy path: single completion (all hosts up) ==="
  out="${tmpdir}/happy.json"
  code="$(chat_completion "$out" 90)"
  assert_success_response "$out" "$code"
  echo "OK (HTTP 200, JSON response)"
fi

echo ""
echo "=== Refusal timeouts: ${RUNS} run(s) ==="
for ((i = 1; i <= RUNS; i++)); do
  slot="$(executor_slot_for_next_inference)"
  svc="$(participant_for_slot "$slot")"
  echo ""
  echo "Run ${i}/${RUNS}: next executor slot=${slot} -> compose service ${svc}"

  stop_participant "$svc"
  # Give Docker a moment to tear down the HTTP server on that participant.
  sleep 2

  out="${tmpdir}/timeout-${i}.txt"
  code="$(chat_completion "$out" "$REFUSAL_MAX_SECONDS")" || true

  start_participant "$svc"
  sleep 2

  assert_refusal_timeout_response "$out" "$code"
  echo "OK (HTTP 502, refusal timeout message)"

  # Optional: show aggregate inference status counts after each run
  if curl -fsS "${SUBNETCTL_URL}/v1/debug/state" | jq -e . >/dev/null 2>&1; then
    echo "state snapshot:"
    curl -fsS "${SUBNETCTL_URL}/v1/debug/state" | jq '{nonce, total_inferences, status_counts}'
  fi
done

echo ""
echo "All checks passed."
