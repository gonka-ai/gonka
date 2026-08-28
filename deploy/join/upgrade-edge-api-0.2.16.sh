#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
config_env=${GONKA_CONFIG_ENV:-$script_dir/config.env}
release_contract=$script_dir/edge-api-0.2.16-release.env
edge_mode=auto
preflight_only=false
maintenance_ack=false
compose_project_name=
compose_project_directory=
declare -a compose_file_args=()

fail() {
    echo "upgrade-edge-api-0.2.16: $*" >&2
    exit 1
}

warn() {
    echo "upgrade-edge-api-0.2.16: warning: $*" >&2
}

usage() {
    cat >&2 <<'EOF'
Usage: upgrade-edge-api-0.2.16.sh [OPTIONS]

Updates only edge-api and the ingress services that route to it. PostgreSQL,
versiond, and versiond-router fleet containers are observed but never changed.

Options:
  --edge-mode auto|single|multi
  --compose-file FILE              repeat for the complete ordered model
  --compose-project-name NAME      must match the running Compose project
  --compose-project-directory DIR  must match the running Compose project
  --preflight-only                 verify the release and host without mutation
  --acknowledge-maintenance        allow the public ingress replacement
EOF
}

while (($# > 0)); do
    case $1 in
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
        --preflight-only)
            preflight_only=true
            shift
            ;;
        --acknowledge-maintenance)
            maintenance_ack=true
            shift
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            usage
            fail "unknown argument: $1"
            ;;
    esac
done

case $edge_mode in auto | single | multi) ;; *) fail "invalid edge-api mode" ;; esac
[[ -f $release_contract ]] || fail "release contract not found: $release_contract"
[[ -f $config_env ]] || fail "configuration file not found: $config_env"
set -a
# shellcheck disable=SC1090
source "$config_env"
set +a
# Load release-owned targets after host configuration so config.env cannot
# silently replace the images selected by the 0.2.16 contract.
# shellcheck disable=SC1090
source "$release_contract"
for name in EDGE_API_0_2_16_RELEASE_ID \
    EDGE_API_0_2_16_PREREQUISITE_RELEASE_ID \
    EDGE_API_0_2_16_RELEASE_IMAGE_TAG \
    EDGE_API_0_2_16_IMAGE \
    EDGE_API_0_2_16_PROXY_POLICY_IMAGE EDGE_API_0_2_16_PROXY_ROUTER_IMAGE \
    EDGE_API_0_2_16_MIN_COMPOSE_VERSION; do
    [[ -n ${!name:-} ]] || fail "release contract has no $name"
done
[[ $EDGE_API_0_2_16_RELEASE_ID =~ ^[A-Za-z0-9._-]+$ ]] || fail \
    "release contract has an invalid release ID"
allow_unreleased_images=${EDGE_API_0_2_16_ALLOW_UNRELEASED_IMAGES:-false}
case $allow_unreleased_images in true | false) ;; *) fail \
    "EDGE_API_0_2_16_ALLOW_UNRELEASED_IMAGES must be true or false" ;; esac
for name in EDGE_API_0_2_16_IMAGE EDGE_API_0_2_16_PROXY_POLICY_IMAGE \
    EDGE_API_0_2_16_PROXY_ROUTER_IMAGE; do
    if [[ ${!name} =~ ^[^[:space:]@]+@sha256:[0-9a-f]{64}$ ]]; then
        continue
    fi
    [[ $allow_unreleased_images == true && \
        ${!name} == *":$EDGE_API_0_2_16_RELEASE_IMAGE_TAG" ]] || fail \
        "$name must use an immutable sha256 digest"
done
[[ $allow_unreleased_images == false ]] || warn \
    "unreleased image tags are enabled explicitly"
docker_bin=${DOCKER_BIN:-docker}
enable_router_bin=${ROUTER_HA_ENABLE_BIN:-$script_dir/enable-router-ha.sh}
config_dir=$(cd -- "$(dirname -- "$config_env")" && pwd -P)
operation_id="$(date +%s%N)-$$"
marker=${EDGE_API_0_2_16_UPGRADE_MARKER:-$config_dir/.gonka-edge-api-0.2.16-upgrade-complete}
prerequisite_marker=${DEVSHARD_V5_UPGRADE_MARKER:-$config_dir/.gonka-devshard-v5-upgrade-complete}
export EDGE_API_IMAGE=$EDGE_API_0_2_16_IMAGE
export PROXY_POLICY_IMAGE=$EDGE_API_0_2_16_PROXY_POLICY_IMAGE
export PROXY_ROUTER_IMAGE=$EDGE_API_0_2_16_PROXY_ROUTER_IMAGE

for command in "$docker_bin" jq flock sha256sum; do
    command -v "$command" >/dev/null 2>&1 || fail "$command is required"
done
compose_version=$("$docker_bin" compose version --short 2>/dev/null) || fail \
    "Docker Compose v2 is required"
version_at_least() {
    local actual=${1#v} required=${2#v}
    [[ $(printf '%s\n%s\n' "$required" "$actual" | sort -V | head -n1) == "$required" ]]
}
version_at_least "$compose_version" "$EDGE_API_0_2_16_MIN_COMPOSE_VERSION" || fail \
    "Docker Compose $EDGE_API_0_2_16_MIN_COMPOSE_VERSION or newer is required"

# shellcheck disable=SC1091
source "$script_dir/compose-topology.sh"
# shellcheck disable=SC1091
source "$script_dir/deployment-lock.sh"
gonka_acquire_deployment_lock "$config_dir" || exit 1

container_exists() {
    "$docker_bin" inspect "$1" >/dev/null 2>&1
}

versiond_mode=single
if container_exists versiond2 || container_exists devshard-postgres; then
    versiond_mode=ha
fi
if [[ $edge_mode == auto ]]; then
    if container_exists edge-api-router || container_exists edge-api2 || \
        container_exists edge-api3; then
        edge_mode=multi
    elif container_exists edge-api; then
        edge_mode=single
    else
        fail "cannot detect edge-api topology"
    fi
fi

runtime_containers=(proxy versiond edge-api)
for container in proxy-policy proxy-policy2 versiond2 devshard-postgres \
    edge-api2 edge-api3 edge-api-router; do
    container_exists "$container" && runtime_containers+=("$container")
done
gonka_compose_resolve \
    "$docker_bin" "$script_dir" "$versiond_mode" "$edge_mode" \
    "$compose_project_name" "$compose_project_directory" \
    compose_file_args runtime_containers
compose=("${GONKA_COMPOSE_COMMAND[@]}")
compose_config=$("${compose[@]}" config --format json)
compose_sha=$(jq -Sc . <<<"$compose_config" | sha256sum | awk '{print $1}')

verify_compose_unchanged() {
    local current
    current=$("${compose[@]}" config --format json | jq -Sc . | \
        sha256sum | awk '{print $1}') || fail "effective Compose model no longer renders"
    [[ $current == "$compose_sha" ]] || fail \
        "effective Compose model changed during the update"
}

for service in edge-api proxy-policy proxy-policy2 proxy; do
    jq -e --arg service "$service" '.services | has($service)' \
        >/dev/null <<<"$compose_config" || fail \
        "resolved Compose topology has no $service service"
done
if [[ $edge_mode == multi ]]; then
    for service in edge-api2 edge-api3; do
        jq -e --arg service "$service" '.services | has($service)' \
            >/dev/null <<<"$compose_config" || fail \
            "resolved multi-edge topology has no $service service"
    done
fi
proxy_component=$("$docker_bin" inspect --format \
    '{{index .Config.Labels "ai.gonka.component"}}' proxy 2>/dev/null || true)
[[ $proxy_component == proxy-router ]] || fail \
    "the 0.2.15 public proxy rollout must complete before this update"
[[ -f $prerequisite_marker ]] || fail \
    "the 0.2.15 host update marker is missing: $prerequisite_marker"
prerequisite_release=$(jq -er '.release_id | strings | select(length > 0)' \
    "$prerequisite_marker" 2>/dev/null) || fail \
    "the 0.2.15 host update marker is invalid: $prerequisite_marker"
[[ $prerequisite_release == "$EDGE_API_0_2_16_PREREQUISITE_RELEASE_ID" ]] || fail \
    "host update prerequisite is $prerequisite_release, expected $EDGE_API_0_2_16_PREREQUISITE_RELEASE_ID"

echo "Edge API 0.2.16 preflight passed: topology=$edge_mode compose=$compose_sha"
if [[ $preflight_only == true ]]; then
    exit 0
fi
if [[ $maintenance_ack == false ]]; then
    case ${EDGE_API_0_2_16_MAINTENANCE_ACK:-false} in
        true | 1 | yes) maintenance_ack=true ;;
    esac
fi
[[ $maintenance_ack == true ]] || fail \
    "public ingress is replaced during this update; rerun with --acknowledge-maintenance"

declare -A rollback_images=()
active_service=
barrier_active=false
barrier_hosts=

container_env_value() {
    local container=$1 name=$2 line
    while IFS= read -r line; do
        case $line in "$name="*) printf '%s\n' "${line#*=}"; return 0 ;; esac
    done < <("$docker_bin" inspect --format \
        '{{range .Config.Env}}{{println .}}{{end}}' "$container")
    return 1
}

without_host() {
    local hosts=$1 excluded=$2 host
    local -a remaining=()
    for host in $hosts; do
        [[ $host == "$excluded" ]] || remaining+=("$host")
    done
    ((${#remaining[@]} > 0)) || return 1
    printf '%s\n' "${remaining[*]}"
}

render_router_hosts() {
    local hosts=$1
    # The program is evaluated by /bin/sh inside edge-api-router.
    # shellcheck disable=SC2016
    "$docker_bin" exec --env "EDGE_API_HOSTS=$hosts" edge-api-router \
        /bin/sh -ec '
            conf=/etc/nginx/conf.d/default.conf
            backup=$(mktemp)
            cp "$conf" "$backup"
            if /docker-entrypoint.d/40-render-edge-api-upstream.sh && nginx -t && nginx -s reload; then
                rm -f "$backup"
            else
                cp "$backup" "$conf"
                rm -f "$backup"
                exit 1
            fi
        '
}

install_router_barrier() {
    local hosts=$1
    local hook=/docker-entrypoint.d/99-gonka-upgrade-barrier.sh
    local staged_hook=/tmp/99-gonka-upgrade-barrier.sh
    local state=/etc/gonka-upgrade-barrier

    "$docker_bin" cp "$script_dir/legacy-router-upgrade-barrier.sh" \
        "edge-api-router:$staged_hook"
    # The program is evaluated by /bin/sh inside edge-api-router.
    # shellcheck disable=SC2016
    "$docker_bin" exec \
        --env GONKA_BARRIER_ENV_NAME=EDGE_API_HOSTS \
        --env "GONKA_BARRIER_HOSTS=$hosts" \
        --env GONKA_BARRIER_RENDERER=/docker-entrypoint.d/40-render-edge-api-upstream.sh \
        edge-api-router /bin/sh -ec '
            state=$1
            hook=$2
            staged_hook=$3
            staged_state="${state}.tmp"
            printf "%s\n%s\n%s\n" \
                "$GONKA_BARRIER_ENV_NAME" \
                "$GONKA_BARRIER_HOSTS" \
                "$GONKA_BARRIER_RENDERER" >"$staged_state"
            chmod 0600 "$staged_state"
            mv "$staged_state" "$state"
            chmod 0755 "$staged_hook"
            mv "$staged_hook" "$hook"
        ' sh "$state" "$hook" "$staged_hook"
}

remove_router_barrier() {
    "$docker_bin" exec edge-api-router rm -f \
        /etc/gonka-upgrade-barrier \
        /docker-entrypoint.d/99-gonka-upgrade-barrier.sh \
        /tmp/99-gonka-upgrade-barrier.sh
}

begin_barrier() {
    local service=$1 isolated
    [[ $edge_mode == multi ]] || return 0
    barrier_hosts=$(container_env_value edge-api-router EDGE_API_HOSTS) || fail \
        "cannot read EDGE_API_HOSTS from edge-api-router"
    isolated=$(without_host "$barrier_hosts" "$service") || fail \
        "cannot isolate $service from edge-api-router"
    # Arm compensation before publishing either the persistent or live barrier.
    barrier_active=true
    install_router_barrier "$isolated"
    render_router_hosts "$isolated"
}

restore_barrier() {
    [[ $barrier_active == true ]] || return 0
    remove_router_barrier
    render_router_hosts "$barrier_hosts"
    barrier_active=false
}

service_available() {
    local service=$1 id
    id=$("${compose[@]}" ps --all --quiet "$service")
    [[ -n $id ]] || return 1
    [[ $("$docker_bin" inspect --format \
        '{{.State.Running}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' \
        "$id") == 'true|healthy' ]] || return 1
    "$docker_bin" exec "$id" /bin/busybox wget -q -T 5 -O /dev/null \
        http://127.0.0.1:18080/v1/versions
}

rollback_service_available() {
    local service=$1 id
    id=$("${compose[@]}" ps --all --quiet "$service")
    [[ -n $id ]] || return 1
    [[ $("$docker_bin" inspect --format '{{.State.Running}}' "$id") == true ]] || \
        return 1
    # The 0.2.15 edge-api image predates /readyz. Verify its legacy liveness
    # endpoint and one real API route instead of the 0.2.16 Compose healthcheck.
    "$docker_bin" exec "$id" /bin/busybox wget -q -T 5 -O /dev/null \
        http://127.0.0.1:18080/healthz && \
        "$docker_bin" exec "$id" /bin/busybox wget -q -T 5 -O /dev/null \
        http://127.0.0.1:18080/v1/versions
}

service_uses_target_image() {
    local service=$1 id image
    id=$("${compose[@]}" ps --all --quiet "$service")
    [[ -n $id ]] || return 1
    image=$("$docker_bin" inspect --format '{{.Config.Image}}' "$id")
    [[ $image == "$EDGE_API_IMAGE" ]] && service_available "$service"
}

service_matches_image() {
    local service=$1 expected=$2 id image state
    id=$("${compose[@]}" ps --all --quiet "$service")
    [[ -n $id ]] || return 1
    image=$("$docker_bin" inspect --format '{{.Config.Image}}' "$id")
    state=$("$docker_bin" inspect --format \
        '{{.State.Running}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' \
        "$id")
    [[ $image == "$expected" && $state == 'true|healthy' ]]
}

capture_rollback() {
    local service=$1 id image rollback
    id=$("${compose[@]}" ps --all --quiet "$service")
    [[ -n $id ]] || fail "cannot find existing $service container"
    image=$("$docker_bin" inspect --format '{{.Image}}' "$id")
    rollback="gonka-upgrade-rollback/$service:$operation_id"
    "$docker_bin" tag "$image" "$rollback"
    rollback_images[$service]=$rollback
}

rollback_active() {
    local rollback
    [[ -n $active_service ]] || return 0
    rollback=${rollback_images[$active_service]-}
    [[ -n $rollback ]] || return 1
    warn "restoring $active_service from $rollback"
    EDGE_API_IMAGE=$rollback "${compose[@]}" up -d --no-deps \
        --force-recreate "$active_service" && \
        rollback_service_available "$active_service"
}

cleanup_rollback_images() {
    local image
    for image in "${rollback_images[@]}"; do
        "$docker_bin" image rm "$image" >/dev/null || warn \
            "could not remove temporary image $image"
    done
}

handle_exit() {
    local status=$? recovery_ok=true
    trap - EXIT INT TERM HUP
    if ((status != 0)); then
        rollback_active || recovery_ok=false
        restore_barrier || recovery_ok=false
        if [[ $recovery_ok == true ]]; then
            cleanup_rollback_images
        else
            warn "rollback artifacts were retained for manual recovery"
        fi
    fi
    exit "$status"
}
trap handle_exit EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

replace_edge_service() {
    local service=$1
    if service_uses_target_image "$service"; then
        echo "$service already uses the 0.2.16 release image"
        return 0
    fi
    verify_compose_unchanged
    capture_rollback "$service"
    begin_barrier "$service"
    active_service=$service
    if ! "${compose[@]}" up -d --no-deps --force-recreate --wait \
        --wait-timeout 180 "$service" || ! service_available "$service"; then
        fail "$service did not become ready"
    fi
    active_service=
    restore_barrier
}

pull_services=(edge-api proxy-policy2 proxy-policy proxy)
if [[ $edge_mode == multi ]]; then
    pull_services+=(edge-api2 edge-api3)
fi
"${compose[@]}" pull "${pull_services[@]}"
if [[ $edge_mode == multi ]]; then
    migration_required=false
    for service in edge-api2 edge-api3 edge-api; do
        service_uses_target_image "$service" || migration_required=true
    done
    if [[ $migration_required == true ]]; then
        container_exists edge-api-router || fail \
            "an unfinished multi-edge migration needs the existing edge-api-router"
    fi
fi
if [[ $edge_mode == multi ]]; then
    for service in edge-api2 edge-api3 edge-api; do
        replace_edge_service "$service"
    done
else
    replace_edge_service edge-api
fi

verify_compose_unchanged
"$enable_router_bin" --ingress-only \
    --versiond-mode "$versiond_mode" --edge-mode "$edge_mode" \
    "${GONKA_COMPOSE_FORWARD_ARGS[@]}"

service_matches_image proxy-policy2 "$PROXY_POLICY_IMAGE" || fail \
    "proxy-policy2 did not converge to the 0.2.16 ingress image"
service_matches_image proxy-policy "$PROXY_POLICY_IMAGE" || fail \
    "proxy-policy did not converge to the 0.2.16 ingress image"
service_matches_image proxy "$PROXY_ROUTER_IMAGE" || fail \
    "proxy did not converge to the 0.2.16 ingress image"

service_available edge-api || fail "edge-api is not ready after ingress update"
if [[ $edge_mode == multi ]]; then
    service_available edge-api2 || fail "edge-api2 is not ready after ingress update"
    service_available edge-api3 || fail "edge-api3 is not ready after ingress update"
    "$docker_bin" rm -f edge-api-router >/dev/null || warn \
        "could not remove the retired edge-api-router"
fi

marker_payload=$(jq -cn \
    --arg release "$EDGE_API_0_2_16_RELEASE_ID" \
    --arg prerequisite "$prerequisite_release" \
    --arg topology "$edge_mode" --arg compose "$compose_sha" \
    --arg edge "$EDGE_API_IMAGE" --arg policy "$PROXY_POLICY_IMAGE" \
    --arg proxy "$PROXY_ROUTER_IMAGE" \
    '{release_id:$release, prerequisite_release_id:$prerequisite,
      topology:$topology, compose_sha256:$compose,
      images:{edge_api:$edge, proxy_policy:$policy, proxy_router:$proxy}}')
marker_tmp=$(mktemp "$config_dir/.gonka-edge-api-0.2.16.XXXXXX")
printf '%s\n' "$marker_payload" >"$marker_tmp"
chmod 600 "$marker_tmp"
mv -f "$marker_tmp" "$marker"

cleanup_rollback_images
trap - EXIT INT TERM HUP
echo "Edge API 0.2.16 update completed"
