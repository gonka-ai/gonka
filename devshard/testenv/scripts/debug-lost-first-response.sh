#!/usr/bin/env bash
# Manual repro for TestContainerE2E_HeightSync_LostFirstResponse.
#
# Model: session advances on StartInference (Prepare); Confirm can arrive while ML runs.
# Advance until sync-turn LEAD (1, 8, 16, …; periodic leads have nonce%8==0), then:
#   1. POST /v1/debug/arm-hold-inference-response on the lead's executor host
#   2. POST chat completion (lead) — host processes diff but sends no SSE yet
#   3. compose stop that host — proxy sees SendOnly failed (no response body)
#   4. compose start host, POST lead+1 (recover)
#
# Requires images built with -tags=dev and DEVSHARDCTL_DEBUG=1 / DEVSHARDD_DEBUG=1 (gencompose).
# Arm-hold goes through
# devshardctl POST /v1/debug/arm-host-hold?host_idx=N (no curl sidecar on the compose network).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTENV_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
PROJECT="${CONTAINER_E2E_PROJECT:-heightsynce2e}"
ANCHOR_K="${HEIGHT_SYNC_ANCHOR_K:-8}"
HOST_HTTP_PORT="${TESTENV_HOST_HTTP_PORT:-8080}"
STATUS_URL="${DEVSHARDCTL_STATUS_URL:-http://127.0.0.1:8081/v1/status}"
CHAT_URL="${DEVSHARDCTL_CHAT_URL:-http://127.0.0.1:8081/v1/chat/completions}"
ARM_HOLD_URL="${DEVSHARDCTL_ARM_HOLD_URL:-http://127.0.0.1:8081/v1/debug/arm-host-hold}"

compose() {
  docker compose -f "$TESTENV_ROOT/docker-compose.yml" -p "$PROJECT" "$@"
}

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "ERROR: $*"; exit 1; }

is_sync_turn_lead() {
  local n=$1
  if (( n == 1 )); then return 0; fi
  if (( n >= ANCHOR_K && n % ANCHOR_K == 0 )); then return 0; fi
  return 1
}

sync_turn_lead_from() {
  local start=$1
  if (( start <= 1 )); then echo 1; return 0; fi
  if is_sync_turn_lead "$start"; then echo "$start"; return 0; fi
  if (( start < ANCHOR_K )); then echo "$ANCHOR_K"; return 0; fi
  local rem=$((start % ANCHOR_K))
  echo $((start + ANCHOR_K - rem))
}

host_service_for_nonce() {
  echo "devshardd-testenv-$(( $1 % 4 ))"
}

fetch_next_nonce() {
  local n
  n="$(curl -fsS "$STATUS_URL" | jq -r '.nonce')"
  [[ -n "$n" && "$n" =~ ^[0-9]+$ ]] || die "could not read .nonce from $STATUS_URL"
  echo "$n"
}

# Fire-and-forget POST; poll status until nonce advances (matches real session semantics).
post_inference_advance_nonce() {
  local expect_start=$1
  local label=$2
  log "  POST $label (advance status past $expect_start) ..."
  curl -sS -N --max-time 300 -X POST "$CHAT_URL" \
    -H 'Content-Type: application/json' \
    -d '{"model":"llama","stream":true,"max_tokens":50}' >/dev/null 2>&1 &
  local deadline=$(( $(date +%s) + 120 ))
  while (( $(date +%s) < deadline )); do
    local n
    n="$(fetch_next_nonce)"
    if (( n > expect_start )); then
      return 0
    fi
    sleep 0.25
  done
  die "timeout: nonce still $expect_start after $label"
}

advance_to_nonce() {
  local target=$1
  local cur
  cur="$(fetch_next_nonce)"
  if (( cur == target )); then
    log "session at nonce $cur (ready for lead=$target)"
    return 0
  fi
  if (( cur > target )); then
    die "session nonce $cur past lead $target"
  fi
  log "advance: warm up to lead=$target (nonce may advance before stream [DONE] — expected)"
  while (( cur < target )); do
    post_inference_advance_nonce "$cur" "nonce $cur"
    cur="$(fetch_next_nonce)"
    if (( cur > target )); then
      die "overshot: nonce $cur > lead $target"
    fi
  done
  (( cur == target )) || die "advance ended at $cur, expected $target"
  log "session at nonce $cur (next POST is lead / logical 1)"
}

arm_host_hold() {
  local svc=$1
  local idx="${svc##*-}"
  local url="${ARM_HOLD_URL}?host_idx=${idx}"
  log "arm hold on $svc (host_idx=$idx) via devshardctl ($url) ..."
  local out code
  out="$(mktemp)"
  code="$(curl -sS -o "$out" -w '%{http_code}' -X POST "$url")" || die "arm hold curl failed on $svc"
  if [[ "$code" != "204" && "$code" != "200" ]]; then
    die "arm hold failed on $svc (HTTP $code): $(tr -d '\n' <"$out"); rebuild with -tags=dev and DEVSHARDCTL_DEBUG=1 DEVSHARDD_DEBUG=1 (make gen-integration-config)"
  fi
  rm -f "$out"
}

pick_lead_recover() {
  local next=$1
  if [[ -n "${LEAD_NONCE:-}" ]]; then
    echo "${LEAD_NONCE} ${RECOVER_NONCE:-$((LEAD_NONCE + 1))}"
    return 0
  fi
  local lead
  lead="$(sync_turn_lead_from "$next")"
  echo "$lead $((lead + 1))"
}

NEXT="$(fetch_next_nonce)"
read -r LEAD_NONCE RECOVER_NONCE < <(pick_lead_recover "$NEXT")
KILL_SVC="$(host_service_for_nonce "$LEAD_NONCE")"

log "project=$PROJECT next=$NEXT lead=$LEAD_NONCE recover=$RECOVER_NONCE kill=$KILL_SVC"
curl -fsS "$STATUS_URL" | jq . 2>/dev/null || curl -fsS "$STATUS_URL"
echo

advance_to_nonce "$LEAD_NONCE"
arm_host_hold "$KILL_SVC"

log "━━ logical 1: POST lead while host holds SSE, then stop host ━━"
(
  curl -sS -N --max-time 120 -X POST "$CHAT_URL" \
    -H 'Content-Type: application/json' \
    -d '{"model":"llama","stream":true,"max_tokens":50}' >/tmp/lost-first-fail-sse.txt 2>&1 || true
) &
sleep 1
log "stopping $KILL_SVC ..."
compose stop "$KILL_SVC"
sleep 3
if compose logs --tail=50 devshardctl 2>/dev/null | grep -q "inference nonce=${LEAD_NONCE}.*SendOnly failed"; then
  log "OK: SendOnly failed for lead nonce $LEAD_NONCE"
else
  log "WARN: no SendOnly failed yet for nonce=$LEAD_NONCE"
  compose logs --tail=25 devshardctl 2>/dev/null | grep -E "nonce=${LEAD_NONCE}|SendOnly" || true
fi

# JSON logs use "nonce":N (not nonce=N). Match anchor request emit for a given nonce.
logs_have_anchor_emit() {
  local n=$1
  compose logs --tail=300 devshardctl 2>/dev/null \
    | grep -F '"msg":"heightsync: emit"' \
    | grep -F '"direction":"request"' \
    | grep -F '"mode":"anchor"' \
    | grep -E "\"nonce\":${n}[,}]" \
    | grep -q .
}

log "━━ logical 2: start host, POST recover $RECOVER_NONCE ━━"
compose start "$KILL_SVC"
sleep 3
cur="$(fetch_next_nonce)"
log "session nonce before recover POST: $cur (expect <= $RECOVER_NONCE)"
log "  POST recover nonce $RECOVER_NONCE (fire-and-forget; do not wait for [DONE]) ..."
curl -sS -N --max-time 120 -X POST "$CHAT_URL" \
  -H 'Content-Type: application/json' \
  -d '{"model":"llama","stream":true,"max_tokens":50}' >/dev/null 2>&1 &
recover_curl_pid=$!
deadline=$(( $(date +%s) + 60 ))
found_emit=0
while (( $(date +%s) < deadline )); do
  if logs_have_anchor_emit "$RECOVER_NONCE"; then
    found_emit=1
    break
  fi
  if ! kill -0 "$recover_curl_pid" 2>/dev/null; then
    break
  fi
  sleep 0.5
done
if kill -0 "$recover_curl_pid" 2>/dev/null; then
  kill "$recover_curl_pid" 2>/dev/null || true
fi
if (( found_emit )); then
  log "OK: devshardctl heightsync emit request anchor for nonce $RECOVER_NONCE"
else
  log "WARN: no heightsync emit anchor for nonce=$RECOVER_NONCE within 60s (check logs below)"
  compose logs --tail=40 devshardctl 2>/dev/null \
    | grep -F '"msg":"heightsync: emit"' \
    | grep -F '"mode":"anchor"' \
    | tail -5 || true
fi
curl -fsS "$STATUS_URL" | jq .nonce 2>/dev/null || true
log "done"
