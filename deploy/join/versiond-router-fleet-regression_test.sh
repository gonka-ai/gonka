#!/usr/bin/env bash
# shellcheck disable=SC2016,SC2034,SC2154,SC2329

# Focused executable reproducers for router-fleet failure windows found during
# the HA updater review. The scenarios execute the orchestration functions of
# versiond-router-fleet.sh against an in-memory Docker model: containers,
# generations, images and the versiond pool's readiness are files, the
# `docker` command is a shell function over them, and only HAProxy internals
# (route readiness, the runtime catalog, parent admission) are replaced at
# the function seam. No daemon is needed and a run finishes in seconds.
#
# Two modes:
#
#   --repro  succeeds only while the known unsafe outcome is reproduced;
#   --gate   asserts the desired invariant. Only --gate counts for acceptance
#            and only --gate runs in CI.

set -Eeuo pipefail

self=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/$(basename -- "${BASH_SOURCE[0]}")
script_dir=$(dirname -- "$self")
fleet_script=$script_dir/versiond-router-fleet.sh

scenarios=(
    RT-BOOTSTRAP-ZERO-READY
    RT-PG-DROP-AFTER-GATE
    RT-MAINT-GATE-RACE
    RT-SIGKILL-RETRY-PREVIOUS
    RT-CRASH-EXITED-CANDIDATE
    RT-MAINT-RETRY-EXACT
    RT-MAINT-RETRY-EXACT-ENV
    RT-MAINT-UNHEALTHY-CANDIDATE
    RT-ROLLBACK-RECORD-ISOLATION
    RT-ROLLBACK-RECORD-PROVENANCE
    RT-OFFLINE-RETRY-CACHED
    RT-CATALOG-DROP-AFTER-RESERVE
    RT-CATALOG-ZERO-READY-AT-START
    RT-STATUS-STATIC-ZERO-READY
    RT-ENDPOINT-MEMBERSHIP-ATOMIC
    RT-STATIC-HA-REMOVAL
    RT-DYNAMIC-CATALOG-REMOVAL
    RT-CATALOG-ROUTE-LOST-BY-CANDIDATE
    RT-PREVIOUS-NO-AUTORESTART
    RT-PG-DROP-BEFORE-COMMIT
    RT-PG-DROP-DURING-MAINTENANCE-COMMIT
    RT-REBOOT-BEFORE-STOP
    RT-SIGKILL-DURING-COMMIT-CLEANUP
    RT-DEAD-CANDIDATE-AT-COMMIT
    RT-COMMIT-CLEANUP-CONFIG-CHANGE
    RT-DOWN-CLEARS-COMMIT-MARKER
    RT-ROUTE-VANISHED-AT-COMMIT
    RT-COMMIT-CLEANUP-TAG-MOVED
    RT-ROUTE-REMOVED-AT-COMMIT-ALLOWED
    RT-NONHA-OWNER-DOWN
)

usage() {
    cat >&2 <<'EOF'
Usage:
  versiond-router-fleet-regression_test.sh --list
  versiond-router-fleet-regression_test.sh --repro [SCENARIO ...]
  versiond-router-fleet-regression_test.sh --gate  [SCENARIO ...]

--repro is green only when the known unsafe outcome is observed.
--gate asserts the acceptance invariant; it is the only mode that counts.
EOF
}

is_scenario() {
    local wanted=$1 item
    for item in "${scenarios[@]}"; do
        [[ $item != "$wanted" ]] || return 0
    done
    return 1
}

tmpdir=
model=
LAST_RC=0

cleanup_internal() {
    [[ -z ${tmpdir:-} ]] || rm -rf -- "$tmpdir"
}

# versiond-router-fleet.sh is an executable script rather than a sourceable
# library. Copy its declarations and functions and execute those exact
# functions. The sentinel is checked so a refactor cannot silently turn this
# into a test of a partial or empty file.
load_fleet_functions() {
    tmpdir=$(mktemp -d)
    trap cleanup_internal EXIT
    model=$tmpdir/model
    mkdir -p "$model/containers" "$model/images" "$model/volumes"

    local library=$tmpdir/versiond-router-fleet.lib.sh
    local config=$tmpdir/config.env
    local sentinel_count
    sentinel_count=$(grep -c '^command=${1:-}$' "$fleet_script")
    [[ $sentinel_count == 1 ]] || {
        echo "HARNESS_ERROR: cannot isolate fleet functions: command sentinel count is $sentinel_count" >&2
        return 2
    }
    sed \
        -e 's|^script_dir=.*|script_dir=${VERSIOND_ROUTER_TEST_SCRIPT_DIR:?}|' \
        -e 's/^declare -A /declare -gA /' \
        -e 's/^declare -a /declare -ga /' \
        -e '/^command=${1:-}$/,$d' \
        "$fleet_script" >"$library"

    cat >"$config" <<'EOF'
DOCKER_BIN=fake_docker
VERSIOND_ROUTER_IMAGE=registry.invalid/router:candidate
VERSIOND_ROUTER_FLEET_ID=test-fleet
VERSIOND_ROUTER_PROJECT_PREFIX=test-router
VERSIOND_ROUTER_FLEET_SLOTS="0 1 2"
VERSIOND_ROUTER_MIN_READY=2
VERSIOND_ROUTER_DRAIN_TIMEOUT_SECONDS=1
VERSIOND_ROUTER_START_TIMEOUT_SECONDS=1
VERSIOND_ROUTING_ACTIVATION_TIMEOUT_SECONDS=1
VERSIOND_ROUTER_PULL_POLICY=never
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true
VERSIOND_ROUTER_FRONT_NETWORK=test-front
VERSIOND_ROUTER_BACK_NETWORK=test-back
VERSIOND_ROUTER_METRICS_NETWORK=test-metrics
VERSIOND_POOL_HOST=versiond-pool
VERSIOND_LEGACY_HOST=versiond
VERSIOND_NON_HA_VERSIONS=
VERSIOND_VERSIONS=v4
VERSIOND_ROUTING_CATALOG_URL=
PROXY_ROUTER_CONTAINER=test-parent
EOF
    [[ -z ${EXTRA_CONFIG:-} ]] || printf '%s\n' "$EXTRA_CONFIG" >>"$config"

    # The model's knobs: what the versiond pool serves right now, what the
    # shared catalog cache holds, how a fresh candidate comes up.
    printf 'v4\n' >"$model/pool-serves"
    : >"$model/catalog"
    printf 'healthy\n' >"$model/candidate-health"
    printf 'sha256:candidate\n' >"$model/images/registry.invalid|router:candidate"
    printf '0\n' >"$model/sequence"
    printf 'desired-hash\n' >"$model/desired-hash"
    : >"$model/log"

    # command -v in the production preamble accepts shell functions.
    fake_docker() { model_docker "$@"; }
    export VERSIOND_ROUTER_TEST_SCRIPT_DIR=$script_dir
    export GONKA_CONFIG_ENV=$config
    # shellcheck disable=SC1090
    source "$library"
    install_model_seams
}

# --- the in-memory Docker model ---------------------------------------------

next_sequence() {
    local sequence
    sequence=$(<"$model/sequence")
    ((sequence += 1))
    printf '%s\n' "$sequence" >"$model/sequence"
    printf '%s\n' "$sequence"
}

# add_container NAME fleet=F slot=S state=running image=ID hash=H health=healthy
#               routes="v4 v9" env.KEY=VALUE ...
add_container() {
    local name=$1 field sequence
    shift
    sequence=$(next_sequence)
    {
        printf 'id=%s\n' "$name"
        printf 'created=2026-01-01T00:00:%09dZ\n' "$sequence"
        printf 'fleet=test-fleet\nslot=0\nstate=running\nimage=sha256:previous\n'
        printf 'hash=previous-hash\nhealth=healthy\nroutes=v4\ngeneration=g%s\n' "$sequence"
        printf 'env.VERSIOND_VERSIONS=v4\nenv.VERSIOND_NON_HA_VERSIONS=\n'
        printf 'env.VERSIOND_POOL_HOST=versiond-pool\nenv.VERSIOND_ROUTER_BACK_NETWORK_NAME=test-back\n'
        printf 'env.VERSIOND_LEGACY_HOST=versiond\nenv.VERSIOND_ROUTING_CATALOG_URL=\n'
        printf 'env.VERSIOND_ROUTER_ALLOW_COARSE_READINESS=false\nenv.HAPROXY_DNS_RESOLVER=127.0.0.11:53\n'
        for field in "$@"; do printf '%s\n' "$field"; done
    } >"$model/containers/$name"
}

container_field() {
    local id=$1 key=$2
    [[ -f $model/containers/$id ]] || return 1
    sed -n "s/^${key//./\\.}=//p" "$model/containers/$id" | tail -n 1
}

set_container_field() {
    local id=$1 key=$2 value=$3
    printf '%s=%s\n' "$key" "$value" >>"$model/containers/$id"
}

container_exists() {
    [[ -f $model/containers/$1 ]]
}

list_containers() {
    local fleet=$1 slot=${2:-} id
    for id in "$model"/containers/*; do
        [[ -f $id ]] || continue
        id=${id##*/}
        [[ $(container_field "$id" fleet) == "$fleet" ]] || continue
        [[ -z $slot || $(container_field "$id" slot) == "$slot" ]] || continue
        printf '%s\n' "$id"
    done
}

model_inspect() {
    local format=$1 id=$2 key value
    container_exists "$id" || {
        if [[ $id == test-parent && -f $model/parent ]]; then
            case $format in
                *ai.gonka.component*) printf 'proxy-router\n' ;;
                *) printf '\n' ;;
            esac
            return 0
        fi
        echo "Error response from daemon: No such object: $id" >&2
        return 1
    }
    case $format in
        '{{.Created}} {{.Id}} {{.State.Status}}')
            printf '%s %s %s\n' "$(container_field "$id" created)" "$id" "$(container_field "$id" state)"
            ;;
        '{{.State.Status}}') container_field "$id" state ;;
        '{{.Image}}') container_field "$id" image ;;
        *config-hash*) container_field "$id" hash ;;
        *ai.gonka.slot*) container_field "$id" slot ;;
        *ai.gonka.component*) printf 'versiond-router\n' ;;
        *'.Config.Env'*)
            # The last assignment of a key wins, as in the container files.
            sed -n 's/^env\.//p' "$model/containers/$id" | \
                awk -F= '{ value[$1] = $0; if (!($1 in order)) order[$1] = ++n } END { for (key in value) line[order[key]] = value[key]; for (i = 1; i <= n; i++) print line[i] }'
            ;;
        *'.State.Status}} {{if .State.Health}}'*'{{.Config.Image}}')
            printf '%s %s %s\n' "$(container_field "$id" state)" "$(container_field "$id" health)" \
                "$(container_field "$id" image)"
            ;;
        *'.State.Status}} {{if .State.Health}}'*)
            printf '%s %s\n' "$(container_field "$id" state)" "$(container_field "$id" health)"
            ;;
        *NetworkSettings.Networks*) printf '10.0.0.%s\n' "$(( 10 + $(container_field "$id" slot) ))" ;;
        *) printf '\n' ;;
    esac
}

model_docker() {
    printf '%s\n' "$*" >>"$model/log"
    local command=${1:-}
    shift || true
    case $command in
        ps)
            local fleet='' slot='' arg
            for arg in "$@"; do
                case $arg in
                    label=ai.gonka.fleet=*) fleet=${arg#label=ai.gonka.fleet=} ;;
                    label=ai.gonka.slot=*) slot=${arg#label=ai.gonka.slot=} ;;
                esac
            done
            list_containers "$fleet" "$slot"
            ;;
        inspect)
            local format='' ids=() status=0
            while (($# > 0)); do
                case $1 in
                    --format) format=$2; shift 2 ;;
                    --type) shift 2 ;;
                    *) ids+=("$1"); shift ;;
                esac
            done
            for id in "${ids[@]}"; do
                model_inspect "$format" "$id" || status=1
            done
            return "$status"
            ;;
        update)
            local policy=${1#--restart=} target=${*: -1}
            container_exists "$target" || return 1
            set_container_field "$target" restart "$policy"
            ;;
        start)
            container_exists "$1" || return 1
            set_container_field "$1" state running
            local health
            health=$(container_field "$1" start_health)
            set_container_field "$1" health "${health:-healthy}"
            ;;
        stop)
            local id=${*: -1}
            container_exists "$id" || return 1
            set_container_field "$id" state exited
            ;;
        rm)
            local id status=0
            for id in "$@"; do
                [[ $id != -* ]] || continue
                container_exists "$id" || { status=1; continue; }
                rm -f "$model/containers/$id"
            done
            return "$status"
            ;;
        image)
            [[ ${1:-} == inspect ]] || return 0
            shift
            local format='' reference
            while (($# > 0)); do
                case $1 in
                    --format) format=$2; shift 2 ;;
                    *) reference=$1; shift ;;
                esac
            done
            [[ -f "$model/images/${reference//\//|}" ]] || {
                echo "Error: No such image: $reference" >&2
                return 1
            }
            [[ -z $format ]] || cat "$model/images/${reference//\//|}"
            ;;
        volume)
            local verb=${1:-} name format='' labels=() arg
            shift || true
            case $verb in
                create)
                    while (($# > 0)); do
                        case $1 in
                            --label) labels+=("$2"); shift 2 ;;
                            *) name=$1; shift ;;
                        esac
                    done
                    printf '%s\n' "${labels[@]}" >"$model/volumes/$name"
                    ;;
                inspect)
                    while (($# > 0)); do
                        case $1 in
                            --format) format=$2; shift 2 ;;
                            *) name=$1; shift ;;
                        esac
                    done
                    [[ -f $model/volumes/$name ]] || { echo "Error response from daemon: get $name: no such volume" >&2; return 1; }
                    if [[ $format == *ai.gonka.* ]]; then
                        local key keys=() values=()
                        mapfile -t keys < <(grep -o 'ai\.gonka\.[A-Za-z0-9.-]*' <<<"$format")
                        for key in "${keys[@]}"; do
                            values+=("$(sed -n "s/^${key//./\\.}=//p" "$model/volumes/$name")")
                        done
                        printf '%s\n' "${values[*]}"
                    else
                        printf '%s\n' "$name"
                    fi
                    ;;
                rm) for name in "$@"; do rm -f "$model/volumes/$name"; done ;;
                ls) ;;
            esac
            ;;
        network) return 0 ;;
        *) return 0 ;;
    esac
}

# A Docker daemon restart: running containers come back when their policy
# is always or unless-stopped, stopped ones only with always.
model_reboot() {
    local id policy state
    for id in $(list_containers test-fleet) $(list_containers other-fleet); do
        policy=$(container_field "$id" restart)
        state=$(container_field "$id" state)
        case "$state/${policy:-always}" in
            running/always | running/unless-stopped) ;;
            running/*) set_container_field "$id" state exited ;;
            */always) set_container_field "$id" state running ;;
        esac
    done
}

# Compose is the one thing the model does not run: `up` records a generation
# on the candidate specification (image, configuration hash, environment from
# the current configuration, routes from the static declaration plus the
# shared catalog cache, health from the model's knob).
model_compose() {
    local slot=$1 generation=$2 verb=$3 name
    shift 3
    case $verb in
        config)
            case ${1:-} in
                --hash) printf 'router %s\n' "$(model_desired_hash)" ;;
                --format) printf '{"services":{"router":{"image":"%s"}},"desired":"%s","pool":"%s"}\n' "${VERSIOND_ROUTER_IMAGE:-}" "$(<"$model/desired-hash")" "${VERSIOND_POOL_HOST:-versiond-pool}" ;;
            esac
            ;;
        pull)
            [[ ! -f $model/registry-down ]] || { echo "simulated registry outage" >&2; return 1; }
            ;;
        up)
            name="gen-$slot-$generation"
            add_container "$name" "slot=$slot" "generation=$generation" \
                "image=$(<"$model/images/${VERSIOND_ROUTER_IMAGE//\//|}")" \
                "hash=$(model_desired_hash)" \
                "health=$(<"$model/candidate-health")" \
                "routes=$(candidate_declared_routes)" \
                "env.VERSIOND_VERSIONS=${VERSIOND_VERSIONS-v4 v5}" \
                "env.VERSIOND_NON_HA_VERSIONS=${VERSIOND_NON_HA_VERSIONS-v1 v2 v3}" \
                "env.VERSIOND_POOL_HOST=${VERSIOND_POOL_HOST:-versiond-pool}" \
                "env.VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS=${VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS:-false}" \
                "env.VERSIOND_POOL_ENDPOINTS_SHA256=${VERSIOND_POOL_ENDPOINTS_SHA256:-}"
            [[ $(<"$model/candidate-health") == healthy ]] || return 1
            ;;
    esac
}

# Compose's configuration hash covers the image reference.
model_desired_hash() {
    printf '%s@%s\n' "$(<"$model/desired-hash")" "${VERSIOND_ROUTER_IMAGE:-}"
}

candidate_declared_routes() {
    {
        printf '%s\n' "${VERSIOND_VERSIONS-v4 v5}" "${VERSIOND_NON_HA_VERSIONS-v1 v2 v3}" | tr ',;' '  ' | tr -s ' ' '\n'
        cat "$model/catalog"
    } | sed '/^$/d' | LC_ALL=C sort -u | paste -sd' ' -
}

# HAProxy internals replaced at the function seam: a route is ready on a
# generation when the generation runs healthy, declares the route and the
# versiond pool serves it.
install_model_seams() {
    slot_compose() { model_compose "$@"; }
    slot_route_ready() {
        local slot=$1 route=$2 id
        id=$(slot_id "$slot" 2>/dev/null) || return 1
        [[ $(container_field "$id" state) == running && $(container_field "$id" health) == healthy ]] || return 1
        [[ " $(container_field "$id" routes) " == *" $route "* ]] || return 1
        [[ " $(tr '\n' ' ' <"$model/pool-serves") " == *" $route "* ]]
    }
    slot_catalog_routes() {
        local id
        id=$(slot_id "$1") || return 1
        container_field "$id" routes | tr ' ' '\n'
    }
    prepare_slot_networks() { :; }
    resolve_metrics_network() { :; }
    parent_proxy_active() { [[ -f $model/parent ]]; }
    parent_diagnostic_available() { [[ -f $model/parent ]]; }
    parent_route_admitted() { return 0; }
    prepare_parent_slot_stop() { parent_drained_refs=(); }
    reset_parent_slot_health() { :; }
    repair_stale_parent_drain() { :; }
    placement_version_for_image() { printf '2\n'; }
    cache_protocol_for_image() { printf '1\n'; }
    sleep() { :; }
}

# Run a production orchestration function with its own errexit context. A
# production `fail` exits only this subshell, leaving the scenario able to
# decide whether that rejection satisfies the desired invariant.
run_capture() {
    set +e
    (
        set -Eeuo pipefail
        "$@"
    ) >"$tmpdir/capture.out" 2>&1
    LAST_RC=$?
    set -e
}

invariant_holds() {
    echo "INVARIANT_HOLDS: $*"
    return 0
}

invariant_violated() {
    echo "INVARIANT_VIOLATED: $*" >&2
    [[ ! -s $tmpdir/capture.out ]] || sed 's/^/  | /' "$tmpdir/capture.out" >&2
    return 1
}

# A fleet of three serving generations on the previous image.
seed_previous_fleet() {
    local slot
    for slot in 0 1 2; do
        add_container "prev-$slot" "slot=$slot" "$@"
    done
}

# Helpers used from hooks inside a traced run must never fail: a non-zero
# status there would fire the production ERR trap in the wrong place.
serving_id() {
    local slot=$1 id
    for id in $(list_containers test-fleet "$slot"); do
        [[ $(container_field "$id" state) != running ]] || printf '%s\n' "$id"
    done
    return 0
}

count_containers() {
    list_containers test-fleet "${1:-}" | wc -l
}

# --- scenarios ---------------------------------------------------------------

scenario_bootstrap_zero_ready() {
    load_fleet_functions || return $?
    : >"$model/pool-serves"
    run_capture fleet_apply
    if ((LAST_RC != 0)); then
        invariant_holds 'an absent fleet was not bootstrapped while declared route v4 had zero ready upstreams'
    else
        invariant_violated 'absent-fleet apply returned 0 while static route v4 had zero ready upstreams'
    fi
}

scenario_pg_drop_after_gate() {
    load_fleet_functions || return $?
    seed_previous_fleet
    # The pool loses v4 right after the last pre-stop gate of the first slot.
    eval "$(declare -f require_ready_reserve | sed '1s/require_ready_reserve/real_require_ready_reserve/')"
    require_ready_reserve() {
        real_require_ready_reserve "$@"
        : >"$model/pool-serves"
    }
    run_capture fleet_rollout
    if ((LAST_RC != 0)) && [[ $(serving_id 0) == prev-0 && $(count_containers 0) == 1 ]]; then
        invariant_holds 'rollout failed closed when v4 disappeared after the gate, and the previous generation of the slot serves again'
    else
        invariant_violated "rollout rc=$LAST_RC, slot 0 serving=$(serving_id 0 | paste -sd, -), containers=$(count_containers 0)"
    fi
}

scenario_maintenance_gate_race() {
    load_fleet_functions || return $?
    seed_previous_fleet "env.VERSIOND_POOL_HOST=old-pool"
    EXTRA=1
    eval "$(declare -f require_static_routes_served | sed '1s/require_static_routes_served/real_require_static_routes_served/')"
    require_static_routes_served() {
        real_require_static_routes_served
        : >"$model/pool-serves"
    }
    run_capture fleet_maintenance_rollout
    local slot ok=true
    for slot in 0 1 2; do
        [[ $(serving_id "$slot") == "prev-$slot" && $(count_containers "$slot") == 1 ]] || ok=false
    done
    if ((LAST_RC != 0)) && [[ $ok == true ]]; then
        invariant_holds 'maintenance failed when v4 vanished after the gate and every previous generation serves again'
    else
        invariant_violated "maintenance rc=$LAST_RC, fleet restored=$ok"
    fi
}

scenario_sigkill_retry_previous() {
    load_fleet_functions || return $?
    seed_previous_fleet
    # Killed right after slot 2 was stopped: no candidate exists yet. The
    # candidate image never becomes healthy.
    set_container_field prev-2 state exited
    printf 'unhealthy\n' >"$model/candidate-health"
    run_capture fleet_apply
    if ((LAST_RC != 0)) && [[ $(serving_id 2) == prev-2 && $(count_containers 2) == 1 ]]; then
        invariant_holds 'retry started the stopped previous generation, tried the candidate under its trap and put the previous generation back'
    else
        invariant_violated "apply rc=$LAST_RC, slot 2 serving=$(serving_id 2 | paste -sd, -), containers=$(count_containers 2)"
    fi
}

scenario_crash_exited_candidate() {
    load_fleet_functions || return $?
    seed_previous_fleet
    # Killed after the candidate of slot 2 was created and exited: the
    # candidate is unhealthy by nature, the previous generation is stopped.
    set_container_field prev-2 state exited
    add_container cand-2 slot=2 state=exited image=sha256:candidate hash=desired-hash@registry.invalid/router:candidate health=unhealthy start_health=unhealthy
    printf 'unhealthy\n' >"$model/candidate-health"
    run_capture fleet_apply
    if ((LAST_RC != 0)) && [[ $(serving_id 2) == prev-2 && $(count_containers 2) == 1 ]]; then
        invariant_holds 'retry removed the exited candidate and restored the previous generation instead of restarting the candidate'
    else
        invariant_violated "apply rc=$LAST_RC, slot 2 serving=$(serving_id 2 | paste -sd, -), containers=$(count_containers 2)"
    fi
}

# An interrupted maintenance replaced slot 0 (candidate serving, previous
# stopped next to it) and left slots 1 and 2 on the previous generation.
seed_interrupted_maintenance() {
    add_container prev-0 slot=0 state=exited "env.VERSIOND_POOL_HOST=old-pool"
    add_container prev-1 slot=1 "env.VERSIOND_POOL_HOST=old-pool"
    add_container prev-2 slot=2 "env.VERSIOND_POOL_HOST=old-pool"
    add_container cand-0 slot=0 image=sha256:candidate hash=desired-hash@registry.invalid/router:candidate "env.VERSIOND_POOL_HOST=new-pool"
}

scenario_maintenance_retry_exact() {
    load_fleet_functions || return $?
    seed_interrupted_maintenance
    # The retry's candidates never become healthy.
    printf 'unhealthy\n' >"$model/candidate-health"
    run_capture fleet_maintenance_rollout
    local slot ok=true
    for slot in 0 1 2; do
        [[ $(serving_id "$slot") == "prev-$slot" && $(count_containers "$slot") == 1 ]] || ok=false
    done
    if ((LAST_RC != 0)) && [[ $ok == true ]]; then
        invariant_holds 'maintenance retry failure restored every slot, including the one the interrupted run had replaced'
    else
        invariant_violated "maintenance rc=$LAST_RC, exact previous fleet restored=$ok: $(list_containers test-fleet | paste -sd, -)"
    fi
}

scenario_maintenance_retry_exact_env() {
    load_fleet_functions || return $?
    seed_interrupted_maintenance
    printf 'unhealthy\n' >"$model/candidate-health"
    run_capture fleet_maintenance_rollout
    local slot ok=true
    for slot in 0 1 2; do
        [[ $(container_field "$(serving_id "$slot")" env.VERSIOND_POOL_HOST) == old-pool ]] || ok=false
    done
    if ((LAST_RC != 0)) && [[ $ok == true ]]; then
        invariant_holds 'every restored generation carries its own previous environment, not the candidate configuration'
    else
        invariant_violated "maintenance rc=$LAST_RC, restored environments: $(for slot in 0 1 2; do container_field "$(serving_id "$slot")" env.VERSIOND_POOL_HOST; done | paste -sd, -)"
    fi
}

scenario_maintenance_unhealthy_candidate() {
    load_fleet_functions || return $?
    # An interrupted maintenance left slot 1 with an unhealthy candidate on
    # the candidate specification and its previous generation stopped.
    add_container prev-0 slot=0 "env.VERSIOND_POOL_HOST=old-pool"
    add_container prev-1 slot=1 state=exited "env.VERSIOND_POOL_HOST=old-pool"
    add_container prev-2 slot=2 "env.VERSIOND_POOL_HOST=old-pool"
    add_container cand-1 slot=1 image=sha256:candidate hash=desired-hash@registry.invalid/router:candidate health=unhealthy "env.VERSIOND_POOL_HOST=new-pool"
    printf 'unhealthy\n' >"$model/candidate-health"
    run_capture fleet_maintenance_rollout
    if ((LAST_RC != 0)) && [[ $(serving_id 1) == prev-1 && $(count_containers 1) == 1 ]] && ! container_exists cand-1; then
        invariant_holds 'the unhealthy candidate was removed and its previous generation put back when the retry failed'
    else
        invariant_violated "maintenance rc=$LAST_RC, slot 1 serving=$(serving_id 1 | paste -sd, -), containers=$(list_containers test-fleet 1 | paste -sd, -)"
    fi
}

scenario_rollback_record_isolation() {
    load_fleet_functions || return $?
    seed_previous_fleet
    # Another fleet on the same daemon uses the same slot numbers.
    add_container other-0 slot=0 fleet=other-fleet
    add_container other-1 slot=1 fleet=other-fleet
    printf 'unhealthy\n' >"$model/candidate-health"
    run_capture fleet_rollout
    if ((LAST_RC != 0)) && [[ $(container_field other-0 state) == running && $(container_field other-1 state) == running ]] && \
        [[ $(list_containers other-fleet | wc -l) == 2 && $(serving_id 0) == prev-0 ]]; then
        invariant_holds 'a failed rollout touched only the containers of its own fleet'
    else
        invariant_violated "rollout rc=$LAST_RC, other fleet: $(list_containers other-fleet | paste -sd, -)"
    fi
}

scenario_rollback_record_provenance() {
    load_fleet_functions || return $?
    seed_previous_fleet
    # Slot 1: a healthy generation on the candidate specification serves,
    # with a stale stopped generation from an older operation next to it.
    set_container_field prev-1 state exited
    add_container serving-1 slot=1 image=sha256:candidate hash=desired-hash@registry.invalid/router:candidate
    run_capture fleet_apply
    if ((LAST_RC == 0)) && [[ $(serving_id 1) == serving-1 && $(count_containers 1) == 1 ]] && ! container_exists prev-1; then
        invariant_holds 'a serving candidate was committed; the stale record was removed, not restored over it'
    else
        invariant_violated "apply rc=$LAST_RC, slot 1 serving=$(serving_id 1 | paste -sd, -), containers=$(list_containers test-fleet 1 | paste -sd, -)"
    fi
}

scenario_offline_retry_cached() {
    load_fleet_functions || return $?
    pull_policy=always
    seed_previous_fleet image=sha256:candidate hash=desired-hash@registry.invalid/router:candidate
    set_container_field prev-2 state exited
    : >"$model/registry-down"
    run_capture fleet_apply
    if ((LAST_RC == 0)) && [[ $(serving_id 2) == prev-2 ]]; then
        invariant_holds 'a registry outage did not block recovery because the candidate image was cached'
    else
        invariant_violated "apply rc=$LAST_RC with the registry down and the candidate cached"
    fi
}

scenario_catalog_drop_after_reserve() {
    load_fleet_functions || return $?
    printf 'v9\n' >"$model/catalog"
    printf 'v4\nv9\n' >"$model/pool-serves"
    seed_previous_fleet "routes=v4 v9"
    discover_expected_routes
    # The pool loses catalog route v9 after the reserve check; the candidate
    # declares it from the shared cache but cannot serve it.
    eval "$(declare -f require_ready_reserve | sed '1s/require_ready_reserve/real_require_ready_reserve/')"
    require_ready_reserve() {
        real_require_ready_reserve "$@"
        printf 'v4\n' >"$model/pool-serves"
    }
    run_capture fleet_rollout
    if ((LAST_RC != 0)) && [[ $(serving_id 0) == prev-0 && $(count_containers 0) == 1 ]]; then
        invariant_holds 'a candidate that could not serve protected catalog route v9 was rejected and the previous generation restored'
    else
        invariant_violated "rollout rc=$LAST_RC, slot 0 serving=$(serving_id 0 | paste -sd, -)"
    fi
}

scenario_catalog_zero_ready_at_start() {
    load_fleet_functions || return $?
    : >"$model/parent"
    seed_previous_fleet "routes=v4 v9"
    discover_expected_routes
    run_capture fleet_status
    local status_rc=$LAST_RC
    grep -q 'ROUTE v9 .*catalog .*ready 0/3' "$tmpdir/capture.out" || status_rc=0
    run_capture verify_parent_fleet_admission
    if ((status_rc != 0 && LAST_RC != 0)); then
        invariant_holds 'a catalog route every slot declares but nobody serves is reported by status and fails admission'
    else
        invariant_violated "status rc=$status_rc, admission rc=$LAST_RC for catalog route v9 with zero ready routers"
    fi
}

scenario_status_static_zero_ready() {
    EXTRA_CONFIG='VERSIOND_VERSIONS="v4 v5"' load_fleet_functions || return $?
    : >"$model/parent"
    seed_previous_fleet "routes=v4 v5" "env.VERSIOND_VERSIONS=v4 v5"
    discover_expected_routes
    run_capture fleet_status
    if ((LAST_RC != 0)) && grep -q 'required version v5 is served by no ready router' "$tmpdir/capture.out"; then
        invariant_holds 'status is red while required static HA route v5 has zero ready routers'
    else
        invariant_violated "status rc=$LAST_RC with v5 required and unserved"
    fi
}

scenario_endpoint_membership_atomic() {
    local endpoints
    endpoints=$(mktemp)
    printf '[{"id":"a","host":"10.0.0.1"},{"id":"b","host":"10.0.0.2"}]\n' >"$endpoints"
    EXTRA_CONFIG="VERSIOND_POOL_ENDPOINTS_FILE=$endpoints" load_fleet_functions || return $?
    rm -f "$endpoints"
    seed_previous_fleet "env.VERSIOND_POOL_ENDPOINTS_SHA256=0000000000000000000000000000000000000000000000000000000000000000"
    run_capture fleet_rollout
    if ((LAST_RC != 0)) && grep -q 'use maintenance-rollout' "$tmpdir/capture.out" && [[ $(count_containers) == 3 ]]; then
        invariant_holds 'a pool membership change is refused as a rolling replacement and routed through maintenance'
    else
        invariant_violated "rollout rc=$LAST_RC on a membership change: $(tail -n 3 "$tmpdir/capture.out" | paste -sd' ' -)"
    fi
}

scenario_static_ha_removal() {
    load_fleet_functions || return $?
    printf 'v4\nv5\n' >"$model/pool-serves"
    seed_previous_fleet "routes=v4 v5" "env.VERSIOND_VERSIONS=v4 v5"
    discover_expected_routes
    run_capture fleet_rollout
    local slot ok=true
    for slot in 0 1 2; do
        [[ $(container_field "$(serving_id "$slot")" env.VERSIOND_VERSIONS) == v4 && $(count_containers "$slot") == 1 ]] || ok=false
    done
    if ((LAST_RC == 0)) && [[ $ok == true ]]; then
        invariant_holds 'withdrawing v5 from the static declaration rolled the fleet; the withdrawn route did not block its own removal'
    else
        invariant_violated "rollout rc=$LAST_RC, fleet on v4 only=$ok"
    fi
}

scenario_dynamic_catalog_removal() {
    EXTRA_CONFIG='VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS=true' load_fleet_functions || return $?
    printf 'v4\nv9\n' >"$model/pool-serves"
    seed_previous_fleet "routes=v4 v9"
    # The catalog removed v9; the candidates' shared cache no longer lists it.
    : >"$model/catalog"
    discover_expected_routes
    run_capture fleet_rollout
    local slot ok=true
    for slot in 0 1 2; do
        [[ $(container_field "$(serving_id "$slot")" env.VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS) == true ]] || ok=false
        [[ " $(container_field "$(serving_id "$slot")" routes) " != *" v9 "* ]] || ok=false
    done
    if ((LAST_RC == 0)) && [[ $ok == true ]]; then
        invariant_holds 'a catalog route removed with removals allowed is not demanded of the candidates, which carry the removal policy'
    else
        invariant_violated "rollout rc=$LAST_RC, candidates without v9 and with the policy=$ok"
    fi
}

scenario_catalog_route_lost_by_candidate() {
    load_fleet_functions || return $?
    printf 'v4\nv9\n' >"$model/pool-serves"
    seed_previous_fleet "routes=v4 v9"
    # Removals are not allowed, yet the candidate comes up without v9: its
    # cache is gone. It must not be admitted.
    : >"$model/catalog"
    discover_expected_routes
    run_capture fleet_rollout
    if ((LAST_RC != 0)) && [[ $(serving_id 0) == prev-0 && $(count_containers 0) == 1 ]]; then
        invariant_holds 'a candidate that silently lost protected catalog route v9 was rejected and the previous generation restored'
    else
        invariant_violated "rollout rc=$LAST_RC, slot 0 serving=$(serving_id 0 | paste -sd, -)"
    fi
}

scenario_previous_no_autorestart() {
    load_fleet_functions || return $?
    seed_previous_fleet restart=always
    # Observe the kept generation right after it was stopped: a daemon
    # restart must not start it next to its replacement.
    eval "$(declare -f create_generation | sed '1s/create_generation/real_create_generation/')"
    create_generation() {
        container_field prev-0 restart >"$model/restart-while-kept"
        real_create_generation "$@"
    }
    printf 'unhealthy\n' >"$model/candidate-health"
    run_capture fleet_rollout
    if ((LAST_RC != 0)) && [[ $(<"$model/restart-while-kept") == unless-stopped && $(container_field prev-0 restart) == always && $(serving_id 0) == prev-0 ]]; then
        invariant_holds 'the kept previous generation is unless-stopped while kept (a reboot before the stop brings it back, after the stop leaves it), and always again when restored'
    else
        invariant_violated "rollout rc=$LAST_RC, restart while kept=$(<"$model/restart-while-kept"), after restore=$(container_field prev-0 restart)"
    fi
}

scenario_pg_drop_before_commit() {
    load_fleet_functions || return $?
    seed_previous_fleet
    # The pool loses v4 after the candidate of slot 0 passed its route gate
    # and before the previous generation would be removed.
    eval "$(declare -f wait_slot_routes | sed '1s/wait_slot_routes/real_wait_slot_routes/')"
    wait_slot_routes() {
        real_wait_slot_routes "$@" || return $?
        [[ $1 != 0 ]] || : >"$model/pool-serves"
    }
    run_capture fleet_rollout
    if ((LAST_RC != 0)) && container_exists prev-0 && [[ $(serving_id 0) == prev-0 && $(count_containers 0) == 1 ]]; then
        invariant_holds 'v4 vanishing between the route gate and the commit did not cost the exact previous generation'
    else
        invariant_violated "rollout rc=$LAST_RC, prev-0 exists=$(container_exists prev-0 && echo yes || echo no), slot 0 serving=$(serving_id 0 | paste -sd, -)"
    fi
}

scenario_pg_drop_during_maintenance_commit() {
    EXTRA_CONFIG='VERSIOND_POOL_HOST=new-pool' load_fleet_functions || return $?
    seed_previous_fleet "env.VERSIOND_POOL_HOST=old-pool"
    # The pool loses v4 right after the first previous generation was
    # removed: past the commit point, nothing may be rolled back.
    eval "$(declare -f remove_previous_generation | sed '1s/remove_previous_generation/real_remove_previous_generation/')"
    remove_previous_generation() {
        real_remove_previous_generation "$@" || return $?
        : >"$model/pool-serves"
    }
    run_capture fleet_maintenance_rollout
    local slot ok=true
    for slot in 0 1 2; do
        [[ $(count_containers "$slot") == 1 && $(serving_id "$slot") == gen-$slot-* ]] || ok=false
    done
    if ((LAST_RC == 0)) && [[ $ok == true ]]; then
        invariant_holds 'an outage during the commit cleanup neither rolled back nor emptied a slot; every slot keeps its serving candidate'
    else
        invariant_violated "maintenance rc=$LAST_RC, slots: $(for slot in 0 1 2; do printf '%s=%s ' "$slot" "$(list_containers test-fleet "$slot" | paste -sd, -)"; done)"
    fi
}

scenario_reboot_before_stop() {
    load_fleet_functions || return $?
    seed_previous_fleet
    # The daemon restarts after the policy change and before the stop; the
    # fleet process dies with it.
    eval "$(declare -f model_docker | sed '1s/model_docker/real_model_docker/')"
    model_docker() {
        if [[ ${1:-} == stop && ! -f $model/rebooted ]]; then
            : >"$model/rebooted"
            model_reboot
            exit 137
        fi
        real_model_docker "$@"
    }
    run_capture fleet_rollout
    if [[ $(container_field prev-0 state) == running && $(count_containers 0) == 1 ]]; then
        invariant_holds 'a daemon restart between the policy change and the stop brings the only generation of the slot back'
    else
        invariant_violated "after the reboot slot 0 is $(container_field prev-0 state) with restart=$(container_field prev-0 restart)"
    fi
}

scenario_sigkill_during_commit_cleanup() {
    EXTRA_CONFIG='VERSIOND_POOL_HOST=new-pool' load_fleet_functions || return $?
    seed_previous_fleet "env.VERSIOND_POOL_HOST=old-pool"
    # Killed after the first previous generation was removed; on the retry
    # the candidate of slot 1 is dead and the pool has lost v4.
    eval "$(declare -f remove_previous_generation | sed '1s/remove_previous_generation/real_remove_previous_generation/')"
    remove_previous_generation() {
        real_remove_previous_generation "$@" || return $?
        exit 137
    }
    run_capture fleet_maintenance_rollout
    local killed_rc=$LAST_RC
    unset -f remove_previous_generation
    eval "$(declare -f real_remove_previous_generation | sed '1s/real_remove_previous_generation/remove_previous_generation/')"
    set_container_field "$(serving_id 1)" health unhealthy
    run_capture fleet_apply
    local slot ok=true
    for slot in 0 1 2; do
        [[ $(count_containers "$slot") == 1 && $(container_field "$(serving_id "$slot")" env.VERSIOND_POOL_HOST) == new-pool ]] || ok=false
    done
    if ((killed_rc != 0 && LAST_RC == 0)) && [[ $ok == true && ! -f $model/volumes/gonka-versiond-router-commit-test-fleet ]]; then
        invariant_holds 'the retry rolled every slot forward to the committed placement and removed the marker; nothing went back to the old contract'
    else
        invariant_violated "killed rc=$killed_rc, retry rc=$LAST_RC, slots: $(for slot in 0 1 2; do printf '%s=%s/%s ' "$slot" "$(serving_id "$slot" | paste -sd, -)" "$(container_field "$(serving_id "$slot")" env.VERSIOND_POOL_HOST)"; done), marker=$([[ -f $model/volumes/gonka-versiond-router-commit-test-fleet ]] && echo present || echo absent)"
    fi
}

scenario_dead_candidate_at_commit() {
    load_fleet_functions || return $?
    seed_previous_fleet
    # The candidate of slot 0 dies after its route gate, before the commit.
    eval "$(declare -f wait_slot_routes | sed '1s/wait_slot_routes/real_wait_slot_routes/')"
    wait_slot_routes() {
        local candidate
        real_wait_slot_routes "$@" || return $?
        if [[ $1 == 0 ]]; then
            candidate=$(serving_id 0)
            set_container_field "$candidate" health unhealthy
        fi
        return 0
    }
    run_capture fleet_rollout
    if ((LAST_RC != 0)) && [[ $(serving_id 0) == prev-0 && $(count_containers 0) == 1 ]]; then
        invariant_holds 'a candidate that died before the commit was not committed; the previous generation serves again'
    else
        invariant_violated "rollout rc=$LAST_RC, slot 0 serving=$(serving_id 0 | paste -sd, -), containers=$(count_containers 0)"
    fi
}

# A maintenance rollout killed after its commit point, with the previous
# generation of slot 0 already removed and the others still there.
seed_killed_commit_cleanup() {
    seed_previous_fleet "env.VERSIOND_POOL_HOST=old-pool"
    eval "$(declare -f remove_previous_generation | sed '1s/remove_previous_generation/real_remove_previous_generation/')"
    remove_previous_generation() {
        real_remove_previous_generation "$@" || return $?
        exit 137
    }
    run_capture fleet_maintenance_rollout
    unset -f remove_previous_generation
    eval "$(declare -f real_remove_previous_generation | sed '1s/real_remove_previous_generation/remove_previous_generation/')"
}

scenario_commit_cleanup_config_change() {
    EXTRA_CONFIG='VERSIOND_POOL_HOST=new-pool' load_fleet_functions || return $?
    seed_killed_commit_cleanup
    # The configuration changes with the same image before the retry.
    VERSIOND_POOL_HOST=other-pool
    run_capture fleet_apply
    local refused_rc=$LAST_RC refused_ok=true
    container_exists prev-1 && container_exists prev-2 || refused_ok=false
    [[ -f $model/volumes/gonka-versiond-router-commit-test-fleet ]] || refused_ok=false
    grep -q 'configuration has changed since' "$tmpdir/capture.out" || refused_ok=false
    # With the committed configuration back, the cleanup finishes.
    VERSIOND_POOL_HOST=new-pool
    run_capture fleet_apply
    local slot ok=true
    for slot in 0 1 2; do
        [[ $(count_containers "$slot") == 1 && $(container_field "$(serving_id "$slot")" env.VERSIOND_POOL_HOST) == new-pool ]] || ok=false
    done
    if ((refused_rc != 0 && LAST_RC == 0)) && [[ $refused_ok == true && $ok == true && ! -f $model/volumes/gonka-versiond-router-commit-test-fleet ]]; then
        invariant_holds 'a changed configuration is refused before any record is removed; the committed configuration finishes the cleanup'
    else
        invariant_violated "refused rc=$refused_rc (records kept=$refused_ok), retry rc=$LAST_RC, slots ok=$ok, marker=$([[ -f $model/volumes/gonka-versiond-router-commit-test-fleet ]] && echo present || echo absent)"
    fi
}

scenario_down_clears_commit_marker() {
    EXTRA_CONFIG='VERSIOND_POOL_HOST=new-pool' load_fleet_functions || return $?
    seed_killed_commit_cleanup
    collect_cleanup_networks() { cleanup_networks=(); }
    require_networks_detached_from_main_stack() { :; }
    run_capture fleet_down
    local down_rc=$LAST_RC
    # A new image is bootstrapped on the torn-down host.
    printf 'sha256:candidate2\n' >"$model/images/registry.invalid|router:candidate"
    run_capture fleet_apply
    if ((down_rc == 0 && LAST_RC == 0)) && [[ $(count_containers) == 3 && ! -f $model/volumes/gonka-versiond-router-commit-test-fleet ]]; then
        invariant_holds 'down --maintenance removes a pending commit marker; a new image bootstraps cleanly afterwards'
    else
        invariant_violated "down rc=$down_rc, apply rc=$LAST_RC, containers=$(count_containers), marker=$([[ -f $model/volumes/gonka-versiond-router-commit-test-fleet ]] && echo present || echo absent)"
    fi
}

scenario_route_vanished_at_commit() {
    load_fleet_functions || return $?
    printf 'v9\n' >"$model/catalog"
    printf 'v4\nv9\n' >"$model/pool-serves"
    seed_previous_fleet "routes=v4 v9"
    discover_expected_routes
    # Protected catalog route v9 disappears from the candidate's catalog
    # after its route gate; removals are not allowed.
    eval "$(declare -f wait_slot_routes | sed '1s/wait_slot_routes/real_wait_slot_routes/')"
    wait_slot_routes() {
        local candidate
        real_wait_slot_routes "$@" || return $?
        if [[ $1 == 0 ]]; then
            candidate=$(serving_id 0)
            set_container_field "$candidate" routes v4
        fi
        return 0
    }
    run_capture fleet_rollout
    if ((LAST_RC != 0)) && container_exists prev-0 && [[ $(serving_id 0) == prev-0 && $(count_containers 0) == 1 ]]; then
        invariant_holds 'a protected route that vanished before the commit was not skipped; the previous generation serves again'
    else
        invariant_violated "rollout rc=$LAST_RC, prev-0 exists=$(container_exists prev-0 && echo yes || echo no), slot 0 serving=$(serving_id 0 | paste -sd, -)"
    fi
}

scenario_commit_cleanup_tag_moved() {
    EXTRA_CONFIG='VERSIOND_POOL_HOST=new-pool' load_fleet_functions || return $?
    seed_killed_commit_cleanup
    # The same tag resolves to another image before the retry.
    printf 'sha256:candidate2\n' >"$model/images/registry.invalid|router:candidate"
    run_capture fleet_apply
    local refused_rc=$LAST_RC refused_ok=true
    container_exists prev-1 && container_exists prev-2 || refused_ok=false
    [[ -f $model/volumes/gonka-versiond-router-commit-test-fleet ]] || refused_ok=false
    grep -q 'the tag moved' "$tmpdir/capture.out" || refused_ok=false
    # The operator pins the committed digest, as the message says.
    printf 'sha256:candidate\n' >"$model/images/registry.invalid|router@sha256:pinned"
    VERSIOND_ROUTER_IMAGE=registry.invalid/router@sha256:pinned
    run_capture fleet_apply
    local slot ok=true
    for slot in 0 1 2; do
        [[ $(count_containers "$slot") == 1 && $(container_field "$(serving_id "$slot")" image) == sha256:candidate ]] || ok=false
    done
    if ((refused_rc != 0 && LAST_RC == 0)) && [[ $refused_ok == true && $ok == true && ! -f $model/volumes/gonka-versiond-router-commit-test-fleet ]]; then
        invariant_holds 'a moved tag is refused before any record is removed; the committed digest, pinned, finishes the cleanup'
    else
        invariant_violated "refused rc=$refused_rc (records kept=$refused_ok), retry rc=$LAST_RC, slots ok=$ok"
    fi
}

scenario_route_removed_at_commit_allowed() {
    EXTRA_CONFIG='VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS=true' load_fleet_functions || return $?
    printf 'v9\n' >"$model/catalog"
    printf 'v4\nv9\n' >"$model/pool-serves"
    seed_previous_fleet "routes=v4 v9"
    discover_expected_routes
    # The catalog removes v9 after the route gate of slot 0; removals are
    # allowed, so the rollout must carry on through the other slots.
    eval "$(declare -f wait_slot_routes | sed '1s/wait_slot_routes/real_wait_slot_routes/')"
    wait_slot_routes() {
        local candidate
        real_wait_slot_routes "$@" || return $?
        if [[ $1 == 0 ]]; then
            candidate=$(serving_id 0)
            set_container_field "$candidate" routes v4
            : >"$model/catalog"
        fi
        return 0
    }
    run_capture fleet_rollout
    local slot ok=true
    for slot in 0 1 2; do
        [[ $(count_containers "$slot") == 1 && $(serving_id "$slot") == gen-$slot-* ]] || ok=false
    done
    if ((LAST_RC == 0)) && [[ $ok == true ]]; then
        invariant_holds 'a catalog removal taking effect at the first commit does not stop the rollout at the next slot'
    else
        invariant_violated "rollout rc=$LAST_RC, slots: $(for slot in 0 1 2; do printf '%s=%s ' "$slot" "$(list_containers test-fleet "$slot" | paste -sd, -)"; done)"
    fi
}

scenario_nonha_owner_down() {
    EXTRA_CONFIG='VERSIOND_NON_HA_VERSIONS=v1' load_fleet_functions || return $?
    seed_previous_fleet "routes=v1 v4" "env.VERSIOND_NON_HA_VERSIONS=v1"
    discover_expected_routes
    run_capture require_static_routes_served
    local gate_rc=$LAST_RC
    run_capture fleet_rollout
    if ((gate_rc == 0 && LAST_RC == 0)) && [[ -z ${required_routes[v1]-} ]]; then
        invariant_holds 'a pinned non-HA version whose owner is down neither blocks the gate nor the rollout'
    else
        invariant_violated "gate rc=$gate_rc, rollout rc=$LAST_RC while pinned v1 has no ready router"
    fi
}

run_internal() {
    local scenario=$1
    case $scenario in
        RT-BOOTSTRAP-ZERO-READY) scenario_bootstrap_zero_ready ;;
        RT-PG-DROP-AFTER-GATE) scenario_pg_drop_after_gate ;;
        RT-MAINT-GATE-RACE) scenario_maintenance_gate_race ;;
        RT-SIGKILL-RETRY-PREVIOUS) scenario_sigkill_retry_previous ;;
        RT-CRASH-EXITED-CANDIDATE) scenario_crash_exited_candidate ;;
        RT-MAINT-RETRY-EXACT) scenario_maintenance_retry_exact ;;
        RT-MAINT-RETRY-EXACT-ENV) scenario_maintenance_retry_exact_env ;;
        RT-MAINT-UNHEALTHY-CANDIDATE) scenario_maintenance_unhealthy_candidate ;;
        RT-ROLLBACK-RECORD-ISOLATION) scenario_rollback_record_isolation ;;
        RT-ROLLBACK-RECORD-PROVENANCE) scenario_rollback_record_provenance ;;
        RT-OFFLINE-RETRY-CACHED) scenario_offline_retry_cached ;;
        RT-CATALOG-DROP-AFTER-RESERVE) scenario_catalog_drop_after_reserve ;;
        RT-CATALOG-ZERO-READY-AT-START) scenario_catalog_zero_ready_at_start ;;
        RT-STATUS-STATIC-ZERO-READY) scenario_status_static_zero_ready ;;
        RT-ENDPOINT-MEMBERSHIP-ATOMIC) scenario_endpoint_membership_atomic ;;
        RT-STATIC-HA-REMOVAL) scenario_static_ha_removal ;;
        RT-DYNAMIC-CATALOG-REMOVAL) scenario_dynamic_catalog_removal ;;
        RT-CATALOG-ROUTE-LOST-BY-CANDIDATE) scenario_catalog_route_lost_by_candidate ;;
        RT-PREVIOUS-NO-AUTORESTART) scenario_previous_no_autorestart ;;
        RT-PG-DROP-BEFORE-COMMIT) scenario_pg_drop_before_commit ;;
        RT-PG-DROP-DURING-MAINTENANCE-COMMIT) scenario_pg_drop_during_maintenance_commit ;;
        RT-REBOOT-BEFORE-STOP) scenario_reboot_before_stop ;;
        RT-SIGKILL-DURING-COMMIT-CLEANUP) scenario_sigkill_during_commit_cleanup ;;
        RT-DEAD-CANDIDATE-AT-COMMIT) scenario_dead_candidate_at_commit ;;
        RT-COMMIT-CLEANUP-CONFIG-CHANGE) scenario_commit_cleanup_config_change ;;
        RT-DOWN-CLEARS-COMMIT-MARKER) scenario_down_clears_commit_marker ;;
        RT-ROUTE-VANISHED-AT-COMMIT) scenario_route_vanished_at_commit ;;
        RT-COMMIT-CLEANUP-TAG-MOVED) scenario_commit_cleanup_tag_moved ;;
        RT-ROUTE-REMOVED-AT-COMMIT-ALLOWED) scenario_route_removed_at_commit_allowed ;;
        RT-NONHA-OWNER-DOWN) scenario_nonha_owner_down ;;
        *) echo "HARNESS_ERROR: unknown scenario $scenario" >&2; return 2 ;;
    esac
}

if [[ ${1:-} == --internal ]]; then
    [[ $# == 2 ]] || exit 2
    set +e
    run_internal "$2"
    exit $?
fi

case ${1:-} in
    --list)
        printf '%s\n' "${scenarios[@]}"
        exit 0
        ;;
    --repro | --gate) mode=$1; shift ;;
    *) usage; exit 2 ;;
esac

if (($# == 0)); then
    selected=("${scenarios[@]}")
else
    selected=("$@")
fi

failed=0
for scenario in "${selected[@]}"; do
    is_scenario "$scenario" || {
        echo "versiond-router-fleet-regression_test: unknown scenario $scenario" >&2
        failed=1
        continue
    }
    output=$(mktemp)
    set +e
    bash "$self" --internal "$scenario" >"$output" 2>&1
    status=$?
    set -e
    if ! grep -q '^INVARIANT_\(HOLDS\|VIOLATED\):' "$output"; then
        cat "$output" >&2
        echo "$scenario HARNESS_ERROR (exit $status)" >&2
        failed=1
    elif [[ $mode == --gate ]]; then
        if ((status == 0)); then
            echo "$scenario GATE PASS"
        else
            cat "$output" >&2
            echo "$scenario GATE RED" >&2
            failed=1
        fi
    else
        if ((status != 0)); then
            echo "$scenario REPRO PASS"
        else
            cat "$output" >&2
            echo "$scenario REPRO STALE: invariant now holds; move this scenario to --gate" >&2
            failed=1
        fi
    fi
    rm -f -- "$output"
done

if ((failed == 0)); then
    echo "versiond-router-fleet-regression_test: $mode ok"
fi
exit "$failed"
