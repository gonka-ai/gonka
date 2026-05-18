# shellcheck shell=bash
# wait_mockdapi_oracle_ready — sourced by stack drivers after height-sync HTTP is up.
#
# height-sync /block/latest can be positive while devshardd mockdapi clients still have
# Stale()==true (no SSE yet). Sync-turn host responses then log heightsync emit mode=omit.
#
# Requires: TESTENV_ROOT, optional COMPOSE_FILE (for MOCKDAPI_STALE_AFTER parse).
# Optional: MOCKDAPI_ORACLE_WAIT_SEC (default 120).

wait_mockdapi_oracle_ready() {
  local deadline=$((SECONDS + ${MOCKDAPI_ORACLE_WAIT_SEC:-120}))
  local cfg="${TESTENV_ROOT:?}/config.yaml"
  [[ -f "$cfg" ]] || die "wait_mockdapi_oracle_ready: missing $cfg"

  local stale_after=3
  if [[ -n "${COMPOSE_FILE:-}" && -f "$COMPOSE_FILE" ]] \
    && grep -q 'MOCKDAPI_STALE_AFTER' "$COMPOSE_FILE" 2>/dev/null; then
    local s
    s=$(grep -m1 'MOCKDAPI_STALE_AFTER' "$COMPOSE_FILE" | sed -E 's/.*"([0-9]+)s".*/\1/' || true)
    [[ -n "$s" ]] && stale_after="$s"
  fi

  local h0=0 h1=0 body
  body=$(curl -fsS --max-time 5 http://127.0.0.1:9100/block/latest 2>/dev/null || true)
  if echo "$body" | grep -qE '"[Hh]eight"[[:space:]]*:[[:space:]]*([0-9]+)'; then
    h0=$(echo "$body" | sed -nE 's/.*"[Hh]eight"[[:space:]]*:[[:space:]]*([0-9]+).*/\1/p' | head -1)
  fi
  log "  mockdapi oracle: wait height-sync block advance (from height=${h0:-0})"
  while (( SECONDS < deadline )); do
    body=$(curl -fsS --max-time 5 http://127.0.0.1:9100/block/latest 2>/dev/null || true)
    if echo "$body" | grep -qE '"[Hh]eight"[[:space:]]*:[[:space:]]*([0-9]+)'; then
      h1=$(echo "$body" | sed -nE 's/.*"[Hh]eight"[[:space:]]*:[[:space:]]*([0-9]+).*/\1/p' | head -1)
      if [[ -n "$h1" && "$h1" -gt "${h0:-0}" ]]; then
        log "  mockdapi oracle: height-sync advanced ${h0:-0} → $h1"
        break
      fi
    fi
    sleep 0.4
  done
  sleep "$(( stale_after + 1 ))"

  mapfile -t ports < <(grep 'public_metrics_port:' "$cfg" | sed -E 's/.*: *([0-9]+).*/\1/')
  ((${#ports[@]} > 0)) || die "no public_metrics_port in $cfg"

  log "  mockdapi oracle: poll devshardd_height_at_latest_nonce on ports ${ports[*]}"
  while (( SECONDS < deadline )); do
    local ready=0
    for p in "${ports[@]}"; do
      body=$(curl -fsS --max-time 3 "http://127.0.0.1:${p}/metrics" 2>/dev/null || true)
      if echo "$body" | grep -qE '^devshardd_height_at_latest_nonce(\{[^}]*\})? [1-9][0-9]*'; then
        ready=$((ready + 1))
      fi
    done
    if (( ready == ${#ports[@]} )); then
      log "  mockdapi oracle ready on ${#ports[@]} host(s)"
      return 0
    fi
    sleep 0.5
  done
  die "mockdapi block oracle not ready on all hosts (devshardd_height_at_latest_nonce > 0; MOCKDAPI_STALE_AFTER=${stale_after}s)"
}
