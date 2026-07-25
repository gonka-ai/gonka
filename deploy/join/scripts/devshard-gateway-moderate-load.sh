#!/usr/bin/env bash
# Moderate gateway load for scheduled CI: reuse one funded escrow, Bearer API key.
#
# Exit codes:
#   0  success, or skipped because chain is in PoC / not Inference
#   1  soft failures (preflight, missing tools/env)
#   2  load assertions failed (< SUCCESS_PCT HTTP 200)
#
# Required env:
#   GATEWAY_URL   e.g. http://89.169.111.79:8000/devshard-gateway
#   API_KEY       gateway user/admin bearer token
#   MODEL         model id registered on the escrow
#
# Optional:
#   EPOCH_URL           default: derive from GATEWAY_URL host → /v1/epochs/latest
#   ESCROW_ID           set X-Devshard-Id when multiple escrows are active
#   N                   requests (default 10)
#   CONCURRENCY         in-flight cap (default 5)
#   MAX_TOKENS          default 128
#   TIMEOUT_S           per-request curl timeout (default 120)
#   SUCCESS_PCT         min % of HTTP 200 (default 90)
#   SKIP_ON_POC         1 = exit 0 outside Inference (default 1)
#   STREAM              0 = non-stream (default); 1 = stream
set -euo pipefail

GATEWAY_URL="${GATEWAY_URL:?GATEWAY_URL is required}"
API_KEY="${API_KEY:?API_KEY is required}"
MODEL="${MODEL:?MODEL is required}"
GATEWAY_URL="${GATEWAY_URL%/}"

N="${N:-10}"
CONCURRENCY="${CONCURRENCY:-5}"
MAX_TOKENS="${MAX_TOKENS:-128}"
TIMEOUT_S="${TIMEOUT_S:-120}"
SUCCESS_PCT="${SUCCESS_PCT:-90}"
SKIP_ON_POC="${SKIP_ON_POC:-1}"
STREAM="${STREAM:-0}"
ESCROW_ID="${ESCROW_ID:-}"

if [[ -z "${EPOCH_URL:-}" ]]; then
  # http://host:8000/devshard-gateway → http://host:8000/v1/epochs/latest
  base="${GATEWAY_URL%/devshard-gateway}"
  base="${base%/devshard/v4}"
  base="${base%/devshard/v3}"
  EPOCH_URL="${base}/v1/epochs/latest"
fi

command -v curl >/dev/null || { echo "curl required" >&2; exit 1; }
command -v jq >/dev/null || { echo "jq required" >&2; exit 1; }
command -v python3 >/dev/null || { echo "python3 required" >&2; exit 1; }

ts() { date -u +%Y-%m-%dT%H:%M:%SZ; }
log() { echo "$(ts) $*"; }

phase_gate() {
  local phase conf
  phase=$(curl -fsS --max-time 20 "$EPOCH_URL" | jq -r '.phase // "unknown"')
  conf=$(curl -fsS --max-time 20 "$EPOCH_URL" | jq -r '.is_confirmation_poc_active // false')
  log "epoch_url=$EPOCH_URL phase=$phase confirmation_poc=$conf"
  if [[ "$phase" == "Inference" && "$conf" == "false" ]]; then
    return 0
  fi
  if [[ "$SKIP_ON_POC" == "1" ]]; then
    log "SKIP: not in Inference (or confirmation PoC active)"
    exit 0
  fi
  echo "chain not in Inference (phase=$phase confirmation_poc=$conf)" >&2
  exit 1
}

phase_gate

OUTDIR="$(mktemp -d "${TMPDIR:-/tmp}/gw-moderate-load.XXXXXX")"
trap 'rm -rf "$OUTDIR"' EXIT

AUTH_HDR=(-H "Authorization: Bearer $API_KEY" -H "Content-Type: application/json")
EXTRA_HDR=()
if [[ -n "$ESCROW_ID" ]]; then
  EXTRA_HDR=(-H "X-Devshard-Id: $ESCROW_ID")
fi

PROMPTS=(
  "Say hello in one short sentence."
  "Name three primary colors."
  "What is 7 plus 5? Reply with just the number."
  "Write one sentence about rain."
  "List two mammals."
  "Reply with the word ready."
  "Give a one-word greeting."
  "What day comes after Monday? One word."
  "Complete: the sky is"
  "Reply OK."
)

log "target=$GATEWAY_URL/v1/chat/completions model=$MODEL N=$N concurrency=$CONCURRENCY max_tokens=$MAX_TOKENS escrow=${ESCROW_ID:-auto} stream=$STREAM"

do_one() {
  local i="$1"
  local prompt="${PROMPTS[$((i % ${#PROMPTS[@]}))]}"
  local body code
  local stream_json=false
  [[ "$STREAM" == "1" ]] && stream_json=true
  body="$(jq -nc \
    --arg m "$MODEL" \
    --arg c "$prompt" \
    --argjson mt "$MAX_TOKENS" \
    --argjson stream "$stream_json" \
    '{model:$m, messages:[{role:"user",content:$c}], max_tokens:$mt, stream:$stream}')"
  local out="$OUTDIR/req_${i}.out"
  local meta="$OUTDIR/req_${i}.meta"
  local start end
  start="$(date +%s)"
  code="$(curl -sS -o "$out" -w '%{http_code}' --max-time "$TIMEOUT_S" \
    -X POST "$GATEWAY_URL/v1/chat/completions" \
    "${AUTH_HDR[@]}" "${EXTRA_HDR[@]}" \
    -d "$body" || echo 000)"
  end="$(date +%s)"
  printf '%s %s %s\n' "$i" "$code" "$((end - start))" >"$meta"
}

# Simple semaphore via background jobs
running=0
for i in $(seq 1 "$N"); do
  while [[ "$running" -ge "$CONCURRENCY" ]]; do
    wait -n || true
    running=$((running - 1))
  done
  do_one "$i" &
  running=$((running + 1))
done
wait || true

ok=0
fail=0
echo "---- per-request ----"
for i in $(seq 1 "$N"); do
  meta="$OUTDIR/req_${i}.meta"
  if [[ ! -f "$meta" ]]; then
    log "req=$i missing meta"
    fail=$((fail + 1))
    continue
  fi
  read -r idx code ms <"$meta"
  if [[ "$code" == "200" ]]; then
    ok=$((ok + 1))
  else
    fail=$((fail + 1))
    head -c 200 "$OUTDIR/req_${i}.out" 2>/dev/null | tr '\n' ' ' || true
    echo
  fi
  echo "req=$idx http=$code ms=$ms"
done

pct="$(python3 -c "print(0 if $N==0 else int(100.0*$ok/$N))")"
log "summary ok=$ok fail=$fail total=$N success_pct=$pct threshold=$SUCCESS_PCT"
if [[ -n "${GITHUB_STEP_SUMMARY:-}" ]]; then
  {
    echo "## Gateway moderate load"
    echo ""
    echo "| metric | value |"
    echo "|--------|-------|"
    echo "| ok | $ok |"
    echo "| fail | $fail |"
    echo "| total | $N |"
    echo "| success_pct | $pct |"
    echo "| threshold | $SUCCESS_PCT |"
    echo "| gateway | \`$GATEWAY_URL\` |"
    echo "| escrow | \`${ESCROW_ID:-auto}\` |"
  } >>"$GITHUB_STEP_SUMMARY"
fi

if [[ "$pct" -lt "$SUCCESS_PCT" ]]; then
  log "FAIL: success_pct $pct < $SUCCESS_PCT"
  exit 2
fi
log "PASS"
exit 0
