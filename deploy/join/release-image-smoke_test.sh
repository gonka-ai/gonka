#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
base="$script_dir/docker-compose.yml"
versiond_overlay="$script_dir/docker-compose.versiond.yml"
edge_overlay="$script_dir/docker-compose.edge-api-multi.yml"
slot_compose="$script_dir/versiond-router-slot/docker-compose.yml"
versiond_compat="$script_dir/docker-compose.versiond-v5-compat.yml"
proxy_compat="$script_dir/docker-compose.proxy-v4-compat.yml"

release_contract=$script_dir/devshard-v5-release.env
# shellcheck disable=SC1090 # Runtime path is anchored to this script.
source "$release_contract"
versiond_image=$DEVSHARD_V5_VERSIOND_IMAGE
versiond_router_image=$DEVSHARD_V5_VERSIOND_ROUTER_IMAGE
proxy_policy_image=$DEVSHARD_V5_PROXY_POLICY_IMAGE
proxy_router_image=$DEVSHARD_V5_PROXY_ROUTER_IMAGE
postgres_image=$DEVSHARD_V5_POSTGRES_IMAGE

# This test validates the shipped defaults, not an operator's local override.
unset VERSIOND_IMAGE VERSIOND_ROUTER_IMAGE
unset PROXY_POLICY_IMAGE PROXY_ROUTER_IMAGE DEVSHARD_POSTGRES_IMAGE
export VERSIOND_ROUTER_METRICS_NETWORK=join_default

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

assert_environment_absent() {
	local description=$1 service=$2 key=$3
	shift 3

	DEVSHARD_POSTGRES_PASSWORD=compose-contract \
		docker compose "$@" config --format json 2>/dev/null |
		jq -e --arg service "$service" --arg key "$key" \
			'.services[$service].environment | has($key) | not' \
			>/dev/null || fail "$description unexpectedly overrides $key"
}

assert_hardened() {
    local description=$1 service=$2
    shift 2

    DEVSHARD_POSTGRES_PASSWORD=compose-contract \
        VERSIOND_ROUTER_SLOT=${VERSIOND_ROUTER_SLOT:-0} \
        docker compose "$@" config --format json 2>/dev/null | \
        jq -e --arg service "$service" '
            (.services[$service].cap_drop | index("ALL") != null) and
            (.services[$service].security_opt | index("no-new-privileges:true") != null)
        ' >/dev/null || fail "$description lacks container capability hardening"
}

check_compose_contract() {
    assert_image "base versiond" "$versiond_image" \
        "$(compose_image versiond -f "$base")"
    assert_healthcheck_url "base versiond healthcheck" versiond \
        "http://127.0.0.1:8080/readyz" -f "$base"
    assert_image "versiond HA overlay" "$versiond_image" \
        "$(compose_image versiond -f "$base" -f "$versiond_overlay")"
    assert_image "shared PostgreSQL" "$postgres_image" \
        "$(compose_image devshard-postgres -f "$base" -f "$versiond_overlay")"
    assert_image "private proxy policy" "$proxy_policy_image" \
        "$(compose_image proxy-policy -f "$base")"
    assert_image "private proxy policy reserve" "$proxy_policy_image" \
        "$(compose_image proxy-policy2 -f "$base")"
    assert_image "public proxy router" "$proxy_router_image" \
        "$(compose_image proxy -f "$base")"
    assert_healthcheck_url "public proxy readiness" proxy \
        "http://127.0.0.1:8404/readyz" -f "$base"
    assert_hardened "public proxy router" proxy -f "$base"
    assert_environment "public proxy router fleet" proxy \
        VERSIOND_ROUTER_POOL_HOST versiond-router-fleet \
        -f "$base" -f "$versiond_overlay"
    assert_healthcheck_url "private policy healthcheck" proxy-policy \
        "http://127.0.0.1:8081/health" -f "$base"
    assert_healthcheck_url "private policy reserve healthcheck" proxy-policy2 \
        "http://127.0.0.1:8081/health" -f "$base"
    assert_service_absent "steady-state versiond model" versiond-router \
        -f "$base" -f "$versiond_overlay"
    assert_image "versiond router slot" "$versiond_router_image" \
        "$(VERSIOND_ROUTER_SLOT=0 compose_image router -f "$slot_compose")"
    VERSIOND_ROUTER_SLOT=0 assert_healthcheck_url \
        "versiond router slot healthcheck" router \
        "http://127.0.0.1:8404/readyz" -f "$slot_compose"
    VERSIOND_ROUTER_SLOT=0 assert_hardened \
        "versiond router slot" router -f "$slot_compose"

    assert_image "versiond migration router" "$versiond_router_image" \
        "$(compose_image versiond-router -f "$base" -f "$versiond_overlay" -f "$versiond_compat")"
    assert_environment "versiond v4 rollback hosts" versiond-router \
        VERSIOND_HOSTS "versiond versiond2" \
        -f "$base" -f "$versiond_overlay" -f "$versiond_compat"
    rollback_proxy=$(PROXY_V4_IMAGE=sha256:captured-proxy \
        PROXY_V4_VERSIOND_SERVICE_NAME=versiond-router \
        PROXY_V4_EDGE_API_SERVICE_NAME=edge-api-router \
        compose_image proxy -f "$base" -f "$versiond_overlay" \
            -f "$edge_overlay" -f "$proxy_compat")
    assert_image "public nginx rollback" sha256:captured-proxy "$rollback_proxy"
	PROXY_V4_IMAGE=sha256:captured-proxy \
		PROXY_V4_VERSIOND_SERVICE_NAME=versiond-router \
		PROXY_V4_EDGE_API_SERVICE_NAME=edge-api-router \
		assert_environment_absent "public nginx rollback protocol" proxy \
			PROXY_PROTOCOL -f "$base" -f "$versiond_overlay" \
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
        docker pull "$versiond_image"
        docker pull "$versiond_router_image"
        docker pull "$proxy_policy_image"
        docker pull "$proxy_router_image"
        docker pull "$postgres_image"
        ;;
    false) ;;
    *) fail "RELEASE_IMAGE_PULL must be true or false" ;;
esac

versiond_image_id=$(docker image inspect "$versiond_image" --format '{{.Id}}')
versiond_router_image_id=$(docker image inspect "$versiond_router_image" --format '{{.Id}}')
proxy_policy_image_id=$(docker image inspect "$proxy_policy_image" --format '{{.Id}}')
proxy_router_image_id=$(docker image inspect "$proxy_router_image" --format '{{.Id}}')
postgres_image_id=$(docker image inspect "$postgres_image" --format '{{.Id}}')

versiond_cache_protocol=$(docker image inspect "$versiond_router_image_id" \
    --format '{{index .Config.Labels "ai.gonka.catalog-cache-protocol-version"}}')
proxy_cache_protocol=$(docker image inspect "$proxy_router_image_id" \
    --format '{{index .Config.Labels "ai.gonka.catalog-cache-protocol-version"}}')
[[ $versiond_cache_protocol == 2 && $proxy_cache_protocol == 2 ]] || fail \
    "router images must publish catalog cache protocol 2 (versiond=${versiond_cache_protocol:-missing}, proxy=${proxy_cache_protocol:-missing})"

docker run --rm "$postgres_image_id" postgres --version >/dev/null

# Router images are release artifacts too. Render-only mode executes their real
# entrypoint and HAProxy config validation without requiring live upstreams.
docker run --rm -e VERSIOND_ROUTER_RENDER_ONLY=true \
    "$versiond_router_image_id"
docker run --rm -e PROXY_ROUTER_RENDER_ONLY=true \
    -e VERSIOND_LEGACY_HOST=versiond \
    -e VERSIOND_NON_HA_VERSIONS='v1 v2 v3' \
    -e VERSIOND_VERSIONS='v4 v5' \
    "$proxy_router_image_id"
# The private policy image has no render-only shortcut. Run its real entrypoint
# with explicit loopback PROXY trust, then execute nginx -t as its final command.
# This catches an image that pulls successfully but cannot boot the shipped
# deployment contract.
docker run --rm \
    -e NGINX_MODE=http \
    -e PROXY_PROTOCOL=true \
    -e PROXY_PROTOCOL_BIND_ADDRESS=127.0.0.1 \
    -e PROXY_PROTOCOL_TRUSTED_FROM=127.0.0.1/32 \
    -e VERSIOND_SERVICE_NAME=proxy \
    -e VERSIOND_SERVICE_IS_ABSOLUTE=true \
    -e VERSIOND_PORT=18081 \
    -e EDGE_API_SERVICE_NAME=proxy \
    -e EDGE_API_SERVICE_IS_ABSOLUTE=true \
    -e EDGE_API_PORT=18082 \
    "$proxy_policy_image_id" nginx -t

suffix=$$
versiond_container="gonka-release-versiond-$suffix"

cleanup() {
    docker rm -f "$versiond_container" >/dev/null 2>&1 || true
}
trap cleanup EXIT

container_logs() {
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
