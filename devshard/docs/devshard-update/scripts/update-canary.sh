#!/usr/bin/env bash
set -Eeuo pipefail
# -E: inherit ERR trap into functions (bash >= 4).

# Operator-driven canary cutover for a single-instance Gonka gateway.
# Temp runs the target image; MAIN stays on the source image until finish.
# No sleeps — you wait, then re-run check / set yourself.
#
# Modes:
#   prepare          stand up temp + mint escrows + smoke (nginx still 100% MAIN)
#   set <percent>    send <percent>% of nginx traffic to TEMP (0..100)
#   check            verify MAIN/TEMP/public health + show current canary weights
#   finish           require/force 100% TEMP, then drain/recreate MAIN and fold temp
#   plan|validate    inspection (no side effects beyond validate)
#   recover          delegate to update.sh recover
#
# Usage:
#   ./update-canary.sh --config update.config.json prepare
#   ./update-canary.sh --config update.config.json set 10 --run
#   ./update-canary.sh --config update.config.json check --run
#   ./update-canary.sh --config update.config.json set 50 --run
#   ./update-canary.sh --config update.config.json finish --run

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
UPDATE_SH="${SCRIPT_DIR}/update.sh"

usage() {
  cat <<'EOF'
Canary update for a single-instance devshard gateway (temp=v3, MAIN=source).

  ./update-canary.sh --config <file> <mode> [percent] [flags]

Modes:
  prepare           create temp gateway + escrows; leave nginx on MAIN
  set <percent>     weighted traffic to TEMP (0=MAIN only, 100=TEMP only)
  check             health gates + print current upstream weight lines
  finish            100% TEMP (if needed) then recreate MAIN and fold temp
  plan|validate     show plan / validate shared update.config.json
  recover           stranded temp escrow recover (delegates to update.sh)

Flags: same as update.sh (--run, --dry-run, --yes, --deploy-dir, --settle).
There is no built-in wait — observe yourself between set/check calls.
EOF
}

CONFIG_ARG="${DEVSHARD_UPDATE_CONFIG:-./update.config.json}"
MODE=""; SET_PERCENT=""; DEPLOY_DIR="$(pwd)"
DRY_RUN=1; [[ "${RUN:-0}" == "1" ]] && DRY_RUN=0
ASSUME_YES=0; RECOVER_SETTLE=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --config)       CONFIG_ARG="${2:?--config needs a value}"; shift 2 ;;
    --config=*)     CONFIG_ARG="${1#*=}"; shift ;;
    --deploy-dir)   DEPLOY_DIR="${2:?--deploy-dir needs a value}"; shift 2 ;;
    --deploy-dir=*) DEPLOY_DIR="${1#*=}"; shift ;;
    --settle)       RECOVER_SETTLE=1; shift ;;
    --run)          DRY_RUN=0; shift ;;
    --dry-run)      DRY_RUN=1; shift ;;
    --yes|-y)       ASSUME_YES=1; shift ;;
    --help|-h)      usage; exit 0 ;;
    --*)            echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
    prepare|check|finish|plan|validate|recover)
      MODE="$1"; shift ;;
    set)
      MODE="set"; shift
      SET_PERCENT="${1:?set needs a percent 0..100}"; shift ;;
    *)
      # allow: set 10 as two tokens already handled; bare number after set only
      echo "unknown argument: $1" >&2; usage >&2; exit 2 ;;
  esac
done
[[ -n "${MODE}" ]] || MODE="plan"

gate_ok()   { printf '    GATE-OK %s\n' "$1"; }
gate_fail() { printf '    GATE-FAIL %s\n' "$1"; }
note()      { printf '      - %s\n' "$1"; }

abspath() { case "$1" in /*) printf '%s\n' "$1" ;; *) printf '%s/%s\n' "$(pwd)" "$1" ;; esac; }

update_flags() {
  local -a flags=(--config "${CONFIG_ARG}" --deploy-dir "${DEPLOY_DIR}")
  [[ "${DRY_RUN}" == "0" ]] && flags+=(--run)
  [[ "${ASSUME_YES}" == "1" ]] && flags+=(--yes)
  [[ "${RECOVER_SETTLE}" == "1" ]] && flags+=(--settle)
  printf '%s\n' "${flags[@]}"
}

run_update() {
  local -a flags=()
  local line
  while IFS= read -r line; do flags+=("${line}"); done < <(update_flags)
  note "update.sh $* ${flags[*]}"
  bash "${UPDATE_SH}" "${flags[@]}" "$@"
}

# Load the same JSON fields update.sh uses for nginx / admin URLs (lightweight).
load_canary_config() {
  local path; path="$(abspath "${CONFIG_ARG}")"
  [[ -f "${path}" ]] || { echo "config file not found: ${path}" >&2; exit 2; }
  CONFIG_JSON="$(cat "${path}")"
  ROUTING_TYPE="$(jq -r '.routing.type // "nginx"' <<<"${CONFIG_JSON}")"
  [[ "${ROUTING_TYPE}" == "nginx" ]] || {
    echo "update-canary.sh supports routing.type=nginx only (got ${ROUTING_TYPE}); use update.sh for a full ${ROUTING_TYPE} blue/green update" >&2
    exit 2
  }
  MAIN_ADMIN_URL="$(jq -r '.main.admin_url // empty' <<<"${CONFIG_JSON}")"
  TEMP_ADMIN_PORT="$(jq -r '.temp.admin_port // empty' <<<"${CONFIG_JSON}")"
  TEMP_ADMIN_URL="http://127.0.0.1:${TEMP_ADMIN_PORT}"
  TEMP_UPSTREAM_ALIAS="$(jq -r '.temp.upstream_alias // empty' <<<"${CONFIG_JSON}")"
  NGINX_PROXY_CONTAINER="$(jq -r '.nginx.proxy_container // empty' <<<"${CONFIG_JSON}")"
  NGINX_CONFIG_PATH="$(jq -r '.nginx.config_path // empty' <<<"${CONFIG_JSON}")"
  NGINX_OLD_UPSTREAM="$(jq -r '.nginx.old_upstream // empty' <<<"${CONFIG_JSON}")"
  NGINX_NEW_UPSTREAM="$(jq -r '.nginx.new_upstream // empty' <<<"${CONFIG_JSON}")"
  [[ -n "${NGINX_NEW_UPSTREAM}" ]] || NGINX_NEW_UPSTREAM="${TEMP_UPSTREAM_ALIAS}"
  NGINX_UPSTREAM_PORT="$(jq -r '.nginx.upstream_port // empty' <<<"${CONFIG_JSON}")"
  NGINX_PUBLIC_BASE_URL="$(jq -r '.nginx.public_base_url // empty' <<<"${CONFIG_JSON}")"
  NGINX_PUBLIC_PREFIX="$(jq -r '.nginx.public_prefix // "/devshard-gateway"' <<<"${CONFIG_JSON}")"
  SMOKE_MODEL="$(jq -r '.smoke_test.model // empty' <<<"${CONFIG_JSON}")"
  [[ -n "${SMOKE_MODEL}" ]] || SMOKE_MODEL="$(jq -r '.models[0].model // empty' <<<"${CONFIG_JSON}")"
  MAIN_STORAGE_HOST_DIR="$(jq -r '.main.storage_host_dir // ".devshardctl"' <<<"${CONFIG_JSON}")"
  # Prefer deploy-dir state (writable); .devshardctl is often root-owned on prod hosts.
  CANARY_STATE_FILE="$(abspath "${DEPLOY_DIR}")/canary-run-state.env"
}

save_canary_percent() {
  [[ -n "${CANARY_STATE_FILE:-}" ]] || load_canary_config
  mkdir -p "$(dirname "${CANARY_STATE_FILE}")"
  printf 'CANARY_TEMP_PERCENT=%s\n' "$1" > "${CANARY_STATE_FILE}"
}

read_canary_percent() {
  # shellcheck disable=SC1090
  [[ -f "${CANARY_STATE_FILE}" ]] && source "${CANARY_STATE_FILE}" || true
  echo "${CANARY_TEMP_PERCENT:-0}"
}

need_key() {
  [[ -n "${DEVSHARD_ADMIN_API_KEY:-}" ]] || {
    echo "DEVSHARD_ADMIN_API_KEY is not set in this shell (source config.devshard.env first)" >&2
    exit 2
  }
}

admin_get() {
  curl -fsS "$1$2" -H "Authorization: Bearer ${DEVSHARD_ADMIN_API_KEY}"
}

smoke_chat() {
  local auth_hdr=()
  if [[ -n "${DEVSHARD_ADMIN_API_KEY:-}" ]]; then
    auth_hdr=(-H "Authorization: Bearer ${DEVSHARD_ADMIN_API_KEY}")
  elif [[ -n "${DEVSHARD_API_KEYS:-}" ]]; then
    auth_hdr=(-H "Authorization: Bearer ${DEVSHARD_API_KEYS%%,*}")
  fi
  curl -fsS -X POST "$1/v1/chat/completions" "${auth_hdr[@]}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg m "$2" '{model:$m, stream:false, max_tokens:1, messages:[{role:"user", content:"Reply with ok"}]}')" >/dev/null
}

# Rewrite nginx so TEMP gets $1% of traffic (MAIN gets the rest) via upstream
# `server` weights. percent=0 → MAIN only; percent=100 → TEMP only (full switch).
# Direct proxy_pass http://host:port paths cannot weight; those stay as-is and
# check warns if they still pin only MAIN while percent>0.
nginx_set_percent() {
  local percent="$1"
  local main="${NGINX_OLD_UPSTREAM}" temp="${NGINX_NEW_UPSTREAM}" port="${NGINX_UPSTREAM_PORT}"
  local cfg="${NGINX_CONFIG_PATH}" proxy="${NGINX_PROXY_CONTAINER}"
  local backup="${cfg}.canary-backup" tmp="${cfg}.canary-tmp"
  local w_temp="${percent}" w_main=$((100 - percent))

  if ! [[ "${percent}" =~ ^[0-9]+$ ]] || (( percent < 0 || percent > 100 )); then
    echo "percent must be an integer 0..100, got: ${percent}" >&2
    return 2
  fi

  note "nginx canary TEMP=${percent}% MAIN=$((100 - percent))% (${temp}:${port} / ${main}:${port})"
  if [[ "${DRY_RUN}" == "1" ]]; then
    note "would rewrite ${cfg} server weights and nginx -t && reload"
    return 0
  fi

  local inner
  # percent 0 → MAIN only; 100 → TEMP only; else both with relative weights.
  # Also rewrites direct proxy_pass http://host:port → the active sole host when
  # percent is 0 or 100; mid-canary direct proxy_pass cannot weight (check warns).
  inner="$(cat <<INNER
set -eu
test -f '${cfg}'
cp '${cfg}' '${backup}'

awk -v main='${main}' -v temp='${temp}' -v port='${port}' -v wm='${w_main}' -v wt='${w_temp}' '
  BEGIN {
    re_main = "^[[:space:]]*server[[:space:]]+" main ":" port "([^0-9]|\$)"
    re_temp = "^[[:space:]]*server[[:space:]]+" temp ":" port "([^0-9]|\$)"
    touched_any = 0
  }
  function emit_servers() {
    if (wm > 0) printf "        server %s:%s weight=%d max_fails=2 fail_timeout=30s;\\n", main, port, wm
    if (wt > 0) printf "        server %s:%s weight=%d max_fails=2 fail_timeout=30s;\\n", temp, port, wt
    emitted = 1
  }
  /^[[:space:]]*upstream[[:space:]]/ {
    in_up = 1; removed = 0; emitted = 0
    print; next
  }
  in_up && (\$0 ~ re_main || \$0 ~ re_temp) {
    removed = 1; touched_any = 1; next
  }
  in_up && /^[[:space:]]*}/ {
    if (removed && !emitted) emit_servers()
    in_up = 0; print; next
  }
  in_up && /^[[:space:]]*server[[:space:]]/ && removed && !emitted {
    emit_servers()
    print; next
  }
  wm == 100 && wt == 0 {
    gsub("http://" temp ":" port, "http://" main ":" port)
  }
  wm == 0 && wt == 100 {
    gsub("http://" main ":" port, "http://" temp ":" port)
  }
  { print }
  END {
    if (!touched_any) {
      print "ERROR: no upstream server lines matched MAIN/TEMP hosts — check nginx.old_upstream/new_upstream" > "/dev/stderr"
      exit 3
    }
  }
' '${cfg}' > '${tmp}'

if mv '${tmp}' '${cfg}' 2>/dev/null; then
  :
else
  cat '${tmp}' > '${cfg}'
  rm -f '${tmp}'
fi

if [ '${w_main}' -gt 0 ]; then
  grep -Eq "server[[:space:]]+${main}:${port}" '${cfg}'
fi
if [ '${w_temp}' -gt 0 ]; then
  grep -Eq "server[[:space:]]+${temp}:${port}" '${cfg}'
fi
nginx -t
nginx -s reload
echo "canary weights applied TEMP=${w_temp} MAIN=${w_main}"
INNER
)"
  if docker exec "${proxy}" sh -lc "${inner}"; then
    save_canary_percent "${percent}"
    gate_ok "canary TEMP=${percent}% applied and reloaded"
    return 0
  fi
  gate_fail "canary weight set to ${percent}% failed (restore ${backup} if needed)"
  return 1
}

show_canary_nginx() {
  note "nginx server lines for MAIN/TEMP (from nginx -T)"
  docker exec "${NGINX_PROXY_CONTAINER}" sh -lc \
    "nginx -T 2>/dev/null | grep -nE 'server[[:space:]]+(${NGINX_OLD_UPSTREAM}|${NGINX_NEW_UPSTREAM}):${NGINX_UPSTREAM_PORT}|proxy_pass http://(${NGINX_OLD_UPSTREAM}|${NGINX_NEW_UPSTREAM}):${NGINX_UPSTREAM_PORT}' | head -40" \
    || true
}

cmd_prepare() {
  load_canary_config
  echo "==> canary prepare (temp up; nginx stays on MAIN)"
  run_update init
  run_update preflight
  run_update disable-main-rotation
  run_update create-temp-gateway
  run_update create-temp-escrows
  run_update check-temp
  save_canary_percent 0
  gate_ok "prepare complete — next: set 10 --run, then check --run (you wait between)"
}

cmd_set() {
  load_canary_config
  echo "==> canary set TEMP=${SET_PERCENT}%"
  nginx_set_percent "${SET_PERCENT}"
  show_canary_nginx
}

cmd_check() {
  load_canary_config
  need_key
  echo "==> canary check (recorded TEMP percent=$(read_canary_percent))"
  if [[ "${DRY_RUN}" == "1" ]]; then
    note "would check MAIN/TEMP status, smoke, public smoke, nginx weight lines"
    return 0
  fi

  local main_max temp_max
  main_max="$(admin_get "${MAIN_ADMIN_URL}" "/v1/status" | jq '[.devshards[]?.active_requests // 0] | max // 0')"
  temp_max="$(admin_get "${TEMP_ADMIN_URL}" "/v1/status" | jq '[.devshards[]?.active_requests // 0] | max // 0')"
  note "MAIN max active_requests=${main_max}"
  note "TEMP max active_requests=${temp_max}"
  gate_ok "MAIN and TEMP status reachable"

  local pct; pct="$(read_canary_percent)"
  if (( pct > 0 && temp_max == 0 )); then
    note "WARNING: canary percent=${pct} but TEMP shows 0 active_requests (may be idle, or weights not hitting TEMP)"
  fi
  if (( pct > 0 && pct < 100 && temp_max > 0 )); then
    gate_ok "TEMP is receiving traffic (active_requests=${temp_max})"
  fi

  smoke_chat "${MAIN_ADMIN_URL}" "${SMOKE_MODEL}"
  gate_ok "MAIN direct smoke (${SMOKE_MODEL})"
  smoke_chat "${TEMP_ADMIN_URL}" "${SMOKE_MODEL}"
  gate_ok "TEMP direct smoke (${SMOKE_MODEL})"

  if [[ -n "${NGINX_PUBLIC_BASE_URL}" ]]; then
    smoke_chat "${NGINX_PUBLIC_BASE_URL}${NGINX_PUBLIC_PREFIX}" "${SMOKE_MODEL}"
    gate_ok "public smoke (${NGINX_PUBLIC_BASE_URL}${NGINX_PUBLIC_PREFIX})"
  else
    note "public_base_url unset — skipping public smoke"
  fi

  show_canary_nginx
  # Warn about hard-coded single-host proxy_pass (cannot canary).
  if docker exec "${NGINX_PROXY_CONTAINER}" sh -lc \
      "nginx -T 2>/dev/null | grep -E 'proxy_pass http://${NGINX_OLD_UPSTREAM}:${NGINX_UPSTREAM_PORT}'" >/dev/null 2>&1; then
    note "WARNING: some proxy_pass lines still hard-code MAIN (${NGINX_OLD_UPSTREAM}:${NGINX_UPSTREAM_PORT}); those paths do not follow canary weights"
  fi
}

cmd_finish() {
  load_canary_config
  echo "==> canary finish (cut over MAIN to target image, fold temp)"
  local pct; pct="$(read_canary_percent)"
  if (( pct != 100 )); then
    note "recorded canary percent=${pct}; forcing 100% TEMP before finish"
    SET_PERCENT=100
    nginx_set_percent 100
  fi
  run_update check-alias-temp
  run_update drain-main
  run_update deactivate-main-source
  run_update bump-main-image
  run_update update-main
  run_update create-main-seed-escrows
  run_update check-main-direct
  run_update switch-to-main
  run_update check-alias-main
  run_update drain-temp
  run_update stop-temp
  run_update import-temp
  run_update activate-temp
  run_update restore-main-rotation
  run_update status
  save_canary_percent 0
  gate_ok "canary finish complete"
}

print_plan() {
  load_canary_config
  cat <<EOF
Devshard gateway CANARY update (operator-paced)
Config: $(abspath "${CONFIG_ARG}")
Mode:   $([[ "${DRY_RUN}" == "1" ]] && echo "dry-run (use --run to execute)" || echo LIVE)
MAIN:   ${NGINX_OLD_UPSTREAM} @ ${MAIN_ADMIN_URL}
TEMP:   ${NGINX_NEW_UPSTREAM} @ ${TEMP_ADMIN_URL}
Nginx:  ${NGINX_PROXY_CONTAINER}:${NGINX_CONFIG_PATH}
Public: ${NGINX_PUBLIC_BASE_URL:-<unset>}
Smoke:  ${SMOKE_MODEL}
State:  ${CANARY_STATE_FILE} (TEMP percent=$(read_canary_percent))

Operator flow (no built-in wait):
  1) prepare --run
  2) set 10 --run
  3) check --run          # you wait ~30m yourself, re-check as needed
  4) set 50 --run
  5) check --run
  6) finish --run         # forces 100% TEMP, recreates MAIN, folds escrows

Recover stranded temp escrows: recover --run   (add --settle only if you want settle)
EOF
}

main() {
  DEPLOY_DIR="$(abspath "${DEPLOY_DIR}")"
  cd "${DEPLOY_DIR}"
  [[ -f "${UPDATE_SH}" ]] || { echo "update.sh not found next to this script: ${UPDATE_SH}" >&2; exit 2; }

  case "${MODE}" in
    plan)     print_plan ;;
    validate) run_update validate ;;
    prepare)  cmd_prepare ;;
    set)      cmd_set ;;
    check)    cmd_check ;;
    finish)   cmd_finish ;;
    recover)  run_update recover ;;
    *)        echo "unknown mode: ${MODE}" >&2; usage >&2; exit 2 ;;
  esac
}

main
