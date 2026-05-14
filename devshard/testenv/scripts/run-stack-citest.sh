#!/usr/bin/env bash
#
# End-to-end driver for citest stack integration (local or CI-like).
#
# ══ Run order (always this sequence) ══════════════════════════════════════
#
#   [1] Materialize config + compose (unless SKIP_REGEN=1)
#       make gen-integration-config
#         → bash scripts/gen-integration-testenv-config.sh
#         → writes devshard/testenv/config.yaml and docker-compose.yml
#       I9 later: Go test loads config.yaml for the pinned 10-validator
#       verifier; mock-chain signs blocks using the same file **at container
#       start**. If you change config.yaml without recreating containers, I9
#       fails (stream vs disk mismatch).
#
#   [2] Preflight (unless SKIP_PREFLIGHT=1)
#       - Grep docker-compose for HEIGHT_SYNC_* and assert K ≥ sync_turn_slots.
#       - Optionally scan recent devshardd-testenv-0 logs for scheduler fatals.
#
#   [3] Docker Compose (unless SKIP_UP=1)
#       - If [1] ran: `up -d --build --force-recreate` so mock-chain, height-sync,
#         and devshardd-* reload the new config.yaml (required for I9).
#       - If SKIP_REGEN=1: `up -d --build` only when core services are down.
#
#   [4] Wait for height-sync GET http://127.0.0.1:9100/block/latest (height > 0).
#
#   [5] Optional VictoriaMetrics query (inference gossip sum) when jq is installed.
#
#   [6] Post-up log scan on devshardd-testenv-0 for scheduler fatals.
#
#   [7] Citest integration test binary (from devshard module root):
#       go test -tags=testenvci -c -o $tmp ./testenv/citest
#       (cd testenv/citest && TESTENV_REUSE_STACK=1 $tmp -test.v -test.run …)
#       Running the **compiled** binary from the package dir matches `go test`'s
#       working directory and avoids the `go` tool relay-buffering stdout/stderr.
#       Inside TestStackIntegrationI1andSection8_7:
#         · I1   — height-sync /block/latest returns height > 0
#         · §7.7 — Grafana / Loki / Victoria wiring smoke (poll until OK)
#         · I2a  — per-host heights from each /metrics (strict spread)
#         · I2b  — same gauge via VictoriaMetrics (relaxed spread)
#         · I9   — 20× /block/latest headers verify against height_sync.validators
#                  in testenv/config.yaml (see [1] + [3])
#
# Usage:
#   bash devshard/testenv/scripts/run-stack-citest.sh
# Or from devshard/testenv:
#   bash ./scripts/run-stack-citest.sh
#
# Env:
#   SKIP_REGEN=1     — skip [1]; keep existing config.yaml / docker-compose.yml
#   SKIP_PREFLIGHT=1 — skip [2]
#   SKIP_UP=1        — skip [3] (dangerous right after regen; see WARN log)
#   CITEST_LEGACY_GO_TEST=1 — run `go test` instead of compile+exec (may buffer until exit)
#   CITEST_TEST_PTY=1     — force script(1) PTY around the compiled test (use in IDEs that
#                           fake a TTY but still batch child output; also auto when stdout is not a TTY)
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTENV_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DEVSHARD_ROOT="$(cd "$TESTENV_ROOT/.." && pwd)"
COMPOSE_FILE="$TESTENV_ROOT/docker-compose.yml"

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }

die() { log "ERROR: $*"; exit 1; }

need_cmd() { command -v "$1" >/dev/null 2>&1 || die "missing command: $1"; }

# Run citest integration without `go test` relay buffering: compile once, then exec the
# test binary from testenv/citest (same cwd `go test ./testenv/citest` uses for filepath.Abs("..")).
# When stdout is not a TTY (some CI / IDE runners) or CITEST_TEST_PTY=1, wrap the binary in
# script(1) so the process gets a PTY and -v / stderr stream incrementally.
run_citest_integration_exec() {
  citest_bin="$(mktemp "${TMPDIR:-/tmp}/citest-stack.XXXXXX")"
  trap 'rm -f "${citest_bin:-}"' EXIT
  cd "$DEVSHARD_ROOT"
  log "  go test -tags=testenvci -c → $citest_bin"
  go test -tags=testenvci -c -o "$citest_bin" ./testenv/citest
  log "  exec from $TESTENV_ROOT/citest (TESTENV_REUSE_STACK=1)"
  local use_script=0
  if [[ "${CITEST_TEST_PTY:-}" == "1" ]]; then
    use_script=1
  elif ! [[ -t 1 ]]; then
    use_script=1
  fi
  if [[ "$use_script" == "1" ]] && command -v script >/dev/null 2>&1; then
    log "  script(1) PTY wrapper (non-TTY stdout or CITEST_TEST_PTY=1) — live test output"
    if script -V 2>/dev/null | grep -qi util-linux; then
      _td=$(printf %q "$TESTENV_ROOT/citest")
      _tb=$(printf %q "$citest_bin")
      script -qefc "cd ${_td} && export TESTENV_REUSE_STACK=1 && exec ${_tb} -test.v -test.timeout=15m -test.parallel=1 -test.run TestStackIntegrationI1andSection8_7" /dev/null
    else
      script -q /dev/null sh -c 'cd "$1" && export TESTENV_REUSE_STACK=1 && exec "$2" -test.v -test.timeout=15m -test.parallel=1 -test.run TestStackIntegrationI1andSection8_7' _ "$TESTENV_ROOT/citest" "$citest_bin"
    fi
    return
  fi
  if [[ "$use_script" == "1" ]]; then
    log "WARN: wanted script(1) for live output but 'script' not on PATH — output may batch"
  fi
  (cd "$TESTENV_ROOT/citest" && TESTENV_REUSE_STACK=1 exec "$citest_bin" \
    -test.v -test.timeout=15m -test.parallel=1 \
    -test.run TestStackIntegrationI1andSection8_7)
}

run_citest_go_test_streaming() {
  if ! command -v script >/dev/null 2>&1; then
    log "WARN: 'script' not on PATH — running go test directly"
    "$@"
    return
  fi
  local cmdline="" a esc
  for a in "$@"; do
    printf -v esc '%q' "$a"
    cmdline+="${cmdline:+ }${esc}"
  done
  if script -V 2>/dev/null | grep -qi util-linux; then
    script -qefc "$cmdline" /dev/null
  else
    script -q /dev/null "$@"
  fi
}

need_cmd docker
need_cmd go
cd "$TESTENV_ROOT"

log "━━ citest stack driver ━━ order: [1] gen config → [2] preflight → [3] compose → [4] wait HS → [5] VM? → [6] log scan → [7] go test (I1 → §7.7 → I2a → I2b → I9) ━━"

did_regen=0
if [[ "${SKIP_REGEN:-}" != "1" ]]; then
  log "[1/7] make gen-integration-config  (writes testenv/config.yaml + docker-compose.yml for I9 verifier + mock-chain)"
  make gen-integration-config
  did_regen=1
else
  log "[1/7] SKIP_REGEN=1 — leaving config.yaml / docker-compose.yml unchanged"
fi

[[ -f "$COMPOSE_FILE" ]] || die "missing $COMPOSE_FILE"

if [[ "${SKIP_PREFLIGHT:-}" != "1" ]]; then
  log "[2/7] preflight: HEIGHT_SYNC scheduler (K ≥ slots) + optional devshardd logs"
  if ! grep -q 'HEIGHT_SYNC_ANCHOR_PERIOD_NONCES' "$COMPOSE_FILE"; then
    die "docker-compose.yml missing HEIGHT_SYNC_ANCHOR_PERIOD_NONCES (re-run make gen-integration-config)"
  fi
  if ! grep -q 'HEIGHT_SYNC_SYNC_TURN_SLOTS' "$COMPOSE_FILE"; then
    die "docker-compose.yml missing HEIGHT_SYNC_SYNC_TURN_SLOTS"
  fi
  k=$(grep -m1 'HEIGHT_SYNC_ANCHOR_PERIOD_NONCES' "$COMPOSE_FILE" | sed -E 's/.*: "([0-9]+)".*/\1/' || true)
  s=$(grep -m1 'HEIGHT_SYNC_SYNC_TURN_SLOTS' "$COMPOSE_FILE" | sed -E 's/.*: "([0-9]+)".*/\1/' || true)
  [[ -n "$k" && -n "$s" ]] || die "could not parse K/slots from compose (expected quoted integers)"
  if (( k < s )); then
    die "invalid scheduler: ANCHOR K=$k < SYNC_TURN_SLOTS=$s (heightsync would fatal with K < SlotsNum)"
  fi
  log "  preflight compose: K=$k sync_turn_slots=$s (ok)"

  if docker compose -f docker-compose.yml ps -q devshardd-testenv-0 2>/dev/null | grep -q .; then
    if docker compose -f docker-compose.yml logs devshardd-testenv-0 --tail 400 2>/dev/null | grep -E 'invalid scheduler config|K < SlotsNum|devshardd-testenv: fatal'; then
      die "devshardd-testenv-0 logs contain scheduler/fatal lines — fix compose/config"
    fi
    log "  preflight logs: no scheduler fatal pattern in last 400 lines"
  else
    log "  preflight logs: devshardd-testenv-0 not running yet (will re-check after compose up)"
  fi
else
  log "[2/7] SKIP_PREFLIGHT=1"
fi

if [[ "${SKIP_UP:-}" != "1" ]]; then
  log "[3/7] docker compose: ensure stack matches disk config (I9 needs mock-chain + height-sync to have read current config.yaml)"
  if [[ "$did_regen" == "1" ]]; then
    log "  config was regenerated → docker compose up -d --build --force-recreate"
    docker compose -f docker-compose.yml up -d --build --force-recreate
  else
    need_up=0
    for svc in mock-chain height-sync devshardd-testenv-0; do
      st=$(docker compose -f docker-compose.yml ps --status running --format '{{.State}}' "$svc" 2>/dev/null | head -1 || true)
      if [[ "$st" != "running" ]]; then
        need_up=1
        log "  service $svc not running (state=${st:-absent})"
        break
      fi
    done
    if [[ "$need_up" == "1" ]]; then
      log "  docker compose up -d --build"
      docker compose -f docker-compose.yml up -d --build
    else
      log "  core services already running (SKIP_REGEN=1 path; config unchanged on disk)"
    fi
  fi
else
  log "[3/7] SKIP_UP=1: not invoking docker compose up"
  if [[ "$did_regen" == "1" ]]; then
    log "  WARN: config was regenerated but SKIP_UP=1 — recreate containers or I9 may fail (validator set vs config.yaml mismatch)"
  fi
fi

log "[4/7] wait: height-sync http://127.0.0.1:9100/block/latest (I1 gate)"
deadline=$((SECONDS + 240))
hs_ok=0
while (( SECONDS < deadline )); do
  body=$(curl -fsS --max-time 5 http://127.0.0.1:9100/block/latest 2>/dev/null || true)
  if echo "$body" | grep -qE '"[Hh]eight"[[:space:]]*:[[:space:]]*[1-9][0-9]*'; then
    hs_ok=1
    log "  height-sync responding"
    break
  fi
  sleep 2
done
(( hs_ok == 1 )) || die "height-sync did not become ready on :9100 within 4m"

VM_Q='sum(devshardd_gossip_messages_total%7Bkind%3D%22inference%22%7D)'
if command -v jq >/dev/null 2>&1; then
  log "[5/7] VictoriaMetrics (optional): $VM_Q"
  vm_out=$(curl -fsS --max-time 8 "http://127.0.0.1:8428/api/v1/query?query=${VM_Q}" 2>/dev/null || true)
  inf=$(echo "$vm_out" | jq -r '.data.result[0].value[1] // empty' 2>/dev/null || true)
  if [[ -n "$inf" ]]; then
    log "  VM scalar = $inf (may be 0 until scrapes warm)"
  else
    log "  VM: no scalar yet (still warming); continuing"
  fi
else
  log "[5/7] VictoriaMetrics query skipped (install jq for inference-gossip scalar here)"
fi

log "[6/7] post-up log scan: devshardd-testenv-0"
if docker compose -f docker-compose.yml logs devshardd-testenv-0 --tail 500 2>/dev/null | grep -E 'invalid scheduler config|K < SlotsNum|devshardd-testenv: fatal'; then
  die "devshardd-testenv-0 logs show fatal after compose up"
fi

log "[7/7] citest integration — compile + exec test binary (live output); or CITEST_LEGACY_GO_TEST=1 for go test"
cd "$DEVSHARD_ROOT"
export TESTENV_REUSE_STACK=1
if [[ "${CITEST_LEGACY_GO_TEST:-}" == "1" ]]; then
  run_citest_go_test_streaming go test -count=1 -parallel=1 -timeout=15m -tags=testenvci -v \
    -run TestStackIntegrationI1andSection8_7 ./testenv/citest/...
else
  run_citest_integration_exec
fi

log "done — see test log lines for I1, §7.7, I2a, I2b, and I9 (N/20 verified headers)"
