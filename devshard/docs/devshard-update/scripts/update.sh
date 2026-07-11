#!/usr/bin/env bash
set -Eeuo pipefail
# -E (errtrace): the ERR trap must be inherited by shell functions so a failed
# gate deep in a step still reports where it stopped. Requires bash >= 4.

# Zero-downtime update for a Gonka devshard gateway (single instance, blue/green).
# One self-contained file, driven by a JSON config (models config-driven).
#
# Flow: stand up a temp gateway on its OWN fresh escrows, switch nginx to temp,
# drain main, bump the compose image tag + recreate main, switch back, drain +
# remove temp, then fold the temp escrows into main. Dry-run unless --run.
#
# Sections below: CLI -> config load/validate/resolve -> helpers (progress,
# admin API, docker/nginx) -> steps -> orchestration -> recover -> entrypoint.
#
# Usage:
#   ./update.sh --config update.config.json                     # dry-run plan
#   ./update.sh --config update.config.json run --run --yes     # full flow, unattended
#   ./update.sh --config update.config.json <step> --run        # one step
#   ./update.sh --config update.config.json run --run --from-step drain-main
#   ./update.sh --config update.config.json recover --run [--settle]
#   ./update.sh --config update.config.json plan|validate|list-steps

ORDERED_STEPS=(
  init preflight disable-main-rotation create-temp-gateway create-temp-escrows
  check-temp switch-to-temp check-alias-temp drain-main deactivate-main-source
  bump-main-image update-main create-main-seed-escrows check-main-direct
  switch-to-main check-alias-main drain-temp
  stop-temp import-temp activate-temp restore-main-rotation status
)

# --- CLI ---------------------------------------------------------------------

usage() {
  cat <<'EOF'
Zero-downtime devshard gateway update (v2, single-file, config-driven).

  ./update.sh --config <file> [action] [flags]

Actions:  run | plan | validate | list-steps | recover | <step>
Flags:
  --config <file>     JSON config (default $DEVSHARD_UPDATE_CONFIG or ./update.config.json)
  --run               execute for real (default dry-run; also RUN=1)
  --dry-run           force dry-run even if RUN=1
  --yes, -y           auto-confirm destructive steps (unattended)
  --from-step <name>  with 'run': start at this step, skip earlier ones
  --settle            with 'recover': settle stranded escrows instead of activating
  --deploy-dir <dir>  directory to operate from (default: cwd)
EOF
}

CONFIG_ARG="${DEVSHARD_UPDATE_CONFIG:-./update.config.json}"
ACTION=""; FROM_STEP=""; DEPLOY_DIR="$(pwd)"
DRY_RUN=1; [[ "${RUN:-0}" == "1" ]] && DRY_RUN=0
ASSUME_YES=0; RECOVER_SETTLE=0

while [[ $# -gt 0 ]]; do
  case "$1" in
    --config)      CONFIG_ARG="${2:?--config needs a value}"; shift 2 ;;
    --config=*)    CONFIG_ARG="${1#*=}"; shift ;;
    --from-step)   FROM_STEP="${2:?--from-step needs a value}"; shift 2 ;;
    --from-step=*) FROM_STEP="${1#*=}"; shift ;;
    --deploy-dir)  DEPLOY_DIR="${2:?--deploy-dir needs a value}"; shift 2 ;;
    --deploy-dir=*) DEPLOY_DIR="${1#*=}"; shift ;;
    --settle)      RECOVER_SETTLE=1; shift ;;
    --run)         DRY_RUN=0; shift ;;
    --dry-run)     DRY_RUN=1; shift ;;
    --yes|-y)      ASSUME_YES=1; shift ;;
    --help|-h)     usage; exit 0 ;;
    --*)           echo "unknown flag: $1" >&2; usage >&2; exit 2 ;;
    *)             ACTION="$1"; shift ;;
  esac
done
[[ -z "${ACTION}" ]] && ACTION="plan"

# --- progress (linear; markers are grepped by the sandbox test) --------------

STEP_INDEX=0; STEP_TOTAL=0; CURRENT_STEP=""
step_begin() { CURRENT_STEP="$1"; STEP_INDEX=$(( STEP_INDEX + 1 )); printf '==> [%2d/%2d] %s\n' "${STEP_INDEX}" "${STEP_TOTAL}" "$1"; }
gate_ok()    { printf '    GATE-OK %s\n' "$1"; }
gate_fail()  { printf '    GATE-FAIL %s\n' "$1"; }
note()       { printf '      - %s\n' "$1"; }

# --- config: load, validate (fail fast), resolve (env overrides JSON) --------

cfg()  { jq -r "$1 // \"\"" <<<"${CONFIG_JSON}"; }
cfgn() { jq -r "$1 // empty" <<<"${CONFIG_JSON}"; }

config_validate() {
  local errors
  errors="$(jq -r '
    def req(v; name): if (v == null or v == "") then "missing/empty: \(name)" else empty end;
    def num(v; name): if (v|type) != "number" then "not a number: \(name)" else empty end;
    [ req(.image.repository; "image.repository"),
      req(.image.from_tag; "image.from_tag"),
      req(.image.to_tag; "image.to_tag"),
      (if ((.models // []) | length) < 1 then "models must be a non-empty array of {model, escrow_count, escrow_amount}" else empty end),
      (.models // [] | to_entries[] |
        ( req(.value.model; "models[\(.key)].model"),
          num(.value.escrow_count; "models[\(.key)].escrow_count"),
          num((.value.main_seed_count // 0); "models[\(.key)].main_seed_count"),
          num(.value.escrow_amount; "models[\(.key)].escrow_amount") )),
      req(.escrow.protocol_version; "escrow.protocol_version"),
      req(.escrow.private_key_env; "escrow.private_key_env"),
      (if (((.escrow.source_protocol_version // .escrow.protocol_version) | tostring | sub("^v"; "")) !=
           ((.escrow.protocol_version | tostring | sub("^v"; ""))) and
           any(.models[]; (.main_seed_count // 0) < 1))
       then "protocol change requires models[].main_seed_count >= 1 for every model"
       else empty end),
      req(.main.admin_url; "main.admin_url"),
      req(.main.container; "main.container"),
      req(.main.storage_host_dir; "main.storage_host_dir"),
      num(.temp.admin_port; "temp.admin_port"),
      req(.temp.upstream_alias; "temp.upstream_alias"),
      req(.temp.network; "temp.network"),
      req(.nginx.proxy_container; "nginx.proxy_container"),
      req(.nginx.config_path; "nginx.config_path"),
      req(.nginx.old_upstream; "nginx.old_upstream"),
      req(.nginx.new_upstream; "nginx.new_upstream"),
      num(.nginx.upstream_port; "nginx.upstream_port"),
      req(.compose.file; "compose.file"),
      req(.compose.service; "compose.service"),
      num(.timeouts.ready_timeout_seconds; "timeouts.ready_timeout_seconds"),
      num(.timeouts.ready_poll_seconds; "timeouts.ready_poll_seconds"),
      num(.timeouts.drain_timeout_seconds; "timeouts.drain_timeout_seconds"),
      num(.timeouts.drain_poll_seconds; "timeouts.drain_poll_seconds")
    ] | .[]' <<<"${CONFIG_JSON}")"
  if [[ -n "${errors}" ]]; then
    echo "Config validation failed for ${CONFIG_PATH}:" >&2
    sed 's/^/  - /' <<<"${errors}" >&2
    return 1
  fi
}

resolve_config() {
  IMAGE_REPO="${IMAGE_REPO:-$(cfg .image.repository)}"
  IMAGE_FROM_TAG="${IMAGE_FROM_TAG:-$(cfg .image.from_tag)}"
  IMAGE_TO_TAG="${IMAGE_TO_TAG:-$(cfg .image.to_tag)}"
  IMAGE_FROM_REF="${IMAGE_REPO}:${IMAGE_FROM_TAG}"
  IMAGE_TO_REF="${IMAGE_REPO}:${IMAGE_TO_TAG}"
  IMAGE_SKIP_PULL="${IMAGE_SKIP_PULL:-$(jq -r '.image.skip_pull // false' <<<"${CONFIG_JSON}")}"
  ESCROW_PROTOCOL_VERSION="${ESCROW_PROTOCOL_VERSION:-$(cfg .escrow.protocol_version)}"
  SOURCE_PROTOCOL_VERSION="${SOURCE_PROTOCOL_VERSION:-$(jq -r '.escrow.source_protocol_version // .escrow.protocol_version' <<<"${CONFIG_JSON}")}"
  ESCROW_PRIVATE_KEY_ENV="${ESCROW_PRIVATE_KEY_ENV:-$(cfg .escrow.private_key_env)}"
  MAIN_ADMIN_URL="${MAIN_ADMIN_URL:-$(cfg .main.admin_url)}"
  MAIN_CONTAINER="${MAIN_CONTAINER:-$(cfg .main.container)}"
  MAIN_STORAGE_HOST_DIR="${MAIN_STORAGE_HOST_DIR:-$(cfg .main.storage_host_dir)}"
  MAIN_ENV_FILE="${MAIN_ENV_FILE:-$(cfg .main.env_file)}"
  TEMP_ADMIN_PORT="${TEMP_ADMIN_PORT:-$(cfgn .temp.admin_port)}"
  TEMP_ADMIN_URL="${TEMP_ADMIN_URL:-http://127.0.0.1:${TEMP_ADMIN_PORT}}"
  TEMP_UPSTREAM_ALIAS="${TEMP_UPSTREAM_ALIAS:-$(cfg .temp.upstream_alias)}"
  TEMP_NETWORK="${TEMP_NETWORK:-$(cfg .temp.network)}"
  NGINX_PROXY_CONTAINER="${NGINX_PROXY_CONTAINER:-$(cfg .nginx.proxy_container)}"
  NGINX_CONFIG_PATH="${NGINX_CONFIG_PATH:-$(cfg .nginx.config_path)}"
  NGINX_OLD_UPSTREAM="${NGINX_OLD_UPSTREAM:-$(cfg .nginx.old_upstream)}"
  NGINX_NEW_UPSTREAM="${NGINX_NEW_UPSTREAM:-$(cfg .nginx.new_upstream)}"
  [[ -n "${NGINX_NEW_UPSTREAM}" ]] || NGINX_NEW_UPSTREAM="${TEMP_UPSTREAM_ALIAS}"
  NGINX_UPSTREAM_PORT="${NGINX_UPSTREAM_PORT:-$(cfgn .nginx.upstream_port)}"
  NGINX_PUBLIC_BASE_URL="${NGINX_PUBLIC_BASE_URL:-$(cfg .nginx.public_base_url)}"
  NGINX_PUBLIC_PREFIX="${NGINX_PUBLIC_PREFIX:-$(cfg .nginx.public_prefix)}"
  COMPOSE_FILE="${COMPOSE_FILE:-$(cfg .compose.file)}"
  COMPOSE_SERVICE="${COMPOSE_SERVICE:-$(cfg .compose.service)}"
  READY_TIMEOUT_SECONDS="${READY_TIMEOUT_SECONDS:-$(cfgn .timeouts.ready_timeout_seconds)}"
  READY_POLL_SECONDS="${READY_POLL_SECONDS:-$(cfgn .timeouts.ready_poll_seconds)}"
  DRAIN_TIMEOUT_SECONDS="${DRAIN_TIMEOUT_SECONDS:-$(cfgn .timeouts.drain_timeout_seconds)}"
  DRAIN_POLL_SECONDS="${DRAIN_POLL_SECONDS:-$(cfgn .timeouts.drain_poll_seconds)}"
  SMOKE_MODEL="${SMOKE_MODEL:-$(cfg .smoke_test.model)}"
  [[ -n "${SMOKE_MODEL}" ]] || SMOKE_MODEL="$(jq -r '.models[0].model' <<<"${CONFIG_JSON}")"
  ROTATION_RESTORE="${ROTATION_RESTORE:-$(jq -r '.rotation.restore_after_update // false' <<<"${CONFIG_JSON}")}"
}

load_config() {
  local path="$1"
  [[ -f "${path}" ]] || { echo "config file not found: ${path}" >&2; exit 2; }
  CONFIG_PATH="${path}"
  CONFIG_JSON="$(jq -c . "${path}" 2>&1)" || { echo "config is not valid JSON: ${path}"$'\n'"  ${CONFIG_JSON}" >&2; exit 2; }
  config_validate || exit 2
  resolve_config
}

models_tsv()    { jq -r '.models[] | [.model, (.escrow_count|tostring), (.escrow_amount|tostring), ((.main_seed_count // 0)|tostring)] | @tsv' <<<"${CONFIG_JSON}"; }
covered_models() { jq -r '.models[].model, ((.allow_unavailable_models // [])[])' <<<"${CONFIG_JSON}"; }

# --- helpers: admin API, command runner, gateway ops -------------------------

need_key() { [[ -n "${DEVSHARD_ADMIN_API_KEY:-}" ]] || { echo "DEVSHARD_ADMIN_API_KEY is required" >&2; return 1; }; }

admin_get()  { curl -fsS "$1$2" -H "Authorization: Bearer ${DEVSHARD_ADMIN_API_KEY}"; }
admin_post() { curl -fsS -X POST "$1$2" -H "Authorization: Bearer ${DEVSHARD_ADMIN_API_KEY}" -H 'Content-Type: application/json' -d "$3"; }

# Print a command, run it only in live mode.
run()       { note "exec: $*"; [[ "${DRY_RUN}" == "1" ]] || "$@"; }
run_shell() { note "exec: $(sed -E 's/Bearer [^ ]+/Bearer <redacted>/g' <<<"$1")"; [[ "${DRY_RUN}" == "1" ]] || bash -euo pipefail -c "$1"; }

confirm() {
  [[ "${DRY_RUN}" == "1" ]] && return 0
  [[ "${ASSUME_YES}" == "1" ]] && { note "auto-confirm (--yes): $1"; return 0; }
  [[ -t 0 ]] || { gate_fail "confirmation required but no TTY: $1 (rerun with --yes)"; return 1; }
  local reply; printf '\n?? %s [y/N] ' "$1"; read -r reply
  case "${reply}" in y|Y|yes|YES) return 0 ;; *) gate_fail "declined: $1"; return 1 ;; esac
}

wait_ready() {
  local name="$1" url="$2" deadline=$(( SECONDS + READY_TIMEOUT_SECONDS ))
  while :; do
    admin_get "${url}" "/v1/status" >/dev/null 2>&1 && { gate_ok "${name} ready"; return 0; }
    (( SECONDS >= deadline )) && { gate_fail "${name} not ready within ${READY_TIMEOUT_SECONDS}s"; return 1; }
    sleep "${READY_POLL_SECONDS}"
  done
}

# The drain gate: block until max active_requests == 0 or timeout.
wait_drain() {
  local name="$1" url="$2" deadline=$(( SECONDS + DRAIN_TIMEOUT_SECONDS )) active
  while :; do
    active="$(admin_get "${url}" "/v1/status" | jq '[.devshards[]?.active_requests // 0] | max // 0')"
    note "${name} active_requests=${active}"
    [[ "${active}" == "0" ]] && { gate_ok "${name} drained (active_requests=0)"; return 0; }
    (( SECONDS >= deadline )) && { gate_fail "${name} did not drain within ${DRAIN_TIMEOUT_SECONDS}s (active_requests=${active})"; return 1; }
    sleep "${DRAIN_POLL_SECONDS}"
  done
}

smoke_chat() {
  # Prefer admin key when models are admin-only; fall back to first API key; else unauthenticated.
  local auth_hdr=()
  if [[ -n "${DEVSHARD_ADMIN_API_KEY:-}" ]]; then
    auth_hdr=(-H "Authorization: Bearer ${DEVSHARD_ADMIN_API_KEY}")
  elif [[ -n "${DEVSHARD_API_KEYS:-}" ]]; then
    auth_hdr=(-H "Authorization: Bearer ${DEVSHARD_API_KEYS%%,*}")
  fi
  curl -fsS -X POST "$1/v1/chat/completions" "${auth_hdr[@]}" -H 'Content-Type: application/json' \
    -d "$(jq -nc --arg m "$2" '{model:$m, stream:false, max_tokens:1, messages:[{role:"user", content:"Reply with ok"}]}')" >/dev/null
}

settings_sync_to_temp() {
  local settings
  settings="$(admin_get "${MAIN_ADMIN_URL}" "/v1/admin/settings" | jq '.escrow_rotation.enabled=false | .escrow_rotation.models=[]')"
  admin_post "${TEMP_ADMIN_URL}" "/v1/admin/settings" "${settings}" >/dev/null
}

assert_settings_aligned() {
  local main_settings temp_settings
  main_settings="$(admin_get "${MAIN_ADMIN_URL}" "/v1/admin/settings" | jq -S '.escrow_rotation.enabled=false | .escrow_rotation.models=[]')"
  temp_settings="$(admin_get "${TEMP_ADMIN_URL}" "/v1/admin/settings" | jq -S '.escrow_rotation.enabled=false | .escrow_rotation.models=[]')"
  [[ "${main_settings}" == "${temp_settings}" ]] || { gate_fail "temp settings do not match main"; return 1; }
  gate_ok "temp settings match main (rotation disabled on temp)"
}

# Start the temp gateway on its OWN empty escrow set. Reuses MAIN's --env-file
# so no secret material is written to a new path. --restart unless-stopped, so
# it MUST later be stopped AND removed to avoid a two-writer resurrection.
temp_start() {
  run_shell "docker rm -f '${TEMP_CONTAINER}' >/dev/null 2>&1 || true"
  local -a args=(docker run -d --name "${TEMP_CONTAINER}" --restart unless-stopped
                 --network "${TEMP_NETWORK}" --network-alias "${TEMP_UPSTREAM_ALIAS}")
  [[ -n "${MAIN_ENV_FILE_ABS}" && -f "${MAIN_ENV_FILE_ABS}" ]] && args+=(--env-file "${MAIN_ENV_FILE_ABS}")
  args+=(-e DEVSHARDS_JSON='[]' -e DEVSHARD_STORAGE_DIR="${TEMP_STORAGE_CONTAINER_DIR}" -e DEVSHARD_PORT=8080
         -p "127.0.0.1:${TEMP_ADMIN_PORT}:8080" -v "${STORAGE_HOST_DIR_ABS}:/root/.devshardctl" "${IMAGE_TO_REF}")
  run "${args[@]}"
}

# Rewrite the compose image tag from_ref -> to_ref. Portable (sed to a temp file
# then mv; no GNU-only -i). Idempotent; backs up first; fails loudly if absent.
compose_bump() {
  local file="${COMPOSE_FILE}" from="${IMAGE_FROM_REF}" to="${IMAGE_TO_REF}"
  [[ -f "${file}" ]] || { gate_fail "compose file not found: ${file}"; return 1; }
  if grep -q "${to}" "${file}" && ! grep -q "${from}" "${file}"; then gate_ok "compose already pins ${to}"; return 0; fi
  grep -q "${from}" "${file}" || { gate_fail "neither '${from}' nor '${to}' found in ${file}"; return 1; }
  [[ "${DRY_RUN}" == "1" ]] && { note "would bump ${file}: ${from} -> ${to}"; return 0; }
  cp "${file}" "${file}.blue-green-backup"
  sed "s#${from}#${to}#g" "${file}" > "${file}.blue-green-tmp"
  mv "${file}.blue-green-tmp" "${file}"
  grep -q "${to}" "${file}" || { gate_fail "bump did not apply; restore ${file}.blue-green-backup"; return 1; }
  gate_ok "compose image bumped ${from} -> ${to} (backup: ${file}.blue-green-backup)"
}

# Switch the nginx upstream host inside the proxy container, then validate and
# gracefully reload (never restart). Handles both a direct proxy_pass
# http://host:PORT and a named-upstream `server host:PORT;`. Portable sed;
# backs up first; idempotent if already switched.
nginx_switch() {
  local from="$1" to="$2" port="${NGINX_UPSTREAM_PORT}"
  local cfg_path="${NGINX_CONFIG_PATH}" backup="${NGINX_CONFIG_PATH}.blue-green-backup" tmp="${NGINX_CONFIG_PATH}.blue-green-tmp"
  local old_pat="(http://${from}:${port}|server[[:space:]]+${from}:${port})"
  local new_pat="(http://${to}:${port}|server[[:space:]]+${to}:${port})"
  note "nginx upstream switch ${from} -> ${to} in ${cfg_path}"
  if [[ "${DRY_RUN}" == "1" ]]; then note "would docker exec ${NGINX_PROXY_CONTAINER} sh -lc '<switch ${from}->${to} + nginx -t + reload>'"; return 0; fi
  local inner
  inner="$(cat <<INNER
set -eu
test -f '${cfg_path}'
if ! grep -Eq '${old_pat}' '${cfg_path}'; then
  if grep -Eq '${new_pat}' '${cfg_path}'; then echo 'nginx already on ${to}:${port}'; exit 0; fi
  echo 'ERROR: upstream ${from}:${port} not found in ${cfg_path} (check nginx -T)' >&2; exit 3
fi
cp '${cfg_path}' '${backup}'
sed -E 's#http://${from}:${port}#http://${to}:${port}#g; s#(server[[:space:]]+)${from}:${port}#\1${to}:${port}#g' '${cfg_path}' > '${tmp}'
# Prefer mv (atomic replace). Bind-mounted nginx.conf rejects inode replacement
# ("Resource busy" / "File exists") — fall back to in-place content overwrite.
if mv '${tmp}' '${cfg_path}' 2>/dev/null; then
  :
else
  cat '${tmp}' > '${cfg_path}'
  rm -f '${tmp}'
fi
grep -Eq '${new_pat}' '${cfg_path}' || { echo 'ERROR: switch did not apply; restore ${backup}' >&2; exit 4; }
nginx -t
nginx -s reload
echo 'nginx upstream switched ${from} -> ${to} and reloaded'
INNER
)"
  if docker exec "${NGINX_PROXY_CONTAINER}" sh -lc "${inner}"; then gate_ok "nginx switched ${from} -> ${to} (graceful reload)"; return 0; fi
  gate_fail "nginx switch ${from} -> ${to} failed"; return 1
}

# Fold one temp escrow into main: import inactive (carries its state.db), then
# either activate (route it) or settle (drain-aware on-chain). Shared by the
# activate-temp step and the recover command.
import_escrow_into_main() {
  local id="$1" model="$2" proto="$3"
  local storage="${TEMP_STORAGE_CONTAINER_DIR}/escrow-${id}/state.db" perf="${TEMP_STORAGE_CONTAINER_DIR}/perf.db"
  admin_post "${MAIN_ADMIN_URL}" "/v1/admin/devshards/import" \
    "$(jq -nc --arg id "${id}" --arg m "${model}" --arg s "${storage}" --arg p "${proto}" \
       --arg pk "${ESCROW_PRIVATE_KEY_ENV}" --arg perf "${perf}" \
       '{id:$id, model:$m, storage_path:$s, protocol_version:$p, private_key_env:$pk, perf_path:$perf, active:false}')" >/dev/null
}
activate_escrow_on_main() {
  local id="$1" model="$2" proto="$3"
  local storage="${TEMP_STORAGE_CONTAINER_DIR}/escrow-${id}/state.db"
  admin_post "${MAIN_ADMIN_URL}" "/v1/admin/devshards" \
    "$(jq -nc --arg id "${id}" --arg m "${model}" --arg s "${storage}" --arg p "${proto}" \
       --arg pk "${ESCROW_PRIVATE_KEY_ENV}" \
       '{id:$id, model:$m, storage_path:$s, protocol_version:$p, private_key_env:$pk}')" >/dev/null
}

# --- run state (survives single-step / resume / recover invocations) ---------

runstate_init() {
  RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)"
  TEMP_CONTAINER="${TEMP_UPSTREAM_ALIAS}-${RUN_ID}"
  TEMP_STORAGE_HOST_DIR="${STORAGE_HOST_DIR_ABS}/temp-${RUN_ID}"
  TEMP_STORAGE_CONTAINER_DIR="/root/.devshardctl/temp-${RUN_ID}"
  TEMP_DEVSHARDS_FILE="${TEMP_STORAGE_HOST_DIR}/temp-devshards.json"
}
runstate_write() {
  cat > "${RUN_STATE_FILE}" <<EOF
RUN_ID='${RUN_ID}'
TEMP_CONTAINER='${TEMP_CONTAINER}'
TEMP_STORAGE_HOST_DIR='${TEMP_STORAGE_HOST_DIR}'
TEMP_STORAGE_CONTAINER_DIR='${TEMP_STORAGE_CONTAINER_DIR}'
TEMP_DEVSHARDS_FILE='${TEMP_DEVSHARDS_FILE}'
EOF
}

# --- steps -------------------------------------------------------------------

step_init() {
  run mkdir -p "${STORAGE_HOST_DIR_ABS}"
  runstate_init
  if [[ "${DRY_RUN}" == "1" ]]; then note "would write run state (run id ${RUN_ID}) to ${RUN_STATE_FILE}"; return 0; fi
  mkdir -p "${STORAGE_HOST_DIR_ABS}"; runstate_write
  note "run id ${RUN_ID}; state ${RUN_STATE_FILE}"
}

step_preflight() {
  need_key
  if [[ "${DRY_RUN}" == "1" ]]; then
    admin_get "${MAIN_ADMIN_URL}" "/v1/status" >/dev/null 2>&1 || { gate_ok "dry-run: main not reachable now; coverage check runs live"; return 0; }
  else
    wait_ready "main gateway" "${MAIN_ADMIN_URL}"
  fi
  local uncovered="" served
  while IFS= read -r served; do
    [[ -z "${served}" ]] && continue
    covered_models | grep -Fxq "${served}" || uncovered="${uncovered} ${served}"
  done < <(admin_get "${MAIN_ADMIN_URL}" "/v1/admin/devshards" | jq -r '[.devshards[]? | select(.active==true) | .model] | unique | .[]')
  [[ -n "${uncovered}" ]] && { gate_fail "main serves uncovered models:${uncovered} (add to models[] or allow_unavailable_models)"; return 1; }
  if [[ "${SOURCE_PROTOCOL_VERSION#v}" != "${ESCROW_PROTOCOL_VERSION#v}" ]]; then
    local missing_seeds
    missing_seeds="$(jq -r '[.models[] | select((.main_seed_count // 0) < 1) | .model] | join(" ")' <<<"${CONFIG_JSON}")"
    [[ -z "${missing_seeds}" ]] ||
      { gate_fail "protocol change requires models[].main_seed_count >= 1 for:${missing_seeds}"; return 1; }
  fi
  gate_ok "every model main serves is covered by temp escrows or allow-listed"
}

step_disable_main_rotation() {
  need_key
  [[ "${DRY_RUN}" == "1" ]] && { note "would disable escrow_rotation on MAIN so nothing settles on-chain mid-update"; return 0; }
  local settings; settings="$(admin_get "${MAIN_ADMIN_URL}" "/v1/admin/settings" | jq '.escrow_rotation.enabled=false')"
  admin_post "${MAIN_ADMIN_URL}" "/v1/admin/settings" "${settings}" >/dev/null
  gate_ok "MAIN escrow_rotation disabled"
}

step_create_temp_gateway() {
  need_key
  run mkdir -p "${TEMP_STORAGE_HOST_DIR}"
  if [[ "${IMAGE_SKIP_PULL}" == "true" ]]; then
    run docker image inspect "${IMAGE_TO_REF}"
  else
    run docker pull "${IMAGE_TO_REF}"
  fi
  temp_start
  [[ "${DRY_RUN}" == "1" ]] && { note "would wait for temp readiness and sync main settings (rotation off) to temp"; return 0; }
  wait_ready "temp gateway" "${TEMP_ADMIN_URL}"
  settings_sync_to_temp
  gate_ok "temp gateway up on ${TEMP_ADMIN_URL}; settings synced (rotation off)"
}

step_create_temp_escrows() {
  need_key
  local model count amount i body
  if [[ "${DRY_RUN}" == "1" ]]; then
    while IFS=$'\t' read -r model count amount seed_count; do note "would mint ${count} x ${model} @ ${amount}"; done < <(models_tsv)
    return 0
  fi
  confirm "mint fresh temp escrows on-chain (spends real funds; irreversible)"
  mkdir -p "${TEMP_STORAGE_HOST_DIR}"
  ESCROWS_MINTED=1
  while IFS=$'\t' read -r model count amount seed_count; do
    for (( i=1; i<=count; i++ )); do
      note "mint ${i}/${count} ${model} @ ${amount}"
      body="$(jq -nc --argjson amount "${amount}" --arg model "${model}" --arg pk "${ESCROW_PRIVATE_KEY_ENV}" --arg pv "${ESCROW_PROTOCOL_VERSION}" \
        '{amount:$amount, model_id:$model, private_key_env:$pk, protocol_version:$pv}')"
      admin_post "${TEMP_ADMIN_URL}" "/v1/admin/escrows" "${body}" >/dev/null
    done
  done < <(models_tsv)
  admin_get "${TEMP_ADMIN_URL}" "/v1/admin/devshards" > "${TEMP_DEVSHARDS_FILE}"
  gate_ok "temp escrows minted; recorded to ${TEMP_DEVSHARDS_FILE}"
}

step_check_temp() {
  need_key
  [[ "${DRY_RUN}" == "1" ]] && { note "would verify temp readiness, settings alignment, per-model routable escrows, and a chat smoke test"; return 0; }
  wait_ready "temp gateway" "${TEMP_ADMIN_URL}"
  assert_settings_aligned
  local model count amount active
  while IFS=$'\t' read -r model count amount seed_count; do
    active="$(admin_get "${TEMP_ADMIN_URL}" "/v1/admin/devshards" | jq --arg m "${model}" '[.devshards[]? | select(.model==$m and .active==true)] | length')"
    (( active < count )) && { gate_fail "temp has ${active} active escrows for ${model}, need ${count} (model would be unavailable during update)"; return 1; }
    gate_ok "temp: ${active}/${count} active escrows for ${model}"
  done < <(models_tsv)
  smoke_chat "${TEMP_ADMIN_URL}" "${SMOKE_MODEL}"
  gate_ok "temp chat smoke test passed (${SMOKE_MODEL})"
  admin_get "${TEMP_ADMIN_URL}" "/v1/admin/devshards" > "${TEMP_DEVSHARDS_FILE}"
}

step_switch_to_temp() { confirm "switch nginx upstream to TEMP (${NGINX_NEW_UPSTREAM})"; nginx_switch "${NGINX_OLD_UPSTREAM}" "${NGINX_NEW_UPSTREAM}"; }
step_check_alias_temp() { check_alias temp; }

step_drain_main() {
  need_key
  [[ "${DRY_RUN}" == "1" ]] && { note "would wait for main active_requests to reach 0 (the drain gate)"; return 0; }
  wait_drain "main gateway" "${MAIN_ADMIN_URL}"
}

step_deactivate_main_source() {
  need_key
  if [[ "${SOURCE_PROTOCOL_VERSION#v}" == "${ESCROW_PROTOCOL_VERSION#v}" ]]; then
    note "source and target protocol are the same; leaving main escrows active"
    return 0
  fi
  [[ "${DRY_RUN}" == "1" ]] && {
    note "would deactivate active protocol-${SOURCE_PROTOCOL_VERSION#v} escrows on drained MAIN (no settlement)"
    return 0
  }
  confirm "deactivate source-protocol escrows on drained MAIN (local only; no settlement)"
  local id count=0 source="${SOURCE_PROTOCOL_VERSION#v}"
  while IFS= read -r id; do
    [[ -n "${id}" ]] || continue
    note "deactivate source escrow ${id}"
    admin_post "${MAIN_ADMIN_URL}" "/v1/admin/devshards/${id}/deactivate" '{}' >/dev/null
    count=$(( count + 1 ))
  done < <(admin_get "${MAIN_ADMIN_URL}" "/v1/admin/devshards" |
    jq -r --arg p "${source}" '.devshards[]? | select(.active==true and (.protocol_version|tostring)==$p) | .id')
  local remaining
  remaining="$(admin_get "${MAIN_ADMIN_URL}" "/v1/admin/devshards" |
    jq --arg p "${source}" '[.devshards[]? | select(.active==true and (.protocol_version|tostring)==$p)] | length')"
  (( remaining == 0 )) ||
    { gate_fail "${remaining} source-protocol escrow(s) remain active on main"; return 1; }
  gate_ok "deactivated ${count} source-protocol escrow(s) on main without settlement"
}

step_bump_main_image() { compose_bump; }

step_update_main() {
  confirm "recreate MAIN from ${COMPOSE_FILE} on the bumped image"
  note "MAIN image comes from ${COMPOSE_FILE} (service ${COMPOSE_SERVICE}), not an env ref"
  local src="" pull=""
  [[ -n "${MAIN_ENV_FILE_ABS}" && -f "${MAIN_ENV_FILE_ABS}" ]] && src="source '${MAIN_ENV_FILE_ABS}' && "
  [[ "${IMAGE_SKIP_PULL}" != "true" ]] && pull="docker compose -f '${COMPOSE_FILE}' pull ${COMPOSE_SERVICE} && "
  run_shell "cd '${DEPLOY_DIR}' && ${src}${pull}docker compose -f '${COMPOSE_FILE}' up -d --no-deps --force-recreate ${COMPOSE_SERVICE} && docker compose -f '${COMPOSE_FILE}' ps ${COMPOSE_SERVICE}"
  gate_ok "MAIN recreated on new image"
}

step_create_main_seed_escrows() {
  need_key
  local model temp_count amount seed_count i body total=0
  while IFS=$'\t' read -r model temp_count amount seed_count; do
    total=$(( total + seed_count ))
  done < <(models_tsv)
  (( total > 0 )) || { note "no main seed escrows configured"; return 0; }
  if [[ "${DRY_RUN}" == "1" ]]; then
    while IFS=$'\t' read -r model temp_count amount seed_count; do
      note "would mint ${seed_count} main seed x ${model} @ ${amount}"
    done < <(models_tsv)
    return 0
  fi
  confirm "mint target-protocol seed escrows on updated MAIN (spends real funds; irreversible)"
  while IFS=$'\t' read -r model temp_count amount seed_count; do
    for (( i=1; i<=seed_count; i++ )); do
      note "mint main seed ${i}/${seed_count} ${model} @ ${amount}"
      body="$(jq -nc --argjson amount "${amount}" --arg model "${model}" --arg pk "${ESCROW_PRIVATE_KEY_ENV}" --arg pv "${ESCROW_PROTOCOL_VERSION}" \
        '{amount:$amount, model_id:$model, private_key_env:$pk, protocol_version:$pv}')"
      admin_post "${MAIN_ADMIN_URL}" "/v1/admin/escrows" "${body}" >/dev/null
    done
  done < <(models_tsv)
  gate_ok "main seed escrows minted"
}

step_check_main_direct() {
  need_key
  [[ "${DRY_RUN}" == "1" ]] && { note "would verify main readiness, target-protocol seed coverage, and a direct chat smoke before switching back"; return 0; }
  wait_ready "main gateway" "${MAIN_ADMIN_URL}"
  local model temp_count amount seed_count active
  while IFS=$'\t' read -r model temp_count amount seed_count; do
    (( seed_count > 0 )) || continue
    active="$(admin_get "${MAIN_ADMIN_URL}" "/v1/admin/devshards" |
      jq --arg m "${model}" --arg p "${ESCROW_PROTOCOL_VERSION#v}" \
        '[.devshards[]? | select(.active==true and .model==$m and (.protocol_version|tostring)==$p)] | length')"
    (( active >= seed_count )) ||
      { gate_fail "main has ${active}/${seed_count} active target-protocol seed escrows for ${model}"; return 1; }
    gate_ok "main: ${active}/${seed_count} target-protocol seed escrows active for ${model}"
  done < <(models_tsv)
  smoke_chat "${MAIN_ADMIN_URL}" "${SMOKE_MODEL}"
  gate_ok "main direct chat smoke test passed (${SMOKE_MODEL})"
}

step_switch_to_main() { confirm "switch nginx upstream back to MAIN (${NGINX_OLD_UPSTREAM})"; nginx_switch "${NGINX_NEW_UPSTREAM}" "${NGINX_OLD_UPSTREAM}"; }
step_check_alias_main() { check_alias main; }

step_drain_temp() {
  need_key
  [[ "${DRY_RUN}" == "1" ]] && { note "would wait for temp active_requests to reach 0 before removing it"; return 0; }
  wait_drain "temp gateway" "${TEMP_ADMIN_URL}"
}

step_stop_temp() {
  confirm "stop AND remove the temp container ${TEMP_CONTAINER}"
  run docker stop "${TEMP_CONTAINER}"
  run docker rm "${TEMP_CONTAINER}"
  gate_ok "temp container stopped and removed"
}

step_import_temp() {
  need_key
  [[ "${DRY_RUN}" == "1" ]] && { note "would import temp escrows from ${TEMP_DEVSHARDS_FILE} into main (inactive)"; return 0; }
  confirm "import temp escrows into MAIN (inactive)"
  local row id model proto
  while IFS= read -r row; do
    id="$(jq -r '.id' <<<"${row}")"; model="$(jq -r '.model // ""' <<<"${row}")"; proto="$(jq -r '.protocol_version // ""' <<<"${row}")"
    note "import ${id} (${model}) inactive"; import_escrow_into_main "${id}" "${model}" "${proto}"
  done < <(jq -c '.devshards[]' "${TEMP_DEVSHARDS_FILE}")
  gate_ok "temp escrows imported into main (inactive)"
}

step_activate_temp() {
  need_key
  [[ "${DRY_RUN}" == "1" ]] && { note "would activate imported temp escrows on main"; return 0; }
  confirm "activate imported temp escrows on MAIN"
  local row id model proto
  while IFS= read -r row; do
    id="$(jq -r '.id' <<<"${row}")"; model="$(jq -r '.model // ""' <<<"${row}")"; proto="$(jq -r '.protocol_version // ""' <<<"${row}")"
    note "activate ${id} (${model})"; activate_escrow_on_main "${id}" "${model}" "${proto}"
  done < <(jq -c '.devshards[]' "${TEMP_DEVSHARDS_FILE}")
  ESCROWS_MINTED=0
  gate_ok "temp escrows activated on main"
}

step_restore_main_rotation() {
  need_key
  [[ "${ROTATION_RESTORE}" != "true" ]] && { note "leaving MAIN escrow_rotation disabled (set rotation.restore_after_update=true to re-enable)"; return 0; }
  [[ "${DRY_RUN}" == "1" ]] && { note "would re-enable escrow_rotation on MAIN"; return 0; }
  local settings; settings="$(admin_get "${MAIN_ADMIN_URL}" "/v1/admin/settings" | jq '.escrow_rotation.enabled=true')"
  admin_post "${MAIN_ADMIN_URL}" "/v1/admin/settings" "${settings}" >/dev/null
  gate_ok "MAIN escrow_rotation re-enabled"
}

step_status() {
  need_key
  [[ "${DRY_RUN}" == "1" ]] && { note "would show main/temp status"; return 0; }
  note "main status: $(admin_get "${MAIN_ADMIN_URL}" "/v1/status" | jq -c '{active:([.devshards[]?|select(.active==true)]|length), max_active_requests:([.devshards[]?.active_requests//0]|max//0)}')"
  note "temp status: $(admin_get "${TEMP_ADMIN_URL}" "/v1/status" 2>/dev/null | jq -c '{active:([.devshards[]?|select(.active==true)]|length)}' 2>/dev/null || echo gone)"
}

# check-alias-temp / check-alias-main: verify the public route (skipped if unset).
check_alias() {
  local side="$1"; need_key
  [[ -z "${NGINX_PUBLIC_BASE_URL}" ]] && { note "no nginx.public_base_url set; skipping public ${side} verification"; return 0; }
  [[ "${DRY_RUN}" == "1" ]] && { note "would verify public ${side} route via ${NGINX_PUBLIC_BASE_URL}"; return 0; }
  case "${NGINX_PUBLIC_BASE_URL}" in *127.0.0.1*|*localhost*) note "WARNING: public_base_url is loopback; can pass while the real route is broken" ;; esac
  curl -fsS "${NGINX_PUBLIC_BASE_URL}${NGINX_PUBLIC_PREFIX}/v1/status" -H "Authorization: Bearer ${DEVSHARD_ADMIN_API_KEY}" >/dev/null
  smoke_chat "${NGINX_PUBLIC_BASE_URL}${NGINX_PUBLIC_PREFIX}" "${SMOKE_MODEL}"
  gate_ok "public ${side} route verified"
}

# --- recover: fold stranded temp escrows into main, or settle them -----------

cmd_recover() {
  need_key
  [[ -f "${TEMP_DEVSHARDS_FILE:-}" ]] || { echo "recover: no temp-escrows state at ${TEMP_DEVSHARDS_FILE:-<none>} (run init/create-temp-escrows first, or set --deploy-dir)" >&2; exit 1; }
  local mode; mode="$([[ "${RECOVER_SETTLE}" == "1" ]] && echo settle || echo activate)"
  echo "==> recover from ${TEMP_DEVSHARDS_FILE} (mode: import+${mode}; dry-run=${DRY_RUN})"
  [[ "${DRY_RUN}" == "1" ]] || confirm "recover temp escrows into MAIN (import+${mode})"
  local row id model proto count=0
  while IFS= read -r row; do
    id="$(jq -r '.id' <<<"${row}")"; model="$(jq -r '.model // ""' <<<"${row}")"; proto="$(jq -r '.protocol_version // ""' <<<"${row}")"
    count=$(( count + 1 ))
    if [[ "${DRY_RUN}" == "1" ]]; then note "would import+${mode} ${id} (${model})"; continue; fi
    import_escrow_into_main "${id}" "${model}" "${proto}"
    if [[ "${RECOVER_SETTLE}" == "1" ]]; then
      note "settle ${id} (${model})"; admin_post "${MAIN_ADMIN_URL}" "/v1/admin/devshards/${id}/settle" '{}' >/dev/null
    else
      note "activate ${id} (${model})"; activate_escrow_on_main "${id}" "${model}" "${proto}"
    fi
  done < <(jq -c '.devshards[]' "${TEMP_DEVSHARDS_FILE}")
  ESCROWS_MINTED=0
  gate_ok "recovered ${count} temp escrow(s) into main (import+${mode})"
}

# --- orchestration + error handling ------------------------------------------

run_flow() {
  local -a steps=("$@"); STEP_TOTAL="${#steps[@]}"; STEP_INDEX=0
  local step
  for step in "${steps[@]}"; do
    step_begin "${step}"
    "step_${step//-/_}"
  done
}

print_plan() {
  cat <<EOF
Devshard gateway update (v2, single-file)
Config: ${CONFIG_PATH}
Mode:   $([[ "${DRY_RUN}" == "1" ]] && echo "dry-run (use --run to execute)" || echo LIVE)
Image:  ${IMAGE_FROM_REF} -> ${IMAGE_TO_REF}
Pull:   $([[ "${IMAGE_SKIP_PULL}" == "true" ]] && echo "skip (require exact local target image)" || echo "registry")
Protocol: ${SOURCE_PROTOCOL_VERSION} -> ${ESCROW_PROTOCOL_VERSION}
Main:   ${MAIN_CONTAINER} @ ${MAIN_ADMIN_URL}
Temp:   ${TEMP_UPSTREAM_ALIAS} @ ${TEMP_ADMIN_URL} (network ${TEMP_NETWORK})
Nginx:  ${NGINX_PROXY_CONTAINER}:${NGINX_CONFIG_PATH}  ${NGINX_OLD_UPSTREAM} <-> ${NGINX_NEW_UPSTREAM}:${NGINX_UPSTREAM_PORT}
Public: ${NGINX_PUBLIC_BASE_URL:-<unset; public checks skipped>}
Smoke:  ${SMOKE_MODEL}
Models (fresh temp escrows minted per model):
$(models_tsv | while IFS=$'\t' read -r m c a s; do printf '  %s: temp=%s, main-seed=%s, amount=%s each\n' "${m}" "${c}" "${s}" "${a}"; done)
Steps:
$(printf '  %s\n' "${ORDERED_STEPS[@]}")
EOF
}

ESCROWS_MINTED=0; ON_ERR=0
on_error() {
  local code=$?
  [[ "${ON_ERR}" == "1" ]] && return; ON_ERR=1
  {
    echo ""
    echo "FAILED at step: ${CURRENT_STEP:-setup} (exit ${code})"
    echo "Inspect: ${0##*/} --config '${CONFIG_ARG}' status --run ; restore nginx backup (${NGINX_CONFIG_PATH:-<config>}.blue-green-backup) to revert routing."
    if [[ "${ESCROWS_MINTED}" == "1" ]]; then
      echo ""
      echo "WARNING: temp escrows were minted on-chain but not yet folded into main."
      echo "  Recorded at: ${TEMP_DEVSHARDS_FILE:-<run state>}"
      echo "  Recover: ${0##*/} --config '${CONFIG_ARG}' --deploy-dir '${DEPLOY_DIR}' recover --run   (add --settle to settle instead of activate)"
    fi
  } >&2
}
trap on_error ERR

# --- entrypoint --------------------------------------------------------------

abspath() { case "$1" in /*) printf '%s\n' "$1" ;; *) printf '%s/%s\n' "$(pwd)" "$1" ;; esac; }

main() {
  DEPLOY_DIR="$(abspath "${DEPLOY_DIR}")"
  local config_abs; config_abs="$(abspath "${CONFIG_ARG}")"
  cd "${DEPLOY_DIR}"
  load_config "${config_abs}"

  STORAGE_HOST_DIR_ABS="$(abspath "${MAIN_STORAGE_HOST_DIR}")"
  MAIN_ENV_FILE_ABS=""; [[ -n "${MAIN_ENV_FILE}" ]] && MAIN_ENV_FILE_ABS="$(abspath "${MAIN_ENV_FILE}")"
  RUN_STATE_FILE="${STORAGE_HOST_DIR_ABS}/blue-green-run-state.env"
  # shellcheck disable=SC1090
  [[ -f "${RUN_STATE_FILE}" ]] && source "${RUN_STATE_FILE}"

  case "${ACTION}" in
    validate)   echo "config OK: ${CONFIG_PATH}" ;;
    list-steps) printf '%s\n' "${ORDERED_STEPS[@]}" ;;
    plan)       print_plan ;;
    recover)    cmd_recover ;;
    run)
      local -a steps=("${ORDERED_STEPS[@]}")
      if [[ -n "${FROM_STEP}" ]]; then
        steps=(); local seen=0 s
        for s in "${ORDERED_STEPS[@]}"; do [[ "${s}" == "${FROM_STEP}" ]] && seen=1; [[ "${seen}" == "1" ]] && steps+=("${s}"); done
        [[ "${#steps[@]}" -gt 0 ]] || { echo "unknown --from-step: ${FROM_STEP}" >&2; exit 2; }
      fi
      run_flow "${steps[@]}"
      echo "update complete: ${IMAGE_FROM_TAG} -> ${IMAGE_TO_TAG}"
      ;;
    *)
      local ok=0 s
      for s in "${ORDERED_STEPS[@]}"; do [[ "${s}" == "${ACTION}" ]] && ok=1; done
      (( ok )) || { echo "unknown action/step: ${ACTION}" >&2; usage >&2; exit 2; }
      run_flow "${ACTION}"
      ;;
  esac
}

main
