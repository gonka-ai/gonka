#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
config_env=${GONKA_CONFIG_ENV:-$script_dir/config.env}
docker_bin=${DOCKER_BIN:-docker}
release_tag=0.2.15-devshard-v5
operation_id="$(date +%s%N)-$$"
versiond_mode=auto
edge_mode=auto

fail() {
    echo "upgrade-devshard-v5: $*" >&2
    exit 1
}

warn() {
    echo "upgrade-devshard-v5: warning: $*" >&2
}

usage() {
    cat >&2 <<'EOF'
Usage: upgrade-devshard-v5.sh [OPTIONS]

Normally run without options. The script detects the existing deployment.

Options:
  --versiond-mode auto|single|ha
  --edge-mode     auto|single|multi
EOF
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --versiond-mode)
            [[ $# -ge 2 ]] || fail "--versiond-mode requires a value"
            versiond_mode=$2
            shift 2
            ;;
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

case $versiond_mode in
    auto | single | ha) ;;
    *) fail "--versiond-mode must be auto, single, or ha" ;;
esac
case $edge_mode in
    auto | single | multi) ;;
    *) fail "--edge-mode must be auto, single, or multi" ;;
esac

[[ -f $config_env ]] || fail "configuration file not found: $config_env"
# shellcheck disable=SC1090
source "$config_env"

command -v "$docker_bin" >/dev/null 2>&1 || fail "$docker_bin is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

# A rollback gets the same startup budget as the corresponding forward
# replacement. UPGRADE_ROLLBACK_VERIFY_TIMEOUT remains an emergency override
# for diagnostics and tests; an unset value selects the service budget.
rollback_verify_timeout_override=${UPGRADE_ROLLBACK_VERIFY_TIMEOUT:-}
rollback_verify_interval=${UPGRADE_ROLLBACK_VERIFY_INTERVAL:-2}
rollback_stability_checks=${UPGRADE_ROLLBACK_STABILITY_CHECKS:-3}
case $rollback_verify_timeout_override in
    '') ;;
    *[!0-9]* | 0)
        fail "UPGRADE_ROLLBACK_VERIFY_TIMEOUT must be a positive integer"
        ;;
esac
case $rollback_verify_interval in
    '' | *[!0-9]* | 0)
        fail "UPGRADE_ROLLBACK_VERIFY_INTERVAL must be a positive integer"
        ;;
esac

service_startup_timeout() {
    case $1 in
        versiond | versiond2 | devshard-postgres) printf '2100\n' ;;
        edge-api | edge-api2 | edge-api3) printf '180\n' ;;
        versiond-router | edge-api-router) printf '60\n' ;;
        *) fail "internal error: no startup timeout for $1" ;;
    esac
}

rollback_timeout_for_service() {
    if [[ -n $rollback_verify_timeout_override ]]; then
        printf '%s\n' "$rollback_verify_timeout_override"
        return 0
    fi
    service_startup_timeout "$1"
}
case $rollback_stability_checks in
    '' | *[!0-9]* | 0)
        fail "UPGRADE_ROLLBACK_STABILITY_CHECKS must be a positive integer"
        ;;
esac

container_exists() {
    "$docker_bin" inspect "$1" >/dev/null 2>&1
}

if [[ $versiond_mode == auto ]]; then
    if container_exists devshard-postgres ||
        container_exists versiond-router ||
        container_exists versiond2; then
        versiond_mode=ha
    elif container_exists versiond; then
        versiond_mode=single
    else
        fail "cannot detect versiond topology: no existing versiond deployment was found"
    fi
fi
if [[ $edge_mode == auto ]]; then
    if container_exists edge-api-router ||
        container_exists edge-api2 ||
        container_exists edge-api3; then
        edge_mode=multi
    elif container_exists edge-api; then
        edge_mode=single
    else
        fail "cannot detect edge-api topology: no existing edge-api deployment was found"
    fi
fi
echo "Deployment topology: versiond=$versiond_mode, edge-api=$edge_mode"

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
)
if [[ $versiond_mode == ha ]]; then
    compose+=(-f "$script_dir/docker-compose.versiond.yml")
fi
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
declare -A rollback_version_baselines=()

foreground_pid=
active_service=
active_failure_strategy=
traffic_barrier_router=
traffic_barrier_env=
traffic_barrier_original_hosts=
traffic_barrier_entrypoint=
traffic_barrier_target=

run_interruptible() {
    local status=0

    "$@" &
    foreground_pid=$!
    wait "$foreground_pid" || status=$?
    foreground_pid=
    return "$status"
}

container_env_value() {
    local container=$1
    local name=$2
    local line

    while IFS= read -r line; do
        case $line in
            "$name="*)
                printf '%s\n' "${line#*=}"
                return 0
                ;;
        esac
    done < <(
        "$docker_bin" inspect --format \
            '{{range .Config.Env}}{{println .}}{{end}}' "$container"
    )
    return 1
}

without_host() {
    local hosts=$1
    local excluded=$2
    local host
    local found=false
    local -a host_list=()
    local -a remaining=()

    read -r -a host_list <<<"$hosts"
    for host in "${host_list[@]}"; do
        if [[ $host == "$excluded" ]]; then
            found=true
        else
            remaining+=("$host")
        fi
    done
    [[ $found == true ]] || return 1
    ((${#remaining[@]} > 0)) || return 1
    printf '%s\n' "${remaining[*]}"
}

router_runtime() {
    local router=$1

    if "$docker_bin" exec "$router" nginx -v >/dev/null 2>&1; then
        printf 'nginx\n'
        return 0
    fi
    if "$docker_bin" exec "$router" haproxy -v >/dev/null 2>&1; then
        printf 'haproxy\n'
        return 0
    fi
    return 1
}

apply_nginx_host_list() {
    local router=$1
    local env_name=$2
    local hosts=$3
    local entrypoint=$4

    # The quoted program is evaluated by /bin/sh inside the router container.
    # shellcheck disable=SC2016
    run_interruptible "$docker_bin" exec --env "$env_name=$hosts" "$router" \
        /bin/sh -ec '
            conf=/etc/nginx/conf.d/default.conf
            backup=$(mktemp)
            cp "$conf" "$backup"
            if "$1" && nginx -t && nginx -s reload; then
                rm -f "$backup"
            else
                cp "$backup" "$conf"
                rm -f "$backup"
                exit 1
            fi
        ' sh "$entrypoint"
}

install_nginx_traffic_barrier() {
    local router=$1
    local env_name=$2
    local hosts=$3
    local entrypoint=$4
    local hook=/docker-entrypoint.d/99-gonka-upgrade-barrier.sh
    local staged_hook=/tmp/99-gonka-upgrade-barrier.sh
    local state=/etc/gonka-upgrade-barrier

    run_interruptible "$docker_bin" cp \
        "$script_dir/legacy-router-upgrade-barrier.sh" \
        "$router:$staged_hook"
    # The quoted program is evaluated by /bin/sh inside the router container.
    # shellcheck disable=SC2016
    run_interruptible "$docker_bin" exec \
        --env "GONKA_BARRIER_ENV_NAME=$env_name" \
        --env "GONKA_BARRIER_HOSTS=$hosts" \
        --env "GONKA_BARRIER_RENDERER=$entrypoint" \
        "$router" /bin/sh -ec '
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

remove_nginx_traffic_barrier() {
    local router=$1

    run_interruptible "$docker_bin" exec "$router" rm -f \
        /etc/gonka-upgrade-barrier \
        /docker-entrypoint.d/99-gonka-upgrade-barrier.sh \
        /tmp/99-gonka-upgrade-barrier.sh
}

clear_traffic_barrier() {
    traffic_barrier_router=
    traffic_barrier_env=
    traffic_barrier_original_hosts=
    traffic_barrier_entrypoint=
    traffic_barrier_target=
}

begin_traffic_barrier() {
    local router=$1
    local env_name=$2
    local target=$3
    local entrypoint=$4
    local runtime original_hosts isolated_hosts

    runtime=$(router_runtime "$router") || fail \
        "cannot identify the running $router proxy"
    if [[ $runtime == haproxy ]]; then
        echo "$router already has readiness-aware routing; no legacy barrier needed"
        return 0
    fi

    original_hosts=$(container_env_value "$router" "$env_name") || fail \
        "cannot read $env_name from $router"
    isolated_hosts=$(without_host "$original_hosts" "$target") || fail \
        "cannot isolate the only upstream $target from $router"

    # Record the compensation before publishing the changed nginx config.
    traffic_barrier_router=$router
    traffic_barrier_env=$env_name
    traffic_barrier_original_hosts=$original_hosts
    traffic_barrier_entrypoint=$entrypoint
    traffic_barrier_target=$target

    echo "Removing $target from the legacy $router upstream"
    install_nginx_traffic_barrier \
        "$router" "$env_name" "$isolated_hosts" "$entrypoint"
    apply_nginx_host_list "$router" "$env_name" "$isolated_hosts" \
        "$entrypoint"
    # nginx reload is asynchronous. Keep the replacement out of DNS until old
    # workers have stopped accepting new connections.
    sleep "${UPGRADE_ROUTER_RELOAD_SETTLE:-5}"
}

restore_traffic_barrier() {
    local runtime

    [[ -n $traffic_barrier_router ]] || return 0
    runtime=$(router_runtime "$traffic_barrier_router") || return 1
    if [[ $runtime == haproxy ]]; then
        clear_traffic_barrier
        return 0
    fi

    echo "Restoring $traffic_barrier_target in the legacy router upstream" >&2
    remove_nginx_traffic_barrier "$traffic_barrier_router" || return 1
    apply_nginx_host_list \
        "$traffic_barrier_router" \
        "$traffic_barrier_env" \
        "$traffic_barrier_original_hosts" \
        "$traffic_barrier_entrypoint" || return 1
    clear_traffic_barrier
}

versiond_health_snapshot() {
    local container_id=$1
    local payload

    payload=$("$docker_bin" exec "$container_id" /bin/busybox wget \
        -q -T 3 -O - http://127.0.0.1:8080/healthz) || return 1
    jq -ce '
        def valid_entry:
            type == "object" and
            ((.name | type) == "string") and
            ((.status | type) == "string");
        if type != "array" or (all(.[]; valid_entry) | not) then
            error("invalid versiond health payload")
        else
            {
                settled: (length > 0 and all(.[]; .status == "running")),
                running: ([.[] | select(.status == "running") | .name] | unique)
            }
        end
    ' <<<"$payload"
}

versiond_route_is_available() {
    local container_id=$1
    local encoded_version=$2

    "$docker_bin" exec "$container_id" /bin/busybox wget \
        -q -T 3 -O /dev/null \
        "http://127.0.0.1:8080/$encoded_version/healthz"
}

versiond_routes_are_available() {
    local container_id=$1
    local versions=$2
    local encoded_versions encoded_version

    encoded_versions=$(jq -er '.[] | @uri' <<<"$versions") || return 1
    while IFS= read -r encoded_version; do
        versiond_route_is_available \
            "$container_id" "$encoded_version" || return 1
    done <<<"$encoded_versions"
}

ensure_versiond_rollback_source_running() {
    local service=$1
    local container_id=$2

    if [[ $("$docker_bin" inspect --format '{{.State.Running}}' \
        "$container_id") == true ]]; then
        return 0
    fi

    echo "Starting existing $service to establish its rollback baseline"
    run_interruptible "${compose[@]}" start "$service" || fail \
        "cannot start existing $service before the upgrade"
}

capture_versiond_rollback_baseline() {
    local service=$1
    local container_id=$2
    local timeout deadline snapshot versions display

    ensure_versiond_rollback_source_running "$service" "$container_id"
    timeout=$(service_startup_timeout "$service")
    deadline=$((SECONDS + timeout))
    echo "Waiting up to ${timeout}s for the existing $service baseline"
    while :; do
        if snapshot=$(versiond_health_snapshot "$container_id") &&
            jq -e '.settled and (.running | length > 0)' \
                <<<"$snapshot" >/dev/null; then
            versions=$(jq -ce '.running' <<<"$snapshot")
            if versiond_routes_are_available "$container_id" "$versions"; then
                rollback_version_baselines[$service]=$versions
                display=$(jq -c '.' <<<"$versions")
                echo "Captured $service running-version baseline: $display"
                return 0
            fi
        fi
        ((SECONDS < deadline)) || fail \
            "$service did not establish a complete rollback baseline within ${timeout}s"
        sleep "$rollback_verify_interval"
    done
}

capture_versiond_router_rollback_baseline() {
    local container_id first second versions display

    [[ -n ${rollback_images[versiond-router]-} ]] || return 0
    container_id=$("${compose[@]}" ps --all --quiet versiond-router)
    [[ -n $container_id ]] || fail \
        "cannot find versiond-router while capturing its rollback baseline"
    first=${rollback_version_baselines[versiond]-[]}
    second=${rollback_version_baselines[versiond2]-[]}
    versions=$(jq -cn \
        --argjson first "$first" \
        --argjson second "$second" \
        '$first + $second | unique')
    jq -e 'length > 0' <<<"$versions" >/dev/null || fail \
        "versiond-router has no replica version baseline to preserve"
    versiond_routes_are_available "$container_id" "$versions" || fail \
        "versiond-router cannot route every replica baseline version"

    rollback_version_baselines[versiond-router]=$versions
    display=$(jq -c '.' <<<"$versions")
    echo "Captured versiond-router route baseline: $display"
}

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
    case $service in
        versiond | versiond2)
            capture_versiond_rollback_baseline "$service" "$container_id"
            ;;
    esac
}

rollback_versiond_is_available() {
    local service=$1
    local container_id=$2
    local snapshot current required

    required=${rollback_version_baselines[$service]-}
    [[ -n $required ]] || return 1
    if [[ $service == versiond-router ]]; then
        # The router's /healthz is itself hash-routed and can describe only one
        # replica. Its authoritative contract is the union captured from both
        # supervisors, checked through every public version route below.
        versiond_routes_are_available "$container_id" "$required"
        return
    fi
    snapshot=$(versiond_health_snapshot "$container_id") || return 1
    current=$(jq -ce '.running' <<<"$snapshot") || return 1
    jq -ne \
        --argjson current "$current" \
        --argjson required "$required" \
        '$required - $current | length == 0' >/dev/null || return 1
    versiond_routes_are_available "$container_id" "$required"
}

rollback_edge_is_available() {
    local container_id=$1

    # /v1/versions executes a gRPC query against the chain. Unlike /healthz,
    # it fails when edge-api is alive but cannot serve its production workload.
    "$docker_bin" exec "$container_id" /bin/busybox wget \
        -q -T 3 -O /dev/null http://127.0.0.1:18080/v1/versions
}

rollback_service_is_available() {
    local service=$1
    local container_id

    container_id=$("${compose[@]}" ps --all --quiet "$service")
    [[ -n $container_id ]] || return 1
    [[ $("$docker_bin" inspect --format '{{.State.Running}}' \
        "$container_id") == true ]] || return 1
    case $service in
        versiond | versiond2 | versiond-router)
            rollback_versiond_is_available "$service" "$container_id"
            ;;
        edge-api | edge-api2 | edge-api3 | edge-api-router)
            rollback_edge_is_available "$container_id"
            ;;
        *)
            return 1
            ;;
    esac
}

wait_for_rollback_availability() {
    local service=$1
    local timeout deadline
    local consecutive=0

    timeout=$(rollback_timeout_for_service "$service")
    deadline=$((SECONDS + timeout))
    echo "Verifying rollback of $service for up to ${timeout}s through $rollback_stability_checks consecutive health checks" >&2
    while :; do
        if rollback_service_is_available "$service"; then
            ((consecutive += 1))
            if ((consecutive >= rollback_stability_checks)); then
                return 0
            fi
        else
            consecutive=0
        fi

        ((SECONDS < deadline)) || return 1
        sleep "$rollback_verify_interval"
    done
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
        wait_for_rollback_availability "$service"; then
        return 0
    fi

    warn "rollback of $service did not become stably available; stopping it"
    stop_failed_service "$service" || true
    return 1
}

compensate_active_service() {
    local service=$active_service
    local strategy=$active_failure_strategy
    local status=0

    [[ -n $service ]] || return 0
    case $strategy in
        rollback)
            rollback_service "$service" || status=$?
            ;;
        stop)
            stop_failed_service "$service" || status=$?
            ;;
        *)
            warn "unknown compensation strategy $strategy for $service"
            status=1
            ;;
    esac
    active_service=
    active_failure_strategy=
    return "$status"
}

handle_signal() {
    local signal=$1
    local status=$2
    local interrupted_pid
    local watchdog_pid

    trap - HUP INT TERM
    warn "received $signal; aborting the active upgrade step"
    if [[ -n $foreground_pid ]]; then
        interrupted_pid=$foreground_pid
        kill -TERM "$interrupted_pid" 2>/dev/null || true
        (
            sleep 5
            kill -KILL "$interrupted_pid" 2>/dev/null || true
        ) &
        watchdog_pid=$!
        wait "$interrupted_pid" 2>/dev/null || true
        kill -KILL "$watchdog_pid" 2>/dev/null || true
        wait "$watchdog_pid" 2>/dev/null || true
        foreground_pid=
    fi
    exit "$status"
}

handle_exit() {
    local status=$1
    local interrupted_service=$active_service
    local compensation_ok=true
    local restore_allowed=true

    trap - EXIT HUP INT TERM
    if ((status == 0)); then
        exit 0
    fi

    set +e
    if [[ -n $active_service ]]; then
        warn "compensating interrupted replacement of $active_service"
        if ! compensate_active_service; then
            compensation_ok=false
            warn "automatic compensation failed; operator action is required"
        fi
    fi
    if [[ $compensation_ok == false && \
        $interrupted_service == "$traffic_barrier_router" ]]; then
        restore_allowed=false
        warn "leaving the failed upstream isolated from the legacy router"
    fi
    if [[ -n $traffic_barrier_router && \
        $interrupted_service == "$traffic_barrier_target" ]]; then
        # Running is the strongest signal available from a rolled-back v4
        # process; it does not prove that its children have reconciled.
        restore_allowed=false
        warn "leaving $traffic_barrier_target isolated until the upgrade is retried"
    fi
    if [[ -n $traffic_barrier_router && $restore_allowed == true ]] && \
        ! restore_traffic_barrier; then
        warn "could not restore the legacy router upstream"
    fi
    exit "$status"
}

replace_service() {
    local service=$1
    local wait_timeout=$2
    local failure_strategy=$3

    case $failure_strategy in
        rollback | stop) ;;
        *) fail "internal error: unknown failure strategy $failure_strategy" ;;
    esac
    echo "Replacing $service"
    active_service=$service
    active_failure_strategy=$failure_strategy
    if run_interruptible "${compose[@]}" \
        up -d --no-deps --force-recreate --wait \
        --wait-timeout "$wait_timeout" "$service"; then
        active_service=
        active_failure_strategy=
        return 0
    fi
    warn "$service did not become ready; aborting the upgrade"
    return 1
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

trap 'handle_signal HUP 129' HUP
trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM
trap 'handle_exit $?' EXIT

services=(versiond edge-api)
if [[ $versiond_mode == ha ]]; then
    services+=(versiond2 versiond-router)
fi
if [[ $edge_mode == multi ]]; then
    services+=(edge-api2 edge-api3 edge-api-router)
fi
for service in "${services[@]}"; do
    capture_rollback_image "$service"
done
if [[ $versiond_mode == ha ]]; then
    capture_versiond_router_rollback_baseline
fi

pull_services=(versiond edge-api)
if [[ $versiond_mode == ha ]]; then
    pull_services+=(devshard-postgres versiond2 versiond-router)
fi
if [[ $edge_mode == multi ]]; then
    pull_services+=(edge-api2 edge-api3 edge-api-router)
fi
run_interruptible "${compose[@]}" pull "${pull_services[@]}"

if [[ $versiond_mode == ha ]]; then
    postgres_container=$("${compose[@]}" ps --all --quiet devshard-postgres)
    [[ -n $postgres_container ]] || fail \
        "cannot preflight PostgreSQL migration: the existing container is missing; use the detached-volume recovery procedure"
    postgres_target_dir=${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}
    case $postgres_target_dir in
        /*) ;;
        *) postgres_target_dir="$script_dir/$postgres_target_dir" ;;
    esac
    run_interruptible env DOCKER_BIN="$docker_bin" \
        "$script_dir/devshard-postgres-migration-preflight.sh" \
        --source-container "$postgres_container" \
        --target-dir "$postgres_target_dir"

    echo "Migrating and starting devshard-postgres"
    active_service=devshard-postgres
    active_failure_strategy=stop
    if ! run_interruptible "${compose[@]}" \
        up -d --no-deps --wait \
        --wait-timeout "$(service_startup_timeout devshard-postgres)" \
        devshard-postgres; then
        fail "devshard-postgres did not become ready and will be stopped; its source volume and persistent target are preserved for recovery"
    fi
    active_service=
    active_failure_strategy=

    # Remove the first replacement from the v4 nginx config before Docker gives
    # its new container the old DNS name. Once one v5 replica is ready, install
    # HAProxy so active checks protect the remaining replacement.
    begin_traffic_barrier \
        versiond-router VERSIOND_HOSTS versiond2 \
        /docker-entrypoint.d/40-render-versiond-upstream.sh
    replace_service versiond2 "$(service_startup_timeout versiond2)" rollback
    replace_service versiond-router \
        "$(service_startup_timeout versiond-router)" rollback
    clear_traffic_barrier
    replace_service versiond "$(service_startup_timeout versiond)" rollback
else
    # The standard Quickstart topology has no versiond router or shared
    # PostgreSQL. Replace its only supervisor and restore the old image if the
    # v5 /readyz contract does not converge.
    replace_service versiond "$(service_startup_timeout versiond)" rollback
fi

if [[ $edge_mode == single ]]; then
    # There is no second Tier A replica in this topology, so preserve the old
    # image if the only edge-api replacement does not become ready.
    replace_service edge-api "$(service_startup_timeout edge-api)" rollback
else
    # First remove edge-api2 from the v4 nginx pool. Once it passes /readyz,
    # switch to HAProxy; active checks then isolate every later replacement.
    begin_traffic_barrier \
        edge-api-router EDGE_API_HOSTS edge-api2 \
        /docker-entrypoint.d/40-render-edge-api-upstream.sh
    replace_service edge-api2 "$(service_startup_timeout edge-api2)" rollback
    replace_service edge-api-router \
        "$(service_startup_timeout edge-api-router)" rollback
    clear_traffic_barrier
    replace_service edge-api3 "$(service_startup_timeout edge-api3)" stop
    replace_service edge-api "$(service_startup_timeout edge-api)" stop
fi

cleanup_rollback_tags
echo "Devshard v5 upgrade completed"
