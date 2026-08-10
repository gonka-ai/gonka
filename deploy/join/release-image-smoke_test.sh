#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
base="$script_dir/docker-compose.yml"
versiond_overlay="$script_dir/docker-compose.versiond.yml"
edge_overlay="$script_dir/docker-compose.edge-api-multi.yml"
slot_compose="$script_dir/versiond-router-slot/docker-compose.yml"
versiond_compat="$script_dir/docker-compose.versiond-v5-compat.yml"
edge_compat="$script_dir/docker-compose.edge-api-v5-compat.yml"
proxy_compat="$script_dir/docker-compose.proxy-v4-compat.yml"

release_tag=0.2.15-devshard-v5
edge_image="ghcr.io/product-science/edge-api:$release_tag"
versiond_image="ghcr.io/product-science/versiond:$release_tag"
edge_router_image="ghcr.io/product-science/edge-api-router:$release_tag"
versiond_router_image="ghcr.io/product-science/versiond-router:$release_tag"
proxy_policy_image="ghcr.io/product-science/proxy:$release_tag"
proxy_router_image="ghcr.io/product-science/proxy-router:$release_tag"

# This test validates the shipped defaults, not an operator's local override.
unset EDGE_API_IMAGE VERSIOND_IMAGE EDGE_API_ROUTER_IMAGE VERSIOND_ROUTER_IMAGE
unset PROXY_POLICY_IMAGE PROXY_ROUTER_IMAGE

fail() {
    echo "release-image-smoke_test: $*" >&2
    exit 1
}

compose_image() {
    local service=$1
    shift
    DEVSHARD_POSTGRES_PASSWORD=compose-contract \
        docker compose "$@" config --format json 2>/dev/null |
        jq -er --arg service "$service" '.services[$service].image'
}

assert_service_absent() {
    local description=$1
    local service=$2
    shift 2

    if DEVSHARD_POSTGRES_PASSWORD=compose-contract \
        docker compose "$@" config --services 2>/dev/null | \
        grep -Fxq "$service"; then
        fail "$description unexpectedly defines service $service"
    fi
}

assert_image() {
    local description=$1
    local expected=$2
    local actual=$3
    [[ $actual == "$expected" ]] || fail \
        "$description resolves to $actual, expected $expected"
}

assert_healthcheck_url() {
    local description=$1
    local service=$2
    local url=$3
    shift 3

    DEVSHARD_POSTGRES_PASSWORD=compose-contract \
        docker compose "$@" config --format json 2>/dev/null |
        jq -e --arg service "$service" --arg url "$url" \
            '.services[$service].healthcheck.test | index($url) != null' \
            >/dev/null || fail "$description does not check $url"
}

assert_environment() {
    local description=$1
    local service=$2
    local key=$3
    local expected=$4
    shift 4
    local actual

    actual=$(DEVSHARD_POSTGRES_PASSWORD=compose-contract \
        docker compose "$@" config --format json 2>/dev/null |
        jq -er --arg service "$service" --arg key "$key" \
            '.services[$service].environment[$key]')
    [[ $actual == "$expected" ]] || fail \
        "$description resolves to $actual, expected $expected"
}

check_compose_contract() {
    assert_image "base edge-api" "$edge_image" \
        "$(compose_image edge-api -f "$base")"
    assert_image "edge-api-multi edge-api" "$edge_image" \
        "$(compose_image edge-api -f "$base" -f "$edge_overlay")"
    assert_image "base versiond" "$versiond_image" \
        "$(compose_image versiond -f "$base")"
    assert_healthcheck_url "base versiond healthcheck" versiond \
        "http://127.0.0.1:8080/readyz" -f "$base"
    assert_image "versiond HA overlay" "$versiond_image" \
        "$(compose_image versiond -f "$base" -f "$versiond_overlay")"
    assert_image "private proxy policy" "$proxy_policy_image" \
        "$(compose_image proxy-policy -f "$base")"
    assert_image "public proxy router" "$proxy_router_image" \
        "$(compose_image proxy -f "$base")"
    assert_healthcheck_url "public proxy readiness" proxy \
        "http://127.0.0.1:8404/readyz" -f "$base"
    assert_healthcheck_url "private policy healthcheck" proxy-policy \
        "http://127.0.0.1:8081/health" -f "$base"
    assert_service_absent "steady-state versiond model" versiond-router \
        -f "$base" -f "$versiond_overlay"
    assert_service_absent "steady-state edge-api model" edge-api-router \
        -f "$base" -f "$edge_overlay"

    assert_image "versiond router slot" "$versiond_router_image" \
        "$(VERSIOND_ROUTER_SLOT=0 compose_image router -f "$slot_compose")"
    VERSIOND_ROUTER_SLOT=0 assert_healthcheck_url \
        "versiond router slot healthcheck" router \
        "http://127.0.0.1:8404/readyz" -f "$slot_compose"

    assert_image "versiond migration router" "$versiond_router_image" \
        "$(compose_image versiond-router -f "$base" -f "$versiond_overlay" -f "$versiond_compat")"
    assert_image "edge-api migration router" "$edge_router_image" \
        "$(compose_image edge-api-router -f "$base" -f "$edge_overlay" -f "$edge_compat")"
    assert_environment "versiond v4 rollback hosts" versiond-router \
        VERSIOND_HOSTS "versiond versiond2" \
        -f "$base" -f "$versiond_overlay" -f "$versiond_compat"
    assert_environment "edge-api v4 rollback hosts" edge-api-router \
        EDGE_API_HOSTS "edge-api edge-api2 edge-api3" \
        -f "$base" -f "$edge_overlay" -f "$edge_compat"

    rollback_proxy=$(PROXY_V4_IMAGE=sha256:captured-proxy \
        PROXY_V4_VERSIOND_SERVICE_NAME=versiond-router \
        PROXY_V4_EDGE_API_SERVICE_NAME=edge-api-router \
        compose_image proxy -f "$base" -f "$versiond_overlay" \
            -f "$edge_overlay" -f "$proxy_compat")
    assert_image "public nginx rollback" sha256:captured-proxy "$rollback_proxy"
    PROXY_V4_IMAGE=sha256:captured-proxy \
        PROXY_V4_VERSIOND_SERVICE_NAME=versiond-router \
        PROXY_V4_EDGE_API_SERVICE_NAME=edge-api-router \
        assert_environment "public nginx rollback protocol" proxy \
            PROXY_PROTOCOL false -f "$base" -f "$versiond_overlay" \
            -f "$edge_overlay" -f "$proxy_compat"
    PROXY_V4_IMAGE=sha256:captured-proxy \
        PROXY_V4_VERSIOND_SERVICE_NAME=versiond-router \
        PROXY_V4_EDGE_API_SERVICE_NAME=edge-api-router \
        assert_environment "public nginx rollback versiond" proxy \
            VERSIOND_SERVICE_NAME versiond-router -f "$base" \
            -f "$versiond_overlay" -f "$edge_overlay" -f "$proxy_compat"
}

if [[ ${1:-} == --contract ]]; then
    check_compose_contract
    echo "release-image-smoke_test: compose contract ok"
    exit 0
fi
[[ $# -eq 0 ]] || fail "usage: $0 [--contract]"

check_compose_contract
command -v curl >/dev/null || fail "curl is required"

case ${RELEASE_IMAGE_PULL:-true} in
    true)
        docker pull "$edge_image"
        docker pull "$versiond_image"
        docker pull "$edge_router_image"
        docker pull "$versiond_router_image"
        docker pull "$proxy_policy_image"
        docker pull "$proxy_router_image"
        ;;
    false) ;;
    *) fail "RELEASE_IMAGE_PULL must be true or false" ;;
esac

edge_image_id=$(docker image inspect "$edge_image" --format '{{.Id}}')
versiond_image_id=$(docker image inspect "$versiond_image" --format '{{.Id}}')
edge_router_image_id=$(docker image inspect "$edge_router_image" --format '{{.Id}}')
versiond_router_image_id=$(docker image inspect "$versiond_router_image" --format '{{.Id}}')
proxy_router_image_id=$(docker image inspect "$proxy_router_image" --format '{{.Id}}')

# Router images are release artifacts too. Render-only mode executes their real
# entrypoint and HAProxy config validation without requiring live upstreams.
docker run --rm -e EDGE_API_ROUTER_RENDER_ONLY=true \
    "$edge_router_image_id"
docker run --rm -e VERSIOND_ROUTER_RENDER_ONLY=true \
    "$versiond_router_image_id"
docker run --rm -e PROXY_ROUTER_RENDER_ONLY=true \
    -e VERSIOND_LEGACY_HOST=versiond \
    -e VERSIOND_NON_HA_VERSIONS='v1 v2 v3' \
    -e VERSIOND_VERSIONS='v4 v5' \
    "$proxy_router_image_id"

suffix=$$
edge_container="gonka-release-edge-$suffix"
versiond_container="gonka-release-versiond-$suffix"

cleanup() {
    docker rm -f "$edge_container" "$versiond_container" >/dev/null 2>&1 || true
}
trap cleanup EXIT

container_logs() {
    docker logs "$edge_container" >&2 2>/dev/null || true
    docker logs "$versiond_container" >&2 2>/dev/null || true
}

wait_for_status() {
    local url=$1
    local expected=$2
    local description=$3
    local status=
    local _
    for _ in {1..60}; do
        status=$(curl -sS -o /dev/null -w '%{http_code}' \
            --max-time 5 "$url" 2>/dev/null || true)
        if [[ $status == "$expected" ]]; then
            return 0
        fi
        sleep 0.25
    done
    container_logs
    fail "$description returned ${status:-no response}, expected $expected"
}

docker run -d --name "$edge_container" \
    -p 127.0.0.1::18080 \
    -e EDGE_API_PORT=18080 \
    -e CHAIN_GRPC_URL=127.0.0.1:1 \
    -e CHAIN_RPC_URL=http://127.0.0.1:1 \
    -e EDGE_API_DRAIN_ANNOUNCE=0 \
    "$edge_image_id" >/dev/null
edge_addr=$(docker port "$edge_container" 18080/tcp | tail -n1)
[[ -n $edge_addr ]] || fail "edge-api did not publish port 18080"
wait_for_status "http://$edge_addr/healthz" 200 "edge-api /healthz"
wait_for_status "http://$edge_addr/readyz" 503 "edge-api /readyz"

docker run -d --name "$versiond_container" \
    -p 127.0.0.1::8080 \
    -e VERSIOND_ORACLE_URL=http://127.0.0.1:1/versions \
    -e VERSIOND_DRAIN_ANNOUNCE=0 \
    "$versiond_image_id" >/dev/null
versiond_addr=$(docker port "$versiond_container" 8080/tcp | tail -n1)
[[ -n $versiond_addr ]] || fail "versiond did not publish port 8080"
wait_for_status "http://$versiond_addr/healthz" 200 "versiond /healthz"
wait_for_status "http://$versiond_addr/readyz" 503 "versiond /readyz"

echo "release-image-smoke_test: release images ok"
