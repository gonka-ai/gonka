#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
config_env=${GONKA_CONFIG_ENV:-$script_dir/config.env}
versiond_mode=auto
edge_mode=auto
compose_project_name=
compose_project_directory=
rollback_pending=false
rollback_image=
rollback_kind=
declare -A rollback_env=()
declare -A inherited_env=()
declare -a migration_routes=()
declare -a compose_file_args=()

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
  --compose-file FILE              repeat for the complete ordered model
  --compose-project-name NAME      must match the running Compose project
  --compose-project-directory DIR  must match the running Compose project

Without --compose-file or COMPOSE_FILE, the script recovers the ordered file
list and project identity from Docker Compose labels on the running services.
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
        --compose-file)
            (($# >= 2)) || fail "--compose-file requires a value"
            compose_file_args+=("$2")
            shift 2
            ;;
        --compose-project-name)
            (($# >= 2)) || fail "--compose-project-name requires a value"
            compose_project_name=$2
            shift 2
            ;;
        --compose-project-directory)
            (($# >= 2)) || fail \
                "--compose-project-directory requires a value"
            compose_project_directory=$2
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
while IFS= read -r name; do
    case $name in
        COMPOSE_* | GONKA_* | VERSIOND_* | ROUTER_HA_* | PROXY_* | DOCKER_BIN)
            inherited_env[$name]=${!name}
            ;;
    esac
done < <(compgen -e)
set -a
# shellcheck disable=SC1090
source "$config_env"
set +a
for name in "${!inherited_env[@]}"; do
    printf -v "$name" '%s' "${inherited_env[$name]}"
    export "${name?}"
done
docker_bin=${DOCKER_BIN:-docker}
fleet_bin=${VERSIOND_ROUTER_FLEET_BIN:-$script_dir/versiond-router-fleet.sh}

command -v "$docker_bin" >/dev/null 2>&1 || fail "$docker_bin is required"
command -v flock >/dev/null 2>&1 || fail "flock is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"
# shellcheck source=deploy/join/compose-topology.sh
source "$script_dir/compose-topology.sh"

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

container_exists proxy || fail \
    "cannot recover the deployment Compose topology: public proxy container is missing"
runtime_compose_containers=(proxy versiond edge-api)
for container in versiond2 devshard-postgres edge-api2 edge-api3; do
    container_exists "$container" && runtime_compose_containers+=("$container")
done
gonka_compose_resolve \
    "$docker_bin" "$script_dir" "$versiond_mode" "$edge_mode" \
    "$compose_project_name" "$compose_project_directory" \
    compose_file_args runtime_compose_containers
compose=("${GONKA_COMPOSE_COMMAND[@]}")

ensure_compose_network() {
    local key=$1 name=$2 project=$3 ownership
    if "$docker_bin" network inspect "$name" >/dev/null 2>&1; then
        ownership=$("$docker_bin" network inspect --format \
            '{{or (index .Labels "com.docker.compose.network") ""}}|{{or (index .Labels "com.docker.compose.project") ""}}' \
            "$name")
        [[ $ownership == "$key|$project" ]] || fail \
            "network $name exists with ownership '$ownership', expected '$key|$project'"
        return 0
    fi
    "$docker_bin" network create --internal \
        --label "com.docker.compose.network=$key" \
        --label "com.docker.compose.project=$project" \
        "$name" >/dev/null
}
pull_policy=${ROUTER_HA_PULL_POLICY:-always}
case $pull_policy in always | missing | never) ;; *) fail "ROUTER_HA_PULL_POLICY must be always, missing, or never" ;; esac
cutover_timeout=${ROUTER_HA_CUTOVER_TIMEOUT_SECONDS:-60}
case $cutover_timeout in '' | *[!0-9]* | 0) fail "ROUTER_HA_CUTOVER_TIMEOUT_SECONDS must be positive" ;; esac
config_dir=$(cd -- "$(dirname -- "$config_env")" && pwd -P)
lock_file=${ROUTER_HA_CUTOVER_LOCK:-$config_dir/.gonka-router-ha-cutover.lock}

proxy_component() {
    "$docker_bin" inspect --format '{{index .Config.Labels "ai.gonka.component"}}' proxy 2>/dev/null || true
}

proxy_env_value() {
    local name=$1 line

    while IFS= read -r line; do
        case $line in
            "$name="*) printf '%s\n' "${line#*=}"; return 0 ;;
        esac
    done < <("$docker_bin" inspect --format \
        '{{range .Config.Env}}{{println .}}{{end}}' proxy)
    return 1
}

arm_proxy_rollback() {
    local kind=$1 old_image key

    old_image=$("$docker_bin" inspect --format '{{.Image}}' proxy)
    rollback_image="gonka/router-ha-proxy-rollback:$(date +%s%N)-$$"
    "$docker_bin" tag "$old_image" "$rollback_image"
    rollback_kind=$kind
    rollback_env=()
    if [[ $kind == current ]]; then
        for key in \
            NGINX_MODE PROXY_POLICY_POOL_SLOTS \
            PROXY_ROUTER_PUBLIC_IDLE_SECONDS HAPROXY_DNS_RESOLVER \
            VERSIOND_ROUTER_POOL_HOST VERSIOND_ROUTER_FLEET_CAPACITY \
            VERSIOND_NON_HA_VERSIONS VERSIOND_VERSIONS \
            VERSIOND_ROUTING_CATALOG_URL \
            VERSIOND_ROUTING_CATALOG_POLL_SECONDS \
            VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS \
            VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS \
            PROXY_ROUTER_VERSION_CAPACITY EDGE_API_POOL_HOST EDGE_API_PORT; do
            rollback_env[$key]=$(proxy_env_value "$key" || true)
        done
    fi
    rollback_pending=true
    trap 'restore_proxy "$?"' EXIT
    trap 'exit 129' HUP
    trap 'exit 130' INT
    trap 'exit 143' TERM
}

wait_component() {
    local component=$1 deadline=$((SECONDS + cutover_timeout))
    while ((SECONDS < deadline)); do
        if "$docker_bin" exec proxy /bin/busybox wget -q -T 3 -O /dev/null \
            "http://127.0.0.1:8404/readyz?component=$component"; then
            return 0
        fi
        sleep 1
    done
    return 1
}

run_fleet() {
    GONKA_CONFIG_ENV=$config_env \
    VERSIOND_ROUTER_FRONT_NETWORK=$GONKA_COMPOSE_ROUTER_FRONT_NETWORK \
    VERSIOND_ROUTER_BACK_NETWORK=$GONKA_COMPOSE_ROUTER_BACK_NETWORK \
    VERSIOND_ROUTER_METRICS_NETWORK=$GONKA_COMPOSE_DEFAULT_NETWORK \
        "$fleet_bin" "$@"
}

urlencode() {
    local input=$1 output='' char hex i
    local LC_ALL=C
    for ((i = 0; i < ${#input}; i++)); do
        char=${input:i:1}
        case $char in
            [A-Za-z0-9.~_-]) output+=$char ;;
            *)
                printf -v hex '%%%02X' "'$char"
                output+=$hex
                ;;
        esac
    done
    printf '%s\n' "$output"
}

capture_migration_route_baseline() {
    local route encoded
    local -A seen=()

    container_exists versiond-router || fail \
        "the transitional versiond-router is missing; refusing a cutover whose v4 rollback would have no upstream"
    migration_routes=()
    while IFS= read -r route; do
        [[ -n $route && -z ${seen[$route]-} ]] || continue
        seen[$route]=1
        encoded=$(urlencode "$route")
        if "$docker_bin" exec versiond-router /bin/busybox wget -q -T 3 \
            -O /dev/null "http://127.0.0.1:8404/readyz?version=$encoded" \
            >/dev/null 2>&1; then
            migration_routes+=("$route")
        fi
    done < <(migration_router_routes)
    ((${#migration_routes[@]} > 0)) || fail \
        "the transitional versiond-router serves none of the declared routes; refusing to commit an unverified fleet"
    echo "Captured migration route baseline: ${migration_routes[*]}"
}

migration_router_routes() {
    local diagnostic=/usr/local/lib/router-runtime/catalog-status map
    if "$docker_bin" exec versiond-router test -x "$diagnostic" \
        >/dev/null 2>&1; then
        for map in /etc/haproxy/non_ha.map /etc/haproxy/versions.map; do
            "$docker_bin" exec versiond-router "$diagnostic" "$map"
        done
        return
    fi

    # Transitional images from before runtime catalog projection expose only
    # their startup environment.
    printf '%s\n%s\n' \
        "${VERSIOND_NON_HA_VERSIONS-v1 v2 v3}" \
        "${VERSIOND_VERSIONS-v4 v5 v6 v7 v8}" \
        | tr ',;' '  ' | tr -s ' ' '\n'
}

remove_migration_container() {
    local container=$1 output
    container_exists "$container" || return 0
    if ! output=$("$docker_bin" rm -f "$container" 2>&1); then
        warn "could not remove migration container $container: $output"
    fi
}

remove_migration_routers() {
    [[ $versiond_mode != ha ]] || remove_migration_container versiond-router
    [[ $edge_mode != multi ]] || remove_migration_container edge-api-router
}

restore_proxy() {
    local status=${1:-$?}
    trap - EXIT INT TERM HUP
    if [[ $rollback_pending == true && -n $rollback_image ]]; then
        warn "public ingress update failed; restoring the captured proxy"
        "$docker_bin" rm -f proxy >/dev/null 2>&1 || true
        local versiond_service=versiond edge_service='' restored=false
        if [[ $rollback_kind == v4 ]]; then
            [[ $versiond_mode == ha ]] && versiond_service=versiond-router
            [[ $edge_mode == multi ]] && edge_service=edge-api-router
            if PROXY_V4_IMAGE=$rollback_image \
                PROXY_V4_VERSIOND_SERVICE_NAME=$versiond_service \
                PROXY_V4_EDGE_API_SERVICE_NAME=$edge_service \
                "${compose[@]}" -f "$script_dir/docker-compose.proxy-v4-compat.yml" \
                    up -d --no-deps --force-recreate --wait \
                    --wait-timeout "$cutover_timeout" proxy; then
                restored=true
            fi
        elif NGINX_MODE="${rollback_env[NGINX_MODE]}" \
            PROXY_POLICY_POOL_SLOTS="${rollback_env[PROXY_POLICY_POOL_SLOTS]}" \
            PROXY_ROUTER_PUBLIC_IDLE_SECONDS="${rollback_env[PROXY_ROUTER_PUBLIC_IDLE_SECONDS]}" \
            HAPROXY_DNS_RESOLVER="${rollback_env[HAPROXY_DNS_RESOLVER]}" \
            VERSIOND_ROUTER_POOL_HOST="${rollback_env[VERSIOND_ROUTER_POOL_HOST]}" \
            VERSIOND_ROUTER_FLEET_CAPACITY="${rollback_env[VERSIOND_ROUTER_FLEET_CAPACITY]}" \
            VERSIOND_NON_HA_VERSIONS="${rollback_env[VERSIOND_NON_HA_VERSIONS]}" \
            VERSIOND_VERSIONS="${rollback_env[VERSIOND_VERSIONS]}" \
            VERSIOND_ROUTING_CATALOG_URL="${rollback_env[VERSIOND_ROUTING_CATALOG_URL]}" \
            VERSIOND_ROUTING_CATALOG_POLL_SECONDS="${rollback_env[VERSIOND_ROUTING_CATALOG_POLL_SECONDS]}" \
            VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS="${rollback_env[VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS]}" \
            VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS="${rollback_env[VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS]}" \
            PROXY_ROUTER_VERSION_CAPACITY="${rollback_env[PROXY_ROUTER_VERSION_CAPACITY]}" \
            EDGE_API_POOL_HOST="${rollback_env[EDGE_API_POOL_HOST]}" \
            EDGE_API_PORT="${rollback_env[EDGE_API_PORT]}" \
            PROXY_ROUTER_IMAGE=$rollback_image \
            "${compose[@]}" up -d --no-deps --force-recreate --wait \
                --wait-timeout "$cutover_timeout" proxy; then
            restored=true
        fi
        if [[ $restored == true && $rollback_kind == current ]]; then
            [[ $versiond_mode != ha ]] || run_fleet verify-admission || \
                restored=false
            [[ $edge_mode != multi || $restored != true ]] || \
                wait_component edge-api || restored=false
        fi
        if [[ $restored == true ]]; then
            warn "the previous public proxy was restored"
        else
            warn "automatic public proxy rollback failed; immediate operator action is required"
        fi
    fi
    exit "$status"
}

if [[ ! -e $lock_file ]]; then
    (umask 000; : >"$lock_file") || fail "cannot create router HA lock $lock_file"
fi
exec 9<"$lock_file"
flock -n 9 || fail "another router HA cutover holds $lock_file"

echo "Preparing router HA topology: versiond=$versiond_mode edge-api=$edge_mode"
compose_project=$GONKA_COMPOSE_PROJECT
policy_network=$GONKA_COMPOSE_POLICY_NETWORK
ensure_compose_network proxy-policy-front "$policy_network" "$compose_project"
if [[ $versiond_mode == ha ]]; then
    # `apply` is the lifecycle bridge between the main Compose project and the
    # independent router projects. It bootstraps an absent fleet, but on an
    # existing deployment it pulls and rolls only slots whose image or rendered
    # Compose contract changed.
    run_fleet apply
fi

# During the one-time cutover the old public nginx still owns the `proxy`
# container name. Attach it to the private policy network before starting the
# workers so they can derive a stable trusted subnet from the future ingress
# alias. Recreating `proxy` below replaces this endpoint with proxy-router.
if container_exists proxy; then
    policy_aliases=$("$docker_bin" inspect --format \
        "{{with index .NetworkSettings.Networks \"$policy_network\"}}{{range .Aliases}}{{println .}}{{end}}{{end}}" \
        proxy)
    if ! grep -Fxq proxy-policy-ingress <<<"$policy_aliases"; then
        if [[ -n $policy_aliases ]]; then
            "$docker_bin" network disconnect "$policy_network" proxy
        fi
        "$docker_bin" network connect --alias proxy-policy-ingress \
            "$policy_network" proxy
    fi
fi

if [[ $pull_policy != never ]]; then
    "${compose[@]}" pull --policy "$pull_policy" proxy-policy proxy
fi
"${compose[@]}" up -d --no-deps --wait \
    --wait-timeout "$cutover_timeout" proxy-policy

# A repeated run is also a real update path. Keep the exact previous proxy
# image and routing environment armed until the new public path is admitted.
if [[ $(proxy_component) == proxy-router ]]; then
    arm_proxy_rollback current
    "${compose[@]}" up -d --no-deps --wait \
        --wait-timeout "$cutover_timeout" proxy
    [[ $versiond_mode != ha ]] || run_fleet verify-admission
    [[ $edge_mode != multi ]] || wait_component edge-api
    rollback_pending=false
    trap - EXIT INT TERM HUP
    "$docker_bin" image rm "$rollback_image" >/dev/null 2>&1 || true
    remove_migration_routers
    echo "Router HA topology is already active"
    exit 0
fi

container_exists proxy || fail "the existing public proxy container is missing"
[[ $versiond_mode != ha ]] || capture_migration_route_baseline
arm_proxy_rollback v4

echo "Switching the public listener to proxy-router"
if ! "${compose[@]}" up -d --no-deps --force-recreate --wait \
    --wait-timeout "$cutover_timeout" proxy; then
    fail "proxy-router did not become ready"
fi
[[ $versiond_mode != ha ]] || \
    run_fleet verify-admission "${migration_routes[@]}" || fail \
        "proxy-router did not admit the complete router fleet and migration route baseline"
[[ $edge_mode != multi ]] || wait_component edge-api || fail \
    "proxy-router cannot reach a ready edge-api"

rollback_pending=false
trap - EXIT INT TERM HUP
"$docker_bin" image rm "$rollback_image" >/dev/null 2>&1 || true

# These singleton migration bridges are outside the steady-state model. Remove
# them only after the new public path has passed all component checks.
remove_migration_routers

echo "Router HA cutover completed"
