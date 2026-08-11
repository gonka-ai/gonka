#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

# Unit tests exercise a working-tree build before the release tag exists.
export DEVSHARD_V5_ALLOW_UNRELEASED_SOURCE=true
export DEVSHARD_V5_MAINTENANCE_ACK=true
export DEVSHARD_V5_PREFLIGHT_CPUS=16
export DEVSHARD_V5_PREFLIGHT_MEMORY_MIB=65536

fail() {
    echo "upgrade-devshard-v5_test: $*" >&2
    exit 1
}

write_fake_docker() {
    cat >"$tmpdir/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail

printf 'EDGE_API_IMAGE=%s VERSIOND_IMAGE=%s EDGE_API_ROUTER_IMAGE=%s VERSIOND_ROUTER_IMAGE=%s PROXY_POLICY_IMAGE=%s PROXY_ROUTER_IMAGE=%s' \
    "${EDGE_API_IMAGE-}" "${VERSIOND_IMAGE-}" \
    "${EDGE_API_ROUTER_IMAGE-}" "${VERSIOND_ROUTER_IMAGE-}" \
    "${PROXY_POLICY_IMAGE-}" "${PROXY_ROUTER_IMAGE-}" >>"$DOCKER_LOG"
printf ' ::' >>"$DOCKER_LOG"
printf ' %q' "$@" >>"$DOCKER_LOG"
printf '\n' >>"$DOCKER_LOG"

if [[ ${1:-} == info ]]; then
    exit 0
fi
if [[ ${1:-} == compose && ${2:-} == version && ${3:-} == --short ]]; then
    printf '%s\n' "${FAKE_COMPOSE_VERSION:-2.20.0}"
    exit 0
fi

if [[ ${1:-} == inspect ]]; then
    if [[ $# -eq 2 ]]; then
        container=${2#cid-}
        case " ${EXISTING_CONTAINERS-} " in
            *" $container "*) exit 0 ;;
            *) exit 1 ;;
        esac
    fi
    case ${3:-} in
        '{{json .Config.Labels}}')
            container=${4:-unknown}
            files=${RUNTIME_COMPOSE_FILES-}
            if [[ -z $files ]]; then
                files=$JOIN_DIR/docker-compose.yml
                case " ${EXISTING_CONTAINERS-} " in
                    *' versiond2 '* | *' devshard-postgres '*)
                        files+=",$JOIN_DIR/docker-compose.versiond.yml"
                        ;;
                esac
                case " ${EXISTING_CONTAINERS-} " in
                    *' edge-api2 '* | *' edge-api3 '*)
                        files+=",$JOIN_DIR/docker-compose.edge-api-multi.yml"
                        ;;
                esac
            fi
            if [[ -n ${INCOMPATIBLE_COMPOSE_CONTAINER-} && \
                $container == "$INCOMPATIBLE_COMPOSE_CONTAINER" ]]; then
                files=$JOIN_DIR/docker-compose.observability.yml
            fi
            jq -cn --arg files "$files" \
                --arg workdir "${RUNTIME_COMPOSE_WORKDIR:-$JOIN_DIR}" \
                '{"com.docker.compose.project":"gonka-test",
                  "com.docker.compose.project.config_files":$files,
                  "com.docker.compose.project.working_dir":$workdir}'
            ;;
        '{{index .Config.Labels "ai.gonka.component"}}')
            printf '%s\n' "${EXISTING_PROXY_COMPONENT-}"
            ;;
        '{{json .HostConfig.PortBindings}}')
            jq -cn \
                --arg http "${FAKE_PROXY_HTTP_PORT:-8000}" \
                --arg https "${FAKE_PROXY_HTTPS_PORT:-8443}" \
                '{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":$http}],
                  "443/tcp":[{"HostIp":"0.0.0.0","HostPort":$https}]}'
            ;;
        '{{.Image}}') printf 'sha256:old-%s\n' "${4:-unknown}" ;;
        '{{.Config.Image}}|{{.State.Running}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}')
            service=${4#cid-}
            image=old-$service
            if [[ ${ASSUME_RELEASE_STATE-} == true ]]; then
                case $service in
                    versiond | versiond2) image=$VERSIOND_IMAGE ;;
                    edge-api | edge-api2 | edge-api3) image=$EDGE_API_IMAGE ;;
                    proxy) image=$PROXY_ROUTER_IMAGE ;;
                    proxy-policy) image=$PROXY_POLICY_IMAGE ;;
                    devshard-postgres) image=postgres:16-alpine ;;
                esac
            fi
            printf '%s|true|healthy\n' "$image"
            ;;
        '{{.State.Running}}')
            service=${4#cid-}
            if [[ $service == "${STOPPED_VERSIOND_SERVICE-}" && \
                ! -f $FAKE_STATE_DIR/running-$service ]] || \
                [[ -f $FAKE_STATE_DIR/stopped-$service ]]; then
                printf 'false\n'
            else
                printf 'true\n'
            fi
            ;;
        '{{range .Config.Env}}{{println .}}{{end}}')
            case ${4:-} in
                versiond | versiond2)
                    printf 'PGHOST=%s\nPGDATABASE=devshardd\nPGUSER=devshardd\n' \
                        "${RUNTIME_PGHOST:-devshard-postgres}"
                    ;;
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
    service=${container#cid-}
    barrier_install=false
    barrier_remove=false
    for arg in "$@"; do
        case $arg in
            nginx)
                [[ ! -f $FAKE_STATE_DIR/haproxy-$service ]]
                exit
                ;;
            haproxy)
                [[ -f $FAKE_STATE_DIR/haproxy-$service ]]
                exit
                ;;
            GONKA_BARRIER_HOSTS=versiond | VERSIOND_HOSTS=versiond)
                barrier_install=true
                ;;
            /etc/gonka-upgrade-barrier)
                barrier_remove=true
                ;;
        esac
        case $arg in
            http://*) probe_url=$arg ;;
        esac
    done
    if [[ $barrier_install == true ]]; then
        : >"$FAKE_STATE_DIR/barrier-versiond-router"
    fi
    if [[ $barrier_remove == true ]]; then
        rm -f "$FAKE_STATE_DIR/barrier-versiond-router"
    fi
    if [[ -n ${probe_url:-} ]]; then
        is_rollback=false
        [[ ! -f $FAKE_STATE_DIR/rollback-$service ]] || is_rollback=true
        if [[ $is_rollback == true && \
            $service == "${ROLLBACK_PROBE_FAIL_SERVICE-}" ]]; then
            exit 1
        fi
        if [[ $is_rollback == false && \
            $service == "${INTERRUPT_TARGET_BASELINE_SERVICE-}" && \
            -f $FAKE_STATE_DIR/running-$service && \
            ! -f $FAKE_STATE_DIR/interrupted-target-baseline-$service && \
            $probe_url == http://127.0.0.1:8080/healthz ]]; then
            : >"$FAKE_STATE_DIR/interrupted-target-baseline-$service"
            sleep 1
        fi
        if [[ $is_rollback == true && \
            $service == "${ROLLBACK_MISSING_ROUTE_SERVICE-}" && \
            $probe_url == http://127.0.0.1:8080/v3/healthz ]]; then
            exit 1
        fi
        if [[ $service == versiond-router && \
            -f $FAKE_STATE_DIR/barrier-versiond-router && \
            $probe_url == http://127.0.0.1:8080/v5/healthz ]]; then
            exit 1
        fi
        if [[ $service == versiond-router && \
            -n ${TARGET_ROUTER_MISSING_VERSION-} && \
            $probe_url == "http://127.0.0.1:8080/$TARGET_ROUTER_MISSING_VERSION/healthz" ]]; then
            exit 1
        fi
        if [[ $service == versiond-router && \
            -n ${ROLLBACK_ROUTER_PROBE_FAIL_SERVICE-} && \
            -f $FAKE_STATE_DIR/rollback-$ROLLBACK_ROUTER_PROBE_FAIL_SERVICE ]]; then
            exit 1
        fi
        case $container:$probe_url in
            cid-versiond*:http://127.0.0.1:8080/healthz)
                if [[ $is_rollback == true && \
                    $service == "${ROLLBACK_EMPTY_VERSIOND_SERVICE-}" ]]; then
                    printf '[]\n'
                elif [[ $is_rollback == true && \
                    $service == "${ROLLBACK_MISSING_VERSION_SERVICE-}" ]]; then
                    printf '[{"name":"v3","port":5000,"status":"running"}]\n'
                elif [[ $service == "${SPECIAL_VERSIOND_HEALTH_SERVICE-}" ]]; then
                    printf '[{"name":"v4+hotfix","port":5000,"status":"running"},{"name":"v4}x","port":5001,"status":"running"}]\n'
                else
                    case $service in
                        versiond)
                            printf '[{"name":"v3","port":5000,"status":"running"},{"name":"v4","port":5001,"status":"running"}]\n'
                            ;;
                        versiond2)
                            if [[ ${VERSIOND2_UNIQUE_VERSION-} == true ]]; then
                                printf '[{"name":"v4","port":5001,"status":"running"},{"name":"v5","port":5002,"status":"running"}]\n'
                            else
                                printf '[{"name":"v4","port":5001,"status":"running"}]\n'
                            fi
                            ;;
                        versiond-router)
                            printf '[{"name":"v4","port":5001,"status":"running"}]\n'
                            ;;
                    esac
                fi
                exit 0
                ;;
                cid-versiond*:http://127.0.0.1:8080/v3/healthz | \
                cid-versiond*:http://127.0.0.1:8080/v4/healthz | \
                cid-versiond*:http://127.0.0.1:8080/v5/healthz | \
                cid-versiond*:http://127.0.0.1:8080/v4%2Bhotfix/healthz | \
                cid-versiond*:http://127.0.0.1:8080/v4%7Dx/healthz | \
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

for arg in "$@"; do
    if [[ $arg == config ]]; then
        pg_host=${RENDERED_PGHOST:-devshard-postgres}
        jq -cn --arg pg "$pg_host" \
            --arg pg2db "${RENDERED_PGDATABASE2:-devshardd}" \
            --arg join "$JOIN_DIR" \
            --arg proxy_http "${RENDERED_PROXY_HTTP_PORT:-8000}" \
            --arg proxy_https "${RENDERED_PROXY_HTTPS_PORT:-8443}" \
            --arg policy "${RENDERED_POLICY_NETWORK:-gonka-proxy-policy-front}" \
            --arg front "${RENDERED_ROUTER_FRONT_NETWORK:-gonka-versiond-router-front}" \
            --arg back "${RENDERED_ROUTER_BACK_NETWORK:-gonka-versiond-router-back}" \
            '{name:"gonka-test",networks:{
                default:{name:"gonka-test_default"},
                "proxy-policy-front":{name:$policy},
                "versiond-router-front":{name:$front},
                "versiond-router-back":{name:$back}
            },services:{
                proxy:{container_name:"proxy",ports:[
                    {target:80,published:$proxy_http,protocol:"tcp"},
                    {target:443,published:$proxy_https,protocol:"tcp"}
                ]},
                "proxy-policy":{},
                versiond:{container_name:"versiond",environment:{PGHOST:$pg,PGDATABASE:"devshardd",PGUSER:"devshardd",PGPORT:"5432",DEVSHARD_STORAGE_MODE:"postgres"}},
                versiond2:{container_name:"versiond2",environment:{PGHOST:$pg,PGDATABASE:$pg2db,PGUSER:"devshardd",PGPORT:"5432",DEVSHARD_STORAGE_MODE:"postgres"}},
                "devshard-postgres":{container_name:"devshard-postgres",volumes:[{type:"bind",source:($join + "/devshards/postgres"),target:"/var/lib/postgresql/gonka"}]},
                "edge-api":{container_name:"edge-api"},
                "edge-api2":{container_name:"edge-api2"},
                "edge-api3":{container_name:"edge-api3"}
            }}'
        exit 0
    fi
done

service=${!#}
for arg in "$@"; do
    if [[ $arg == ps ]]; then
        printf 'cid-%s\n' "$service"
        exit 0
    fi
done

for arg in "$@"; do
    if [[ $arg == stop ]]; then
        rm -f "$FAKE_STATE_DIR/running-$service"
        : >"$FAKE_STATE_DIR/stopped-$service"
        exit 0
    fi
    if [[ $arg == pull ]]; then
        exit 0
    fi
    if [[ $arg == start ]]; then
        rm -f "$FAKE_STATE_DIR/stopped-$service"
        : >"$FAKE_STATE_DIR/running-$service"
        exit 0
    fi
done

is_up=false
no_start=false
for arg in "$@"; do
    [[ $arg == up ]] && is_up=true
    [[ $arg == --no-start ]] && no_start=true
done
image=
if [[ $is_up == true ]]; then
    case $service in
        versiond | versiond2) image=${VERSIOND_IMAGE-} ;;
        versiond-router) image=${VERSIOND_ROUTER_IMAGE-} ;;
        edge-api | edge-api2 | edge-api3) image=${EDGE_API_IMAGE-} ;;
        edge-api-router) image=${EDGE_API_ROUTER_IMAGE-} ;;
    esac
fi
if [[ $is_up == true && $service == "${FAIL_SERVICE-}" ]]; then
    [[ $image == gonka-upgrade-rollback/* ]] || exit 1
fi
if [[ $is_up == true ]]; then
    if [[ $image == gonka-upgrade-rollback/* ]]; then
        : >"$FAKE_STATE_DIR/rollback-$service"
    else
        rm -f "$FAKE_STATE_DIR/rollback-$service"
    fi
    if [[ $no_start == true ]]; then
        rm -f "$FAKE_STATE_DIR/running-$service"
        : >"$FAKE_STATE_DIR/stopped-$service"
    else
        rm -f "$FAKE_STATE_DIR/stopped-$service"
        : >"$FAKE_STATE_DIR/running-$service"
    fi
    if [[ $service == versiond-router ]]; then
        if [[ $image == gonka-upgrade-rollback/* ]]; then
            rm -f "$FAKE_STATE_DIR/haproxy-versiond-router"
        else
            : >"$FAKE_STATE_DIR/haproxy-versiond-router"
        fi
    fi
fi
if [[ $is_up == true && $service == "${BLOCK_SERVICE-}" ]]; then
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
    local versiond_mode=${4:-ha}
    local state_dir=$log.state

    rm -rf "$state_dir"
    mkdir -p "$state_dir"
    if [[ ${PERSISTED_VERSIOND_BARRIER-} == true ]]; then
        : >"$state_dir/barrier-versiond-router"
    fi
    : >"$log"
    if DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        FAIL_SERVICE="$failed_service" \
        BLOCK_SERVICE=none \
        BLOCK_SIGNAL=none \
        EXISTING_CONTAINERS="proxy versiond versiond2 versiond-router devshard-postgres edge-api edge-api2 edge-api3 edge-api-router" \
        FAKE_STATE_DIR="$state_dir" \
        JOIN_DIR="$script_dir" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        INTERRUPT_TARGET_BASELINE_SERVICE="${INTERRUPT_TARGET_BASELINE_SERVICE-}" \
        ROLLBACK_EMPTY_VERSIOND_SERVICE="${ROLLBACK_EMPTY_VERSIOND_SERVICE-}" \
        ROLLBACK_MISSING_VERSION_SERVICE="${ROLLBACK_MISSING_VERSION_SERVICE-}" \
        ROLLBACK_MISSING_ROUTE_SERVICE="${ROLLBACK_MISSING_ROUTE_SERVICE-}" \
        ROLLBACK_PROBE_FAIL_SERVICE="${ROLLBACK_PROBE_FAIL_SERVICE-}" \
        ROLLBACK_ROUTER_PROBE_FAIL_SERVICE="${ROLLBACK_ROUTER_PROBE_FAIL_SERVICE-}" \
        SPECIAL_VERSIOND_HEALTH_SERVICE="${SPECIAL_VERSIOND_HEALTH_SERVICE-}" \
        STOPPED_VERSIOND_SERVICE="${STOPPED_VERSIOND_SERVICE-}" \
        TARGET_ROUTER_MISSING_VERSION="${TARGET_ROUTER_MISSING_VERSION-}" \
        VERSIOND2_UNIQUE_VERSION="${VERSIOND2_UNIQUE_VERSION-}" \
        UPGRADE_ROLLBACK_VERIFY_TIMEOUT="${UPGRADE_ROLLBACK_VERIFY_TIMEOUT-}" \
        UPGRADE_ROLLBACK_VERIFY_INTERVAL="${UPGRADE_ROLLBACK_VERIFY_INTERVAL:-1}" \
        UPGRADE_ROLLBACK_STABILITY_CHECKS="${UPGRADE_ROLLBACK_STABILITY_CHECKS:-1}" \
        UPGRADE_ROUTER_RELOAD_SETTLE=0 \
        RUNTIME_COMPOSE_FILES="${RUNTIME_COMPOSE_FILES-}" \
        RUNTIME_COMPOSE_WORKDIR="${RUNTIME_COMPOSE_WORKDIR-}" \
        RUNTIME_PGHOST="${RUNTIME_PGHOST-}" \
        RENDERED_PGHOST="${RENDERED_PGHOST-}" \
        RENDERED_POLICY_NETWORK="${RENDERED_POLICY_NETWORK-}" \
        RENDERED_ROUTER_FRONT_NETWORK="${RENDERED_ROUTER_FRONT_NETWORK-}" \
        RENDERED_ROUTER_BACK_NETWORK="${RENDERED_ROUTER_BACK_NETWORK-}" \
        INCOMPATIBLE_COMPOSE_CONTAINER="${INCOMPATIBLE_COMPOSE_CONTAINER-}" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode "$versiond_mode" --edge-mode "$mode" \
        >"$tmpdir/stdout" 2>"$tmpdir/stderr"; then
        fail "$mode upgrade unexpectedly succeeded when $failed_service failed"
    fi
}

run_auto_upgrade() {
    local containers=$1
    local log=$2
    local stdout=$3
    local state_dir=$log.state

    rm -rf "$state_dir"
    mkdir -p "$state_dir"
    : >"$log"
    if ! DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        FAIL_SERVICE=none \
        BLOCK_SERVICE=none \
        BLOCK_SIGNAL=none \
        EXISTING_CONTAINERS="$containers proxy" \
        FAKE_STATE_DIR="$state_dir" \
        JOIN_DIR="$script_dir" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        UPGRADE_ROUTER_RELOAD_SETTLE=0 \
        RUNTIME_COMPOSE_FILES="${RUNTIME_COMPOSE_FILES-}" \
        RUNTIME_COMPOSE_WORKDIR="${RUNTIME_COMPOSE_WORKDIR-}" \
        RUNTIME_PGHOST="${RUNTIME_PGHOST-}" \
        RENDERED_PGHOST="${RENDERED_PGHOST-}" \
        RENDERED_POLICY_NETWORK="${RENDERED_POLICY_NETWORK-}" \
        RENDERED_ROUTER_FRONT_NETWORK="${RENDERED_ROUTER_FRONT_NETWORK-}" \
        RENDERED_ROUTER_BACK_NETWORK="${RENDERED_ROUTER_BACK_NETWORK-}" \
        INCOMPATIBLE_COMPOSE_CONTAINER="${INCOMPATIBLE_COMPOSE_CONTAINER-}" \
        "$script_dir/upgrade-devshard-v5.sh" >"$stdout" 2>"$tmpdir/stderr"; then
        cat "$tmpdir/stderr" >&2
        fail "automatic topology upgrade failed"
    fi
}

run_postcondition_interrupted_upgrade() {
    local log=$1
    local state_dir=$log.state
    local stdout=$log.stdout
    local stderr=$log.stderr
    local marker=$state_dir/interrupted-target-baseline-versiond2
    local upgrade_pid status=0
    local deadline=$((SECONDS + 10))

    rm -rf "$state_dir"
    mkdir -p "$state_dir"
    : >"$log"
    DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        FAIL_SERVICE=none \
        BLOCK_SERVICE=none \
        BLOCK_SIGNAL=none \
        EXISTING_CONTAINERS="proxy versiond versiond2 versiond-router devshard-postgres edge-api" \
        FAKE_STATE_DIR="$state_dir" \
        JOIN_DIR="$script_dir" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        INTERRUPT_TARGET_BASELINE_SERVICE=versiond2 \
        UPGRADE_ROLLBACK_VERIFY_TIMEOUT=5 \
        UPGRADE_ROLLBACK_VERIFY_INTERVAL=1 \
        UPGRADE_ROLLBACK_STABILITY_CHECKS=1 \
        UPGRADE_ROUTER_RELOAD_SETTLE=0 \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single \
        >"$stdout" 2>"$stderr" &
    upgrade_pid=$!

    while [[ ! -f $marker ]]; do
        if ! kill -0 "$upgrade_pid" 2>/dev/null; then
            wait "$upgrade_pid" || status=$?
            cat "$stderr" >&2
            fail "upgrade exited before the target route postcondition"
        fi
        ((SECONDS < deadline)) || {
            kill -KILL "$upgrade_pid" 2>/dev/null || true
            wait "$upgrade_pid" 2>/dev/null || true
            fail "upgrade did not reach the target route postcondition"
        }
        sleep 0.05
    done

    kill -TERM "$upgrade_pid"
    wait "$upgrade_pid" || status=$?
    ((status != 0)) || fail \
        "upgrade interrupted during the target postcondition exited successfully"
}

run_interrupted_upgrade() {
    local signal=$1
    local log=$2
    local stdout=$3
    local stderr=$4
    local state_dir=$log.state

    rm -rf "$state_dir"
    mkdir -p "$state_dir"
    : >"$log"
    if DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        FAIL_SERVICE=none \
        BLOCK_SERVICE=versiond2 \
        BLOCK_SIGNAL="$signal" \
        EXISTING_CONTAINERS="proxy versiond versiond2 versiond-router devshard-postgres edge-api" \
        FAKE_STATE_DIR="$state_dir" \
        JOIN_DIR="$script_dir" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        ROLLBACK_EMPTY_VERSIOND_SERVICE='' \
        ROLLBACK_MISSING_VERSION_SERVICE='' \
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

assert_no_compose_mutation() {
    local file=$1

    if grep -E -- ' :: compose .* (pull|up|stop|start|rm)($| )' "$file" \
        >/dev/null; then
        cat "$file" >&2
        fail "topology validation performed a Compose mutation"
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
cat >"$tmpdir/fleet" <<'EOF'
#!/usr/bin/env bash
set -eu
printf 'fleet %s\n' "$*" >>"$DOCKER_LOG"
printf 'fleet-networks %s %s\n' \
    "${VERSIOND_ROUTER_FRONT_NETWORK-}" \
    "${VERSIOND_ROUTER_BACK_NETWORK-}" >>"$DOCKER_LOG"
EOF
cat >"$tmpdir/enable-router" <<'EOF'
#!/usr/bin/env bash
set -eu
printf 'enable-router %s\n' "$*" >>"$DOCKER_LOG"
EOF
chmod +x "$tmpdir/fleet" "$tmpdir/enable-router"
printf 'export DEVSHARD_POSTGRES_DATA_DIR=%q\nexport UPGRADE_ENABLE_ROUTER_HA=false\nexport VERSIOND_ROUTER_FLEET_BIN=%q\nexport ROUTER_HA_ENABLE_BIN=%q\nexport DEVSHARD_V5_UPGRADE_MARKER=%q\nexport DEVSHARD_V5_VERSIOND_IMAGE=untrusted-config-image\n' \
    "$tmpdir/postgres" "$tmpdir/fleet" "$tmpdir/enable-router" \
    "$tmpdir/upgrade-complete" >"$tmpdir/config.env"

preflight_log=$tmpdir/preflight.log
: >"$preflight_log"
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$preflight_log" \
FAIL_SERVICE=none \
EXISTING_CONTAINERS="proxy versiond versiond2 versiond-router devshard-postgres edge-api" \
FAKE_STATE_DIR="$tmpdir/preflight-state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    --versiond-mode ha --edge-mode single --preflight-only \
    >"$tmpdir/preflight.stdout" 2>"$tmpdir/preflight.stderr" || {
        cat "$tmpdir/preflight.stderr" >&2
        fail "read-only release preflight failed"
    }
assert_no_compose_mutation "$preflight_log"
assert_not_contains "$preflight_log" "fleet "
assert_not_contains "$preflight_log" "enable-router "
assert_not_contains "$preflight_log" "VERSIOND_IMAGE=untrusted-config-image"
assert_contains "$preflight_log" \
    "VERSIOND_IMAGE=ghcr.io/product-science/versiond:0.2.15-devshard-v5"
grep -q 'Devshard v5 release preflight passed' "$tmpdir/preflight.stdout" || {
    cat "$tmpdir/preflight.stdout" >&2
    fail "release preflight did not report success"
}

custom_port_log=$tmpdir/custom-port.log
: >"$custom_port_log"
RENDERED_PROXY_HTTP_PORT=9000 \
FAKE_PROXY_HTTP_PORT=9000 \
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$custom_port_log" \
EXISTING_CONTAINERS="proxy versiond edge-api" \
FAKE_STATE_DIR="$tmpdir/custom-port-state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    --versiond-mode single --edge-mode single --preflight-only \
    >"$tmpdir/custom-port.stdout" 2>"$tmpdir/custom-port.stderr" || {
        cat "$tmpdir/custom-port.stderr" >&2
        fail "release preflight rejected an effective local port override"
    }
assert_no_compose_mutation "$custom_port_log"
grep -q 'public ports 9000/http' "$tmpdir/custom-port.stdout" || fail \
    "release preflight did not use the effective Compose port override"

undersized_log=$tmpdir/undersized.log
: >"$undersized_log"
if DEVSHARD_V5_PREFLIGHT_CPUS=1 \
    DEVSHARD_V5_PREFLIGHT_MEMORY_MIB=1024 \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$undersized_log" \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/undersized-state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single \
        --preflight-only --strict-capacity \
        >"$tmpdir/undersized.stdout" 2>"$tmpdir/undersized.stderr"; then
    fail "strict release preflight accepted an undersized host"
fi
assert_no_compose_mutation "$undersized_log"
grep -q 'host capacity is below' "$tmpdir/undersized.stderr" || {
    cat "$tmpdir/undersized.stderr" >&2
    fail "strict capacity failure was not actionable"
}

wrong_port_log=$tmpdir/wrong-port.log
: >"$wrong_port_log"
if FAKE_PROXY_HTTP_PORT=9000 \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$wrong_port_log" \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/wrong-port-state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single --preflight-only \
        >"$tmpdir/wrong-port.stdout" 2>"$tmpdir/wrong-port.stderr"; then
    fail "release preflight accepted unexpected public port ownership"
fi
assert_no_compose_mutation "$wrong_port_log"
grep -q 'does not own expected host port 8000' "$tmpdir/wrong-port.stderr" || {
    cat "$tmpdir/wrong-port.stderr" >&2
    fail "public port ownership failure was not actionable"
}

unacknowledged_log=$tmpdir/unacknowledged.log
: >"$unacknowledged_log"
if DEVSHARD_V5_MAINTENANCE_ACK=false \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$unacknowledged_log" \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/unacknowledged-state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single \
        >"$tmpdir/unacknowledged.stdout" \
        2>"$tmpdir/unacknowledged.stderr"; then
    fail "one-time cutover ran without maintenance acknowledgement"
fi
assert_no_compose_mutation "$unacknowledged_log"
grep -q -- '--acknowledge-maintenance' "$tmpdir/unacknowledged.stderr" || {
    cat "$tmpdir/unacknowledged.stderr" >&2
    fail "maintenance acknowledgement failure was not actionable"
}

cp "$tmpdir/config.env" "$tmpdir/config-with-ack.env"
printf 'export DEVSHARD_V5_MAINTENANCE_ACK=true\n' \
    >>"$tmpdir/config-with-ack.env"
: >"$tmpdir/persisted-ack.log"
if env -u DEVSHARD_V5_MAINTENANCE_ACK \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/persisted-ack.log" \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/persisted-ack-state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config-with-ack.env" \
        "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single \
        >"$tmpdir/persisted-ack.stdout" \
        2>"$tmpdir/persisted-ack.stderr"; then
    fail "config.env persisted a maintenance acknowledgement"
fi
assert_no_compose_mutation "$tmpdir/persisted-ack.log"

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

runtime_with_observability="$script_dir/docker-compose.yml,$script_dir/docker-compose.observability.yml"
COMPOSE_FILE="$script_dir/docker-compose.yml:$script_dir/docker-compose.observability.yml" \
RUNTIME_COMPOSE_FILES=$runtime_with_observability \
    run_auto_upgrade "versiond edge-api" \
        "$tmpdir/observability.log" "$tmpdir/observability.stdout"
assert_contains "$tmpdir/observability.log" \
    "-f $script_dir/docker-compose.observability.yml"
grep -q 'source=COMPOSE_FILE' "$tmpdir/observability.stdout" || fail \
    "updater did not accept the complete COMPOSE_FILE model"

# Explicit recovery must not be vetoed by paths recorded from a checkout that
# no longer exists. The current explicit model remains subject to full Compose
# rendering and service-contract validation.
COMPOSE_FILE="$script_dir/docker-compose.yml" \
RUNTIME_COMPOSE_FILES=./removed-docker-compose.yml \
RUNTIME_COMPOSE_WORKDIR="$tmpdir/removed-checkout" \
    run_auto_upgrade "versiond edge-api" \
        "$tmpdir/stale-label.log" "$tmpdir/stale-label.stdout"
grep -q 'source=COMPOSE_FILE' "$tmpdir/stale-label.stdout" || fail \
    "explicit topology did not recover from stale runtime paths"
grep -q 'records stale Compose file metadata' "$tmpdir/stderr" || fail \
    "stale runtime metadata was not reported to the operator"

if DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/missing-override.log" \
    FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/missing-override.state" \
    JOIN_DIR="$script_dir" \
    RUNTIME_COMPOSE_FILES="$runtime_with_observability" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single \
        --compose-file "$script_dir/docker-compose.yml" \
        >"$tmpdir/missing-override.stdout" \
        2>"$tmpdir/missing-override.stderr"; then
    fail "upgrade accepted an explicit model that omitted a runtime override"
fi
grep -q 'omits or reorders a file recorded by running containers' \
    "$tmpdir/missing-override.stderr" || {
    cat "$tmpdir/missing-override.stderr" >&2
    fail "missing runtime override did not produce a useful error"
}
assert_no_compose_mutation "$tmpdir/missing-override.log"

if DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/wrong-project.log" \
    FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/wrong-project.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single \
        --compose-project-name another-project \
        >"$tmpdir/wrong-project.stdout" \
        2>"$tmpdir/wrong-project.stderr"; then
    fail "upgrade accepted a Compose project-name change"
fi
grep -q "does not match running project 'gonka-test'" \
    "$tmpdir/wrong-project.stderr" || {
    cat "$tmpdir/wrong-project.stderr" >&2
    fail "project-name mismatch did not produce a useful error"
}
assert_no_compose_mutation "$tmpdir/wrong-project.log"

if INCOMPATIBLE_COMPOSE_CONTAINER=versiond \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/incompatible-metadata.log" \
    FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
    EXISTING_CONTAINERS="proxy versiond edge-api" \
    FAKE_STATE_DIR="$tmpdir/incompatible-metadata.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode single --edge-mode single \
        >"$tmpdir/incompatible-metadata.stdout" \
        2>"$tmpdir/incompatible-metadata.stderr"; then
    fail "upgrade guessed an order for incompatible runtime Compose metadata"
fi
grep -q 'record incompatible Compose file sets' \
    "$tmpdir/incompatible-metadata.stderr" || {
    cat "$tmpdir/incompatible-metadata.stderr" >&2
    fail "incompatible runtime metadata did not produce a useful error"
}
assert_no_compose_mutation "$tmpdir/incompatible-metadata.log"
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

cat >"$tmpdir/managed-postgres.yml" <<'EOF'
services:
  versiond:
    environment:
      PGHOST: managed-postgres.internal
  versiond2:
    environment:
      PGHOST: managed-postgres.internal
EOF
managed_files="$script_dir/docker-compose.yml,$script_dir/docker-compose.versiond.yml,$tmpdir/managed-postgres.yml"
RUNTIME_COMPOSE_FILES=$managed_files \
RUNTIME_PGHOST=managed-postgres.internal \
RENDERED_PGHOST=managed-postgres.internal \
RENDERED_ROUTER_FRONT_NETWORK=custom-router-front \
RENDERED_ROUTER_BACK_NETWORK=custom-router-back \
    run_auto_upgrade \
        "versiond versiond2 versiond-router edge-api" \
        "$tmpdir/managed-postgres.log" "$tmpdir/managed-postgres.stdout"
assert_contains "$tmpdir/managed-postgres.log" \
    "-f $tmpdir/managed-postgres.yml"
assert_not_contains "$tmpdir/managed-postgres.log" " pull devshard-postgres"
assert_not_contains "$tmpdir/managed-postgres.log" \
    "--wait-timeout 2100 devshard-postgres"
assert_not_contains "$tmpdir/managed-postgres.log" \
    "--volumes-from cid-devshard-postgres:ro"
assert_contains "$tmpdir/managed-postgres.log" \
    "fleet-networks custom-router-front custom-router-back"

if RUNTIME_COMPOSE_FILES=$managed_files \
    RUNTIME_PGHOST=managed-postgres.internal \
    RENDERED_PGHOST=other-postgres.internal \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/postgres-switch.log" \
    FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
    EXISTING_CONTAINERS="proxy versiond versiond2 edge-api" \
    FAKE_STATE_DIR="$tmpdir/postgres-switch.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single \
        >"$tmpdir/postgres-switch.stdout" \
        2>"$tmpdir/postgres-switch.stderr"; then
    fail "upgrade accepted an implicit PostgreSQL endpoint change"
fi
grep -q 'refuse an implicit PostgreSQL identity change' \
    "$tmpdir/postgres-switch.stderr" || {
    cat "$tmpdir/postgres-switch.stderr" >&2
    fail "PostgreSQL endpoint change did not produce a useful error"
}
assert_no_compose_mutation "$tmpdir/postgres-switch.log"

if RENDERED_PGDATABASE2=other-devshard \
    DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/postgres-split.log" \
    FAIL_SERVICE=none BLOCK_SERVICE=none BLOCK_SIGNAL=none \
    EXISTING_CONTAINERS="proxy versiond versiond2 edge-api" \
    FAKE_STATE_DIR="$tmpdir/postgres-split.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
        --versiond-mode ha --edge-mode single \
        >"$tmpdir/postgres-split.stdout" \
        2>"$tmpdir/postgres-split.stderr"; then
    fail "upgrade accepted versiond services connected to different databases"
fi
grep -q 'same non-empty PGDATABASE' "$tmpdir/postgres-split.stderr" || {
    cat "$tmpdir/postgres-split.stderr" >&2
    fail "split PostgreSQL database did not produce a useful error"
}
assert_no_compose_mutation "$tmpdir/postgres-split.log"
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

# A public proxy-router label proves only that ingress cutover happened. It
# must not suppress an unfinished application or PostgreSQL migration.
rm -f "$tmpdir/upgrade-complete"
if DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/active-without-marker.log" \
    EXISTING_PROXY_COMPONENT=proxy-router \
    EXISTING_CONTAINERS="versiond versiond2 devshard-postgres edge-api proxy" \
    FAKE_STATE_DIR="$tmpdir/active-without-marker.state" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    --versiond-mode ha --edge-mode single \
    >"$tmpdir/active-without-marker.stdout" \
    2>"$tmpdir/active-without-marker.stderr"; then
    fail "proxy-router label was accepted as an upgrade commit"
fi
grep -q 'cannot recover the missing commit marker' "$tmpdir/active-without-marker.stderr" || {
    cat "$tmpdir/active-without-marker.stderr" >&2
    fail "incomplete cutover did not explain the missing upgrade commit"
}

# A crash after the irreversible router cutover but before the atomic marker
# write is recoverable only after exact images and health have been proven.
rm -f "$tmpdir/upgrade-complete"
ASSUME_RELEASE_STATE=true \
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$tmpdir/recovered-marker.log" \
EXISTING_PROXY_COMPONENT=proxy-router \
EXISTING_CONTAINERS="versiond versiond2 devshard-postgres edge-api proxy proxy-policy" \
FAKE_STATE_DIR="$tmpdir/recovered-marker.state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    --versiond-mode ha --edge-mode single \
    >"$tmpdir/recovered-marker.stdout" \
    2>"$tmpdir/recovered-marker.stderr"
[[ $(<"$tmpdir/upgrade-complete") == 0.2.15-devshard-v5 ]] || fail \
    "verified cutover did not reconstruct the missing commit marker"
assert_contains "$tmpdir/recovered-marker.log" \
    "enable-router --versiond-mode ha --edge-mode single"
grep -q 'Recovered the verified' "$tmpdir/recovered-marker.stdout" || fail \
    "marker recovery was not reported"

printf '0.2.15-devshard-v5\n' >"$tmpdir/upgrade-complete"
DOCKER_BIN="$tmpdir/docker" \
DOCKER_LOG="$tmpdir/active-with-marker.log" \
EXISTING_PROXY_COMPONENT=proxy-router \
EXISTING_CONTAINERS="versiond versiond2 devshard-postgres edge-api proxy" \
FAKE_STATE_DIR="$tmpdir/active-with-marker.state" \
JOIN_DIR="$script_dir" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-devshard-v5.sh" \
    --versiond-mode ha --edge-mode single \
    >"$tmpdir/active-with-marker.stdout" \
    2>"$tmpdir/active-with-marker.stderr"
assert_contains "$tmpdir/active-with-marker.log" \
    "enable-router --versiond-mode ha --edge-mode single"
assert_contains "$tmpdir/active-with-marker.log" \
    "--compose-project-name gonka-test"
assert_contains "$tmpdir/active-with-marker.log" \
    "--compose-file $script_dir/docker-compose.versiond.yml"

run_upgrade single devshard-postgres "$tmpdir/postgres-failure.log"
assert_contains "$tmpdir/postgres-failure.log" " stop devshard-postgres"
assert_not_contains "$tmpdir/postgres-failure.log" \
    "gonka-upgrade-rollback/devshard-postgres:"
grep -q 'source volume and persistent target are preserved' \
    "$tmpdir/stderr" || {
    cat "$tmpdir/stderr" >&2
    fail "PostgreSQL failure did not explain its preservation contract"
}

UPGRADE_ROLLBACK_VERIFY_TIMEOUT='' \
UPGRADE_ROLLBACK_STABILITY_CHECKS=3 \
    run_upgrade single versiond2 "$tmpdir/versiond2.log"
grep -q 'Verifying rollback of versiond2 for up to 2100s' \
    "$tmpdir/stderr" || {
    cat "$tmpdir/stderr" >&2
    fail "versiond rollback did not inherit the forward startup budget"
}
preflight_line=$(line_number "$tmpdir/versiond2.log" \
    "--volumes-from cid-devshard-postgres:ro")
postgres_up_line=$(line_number "$tmpdir/versiond2.log" \
    "--wait-timeout 2100 devshard-postgres")
[[ -n $preflight_line && -n $postgres_up_line && \
    $preflight_line -lt $postgres_up_line ]] ||
    fail "PostgreSQL space preflight did not run before its first recreate"
assert_contains "$tmpdir/versiond2.log" \
    "VERSIOND_IMAGE=gonka-upgrade-rollback/versiond2:"
rollback_probe_count=$(grep -Ec \
    'exec cid-versiond2 .*http://127.0.0.1:8080/v4/healthz' \
    "$tmpdir/versiond2.log")
[[ $rollback_probe_count -eq 4 ]] || fail \
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

ROLLBACK_MISSING_VERSION_SERVICE=versiond \
UPGRADE_ROLLBACK_VERIFY_TIMEOUT=1 \
    run_upgrade single versiond "$tmpdir/versiond-partial.log" single
assert_contains "$tmpdir/versiond-partial.log" " stop versiond"
partial_v4_probe_count=$(grep -Fc \
    'http://127.0.0.1:8080/v4/healthz' "$tmpdir/versiond-partial.log")
[[ $partial_v4_probe_count -eq 1 ]] || fail \
    "partial rollback unexpectedly restored the missing v4 baseline route"

SPECIAL_VERSIOND_HEALTH_SERVICE=versiond \
    run_upgrade single versiond "$tmpdir/versiond-special-name.log" single
assert_contains "$tmpdir/versiond-special-name.log" \
    'http://127.0.0.1:8080/v4%2Bhotfix/healthz'
assert_contains "$tmpdir/versiond-special-name.log" \
    'http://127.0.0.1:8080/v4%7Dx/healthz'
assert_not_contains "$tmpdir/versiond-special-name.log" " stop versiond"

STOPPED_VERSIOND_SERVICE=versiond2 \
    run_upgrade single versiond2 "$tmpdir/versiond2-stopped.log"
assert_not_contains "$tmpdir/versiond2-stopped.log" " start versiond2"
assert_not_contains "$tmpdir/versiond2-stopped.log" \
    'exec cid-versiond2 /bin/busybox wget -q -T 3 -O - http://127.0.0.1:8080/healthz'
assert_contains "$tmpdir/versiond2-stopped.log" \
    "up --no-start --no-deps --force-recreate versiond2"

run_upgrade single versiond "$tmpdir/versiond-production-rollback.log"
assert_contains "$tmpdir/versiond-production-rollback.log" \
    'exec cid-versiond-router /bin/busybox wget -q -T 3 -O /dev/null http://127.0.0.1:8080/v3/healthz'

ROLLBACK_ROUTER_PROBE_FAIL_SERVICE=versiond \
UPGRADE_ROLLBACK_VERIFY_TIMEOUT=1 \
    run_upgrade single versiond \
        "$tmpdir/versiond-production-rollback-failure.log"
assert_contains "$tmpdir/versiond-production-rollback-failure.log" \
    " stop versiond"

run_upgrade single versiond-router "$tmpdir/versiond-router.log"
assert_contains "$tmpdir/versiond-router.log" \
    "exec versiond-router rm -f /etc/gonka-upgrade-barrier"
assert_contains "$tmpdir/versiond-router.log" \
    "VERSIOND_HOSTS=versiond\\ versiond2"
assert_contains "$tmpdir/versiond-router.log" \
    'exec cid-versiond-router /bin/busybox wget -q -T 3 -O /dev/null http://127.0.0.1:8080/v3/healthz'

ROLLBACK_MISSING_ROUTE_SERVICE=versiond-router \
UPGRADE_ROLLBACK_VERIFY_TIMEOUT=1 \
    run_upgrade single versiond-router \
        "$tmpdir/versiond-router-missing-route.log"
assert_contains "$tmpdir/versiond-router-missing-route.log" \
    " stop versiond-router"

PERSISTED_VERSIOND_BARRIER=true \
VERSIOND2_UNIQUE_VERSION=true \
    run_upgrade single edge-api "$tmpdir/versiond-barrier-retry.log"
barrier_remove_line=$(line_number "$tmpdir/versiond-barrier-retry.log" \
    "exec versiond-router rm -f /etc/gonka-upgrade-barrier")
router_v5_probe_line=$(line_number "$tmpdir/versiond-barrier-retry.log" \
    'exec cid-versiond-router /bin/busybox wget -q -T 3 -O /dev/null http://127.0.0.1:8080/v5/healthz')
retry_router_up_line=$(line_number "$tmpdir/versiond-barrier-retry.log" \
    "--wait-timeout 60 versiond-router")
[[ -n $barrier_remove_line && -n $router_v5_probe_line && \
    -n $retry_router_up_line && \
    $barrier_remove_line -lt $router_v5_probe_line && \
    $router_v5_probe_line -lt $retry_router_up_line ]] ||
    fail "persisted barrier was not reconciled before router baseline capture"

STOPPED_VERSIOND_SERVICE=versiond2 \
TARGET_ROUTER_MISSING_VERSION=v5 \
VERSIOND2_UNIQUE_VERSION=true \
    run_upgrade single none "$tmpdir/versiond2-router-postcondition.log"
assert_contains "$tmpdir/versiond2-router-postcondition.log" \
    "up --no-start --no-deps --force-recreate versiond2"
assert_not_contains "$tmpdir/versiond2-router-postcondition.log" \
    "--wait-timeout 60 versiond-router"

run_postcondition_interrupted_upgrade \
    "$tmpdir/versiond2-postcondition-interrupt.log"
assert_contains "$tmpdir/versiond2-postcondition-interrupt.log" \
    "VERSIOND_IMAGE=gonka-upgrade-rollback/versiond2:"
assert_not_contains "$tmpdir/versiond2-postcondition-interrupt.log" \
    "VERSIOND_HOSTS=versiond\\ versiond2"
grep -q 'received TERM' "$tmpdir/versiond2-postcondition-interrupt.log.stderr" || {
    cat "$tmpdir/versiond2-postcondition-interrupt.log.stderr" >&2
    fail "postcondition interruption did not reach the signal handler"
}

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
