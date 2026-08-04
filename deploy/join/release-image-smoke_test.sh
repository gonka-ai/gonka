#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
base="$script_dir/docker-compose.yml"
versiond_overlay="$script_dir/docker-compose.versiond.yml"
edge_overlay="$script_dir/docker-compose.edge-api-multi.yml"

release_tag=0.2.15-devshard-v5
edge_image="ghcr.io/product-science/edge-api:$release_tag"
versiond_image="ghcr.io/product-science/versiond:$release_tag"
edge_router_image="ghcr.io/product-science/edge-api-router:$release_tag"
versiond_router_image="ghcr.io/product-science/versiond-router:$release_tag"

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

assert_image() {
    local description=$1
    local expected=$2
    local actual=$3
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
    assert_image "versiond HA overlay" "$versiond_image" \
        "$(compose_image versiond -f "$base" -f "$versiond_overlay")"
    assert_image "edge-api router" "$edge_router_image" \
        "$(compose_image edge-api-router -f "$base" -f "$edge_overlay")"
    assert_image "versiond router" "$versiond_router_image" \
        "$(compose_image versiond-router -f "$base" -f "$versiond_overlay")"
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
        ;;
    false) ;;
    *) fail "RELEASE_IMAGE_PULL must be true or false" ;;
esac

edge_image_id=$(docker image inspect "$edge_image" --format '{{.Id}}')
versiond_image_id=$(docker image inspect "$versiond_image" --format '{{.Id}}')
edge_router_image_id=$(docker image inspect "$edge_router_image" --format '{{.Id}}')
versiond_router_image_id=$(docker image inspect "$versiond_router_image" --format '{{.Id}}')

# Router images are release artifacts too. Render-only mode executes their real
# entrypoint and HAProxy config validation without requiring live upstreams.
docker run --rm -e EDGE_API_ROUTER_RENDER_ONLY=true \
    "$edge_router_image_id"
docker run --rm -e VERSIOND_ROUTER_RENDER_ONLY=true \
    "$versiond_router_image_id"

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
