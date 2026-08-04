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
    if [[ $# -eq 2 ]]; then
        container=${2#cid-}
        case " ${EXISTING_CONTAINERS-} " in
            *" $container "*) exit 0 ;;
            *) exit 1 ;;
        esac
    fi
    case ${3:-} in
        '{{.Image}}') printf 'sha256:old-%s\n' "${4:-unknown}" ;;
        '{{.State.Running}}') printf 'true\n' ;;
        '{{range .Config.Env}}{{println .}}{{end}}')
            case ${4:-} in
                versiond-router)
                    printf 'VERSIOND_HOSTS=versiond versiond2\n'
                    ;;
                edge-api-router)
                    printf 'EDGE_API_HOSTS=edge-api edge-api2 edge-api3\n'
                    ;;
                *) exit 1 ;;
            esac
            ;;
        *) exit 1 ;;
    esac
    exit 0
fi

if [[ ${1:-} == exec ]]; then
    container=${2:-}
    for arg in "$@"; do
        [[ $arg == nginx ]] && exit 0
        [[ $arg == haproxy ]] && exit 1
        case $arg in
            http://*) probe_url=$arg ;;
        esac
    done
    if [[ -n ${probe_url:-} ]]; then
        [[ $container != "cid-${ROLLBACK_PROBE_FAIL_SERVICE-}" ]] || exit 1
        case $container:$probe_url in
            cid-versiond*:http://127.0.0.1:8080/healthz)
                if [[ $container == "cid-${ROLLBACK_EMPTY_VERSIOND_SERVICE-}" ]]; then
                    printf '[]\n'
                else
                    printf '[{"name":"v4","port":5000,"status":"running"}]\n'
                fi
                exit 0
                ;;
            cid-versiond*:http://127.0.0.1:8080/v4/healthz | \
                cid-edge-api*:http://127.0.0.1:18080/v1/versions)
                exit 0
                ;;
            *) exit 1 ;;
        esac
    fi
    exit 0
fi

if [[ ${1:-} == run ]]; then
    printf 'source 1 0\n'
    exit 0
fi

if [[ ${1:-} == cp || ${1:-} == tag || \
    (${1:-} == image && ${2:-} == rm) ]]; then
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
if [[ $is_up == true && $service == "${BLOCK_SERVICE-}" ]]; then
    case $service in
        versiond | versiond2) image=${VERSIOND_IMAGE-} ;;
        versiond-router) image=${VERSIOND_ROUTER_IMAGE-} ;;
        edge-api | edge-api2 | edge-api3) image=${EDGE_API_IMAGE-} ;;
        edge-api-router) image=${EDGE_API_ROUTER_IMAGE-} ;;
        *) image= ;;
    esac
    if [[ $image != gonka-upgrade-rollback/* ]]; then
        trap 'exit 143' TERM HUP
        trap 'exit 130' INT
        if [[ ${BLOCK_SIGNAL-} != none ]]; then
            kill -"$BLOCK_SIGNAL" "$PPID"
        fi
        while :; do
            sleep 1
        done
    fi
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
        BLOCK_SERVICE=none \
        BLOCK_SIGNAL=none \
        EXISTING_CONTAINERS="versiond versiond2 versiond-router devshard-postgres edge-api edge-api2 edge-api3 edge-api-router" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        ROLLBACK_EMPTY_VERSIOND_SERVICE="${ROLLBACK_EMPTY_VERSIOND_SERVICE-}" \
        ROLLBACK_PROBE_FAIL_SERVICE="${ROLLBACK_PROBE_FAIL_SERVICE-}" \
        UPGRADE_ROLLBACK_VERIFY_TIMEOUT="${UPGRADE_ROLLBACK_VERIFY_TIMEOUT:-5}" \
        UPGRADE_ROLLBACK_VERIFY_INTERVAL="${UPGRADE_ROLLBACK_VERIFY_INTERVAL:-1}" \
        UPGRADE_ROLLBACK_STABILITY_CHECKS="${UPGRADE_ROLLBACK_STABILITY_CHECKS:-1}" \
        UPGRADE_ROUTER_RELOAD_SETTLE=0 \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode "$mode" \
        >"$tmpdir/stdout" 2>"$tmpdir/stderr"; then
        fail "$mode upgrade unexpectedly succeeded when $failed_service failed"
    fi
}

run_auto_upgrade() {
    local containers=$1
    local log=$2
    local stdout=$3

    : >"$log"
    if ! DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        FAIL_SERVICE=none \
        BLOCK_SERVICE=none \
        BLOCK_SIGNAL=none \
        EXISTING_CONTAINERS="$containers" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        UPGRADE_ROUTER_RELOAD_SETTLE=0 \
        "$script_dir/upgrade-devshard-v5.sh" >"$stdout" 2>"$tmpdir/stderr"; then
        cat "$tmpdir/stderr" >&2
        fail "automatic topology upgrade failed"
    fi
}

run_interrupted_upgrade() {
    local signal=$1
    local log=$2
    local stdout=$3
    local stderr=$4

    : >"$log"
    if DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        FAIL_SERVICE=none \
        BLOCK_SERVICE=versiond2 \
        BLOCK_SIGNAL="$signal" \
        EXISTING_CONTAINERS="versiond versiond2 versiond-router devshard-postgres edge-api" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        ROLLBACK_EMPTY_VERSIOND_SERVICE='' \
        ROLLBACK_PROBE_FAIL_SERVICE='' \
        UPGRADE_ROLLBACK_VERIFY_TIMEOUT=5 \
        UPGRADE_ROLLBACK_VERIFY_INTERVAL=1 \
        UPGRADE_ROLLBACK_STABILITY_CHECKS=1 \
        UPGRADE_ROUTER_RELOAD_SETTLE=0 \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single \
        >"$stdout" 2>"$stderr"; then
        fail "upgrade interrupted by $signal exited successfully"
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

line_number_regex() {
    local file=$1
    local pattern=$2

    grep -nE -- "$pattern" "$file" | head -n1 | cut -d: -f1
}

write_fake_docker
printf 'export DEVSHARD_POSTGRES_DATA_DIR=%q\n' "$tmpdir/postgres" \
    >"$tmpdir/config.env"

if DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/unknown.log" \
    FAIL_SERVICE=none \
    EXISTING_CONTAINERS='' \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    >"$tmpdir/unknown.stdout" 2>"$tmpdir/unknown.stderr"; then
    fail "upgrade unexpectedly accepted an unknown deployment topology"
fi
grep -q 'cannot detect versiond topology' "$tmpdir/unknown.stderr" || {
    cat "$tmpdir/unknown.stderr" >&2
    fail "unknown topology did not produce a useful error"
}

run_auto_upgrade "versiond edge-api" "$tmpdir/base.log" "$tmpdir/base.stdout"
grep -q 'versiond=single, edge-api=single' "$tmpdir/base.stdout" || fail \
    "base-only topology was not detected"
assert_not_contains "$tmpdir/base.log" "docker-compose.versiond.yml"
assert_not_contains "$tmpdir/base.log" "docker-compose.edge-api-multi.yml"
assert_not_contains "$tmpdir/base.log" " pull devshard-postgres"
assert_not_contains "$tmpdir/base.log" \
    "--wait-timeout 2100 devshard-postgres"
if ! grep -E -- '--wait-timeout 2100 versiond$' "$tmpdir/base.log" >/dev/null; then
    cat "$tmpdir/base.log" >&2
    fail "base-only versiond was not replaced"
fi
if ! grep -E -- '--wait-timeout 180 edge-api$' "$tmpdir/base.log" >/dev/null; then
    cat "$tmpdir/base.log" >&2
    fail "base-only edge-api was not replaced"
fi

run_auto_upgrade \
    "versiond edge-api edge-api2 edge-api3 edge-api-router" \
    "$tmpdir/mixed.log" "$tmpdir/mixed.stdout"
grep -q 'versiond=single, edge-api=multi' "$tmpdir/mixed.stdout" || fail \
    "independent versiond and edge-api axes were not detected"
assert_not_contains "$tmpdir/mixed.log" "docker-compose.versiond.yml"
assert_contains "$tmpdir/mixed.log" "docker-compose.edge-api-multi.yml"
assert_not_contains "$tmpdir/mixed.log" \
    "--wait-timeout 2100 devshard-postgres"

run_auto_upgrade \
    "versiond versiond2 versiond-router devshard-postgres edge-api" \
    "$tmpdir/ha.log" "$tmpdir/ha.stdout"
grep -q 'versiond=ha, edge-api=single' "$tmpdir/ha.stdout" || fail \
    "HA versiond topology was not detected"
assert_contains "$tmpdir/ha.log" "docker-compose.versiond.yml"
assert_not_contains "$tmpdir/ha.log" "docker-compose.edge-api-multi.yml"
assert_contains "$tmpdir/ha.log" "--wait-timeout 2100 devshard-postgres"
versiond_barrier_line=$(line_number "$tmpdir/ha.log" \
    "--env VERSIOND_HOSTS=versiond versiond-router")
versiond2_up_line=$(line_number "$tmpdir/ha.log" \
    "--wait-timeout 2100 versiond2")
versiond_barrier_hook_line=$(line_number "$tmpdir/ha.log" \
    "legacy-router-upgrade-barrier.sh versiond-router:/tmp/99-gonka-upgrade-barrier.sh")
versiond_router_up_line=$(line_number "$tmpdir/ha.log" \
    "--wait-timeout 60 versiond-router")
versiond_up_line=$(line_number_regex "$tmpdir/ha.log" \
    '--wait-timeout 2100 versiond$')
[[ -n $versiond_barrier_line && -n $versiond_barrier_hook_line && \
    -n $versiond2_up_line && \
    -n $versiond_router_up_line && -n $versiond_up_line && \
    $versiond_barrier_line -lt $versiond2_up_line && \
    $versiond_barrier_hook_line -lt $versiond2_up_line && \
    $versiond2_up_line -lt $versiond_router_up_line && \
    $versiond_router_up_line -lt $versiond_up_line ]] ||
    fail "versiond traffic barrier or HAProxy cutover order is wrong"

run_upgrade single devshard-postgres "$tmpdir/postgres-failure.log"
assert_contains "$tmpdir/postgres-failure.log" " stop devshard-postgres"
assert_not_contains "$tmpdir/postgres-failure.log" \
    "gonka-upgrade-rollback/devshard-postgres:"
grep -q 'source volume and persistent target are preserved' \
    "$tmpdir/stderr" || {
    cat "$tmpdir/stderr" >&2
    fail "PostgreSQL failure did not explain its preservation contract"
}

UPGRADE_ROLLBACK_STABILITY_CHECKS=3 \
    run_upgrade single versiond2 "$tmpdir/versiond2.log"
preflight_line=$(line_number "$tmpdir/versiond2.log" \
    "--volumes-from cid-devshard-postgres:ro")
postgres_up_line=$(line_number "$tmpdir/versiond2.log" \
    "--wait-timeout 2100 devshard-postgres")
[[ -n $preflight_line && -n $postgres_up_line && \
    $preflight_line -lt $postgres_up_line ]] ||
    fail "PostgreSQL space preflight did not run before its first recreate"
assert_contains "$tmpdir/versiond2.log" \
    "VERSIOND_IMAGE=gonka-upgrade-rollback/versiond2:"
rollback_probe_count=$(grep -Fc \
    'http://127.0.0.1:8080/v4/healthz' "$tmpdir/versiond2.log")
[[ $rollback_probe_count -eq 3 ]] || fail \
    "rollback was not held through the configured stability window"
assert_not_contains "$tmpdir/versiond2.log" \
    "VERSIOND_HOSTS=versiond\\ versiond2"
if grep -E -- '--wait-timeout 2100 versiond$' "$tmpdir/versiond2.log" >/dev/null; then
    cat "$tmpdir/versiond2.log" >&2
    fail "versiond was replaced after versiond2 failed"
fi

ROLLBACK_PROBE_FAIL_SERVICE=versiond2 \
UPGRADE_ROLLBACK_VERIFY_TIMEOUT=1 \
    run_upgrade single versiond2 "$tmpdir/versiond2-unavailable.log"
assert_contains "$tmpdir/versiond2-unavailable.log" " stop versiond2"
grep -q 'did not become stably available' "$tmpdir/stderr" || {
    cat "$tmpdir/stderr" >&2
    fail "unavailable rollback did not report the failed verification"
}

ROLLBACK_EMPTY_VERSIOND_SERVICE=versiond2 \
UPGRADE_ROLLBACK_VERIFY_TIMEOUT=1 \
    run_upgrade single versiond2 "$tmpdir/versiond2-empty.log"
assert_contains "$tmpdir/versiond2-empty.log" " stop versiond2"

run_upgrade single versiond-router "$tmpdir/versiond-router.log"
assert_contains "$tmpdir/versiond-router.log" \
    "exec versiond-router rm -f /etc/gonka-upgrade-barrier"
assert_contains "$tmpdir/versiond-router.log" \
    "VERSIOND_HOSTS=versiond\\ versiond2"

for signal in HUP INT TERM; do
    signal_name=${signal,,}
    run_interrupted_upgrade \
        "$signal" \
        "$tmpdir/interrupted-$signal_name.log" \
        "$tmpdir/interrupted-$signal_name.stdout" \
        "$tmpdir/interrupted-$signal_name.stderr"
    assert_contains "$tmpdir/interrupted-$signal_name.log" \
        "VERSIOND_IMAGE=gonka-upgrade-rollback/versiond2:"
    assert_not_contains "$tmpdir/interrupted-$signal_name.log" \
        "VERSIOND_HOSTS=versiond\\ versiond2"
    grep -q "received $signal" \
        "$tmpdir/interrupted-$signal_name.stderr" || {
        cat "$tmpdir/interrupted-$signal_name.stderr" >&2
        fail "interrupted upgrade did not report $signal"
    }
done

run_upgrade multi edge-api3 "$tmpdir/edge-api3.log"
edge_barrier_line=$(line_number "$tmpdir/edge-api3.log" \
    "EDGE_API_HOSTS=edge-api\\ edge-api3")
edge_api2_line=$(line_number "$tmpdir/edge-api3.log" \
    "--wait-timeout 180 edge-api2")
router_line=$(line_number "$tmpdir/edge-api3.log" \
    "--wait-timeout 60 edge-api-router")
replica_line=$(line_number "$tmpdir/edge-api3.log" \
    "--wait-timeout 180 edge-api3")
[[ -n $edge_barrier_line && -n $edge_api2_line && -n $router_line && \
    -n $replica_line && $edge_barrier_line -lt $edge_api2_line && \
    $edge_api2_line -lt $router_line && $router_line -lt $replica_line ]] ||
    fail "edge-api traffic barrier or HAProxy cutover order is wrong"
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
assert_contains "$tmpdir/edge-router.log" \
    "http://127.0.0.1:18080/v1/versions"
if grep -F -- '--wait-timeout 180 edge-api3' "$tmpdir/edge-router.log" >/dev/null; then
    cat "$tmpdir/edge-router.log" >&2
    fail "edge-api3 was replaced after the HAProxy cutover failed"
fi

run_upgrade single edge-api "$tmpdir/edge-api.log"
assert_contains "$tmpdir/edge-api.log" \
    "EDGE_API_IMAGE=gonka-upgrade-rollback/edge-api:"
assert_contains "$tmpdir/edge-api.log" \
    "http://127.0.0.1:18080/v1/versions"

ROLLBACK_PROBE_FAIL_SERVICE=edge-api \
UPGRADE_ROLLBACK_VERIFY_TIMEOUT=1 \
    run_upgrade single edge-api "$tmpdir/edge-api-unavailable.log"
assert_contains "$tmpdir/edge-api-unavailable.log" " stop edge-api"

echo "upgrade-devshard-v5_test: ok"
