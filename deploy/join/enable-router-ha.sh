#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
config_env=${GONKA_CONFIG_ENV:-$script_dir/config.env}
docker_bin=${DOCKER_BIN:-docker}
fleet_bin=${VERSIOND_ROUTER_FLEET_BIN:-$script_dir/versiond-router-fleet.sh}
versiond_mode=auto
edge_mode=auto
rollback_pending=false
rollback_image=

fail() {
    echo "enable-router-ha: $*" >&2
    exit 1
}

warn() {
    echo "enable-router-ha: warning: $*" >&2
}

usage() {
    cat >&2 <<'EOF'
Usage: enable-router-ha.sh [OPTIONS]

Performs the one-time cutover from the public nginx + singleton-router layout
to the public HAProxy, private nginx policy workers, and versiond-router fleet.

Options:
  --versiond-mode auto|single|ha
  --edge-mode     auto|single|multi
EOF
}

while (($# > 0)); do
    case $1 in
        --versiond-mode)
            (($# >= 2)) || fail "--versiond-mode requires a value"
            versiond_mode=$2
            shift 2
            ;;
        --edge-mode)
            (($# >= 2)) || fail "--edge-mode requires a value"
            edge_mode=$2
            shift 2
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *) fail "unknown argument: $1" ;;
    esac
done

case $versiond_mode in auto | single | ha) ;; *) fail "invalid versiond mode" ;; esac
case $edge_mode in auto | single | multi) ;; *) fail "invalid edge-api mode" ;; esac
[[ -f $config_env ]] || fail "configuration file not found: $config_env"
set -a
# shellcheck disable=SC1090
source "$config_env"
set +a

command -v "$docker_bin" >/dev/null 2>&1 || fail "$docker_bin is required"
command -v flock >/dev/null 2>&1 || fail "flock is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

container_exists() {
    "$docker_bin" inspect "$1" >/dev/null 2>&1
}

if [[ $versiond_mode == auto ]]; then
    if container_exists devshard-postgres || container_exists versiond2 || \
        container_exists versiond-router; then
        versiond_mode=ha
    else
        versiond_mode=single
    fi
fi
if [[ $edge_mode == auto ]]; then
    if container_exists edge-api2 || container_exists edge-api3 || \
        container_exists edge-api-router; then
        edge_mode=multi
    else
        edge_mode=single
    fi
fi

compose=(
    "$docker_bin" compose
    --project-directory "$script_dir"
    -f "$script_dir/docker-compose.yml"
)
if [[ $versiond_mode == ha ]]; then
    compose+=(-f "$script_dir/docker-compose.versiond.yml")
fi
if [[ $edge_mode == multi ]]; then
    compose+=(-f "$script_dir/docker-compose.edge-api-multi.yml")
fi

ensure_compose_network() {
    local key=$1 name=$2 project=$3 ownership
    if "$docker_bin" network inspect "$name" >/dev/null 2>&1; then
        ownership=$("$docker_bin" network inspect --format \
            '{{index .Labels "com.docker.compose.network"}}|{{index .Labels "com.docker.compose.project"}}' \
            "$name")
        [[ $ownership == "$key|$project" ]] || fail \
            "network $name exists with ownership '$ownership', expected '$key|$project'"
        return 0
    fi
    "$docker_bin" network create \
        --label "com.docker.compose.network=$key" \
        --label "com.docker.compose.project=$project" \
        "$name" >/dev/null
}

pull_policy=${ROUTER_HA_PULL_POLICY:-always}
case $pull_policy in always | missing | never) ;; *) fail "ROUTER_HA_PULL_POLICY must be always, missing, or never" ;; esac
cutover_timeout=${ROUTER_HA_CUTOVER_TIMEOUT_SECONDS:-60}
case $cutover_timeout in '' | *[!0-9]* | 0) fail "ROUTER_HA_CUTOVER_TIMEOUT_SECONDS must be positive" ;; esac
lock_file=${ROUTER_HA_CUTOVER_LOCK:-${XDG_RUNTIME_DIR:-/tmp}/gonka-router-ha-cutover.lock}

proxy_component() {
    "$docker_bin" inspect --format '{{index .Config.Labels "ai.gonka.component"}}' proxy 2>/dev/null || true
}

wait_component() {
    local component=$1
    "$docker_bin" exec proxy /bin/busybox wget -q -T 3 -O /dev/null \
        "http://127.0.0.1:8404/readyz?component=$component"
}

remove_migration_routers() {
    [[ $versiond_mode != ha ]] || \
        "$docker_bin" rm -f versiond-router >/dev/null 2>&1 || true
    [[ $edge_mode != multi ]] || \
        "$docker_bin" rm -f edge-api-router >/dev/null 2>&1 || true
}

restore_v4_proxy() {
    local status=$?
    trap - EXIT ERR INT TERM HUP
    if [[ $rollback_pending == true && -n $rollback_image ]]; then
        warn "public ingress cutover failed; restoring the captured nginx image"
        "$docker_bin" rm -f proxy >/dev/null 2>&1 || true
        local versiond_service=versiond edge_service=
        [[ $versiond_mode == ha ]] && versiond_service=versiond-router
        [[ $edge_mode == multi ]] && edge_service=edge-api-router
        if PROXY_V4_IMAGE=$rollback_image \
            PROXY_V4_VERSIOND_SERVICE_NAME=$versiond_service \
            PROXY_V4_EDGE_API_SERVICE_NAME=$edge_service \
            "${compose[@]}" -f "$script_dir/docker-compose.proxy-v4-compat.yml" \
                up -d --no-deps --force-recreate --wait \
                --wait-timeout "$cutover_timeout" proxy; then
            warn "the previous public nginx was restored"
        else
            warn "automatic public proxy rollback failed; immediate operator action is required"
        fi
    fi
    exit "$status"
}

exec 9>"$lock_file"
flock -n 9 || fail "another router HA cutover holds $lock_file"

echo "Preparing router HA topology: versiond=$versiond_mode edge-api=$edge_mode"
if [[ $versiond_mode == ha ]]; then
    compose_project=$("${compose[@]}" config --format json | jq -er '.name')
    ensure_compose_network versiond-router-front \
        "${VERSIOND_ROUTER_FRONT_NETWORK:-gonka-versiond-router-front}" \
        "$compose_project"
    ensure_compose_network versiond-router-back \
        "${VERSIOND_ROUTER_BACK_NETWORK:-gonka-versiond-router-back}" \
        "$compose_project"
    GONKA_CONFIG_ENV=$config_env "$fleet_bin" up
fi

if [[ $pull_policy != never ]]; then
    "${compose[@]}" pull --policy "$pull_policy" proxy-policy proxy
fi
"${compose[@]}" up -d --no-deps --wait \
    --wait-timeout "$cutover_timeout" proxy-policy

# A repeated run converges the current topology without manufacturing a
# rollback image from the already-current proxy-router.
if [[ $(proxy_component) == proxy-router ]]; then
    "${compose[@]}" up -d --no-deps --wait \
        --wait-timeout "$cutover_timeout" proxy
    [[ $versiond_mode != ha ]] || wait_component versiond
    [[ $edge_mode != multi ]] || wait_component edge-api
    remove_migration_routers
    echo "Router HA topology is already active"
    exit 0
fi

container_exists proxy || fail "the existing public proxy container is missing"
old_image=$($docker_bin inspect --format '{{.Image}}' proxy)
rollback_image="gonka/router-ha-proxy-rollback:$(date +%s%N)-$$"
"$docker_bin" tag "$old_image" "$rollback_image"
rollback_pending=true
trap restore_v4_proxy EXIT ERR INT TERM HUP

echo "Switching the public listener to proxy-router"
if ! "${compose[@]}" up -d --no-deps --force-recreate --wait \
    --wait-timeout "$cutover_timeout" proxy; then
    fail "proxy-router did not become ready"
fi
[[ $versiond_mode != ha ]] || wait_component versiond || fail \
    "proxy-router cannot reach a ready versiond-router"
[[ $edge_mode != multi ]] || wait_component edge-api || fail \
    "proxy-router cannot reach a ready edge-api"

rollback_pending=false
trap - EXIT ERR INT TERM HUP
"$docker_bin" image rm "$rollback_image" >/dev/null 2>&1 || true

# These singleton migration bridges are outside the steady-state model. Remove
# them only after the new public path has passed all component checks.
remove_migration_routers

echo "Router HA cutover completed"
