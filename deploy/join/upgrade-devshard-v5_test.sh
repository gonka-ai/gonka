#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
    echo "upgrade-devshard-v5_test: $*" >&2
    exit 1
}

write_fake_docker() {
    cat >"$tmpdir/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail

printf 'EDGE_API_IMAGE=%s VERSIOND_IMAGE=%s EDGE_API_ROUTER_IMAGE=%s VERSIOND_ROUTER_IMAGE=%s' \
    "${EDGE_API_IMAGE-}" "${VERSIOND_IMAGE-}" \
    "${EDGE_API_ROUTER_IMAGE-}" "${VERSIOND_ROUTER_IMAGE-}" >>"$DOCKER_LOG"
printf ' ::' >>"$DOCKER_LOG"
printf ' %q' "$@" >>"$DOCKER_LOG"
printf '\n' >>"$DOCKER_LOG"

if [[ ${1:-} == inspect ]]; then
    case ${3:-} in
        '{{.Image}}') printf 'sha256:old-%s\n' "${4:-unknown}" ;;
        '{{.State.Running}}') printf 'true\n' ;;
        *) exit 1 ;;
    esac
    exit 0
fi

if [[ ${1:-} == tag || (${1:-} == image && ${2:-} == rm) ]]; then
    exit 0
fi

[[ ${1:-} == compose ]] || exit 1

service=${!#}
for arg in "$@"; do
    if [[ $arg == ps ]]; then
        printf 'cid-%s\n' "$service"
        exit 0
    fi
done

for arg in "$@"; do
    if [[ $arg == stop || $arg == pull ]]; then
        exit 0
    fi
done

is_up=false
for arg in "$@"; do
    [[ $arg == up ]] && is_up=true
done
if [[ $is_up == true && $service == "${FAIL_SERVICE-}" ]]; then
    case $service in
        versiond | versiond2) image=${VERSIOND_IMAGE-} ;;
        versiond-router) image=${VERSIOND_ROUTER_IMAGE-} ;;
        edge-api | edge-api2 | edge-api3) image=${EDGE_API_IMAGE-} ;;
        edge-api-router) image=${EDGE_API_ROUTER_IMAGE-} ;;
        *) image= ;;
    esac
    [[ $image == gonka-upgrade-rollback/* ]] && exit 0
    exit 1
fi
exit 0
EOF
    chmod +x "$tmpdir/docker"
}

run_upgrade() {
    local mode=$1
    local failed_service=$2
    local log=$3

    : >"$log"
    if DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        FAIL_SERVICE="$failed_service" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        "$script_dir/upgrade-devshard-v5.sh" --edge-mode "$mode" \
        >"$tmpdir/stdout" 2>"$tmpdir/stderr"; then
        fail "$mode upgrade unexpectedly succeeded when $failed_service failed"
    fi
}

assert_contains() {
    local file=$1
    local pattern=$2

    grep -F -- "$pattern" "$file" >/dev/null || {
        cat "$file" >&2
        fail "missing expected command: $pattern"
    }
}

assert_not_contains() {
    local file=$1
    local pattern=$2

    if grep -F -- "$pattern" "$file" >/dev/null; then
        cat "$file" >&2
        fail "unexpected command: $pattern"
    fi
}

line_number() {
    local file=$1
    local pattern=$2

    grep -nF -- "$pattern" "$file" | head -n1 | cut -d: -f1
}

write_fake_docker
: >"$tmpdir/config.env"

run_upgrade single versiond2 "$tmpdir/versiond2.log"
assert_contains "$tmpdir/versiond2.log" \
    "VERSIOND_IMAGE=gonka-upgrade-rollback/versiond2:"
if grep -E -- '--wait-timeout 2100 versiond$' "$tmpdir/versiond2.log" >/dev/null; then
    cat "$tmpdir/versiond2.log" >&2
    fail "versiond was replaced after versiond2 failed"
fi

run_upgrade multi edge-api3 "$tmpdir/edge-api3.log"
router_line=$(line_number "$tmpdir/edge-api3.log" \
    "--wait-timeout 60 edge-api-router")
replica_line=$(line_number "$tmpdir/edge-api3.log" \
    "--wait-timeout 180 edge-api3")
[[ -n $router_line && -n $replica_line && $router_line -lt $replica_line ]] ||
    fail "edge-api-router was not replaced before edge-api3"
assert_contains "$tmpdir/edge-api3.log" " stop edge-api3"
assert_not_contains "$tmpdir/edge-api3.log" \
    "EDGE_API_IMAGE=gonka-upgrade-rollback/edge-api3:"
if grep -E -- '--wait-timeout 180 edge-api$' "$tmpdir/edge-api3.log" >/dev/null; then
    cat "$tmpdir/edge-api3.log" >&2
    fail "edge-api was replaced after edge-api3 failed"
fi

run_upgrade multi edge-api-router "$tmpdir/edge-router.log"
assert_contains "$tmpdir/edge-router.log" \
    "EDGE_API_ROUTER_IMAGE=gonka-upgrade-rollback/edge-api-router:"
if grep -F -- '--wait-timeout 180 edge-api3' "$tmpdir/edge-router.log" >/dev/null; then
    cat "$tmpdir/edge-router.log" >&2
    fail "edge-api3 was replaced after the HAProxy cutover failed"
fi

run_upgrade single edge-api "$tmpdir/edge-api.log"
assert_contains "$tmpdir/edge-api.log" \
    "EDGE_API_IMAGE=gonka-upgrade-rollback/edge-api:"

echo "upgrade-devshard-v5_test: ok"
