#!/usr/bin/env bash
#
# Driver for height-sync container E2E (devshard/testenv/scenarios/container).
#
# Brings up ONE shared compose stack (preflight + build + health wait), then runs
# go test with TESTENV_REUSE_STACK=1 so scenarios attach instead of each calling
# compose up/down (same pattern as scripts/run-stack-citest.sh for citest).
#
# Each run uses isolated obs + SQLite bind mounts (TESTENV_OBS_REL_SUBDIR,
# TESTENV_E2E_DB_REL_SUBDIR). Before go test, host + devshardctl DB dirs are wiped
# and services restarted so the session starts at nonce 0.
#
# Usage (from anywhere):
#   bash devshard/testenv/scripts/run-container-heightsync-e2e.sh
#
# Env:
#   CONTAINER_E2E_PHASE      — a | cadence | b | phase-b | all (default)
#   CONTAINER_E2E_RUN       — full go test -run regex (overrides CONTAINER_E2E_PHASE)
#   CONTAINER_E2E_PROJECT   — compose project name (default heightsynce2e)
#   CONTAINER_E2E_RUN_ID    — suffix for per-run obs/db dirs (default UTC timestamp-PID)
#   TESTENV_OBS_REL_SUBDIR   — obs bind-mount root (default .container-e2e-obs-data/run-<id>)
#   TESTENV_E2E_DB_REL_SUBDIR — host/ctl SQLite root (default .container-e2e-db/run-<id>)
#   SKIP_REGEN=1            — skip make gen-integration-config
#   SKIP_PREFLIGHT=1        — skip compose K/slots checks
#   SKIP_UP=1               — skip compose up (tests only; stack must already match config)
#   SKIP_SESSION_RESET=1    — skip DB wipe + restart before go test (default: skip)
#   RESET_SESSION=1         — force one DB wipe + restart before go test (debug only)
#   SKIP_DOWN=1             — leave stack running after tests (make e2e-keep); use for log analysis
#   TESTENV_SKIP_DOCKER_STACK=1 — passed through (tests skip)
#
# Bare `go test -tags=testenvci ./testenv/scenarios/container/...` without this script
# still uses isolated per-test stacks (no TESTENV_REUSE_STACK).
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
TESTENV_ROOT="$(cd "$SCRIPT_DIR/.." && pwd)"
DEVSHARD_ROOT="$(cd "$TESTENV_ROOT/.." && pwd)"
COMPOSE_FILE="$TESTENV_ROOT/docker-compose.yml"

export CONTAINER_E2E_PROJECT="${CONTAINER_E2E_PROJECT:-heightsynce2e}"
E2E_RUN_ID="${CONTAINER_E2E_RUN_ID:-$(date -u +%Y%m%dT%H%M%SZ)-$$}"
export TESTENV_OBS_REL_SUBDIR="${TESTENV_OBS_REL_SUBDIR:-.container-e2e-obs-data/run-${E2E_RUN_ID}}"
export TESTENV_E2E_DB_REL_SUBDIR="${TESTENV_E2E_DB_REL_SUBDIR:-.container-e2e-db/run-${E2E_RUN_ID}}"
OBS_ROOT="$TESTENV_ROOT/$TESTENV_OBS_REL_SUBDIR"
DB_ROOT="$TESTENV_ROOT/$TESTENV_E2E_DB_REL_SUBDIR"

log() { printf '%s %s\n' "$(date -u +%Y-%m-%dT%H:%M:%SZ)" "$*"; }
die() { log "ERROR: $*"; exit 1; }
need_cmd() { command -v "$1" >/dev/null 2>&1 || die "missing command: $1"; }
# shellcheck source=wait-mockdapi-oracle-ready.sh
source "$SCRIPT_DIR/wait-mockdapi-oracle-ready.sh"

compose() {
  docker compose -f "$COMPOSE_FILE" -p "$CONTAINER_E2E_PROJECT" "$@"
}

ensure_e2e_db_symlink() {
  mkdir -p "$DB_ROOT/devshardctl" \
    "$DB_ROOT/devshardd-testenv-0" \
    "$DB_ROOT/devshardd-testenv-1" \
    "$DB_ROOT/devshardd-testenv-2" \
    "$DB_ROOT/devshardd-testenv-3"
  rm -rf "$TESTENV_ROOT/db"
  ln -sfn "$TESTENV_E2E_DB_REL_SUBDIR" "$TESTENV_ROOT/db"
}

wipe_e2e_db_dirs() {
  rm -rf "$DB_ROOT/devshardctl"/* \
    "$DB_ROOT/devshardd-testenv-0"/* \
    "$DB_ROOT/devshardd-testenv-1"/* \
    "$DB_ROOT/devshardd-testenv-2"/* \
    "$DB_ROOT/devshardd-testenv-3"/*
  mkdir -p "$DB_ROOT/devshardctl" \
    "$DB_ROOT/devshardd-testenv-0" \
    "$DB_ROOT/devshardd-testenv-1" \
    "$DB_ROOT/devshardd-testenv-2" \
    "$DB_ROOT/devshardd-testenv-3"
}

reset_e2e_session_and_hosts() {
  log "reset: wipe SQLite (obs=${TESTENV_OBS_REL_SUBDIR} db=${TESTENV_E2E_DB_REL_SUBDIR}) + restart devshardctl/hosts"
  wipe_e2e_db_dirs
  compose restart \
    devshardctl \
    devshardd-testenv-0 devshardd-testenv-1 devshardd-testenv-2 devshardd-testenv-3 \
    || die "compose restart after DB wipe failed"
  wait_stack_healthy
}

wait_stack_healthy() {
  local deadline=$((SECONDS + 300))
  local hs_ok=0
  while (( SECONDS < deadline )); do
    local body
    body=$(curl -fsS --max-time 5 http://127.0.0.1:9100/block/latest 2>/dev/null || true)
    if echo "$body" | grep -qE '"[Hh]eight"[[:space:]]*:[[:space:]]*[1-9][0-9]*'; then
      hs_ok=1
      break
    fi
    sleep 2
  done
  (( hs_ok == 1 )) || die "height-sync not ready on :9100 within 5m"

  for url in \
    "http://127.0.0.1:8081/v1/status" \
    "http://127.0.0.1:3100/ready" \
    "http://127.0.0.1:8428/api/v1/query?query=1"; do
    local ok=0
    while (( SECONDS < deadline )); do
      if curl -fsS --max-time 5 "$url" >/dev/null 2>&1; then
        ok=1
        break
      fi
      sleep 2
    done
    (( ok == 1 )) || die "not ready: $url"
  done

  log "  mockdapi: wait host block-oracle consumers (SSE + devshardd_height_at_latest_nonce)"
  wait_mockdapi_oracle_ready
}

prune_stale_testenv_networks() {
  log "prune: compose down for leftover *_testenv bridge networks (frees 172.30.0.0/24)"
  while IFS= read -r name; do
    [[ -z "$name" ]] && continue
    [[ "$name" != *_testenv ]] && continue
    proj="${name%_testenv}"
    log "  tearing down stale project $proj (network $name)"
    docker compose -f "$COMPOSE_FILE" -p "$proj" down --remove-orphans --timeout 60 2>/dev/null || true
    docker network rm "$name" 2>/dev/null || true
  done < <(docker network ls --format '{{.Name}}' 2>/dev/null || true)
}

need_cmd docker
need_cmd go
cd "$TESTENV_ROOT"

phase="$(printf '%s' "${CONTAINER_E2E_PHASE:-all}" | tr '[:upper:]' '[:lower:]')"
if [[ -n "${CONTAINER_E2E_RUN:-}" ]]; then
  run_re="$CONTAINER_E2E_RUN"
else
  case "$phase" in
    a|cadence|phase-a) run_re='^TestContainerE2E_HeightSync_Cadence$' ;;
    b|phase-b) run_re='^TestContainerE2E_HeightSync_(LostFirstResponse|ForceAnchorSingleMessage|CheatingTrail)$' ;;
    c|phase-c) run_re='^TestContainerE2E_HeightSync_(FeedStoppedOmits|FeedRecovers|Smoke)$' ;;
    all|'') run_re='^TestContainerE2E_HeightSync_' ;;
    *) die "unknown CONTAINER_E2E_PHASE=$phase (use a, b, c, or all)" ;;
  esac
fi

log "━━ container height-sync E2E ━━ project=$CONTAINER_E2E_PROJECT run=$E2E_RUN_ID ━━"
log "  obs=${TESTENV_OBS_REL_SUBDIR}/ db=${TESTENV_E2E_DB_REL_SUBDIR}/"

did_regen=0
if [[ "${SKIP_REGEN:-}" != "1" ]]; then
  log "[1/7] make gen-integration-config"
  make gen-integration-config
  did_regen=1
else
  log "[1/7] SKIP_REGEN=1"
fi

ensure_e2e_db_symlink

[[ -f "$COMPOSE_FILE" ]] || die "missing $COMPOSE_FILE"

if [[ "${SKIP_PREFLIGHT:-}" != "1" ]]; then
  log "[2/7] preflight: HEIGHT_SYNC scheduler (K ≥ slots)"
  grep -q 'HEIGHT_SYNC_ANCHOR_PERIOD_NONCES' "$COMPOSE_FILE" || die "compose missing HEIGHT_SYNC_ANCHOR_PERIOD_NONCES"
  grep -q 'HEIGHT_SYNC_SYNC_TURN_SLOTS' "$COMPOSE_FILE" || die "compose missing HEIGHT_SYNC_SYNC_TURN_SLOTS"
  k=$(grep -m1 'HEIGHT_SYNC_ANCHOR_PERIOD_NONCES' "$COMPOSE_FILE" | sed -E 's/.*: "([0-9]+)".*/\1/' || true)
  s=$(grep -m1 'HEIGHT_SYNC_SYNC_TURN_SLOTS' "$COMPOSE_FILE" | sed -E 's/.*: "([0-9]+)".*/\1/' || true)
  [[ -n "$k" && -n "$s" ]] || die "could not parse K/slots from compose"
  (( k >= s )) || die "invalid scheduler: K=$k < slots=$s"
  log "  preflight compose: K=$k sync_turn_slots=$s (ok)"
else
  log "[2/7] SKIP_PREFLIGHT=1"
fi

if [[ "${SKIP_UP:-}" != "1" ]]; then
  log "[3/7] prune stale testenv networks + compose up"
  prune_stale_testenv_networks
  mkdir -p "$OBS_ROOT/victoria-metrics" "$OBS_ROOT/loki" "$OBS_ROOT/grafana" "$OBS_ROOT/alloy"
  wipe_e2e_db_dirs
  if [[ "$did_regen" == "1" ]]; then
    log "  config regenerated → down, wipe obs, up --build --force-recreate"
    compose down --remove-orphans --timeout 120 2>/dev/null || true
    rm -rf "$OBS_ROOT"
    mkdir -p "$OBS_ROOT/victoria-metrics" "$OBS_ROOT/loki" "$OBS_ROOT/grafana" "$OBS_ROOT/alloy"
    wipe_e2e_db_dirs
    compose up -d --build --force-recreate
  else
    need_up=0
    for svc in mock-chain height-sync devshardd-testenv-0 devshardctl; do
      st=$(compose ps --status running --format '{{.State}}' "$svc" 2>/dev/null | head -1 || true)
      if [[ "$st" != "running" ]]; then
        need_up=1
        log "  service $svc not running (state=${st:-absent})"
        break
      fi
    done
    if [[ "$need_up" == "1" ]]; then
      log "  compose up -d --build (isolated obs/db for run $E2E_RUN_ID)"
      compose up -d --build --force-recreate
    else
      log "  core services running — recreate obs/db mounts for run $E2E_RUN_ID"
      compose up -d --force-recreate \
        victoria-metrics loki grafana alloy \
        devshardctl \
        devshardd-testenv-0 devshardd-testenv-1 devshardd-testenv-2 devshardd-testenv-3
    fi
  fi
else
  log "[3/7] SKIP_UP=1"
fi

log "[4/7] wait: height-sync :9100, devshardctl :8081, loki, VM, mockdapi oracle on hosts"
wait_stack_healthy
log "  stack healthy (height-sync HTTP + mockdapi consumers)"

if [[ "${SKIP_DOWN:-}" != "1" ]]; then
  cleanup() {
    log "compose down project=$CONTAINER_E2E_PROJECT"
    compose down --remove-orphans --timeout 120 || true
  }
  trap cleanup EXIT
fi

# Cumulative nonce suite: tests advance to the next sync-turn lead themselves.
# Set RESET_SESSION=1 (or SKIP_SESSION_RESET=0) for a one-shot fresh session at nonce 1.
if [[ "${RESET_SESSION:-}" == "1" ]] || [[ "${SKIP_SESSION_RESET:-1}" == "0" ]]; then
  log "[5/7] reset E2E session (fresh devshardctl + host SQLite)"
  reset_e2e_session_and_hosts
else
  log "[5/7] cumulative session (no DB reset; tests advance nonces relative to /v1/status)"
fi

log "[6/7] go test -tags=testenvci TESTENV_REUSE_STACK=1 -run '$run_re'"
cd "$DEVSHARD_ROOT"
export TESTENV_REUSE_STACK=1
export SKIP_REGEN=1
test_status=0
go test -tags=testenvci -count=1 -timeout=60m -v \
  -run "$run_re" \
  ./testenv/scenarios/container/... || test_status=$?

log "[7/7] finished (exit $test_status)"
if [[ "${SKIP_DOWN:-}" == "1" ]]; then
  log "SKIP_DOWN=1 — stack left up (project=$CONTAINER_E2E_PROJECT). Example log commands:"
  log "  docker compose -f $COMPOSE_FILE -p $CONTAINER_E2E_PROJECT logs --tail=200 devshardctl"
  log "  docker compose -f $COMPOSE_FILE -p $CONTAINER_E2E_PROJECT logs --tail=200 devshardd-testenv-1 | grep -E 'heightsync|SendOnly|inference'"
  log "  curl -sG 'http://127.0.0.1:3100/loki/api/v1/query_range' --data-urlencode 'query={service_name=\"devshardd-testenv\"} |= \"heightsync\"' ..."
fi
exit "$test_status"
