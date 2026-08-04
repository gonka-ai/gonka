#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
config_env=${GONKA_CONFIG_ENV:-$script_dir/config.env}
docker_bin=${DOCKER_BIN:-docker}
release_tag=0.2.15-devshard-v5
operation_id="$(date +%s%N)-$$"
edge_mode=

fail() {
    echo "upgrade-devshard-v5: $*" >&2
    exit 1
}

warn() {
    echo "upgrade-devshard-v5: warning: $*" >&2
}

usage() {
    cat >&2 <<'EOF'
Usage: upgrade-devshard-v5.sh --edge-mode single|multi

Use single for docker-compose.yml + docker-compose.versiond.yml.
Use multi when docker-compose.edge-api-multi.yml is also deployed.
EOF
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --edge-mode)
            [[ $# -ge 2 ]] || fail "--edge-mode requires a value"
            edge_mode=$2
            shift 2
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

case $edge_mode in
    single | multi) ;;
    *)
        usage
        fail "--edge-mode must be single or multi"
        ;;
esac

[[ -f $config_env ]] || fail "configuration file not found: $config_env"
# shellcheck disable=SC1090
source "$config_env"

command -v "$docker_bin" >/dev/null 2>&1 || fail "$docker_bin is required"

# Pin this operation to the exact release. The Compose files expose these
# variables so a failed replacement can be recreated from its captured image.
export EDGE_API_IMAGE="ghcr.io/product-science/edge-api:$release_tag"
export VERSIOND_IMAGE="ghcr.io/product-science/versiond:$release_tag"
export EDGE_API_ROUTER_IMAGE="ghcr.io/product-science/edge-api-router:$release_tag"
export VERSIOND_ROUTER_IMAGE="ghcr.io/product-science/versiond-router:$release_tag"

compose=(
    "$docker_bin" compose
    --project-directory "$script_dir"
    -f "$script_dir/docker-compose.yml"
    -f "$script_dir/docker-compose.versiond.yml"
)
if [[ $edge_mode == multi ]]; then
    compose+=(-f "$script_dir/docker-compose.edge-api-multi.yml")
fi

declare -A image_variables=(
    [versiond]=VERSIOND_IMAGE
    [versiond2]=VERSIOND_IMAGE
    [versiond-router]=VERSIOND_ROUTER_IMAGE
    [edge-api]=EDGE_API_IMAGE
    [edge-api2]=EDGE_API_IMAGE
    [edge-api3]=EDGE_API_IMAGE
    [edge-api-router]=EDGE_API_ROUTER_IMAGE
)
declare -A rollback_images=()

capture_rollback_image() {
    local service=$1
    local container_id image_id rollback_image

    container_id=$("${compose[@]}" ps --all --quiet "$service")
    if [[ -z $container_id ]]; then
        warn "$service has no existing container; rollback will stop a failed replacement"
        return 0
    fi

    image_id=$("$docker_bin" inspect --format '{{.Image}}' "$container_id")
    [[ -n $image_id ]] || fail "cannot determine the current image for $service"
    rollback_image="gonka-upgrade-rollback/$service:$operation_id"
    "$docker_bin" tag "$image_id" "$rollback_image"
    rollback_images[$service]=$rollback_image
    echo "Captured $service rollback image as $rollback_image"
}

service_is_running() {
    local service=$1
    local container_id

    container_id=$("${compose[@]}" ps --all --quiet "$service")
    [[ -n $container_id ]] || return 1
    [[ $("$docker_bin" inspect --format '{{.State.Running}}' "$container_id") == true ]]
}

stop_failed_service() {
    local service=$1

    if ! "${compose[@]}" stop "$service"; then
        warn "could not stop failed service $service; operator action is required"
        return 1
    fi
}

rollback_service() {
    local service=$1
    local rollback_image=${rollback_images[$service]-}
    local image_variable=${image_variables[$service]}

    if [[ -z $rollback_image ]]; then
        warn "$service has no captured image; stopping it instead"
        stop_failed_service "$service"
        return 1
    fi

    echo "Restoring $service from $rollback_image" >&2
    if env "$image_variable=$rollback_image" \
        "${compose[@]}" up -d --no-deps --force-recreate "$service" &&
        service_is_running "$service"; then
        return 0
    fi

    warn "rollback of $service failed; stopping the failed service"
    stop_failed_service "$service" || true
    return 1
}

replace_service() {
    local service=$1
    local wait_timeout=$2
    local failure_strategy=$3

    echo "Replacing $service"
    if "${compose[@]}" up -d --no-deps --force-recreate --wait \
        --wait-timeout "$wait_timeout" "$service"; then
        return 0
    fi

    case $failure_strategy in
        rollback)
            if rollback_service "$service"; then
                fail "$service did not become ready; its previous image was restored"
            fi
            fail "$service did not become ready and rollback failed; it was stopped"
            ;;
        stop)
            stop_failed_service "$service" || true
            fail "$service did not become ready and was removed from service"
            ;;
        *) fail "internal error: unknown failure strategy $failure_strategy" ;;
    esac
}

cleanup_rollback_tags() {
    local service rollback_image

    for service in "${!rollback_images[@]}"; do
        rollback_image=${rollback_images[$service]}
        if ! "$docker_bin" image rm "$rollback_image" >/dev/null; then
            warn "could not remove temporary image tag $rollback_image"
        fi
    done
}

services=(versiond versiond2 versiond-router edge-api)
if [[ $edge_mode == multi ]]; then
    services+=(edge-api2 edge-api3 edge-api-router)
fi
for service in "${services[@]}"; do
    capture_rollback_image "$service"
done

pull_services=(devshard-postgres versiond versiond2 versiond-router edge-api)
if [[ $edge_mode == multi ]]; then
    pull_services+=(edge-api2 edge-api3 edge-api-router)
fi
"${compose[@]}" pull "${pull_services[@]}"

echo "Migrating and starting devshard-postgres"
if ! "${compose[@]}" up -d --no-deps --wait --wait-timeout 2100 \
    devshard-postgres; then
    stop_failed_service devshard-postgres || true
    fail "devshard-postgres did not become ready and was stopped"
fi

# Keep the old nginx router until both versiond hosts implement the v5
# readiness contract. A failed host is restored before the other is touched.
replace_service versiond2 2100 rollback
replace_service versiond 2100 rollback
replace_service versiond-router 60 rollback

if [[ $edge_mode == single ]]; then
    # There is no second Tier A replica in this topology, so preserve the old
    # image if the only edge-api replacement does not become ready.
    replace_service edge-api 180 rollback
else
    # Upgrade one replica behind nginx, then put HAProxy in front immediately.
    # HAProxy routes only to replicas whose /readyz check passes. Later failed
    # replacements can therefore be stopped without poisoning the live pool.
    replace_service edge-api2 180 rollback
    replace_service edge-api-router 60 rollback
    replace_service edge-api3 180 stop
    replace_service edge-api 180 stop
fi

cleanup_rollback_tags
echo "Devshard v5 upgrade completed"
