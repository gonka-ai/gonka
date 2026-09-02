#!/usr/bin/env bash
# shellcheck disable=SC2016,SC2034,SC2154,SC2329

# Focused executable reproducers for router-fleet failure windows found during
# the HA updater review.  The tests execute the orchestration functions from
# versiond-router-fleet.sh with a deterministic in-memory Docker model, so they
# do not need a daemon and finish in seconds.
#
# These regressions intentionally have two modes:
#
#   --repro  succeeds only while the current bug is reproduced;
#   --gate   asserts the desired invariant and is intentionally RED until the
#            corresponding production fix lands.
#
# Do not add --gate to CI while any listed scenario is still open.  Once a bug
# is fixed, invert its CI use by running that scenario through --gate; --repro
# will then fail and make stale reproducer expectations visible.

set -Eeuo pipefail

self=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)/$(basename -- "${BASH_SOURCE[0]}")
script_dir=$(dirname -- "$self")
fleet_script=$script_dir/versiond-router-fleet.sh

scenarios=(
    RT-BOOTSTRAP-ZERO-READY
    RT-PG-DROP-AFTER-GATE
    RT-MAINT-GATE-RACE
    RT-SIGKILL-RETRY-PREVIOUS
    RT-MAINT-RETRY-EXACT
    RT-ROLLBACK-TAG-ISOLATION
    RT-OFFLINE-RETRY-CACHED
)

usage() {
    cat >&2 <<'EOF'
Usage:
  versiond-router-fleet-regression_test.sh --list
  versiond-router-fleet-regression_test.sh --repro [SCENARIO ...]
  versiond-router-fleet-regression_test.sh --gate  [SCENARIO ...]

--repro is green only when the known unsafe outcome is observed.
--gate asserts the acceptance invariant and is expected to be red until fixed.
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
LAST_RC=0

cleanup_internal() {
    [[ -z ${tmpdir:-} ]] || rm -rf -- "$tmpdir"
}

# versiond-router-fleet.sh is an executable script rather than a sourceable
# library.  For these focused tests, copy only its declarations/functions and
# execute those exact functions.  The sentinel is deliberately checked so a
# refactor cannot silently turn this into a test of a partial or empty file.
load_fleet_functions() {
    tmpdir=$(mktemp -d)
    trap cleanup_internal EXIT

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

    # command -v in the production preamble accepts shell functions.  Each
    # scenario replaces this placeholder with its own stateful Docker model.
    fake_docker() { return 0; }
    export VERSIOND_ROUTER_TEST_SCRIPT_DIR=$script_dir
    export GONKA_CONFIG_ENV=$config
    # shellcheck disable=SC1090
    source "$library"
}

# Run a production orchestration function with its own errexit context.  A
# production `fail` exits only this subshell, leaving the scenario able to
# decide whether that rejection satisfies the desired invariant.
run_capture() {
    set +e
    (
        set -Eeuo pipefail
        "$@"
    )
    LAST_RC=$?
    set -e
}

invariant_holds() {
    echo "INVARIANT_HOLDS: $*"
    return 0
}

invariant_violated() {
    echo "INVARIANT_VIOLATED: $*" >&2
    return 1
}

mock_no_parent() {
    return 1
}

mock_slot_id() {
    printf 'slot-%s\n' "$1"
}

mock_static_env() {
    local _id=$1 key=$2
    case $key in
        VERSIOND_VERSIONS) printf 'v4\n' ;;
        VERSIOND_NON_HA_VERSIONS) printf '\n' ;;
        *) printf 'test-value\n' ;;
    esac
}

mock_env_with_defaults() {
    mock_static_env "$@"
}

mock_catalog_v4() {
    printf 'v4\n'
}

# Docker surface used by fleet_status and the rollout image/tag operations.
# Route readiness remains in the shell mocks, independently of coarse health.
mock_rollout_docker() {
    local format target slot
    case ${1:-} in
        image)
            [[ ${2:-} == inspect ]] || return 0
            if [[ ${3:-} == --format ]]; then
                printf 'sha256:candidate\n'
            fi
            return 0
            ;;
        tag) return 0 ;;
        ps)
            printf 'slot-0\nslot-1\nslot-2\n'
            return 0
            ;;
        inspect)
            [[ ${2:-} == --format ]] || return 0
            format=$3
            target=$4
            if [[ $target == test-parent ]]; then
                echo 'Error: No such object: test-parent' >&2
                return 1
            fi
            slot=${target##*-}
            case $format in
                *ai.gonka.slot*) printf '%s\n' "$slot" ;;
                *'.State.Status}} {{if .State.Health}'*)
                    printf 'running healthy registry.invalid/router:candidate\n'
                    ;;
                *'.State.Status}}'*) printf 'running\n' ;;
                *'.Image}}'*) printf 'sha256:previous-%s\n' "$slot" ;;
                *) printf '\n' ;;
            esac
            return 0
            ;;
        *) return 0 ;;
    esac
}

scenario_bootstrap_zero_ready() {
    load_fleet_functions || return $?

    fleet_inventory() { inventory_listing=; inventory_ids=(); }
    prepare_slot_networks() { :; }
    pull_router_image() { :; }
    require_placement_compatible() { :; }
    start_existing_or_create_slot() { :; }
    slot_route_declared() { [[ $2 == v4 ]]; }
    route_ready_count() { printf '0\n'; }
    slot_route_ready() { return 1; }
    wait_parent_admission() { :; }
    fleet_status() { :; }

    run_capture fleet_apply
    if ((LAST_RC != 0)); then
        invariant_holds \
            'an absent fleet rejected the declared HA route because it had zero ready backends'
    else
        invariant_violated \
            'absent-fleet apply returned 0 after creating routers while static route v4 had zero ready backends'
    fi
}

scenario_pg_drop_after_gate() {
    load_fleet_functions || return $?

    local pg_state=$tmpdir/pg-state
    local gate_count=$tmpdir/gate-count
    printf 'up\n' >"$pg_state"
    printf '0\n' >"$gate_count"

    # Keep the real precondition body, then inject the outage immediately after
    # the final pre-stop gate for the only slot that needs replacement.
    eval "$(declare -f require_static_routes_served | \
        sed '1s/require_static_routes_served/real_require_static_routes_served/')"
    require_static_routes_served() {
        real_require_static_routes_served
        local count
        count=$(<"$gate_count")
        ((count += 1))
        printf '%s\n' "$count" >"$gate_count"
        if ((count == 2)); then
            printf 'down\n' >"$pg_state"
        fi
    }

    fake_docker() { mock_rollout_docker "$@"; }
    slot_id() { mock_slot_id "$@"; }
    slot_ready() { return 0; }
    slot_running() { return 0; }
    slot_route_ready() { [[ $(<"$pg_state") == up ]]; }
    slot_catalog_routes() { mock_catalog_v4; }
    container_env_value() { mock_static_env "$@"; }
    container_env_value_or_legacy_default() { mock_env_with_defaults "$@"; }
    parent_proxy_active() { mock_no_parent; }
    prepare_slot_networks() { :; }
    pull_router_image() { :; }
    require_placement_compatible() { :; }
    slot_needs_replacement() { [[ $1 == 2 ]]; }
    stop_slot_generation() { :; }
    start_slot() { :; }
    wait_parent_admission() { :; }

    run_capture fleet_rollout
    if ((LAST_RC != 0)); then
        invariant_holds \
            'rollout failed closed when PostgreSQL-backed v4 disappeared after the last pre-stop gate'
    else
        invariant_violated \
            'rollout returned 0 after v4 dropped to zero ready routers between its final gate and replacement'
    fi
}

scenario_maintenance_gate_race() {
    load_fleet_functions || return $?

    local pg_state=$tmpdir/pg-state
    printf 'up\n' >"$pg_state"

    eval "$(declare -f require_static_routes_served | \
        sed '1s/require_static_routes_served/real_require_static_routes_served/')"
    require_static_routes_served() {
        real_require_static_routes_served
        # The maintenance snapshot starts after this function returns.
        printf 'down\n' >"$pg_state"
    }

    fake_docker() { mock_rollout_docker "$@"; }
    slot_id() { mock_slot_id "$@"; }
    slot_ready() { return 0; }
    slot_running() { return 0; }
    slot_route_ready() { [[ $(<"$pg_state") == up ]]; }
    slot_catalog_routes() { mock_catalog_v4; }
    container_env_value() { mock_static_env "$@"; }
    container_env_value_or_legacy_default() { mock_env_with_defaults "$@"; }
    candidate_placement_contract() { printf 'candidate-contract\n'; }
    running_placement_contract() { printf 'previous-contract\n'; }
    placement_version_for_image() { printf '2\n'; }
    require_cache_compatible() { :; }
    parent_proxy_active() { mock_no_parent; }
    prepare_slot_networks() { :; }
    pull_router_image() { :; }
    stop_slot_generation() { :; }
    start_slot() { :; }
    wait_parent_admission() { :; }

    run_capture fleet_maintenance_rollout
    if ((LAST_RC != 0)); then
        invariant_holds \
            'maintenance rollout rejected a route that disappeared between the gate and route snapshot'
    else
        invariant_violated \
            'maintenance rollout returned 0 because a zero-ready v4 route fell out of maintenance_required_routes'
    fi
}

scenario_sigkill_retry_previous() {
    load_fleet_functions || return $?

    SIGKILL_STATE_FILE=$tmpdir/slot-2.state
    SIGKILL_GENERATION_FILE=$tmpdir/slot-2.generation
    printf 'exited\n' >"$SIGKILL_STATE_FILE"
    printf 'previous\n' >"$SIGKILL_GENERATION_FILE"

    fake_docker() {
        local format=${3:-} target=${4:-}
        case ${1:-} in
            image) return 0 ;;
            tag) return 0 ;;
            start)
                printf 'running\n' >"$SIGKILL_STATE_FILE"
                return 0
                ;;
            inspect)
                if [[ $format == *'.State.Status}}'* ]]; then
                    if [[ $target == slot-2 ]]; then cat "$SIGKILL_STATE_FILE"; else printf 'running\n'; fi
                elif [[ $format == *'.Image}}'* ]]; then
                    if [[ $target == slot-2 ]]; then cat "$SIGKILL_GENERATION_FILE"; else printf 'previous\n'; fi
                fi
                return 0
                ;;
            *) return 0 ;;
        esac
    }
    fleet_inventory() {
        inventory_listing=$'slot-0\nslot-1\nslot-2'
        inventory_ids=(slot-0 slot-1 slot-2)
    }
    validate_inventory_structure() { :; }
    prepare_slot_networks() { :; }
    pull_router_image() { :; }
    require_placement_compatible() { :; }
    slot_id() { mock_slot_id "$@"; }
    slot_ready() {
        [[ $1 != 2 ]] || [[ $(<"$SIGKILL_STATE_FILE") == running ]]
    }
    slot_compose() {
        local slot=$1
        shift
        if [[ " $* " == *' start '* ]]; then
            printf 'running\n' >"$SIGKILL_STATE_FILE"
            return 0
        fi
        if [[ " $* " == *' up '* ]]; then
            case ${VERSIOND_ROUTER_IMAGE:-} in
                *previous*) printf 'previous\n' >"$SIGKILL_GENERATION_FILE" ;;
                *) printf 'candidate\n' >"$SIGKILL_GENERATION_FILE" ;;
            esac
            printf 'running\n' >"$SIGKILL_STATE_FILE"
        fi
    }
    wait_slot_routes() { :; }
    wait_parent_admission() { :; }
    fleet_rollout() { :; }

    run_capture fleet_apply
    if [[ $(<"$SIGKILL_GENERATION_FILE") == previous ]]; then
        invariant_holds \
            'retry recovered the interrupted slot from its durable previous generation before candidate convergence'
    else
        invariant_violated \
            'retry force-recreated the interrupted non-ready slot with the candidate and ignored its durable previous tag'
    fi
}

scenario_maintenance_retry_exact() {
    load_fleet_functions || return $?

    local slot
    for slot in 0 1 2; do
        printf 'running\n' >"$tmpdir/slot-$slot.state"
        if [[ $slot == 0 ]]; then
            printf 'candidate\n' >"$tmpdir/slot-$slot.generation"
            printf 'candidate-contract\n' >"$tmpdir/slot-$slot.contract"
        else
            printf 'previous\n' >"$tmpdir/slot-$slot.generation"
            printf 'previous-contract\n' >"$tmpdir/slot-$slot.contract"
        fi
    done

    declare -gA fake_tags=()
    # The durable tag is scoped by fleet id (see RT-ROLLBACK-TAG-ISOLATION).
    fake_tags[gonka/versiond-router-previous:test-fleet-0]=previous
    fake_docker() {
        local format=${3:-} target=${4:-} slot
        case ${1:-} in
            image)
                [[ ${2:-} == inspect ]] || return 0
                target=${4:-${3:-}}
                if [[ ${3:-} == --format ]]; then
                    [[ $target == registry.invalid/router:candidate ]] && \
                        printf 'candidate\n' || printf '%s\n' "${fake_tags[$target]:-previous}"
                else
                    [[ -n ${fake_tags[$target]-} ]]
                fi
                ;;
            tag)
                fake_tags[$3]=$2
                ;;
            inspect)
                slot=${target##*-}
                case $format in
                    *'.State.Status}}'*) cat "$tmpdir/slot-$slot.state" ;;
                    *'.Image}}'*) cat "$tmpdir/slot-$slot.generation" ;;
                esac
                ;;
            *) return 0 ;;
        esac
    }
    slot_id() { mock_slot_id "$@"; }
    slot_running() { [[ $(<"$tmpdir/slot-$1.state") == running ]]; }
    slot_ready() { slot_running "$1"; }
    slot_route_ready() { slot_ready "$1"; }
    route_ready_count() {
        local _route=$1 excluded=${2:-} count=0 item
        for item in 0 1 2; do
            [[ $item == "$excluded" ]] && continue
            slot_route_ready "$item" v4 && ((count += 1))
        done
        printf '%s\n' "$count"
    }
    slot_catalog_routes() { mock_catalog_v4; }
    candidate_placement_contract() { printf 'candidate-contract\n'; }
    running_placement_contract() { cat "$tmpdir/slot-${1##*-}.contract"; }
    container_env_value_or_legacy_default() { mock_env_with_defaults "$@"; }
    stop_slot_generation() {
        printf 'exited\n' >"$tmpdir/slot-$1.state"
    }
    slot_compose() {
        local slot=$1
        shift
        if [[ " $* " == *' up '* ]]; then
            case ${VERSIOND_ROUTER_IMAGE:-} in
                *previous*)
                    printf 'previous\n' >"$tmpdir/slot-$slot.generation"
                    printf 'previous-contract\n' >"$tmpdir/slot-$slot.contract"
                    ;;
                *)
                    printf 'candidate\n' >"$tmpdir/slot-$slot.generation"
                    printf 'candidate-contract\n' >"$tmpdir/slot-$slot.contract"
                    ;;
            esac
            printf 'running\n' >"$tmpdir/slot-$slot.state"
        fi
    }

    maintenance_candidate_image_id=candidate
    capture_maintenance_state
    maintenance_active=true
    for slot in "${maintenance_pending[@]}"; do
        stop_slot_generation "$slot"
        start_slot "$slot"
    done

    # Trigger the real rollback path.  It exits by design, so keep all modeled
    # container state in files visible after the subshell exits.
    set +e
    (
        set +e
        (exit 99)
        maintenance_rollback
    )
    set -e

    local all_previous=true
    for slot in 0 1 2; do
        [[ $(<"$tmpdir/slot-$slot.generation") == previous ]] || all_previous=false
    done
    if [[ $all_previous == true ]]; then
        invariant_holds \
            'maintenance retry failure restored every slot, including candidate-complete slots from the interrupted run'
    else
        invariant_violated \
            'maintenance retry rollback restored only this run pending slots and left the previously completed slot on candidate'
    fi
}

scenario_tag_isolation() {
    load_fleet_functions || return $?

    declare -gA fake_tags=()
    fake_docker() {
        local format=${3:-} target=${4:-} owner slot image
        case ${1:-} in
            image)
                [[ ${2:-} == inspect ]] || return 0
                target=${4:-${3:-}}
                [[ -n ${fake_tags[$target]-} ]]
                ;;
            tag)
                fake_tags[$3]=$2
                ;;
            inspect)
                owner=${target%%-id-*}
                slot=${target##*-}
                image="sha256:previous-$owner-$slot"
                case $format in
                    *'.State.Status}}'*) printf 'running\n' ;;
                    *'.Image}}'*) printf '%s\n' "$image" ;;
                esac
                ;;
            *) return 0 ;;
        esac
    }
    slot_id() { printf '%s-id-%s\n' "$fleet_id" "$1"; }
    slot_ready() { return 0; }
    slot_catalog_routes() { mock_catalog_v4; }
    route_ready_count() { printf '3\n'; }
    running_placement_contract() { printf 'previous-contract\n'; }
    candidate_placement_contract() { printf 'candidate-contract\n'; }
    container_env_value_or_legacy_default() { mock_env_with_defaults "$@"; }

    fleet_id=fleet-A
    maintenance_candidate_image_id=sha256:candidate-A
    maintenance_images=()
    maintenance_required_routes=()
    capture_maintenance_state
    local tag_a=${maintenance_images[0]}
    local image_a=${fake_tags[$tag_a]-}

    fleet_id=fleet-B
    maintenance_candidate_image_id=sha256:candidate-B
    maintenance_images=()
    maintenance_env=()
    maintenance_required_routes=()
    capture_maintenance_state
    local tag_b=${maintenance_images[0]}

    if [[ $tag_a != "$tag_b" && ${fake_tags[$tag_a]-} == "$image_a" ]]; then
        invariant_holds \
            'two fleet IDs retain distinct rollback references for the same slot name'
    else
        invariant_violated \
            "fleet-B reused '$tag_b' and overwrote fleet-A rollback reference '$tag_a'"
    fi
}

scenario_offline_retry_cached() {
    load_fleet_functions || return $?

    OFFLINE_STATE_FILE=$tmpdir/slot-2.state
    printf 'exited\n' >"$OFFLINE_STATE_FILE"
    pull_policy=always

    fake_docker() {
        local format=${3:-} target=${4:-}
        if [[ ${1:-} == inspect && $format == *'.State.Status}}'* ]]; then
            if [[ $target == slot-2 ]]; then cat "$OFFLINE_STATE_FILE"; else printf 'running\n'; fi
        fi
        return 0
    }
    fleet_inventory() {
        inventory_listing=$'slot-0\nslot-1\nslot-2'
        inventory_ids=(slot-0 slot-1 slot-2)
    }
    validate_inventory_structure() { :; }
    prepare_slot_networks() { :; }
    slot_id() { mock_slot_id "$@"; }
    slot_ready() {
        [[ $1 != 2 ]] || [[ $(<"$OFFLINE_STATE_FILE") == running ]]
    }
    slot_compose() {
        local slot=$1
        shift
        if [[ " $* " == *' pull '* ]]; then
            echo 'simulated registry outage' >&2
            return 1
        fi
        if [[ " $* " == *' up '* || " $* " == *' start '* ]]; then
            printf 'running\n' >"$OFFLINE_STATE_FILE"
        fi
    }
    require_placement_compatible() { :; }
    wait_slot_routes() { :; }
    wait_parent_admission() { :; }
    fleet_rollout() { :; }

    run_capture fleet_apply
    if ((LAST_RC == 0)) && [[ $(<"$OFFLINE_STATE_FILE") == running ]]; then
        invariant_holds \
            'registry outage did not block recovery because the required candidate/previous images were cached'
    else
        invariant_violated \
            'pull-policy always contacted the unavailable registry before repairing the interrupted fleet from cached images'
    fi
}

run_internal() {
    local scenario=$1
    case $scenario in
        RT-BOOTSTRAP-ZERO-READY) scenario_bootstrap_zero_ready ;;
        RT-PG-DROP-AFTER-GATE) scenario_pg_drop_after_gate ;;
        RT-MAINT-GATE-RACE) scenario_maintenance_gate_race ;;
        RT-SIGKILL-RETRY-PREVIOUS) scenario_sigkill_retry_previous ;;
        RT-MAINT-RETRY-EXACT) scenario_maintenance_retry_exact ;;
        RT-ROLLBACK-TAG-ISOLATION) scenario_tag_isolation ;;
        RT-OFFLINE-RETRY-CACHED) scenario_offline_retry_cached ;;
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
