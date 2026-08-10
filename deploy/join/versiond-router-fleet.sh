#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
config_env=${GONKA_CONFIG_ENV:-$script_dir/config.env}
docker_bin=${DOCKER_BIN:-docker}
slot_file=$script_dir/versiond-router-slot/docker-compose.yml
current_slot=
rollback_image=
maintenance_active=false
declare -A maintenance_images=()
declare -A maintenance_env=()
maintenance_required_routes=()
maintenance_keys=(
    VERSIOND_POOL_HOST
    VERSIOND_ROUTER_BACK_NETWORK_NAME
    VERSIOND_LEGACY_HOST
    VERSIOND_NON_HA_VERSIONS
    VERSIOND_VERSIONS
    VERSIOND_ROUTER_ALLOW_COARSE_READINESS
)

fail() {
    echo "versiond-router-fleet: $*" >&2
    exit 1
}

warn() {
    echo "versiond-router-fleet: warning: $*" >&2
}

usage() {
    cat >&2 <<'EOF'
Usage: versiond-router-fleet.sh COMMAND [ARGS]

Commands:
  up                 Create missing slots or start existing containers unchanged.
  rollout            Replace slots one at a time, preserving the ready reserve.
  maintenance-rollout
                     Drain the old fleet before a placement-contract change.
  status             Show the expected fleet and reject duplicate/orphan slots.
  stop SLOT          Gracefully stop one slot after checking the ready reserve.
  start SLOT         Start an existing container unchanged, or create it if absent.

Configuration comes from config.env. VERSIOND_ROUTER_FLEET_SLOTS defaults to
"0 1 2" and identifies independent Compose projects, not replicas in one model.
EOF
}

[[ -f $config_env ]] || fail "configuration file not found: $config_env"
set -a
# shellcheck disable=SC1090
source "$config_env"
set +a

project_prefix=${VERSIOND_ROUTER_PROJECT_PREFIX:-gonka-versiond-router}
fleet_id=${VERSIOND_ROUTER_FLEET_ID:-$project_prefix}
slot_list=${VERSIOND_ROUTER_FLEET_SLOTS:-0 1 2}
min_ready=${VERSIOND_ROUTER_MIN_READY:-2}
drain_timeout=${VERSIOND_ROUTER_DRAIN_TIMEOUT_SECONDS:-1800}
wait_timeout=${VERSIOND_ROUTER_START_TIMEOUT_SECONDS:-60}
pull_policy=${VERSIOND_ROUTER_PULL_POLICY:-always}
lock_file=${VERSIOND_ROUTER_FLEET_LOCK:-${XDG_RUNTIME_DIR:-/tmp}/gonka-versiond-router-$fleet_id.lock}
operation_id="$(date +%s%N)-$$"

command -v "$docker_bin" >/dev/null 2>&1 || fail "$docker_bin is required"
command -v flock >/dev/null 2>&1 || fail "flock is required"

case $min_ready in '' | *[!0-9]*) fail "VERSIOND_ROUTER_MIN_READY must be a non-negative integer" ;; esac
case $pull_policy in always | missing | never) ;; *) fail "VERSIOND_ROUTER_PULL_POLICY must be always, missing, or never" ;; esac
case $fleet_id in '' | *[!A-Za-z0-9._-]*) fail "invalid VERSIOND_ROUTER_FLEET_ID '$fleet_id'" ;; esac
for value in "$drain_timeout" "$wait_timeout"; do
    case $value in '' | *[!0-9]* | 0) fail "router timeouts must be positive integer seconds" ;; esac
done

read -r -a slots <<<"$slot_list"
((${#slots[@]} >= 2)) || fail "at least two router slots are required"
declare -A expected=()
for slot in "${slots[@]}"; do
    case $slot in '' | *[!A-Za-z0-9._-]*) fail "invalid router slot '$slot'" ;; esac
    [[ -z ${expected[$slot]-} ]] || fail "router slot '$slot' is declared twice"
    expected[$slot]=1
done
((min_ready < ${#slots[@]})) || fail "VERSIOND_ROUTER_MIN_READY must be smaller than the fleet"

declare -A expected_routes=()
while read -r route; do
    [[ -n $route ]] || continue
    expected_routes[$route]=1
done < <(printf '%s\n' \
    "${VERSIOND_NON_HA_VERSIONS-v1 v2 v3}" \
    "${VERSIOND_VERSIONS-v4 v5 v6 v7 v8}" \
    | tr ',;' '  ' | tr -s ' ' '\n')

normalize_versions() {
    printf '%s\n' "$1" | tr ',;' '  ' | tr -s ' ' '\n' | \
        sed '/^$/d' | LC_ALL=C sort -u | paste -sd, -
}

placement_contract() {
    local pool_host=$1 back_network=$2 legacy_host=$3 legacy_versions=$4 normalized
    normalized=$(normalize_versions "$legacy_versions")
    [[ -n $normalized ]] || legacy_host=
    printf 'pool=%s;back-network=%s;legacy-host=%s;legacy-versions=%s\n' \
        "$pool_host" "$back_network" "$legacy_host" "$normalized"
}

candidate_placement_contract() {
    placement_contract \
        "${VERSIOND_POOL_HOST:-versiond-pool}" \
        "${VERSIOND_ROUTER_BACK_NETWORK:-gonka-versiond-router-back}" \
        "${VERSIOND_LEGACY_HOST:-versiond}" \
        "${VERSIOND_NON_HA_VERSIONS-v1 v2 v3}"
}

slot_compose() {
    local slot=$1
    shift
    VERSIOND_ROUTER_SLOT=$slot VERSIOND_ROUTER_FLEET_ID=$fleet_id "$docker_bin" compose \
        --project-directory "$script_dir" \
        --project-name "$project_prefix-$slot" \
        -f "$slot_file" "$@"
}

slot_ids() {
    local slot=$1
    "$docker_bin" ps -aq \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot"
}

slot_id() {
    local slot=$1
    local ids count
    ids=$(slot_ids "$slot")
    count=$(wc -w <<<"$ids")
    ((count == 1)) || return 1
    printf '%s\n' "$ids"
}

slot_ready() {
    local id state health
    id=$(slot_id "$1") || return 1
    read -r state health < <("$docker_bin" inspect --format \
        '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$id")
    [[ $state == running && $health == healthy ]]
}

urlencode() {
    local input=$1 output='' char hex i
    local LC_ALL=C
    for ((i = 0; i < ${#input}; i++)); do
        char=${input:i:1}
        case $char in
            [A-Za-z0-9.~_-]) output+=$char ;;
            *)
                printf -v hex '%%%02X' "'$char"
                output+=$hex
                ;;
        esac
    done
    printf '%s\n' "$output"
}

slot_route_ready() {
    local slot=$1 route=$2 id encoded
    id=$(slot_id "$slot") || return 1
    slot_ready "$slot" || return 1
    encoded=$(urlencode "$route")
    "$docker_bin" exec "$id" /bin/busybox wget -q -O /dev/null \
        "http://127.0.0.1:8404/readyz?version=$encoded" 2>/dev/null
}

slot_route_declared() {
    local slot=$1 route=$2 id legacy_versions ha_versions configured
    id=$(slot_id "$slot") || return 1
    legacy_versions=$(container_env_value "$id" VERSIOND_NON_HA_VERSIONS) || return 1
    ha_versions=$(container_env_value "$id" VERSIOND_VERSIONS) || return 1
    configured=$(printf '%s\n%s\n' \
        "$legacy_versions" "$ha_versions" \
        | tr ',;' '  ' | tr -s ' ' '\n')
    while IFS= read -r declared; do
        [[ $declared != "$route" ]] || return 0
    done <<<"$configured"
    return 1
}

ready_count_except() {
    local excluded=${1:-}
    local slot count=0
    for slot in "${slots[@]}"; do
        [[ $slot == "$excluded" ]] && continue
        slot_ready "$slot" && ((count += 1))
    done
    printf '%s\n' "$count"
}

route_ready_count() {
    local route=$1 excluded=${2:-}
    local slot count=0
    for slot in "${slots[@]}"; do
        [[ $slot == "$excluded" ]] && continue
        slot_route_ready "$slot" "$route" && ((count += 1))
    done
    printf '%s\n' "$count"
}

require_ready_reserve() {
    local excluded=$1 route total reserve
    reserve=$(ready_count_except "$excluded")
    ((reserve >= min_ready)) || fail \
        "refusing to stop slot $excluded: only $reserve other routers are ready, need $min_ready"
    for route in "${!expected_routes[@]}"; do
        total=$(route_ready_count "$route")
        ((total > 0)) || continue
        reserve=$(route_ready_count "$route" "$excluded")
        ((reserve >= min_ready)) || fail \
            "refusing to stop slot $excluded: version $route has only $reserve other ready routers, need $min_ready"
    done
}

wait_slot_routes() {
    local slot=$1 deadline=$((SECONDS + wait_timeout))
    local route missing
    for route in "${!expected_routes[@]}"; do
        slot_route_declared "$slot" "$route" || {
            echo "versiond-router-fleet: slot $slot does not declare expected route $route; run rollout" >&2
            return 1
        }
    done
    while ((SECONDS < deadline)); do
        missing=
        for route in "${!expected_routes[@]}"; do
            if (( $(route_ready_count "$route" "$slot") > 0 )) && \
                ! slot_route_ready "$slot" "$route"; then
                missing=$route
                break
            fi
        done
        [[ -z $missing ]] && return 0
        sleep 1
    done
    echo "versiond-router-fleet: slot $slot did not converge route $missing to the fleet view within ${wait_timeout}s" >&2
    return 1
}

wait_slot_ready() {
    local slot=$1 deadline=$((SECONDS + wait_timeout))
    while ((SECONDS < deadline)); do
        slot_ready "$slot" && return 0
        sleep 1
    done
    echo "versiond-router-fleet: slot $slot did not become healthy within ${wait_timeout}s" >&2
    return 1
}

require_networks() {
    local network
    for network in \
        "${VERSIOND_ROUTER_FRONT_NETWORK:-gonka-versiond-router-front}" \
        "${VERSIOND_ROUTER_BACK_NETWORK:-gonka-versiond-router-back}"; do
        "$docker_bin" network inspect "$network" >/dev/null 2>&1 || fail \
            "network $network does not exist; start the main HA Compose model first"
    done
}

pull_router_image() {
    [[ $pull_policy == never ]] || \
        slot_compose "${slots[0]}" pull --policy "$pull_policy" router
}

placement_version_for_image() {
    "$docker_bin" image inspect --format \
        '{{index .Config.Labels "ai.gonka.placement-protocol-version"}}' "$1"
}

container_env_value() {
    local id=$1 key=$2 line
    while IFS= read -r line; do
        case $line in
            "$key="*) printf '%s\n' "${line#*=}"; return 0 ;;
        esac
    done < <("$docker_bin" inspect --format \
        '{{range .Config.Env}}{{println .}}{{end}}' "$id")
    return 1
}

running_placement_contract() {
    local id=$1 pool_host back_network legacy_host legacy_versions
    pool_host=$(container_env_value "$id" VERSIOND_POOL_HOST) || return 1
    back_network=$(container_env_value "$id" VERSIOND_ROUTER_BACK_NETWORK_NAME) || return 1
    legacy_host=$(container_env_value "$id" VERSIOND_LEGACY_HOST) || return 1
    legacy_versions=$(container_env_value "$id" VERSIOND_NON_HA_VERSIONS) || return 1
    placement_contract "$pool_host" "$back_network" "$legacy_host" "$legacy_versions"
}

require_placement_compatible() {
    local candidate=$1 slot id running_image candidate_version running_version
    local candidate_contract running_contract
    candidate_version=$(placement_version_for_image "$candidate")
    [[ -n $candidate_version ]] || fail \
        "candidate image has no placement protocol label; refusing a mixed rollout"
    candidate_contract=$(candidate_placement_contract)
    for slot in "${slots[@]}"; do
        id=$(slot_id "$slot") || continue
        running_image=$($docker_bin inspect --format '{{.Image}}' "$id")
        running_version=$(placement_version_for_image "$running_image")
        [[ $running_version == "$candidate_version" ]] || fail \
            "placement protocol mismatch: candidate=$candidate_version slot-$slot=${running_version:-missing}; use a maintenance cutover"
        running_contract=$(running_placement_contract "$id") || fail \
            "slot $slot does not expose its placement contract"
        [[ $running_contract == "$candidate_contract" ]] || fail \
            "placement contract differs on slot $slot; use maintenance-rollout to avoid mixed escrow placement"
    done
}

capture_maintenance_state() {
    local slot id image key contract first_contract='' route
    local -A routes=()
    for slot in "${slots[@]}"; do
        id=$(slot_id "$slot") || fail "slot $slot has no unique existing container"
        slot_ready "$slot" || fail "slot $slot is not healthy before maintenance"
        contract=$(running_placement_contract "$id") || fail \
            "slot $slot does not expose its placement contract"
        if [[ -z $first_contract ]]; then
            first_contract=$contract
        elif [[ $contract != "$first_contract" ]]; then
            fail "fleet already has divergent placement contracts; refusing automated maintenance"
        fi
        image=$($docker_bin inspect --format '{{.Image}}' "$id")
        maintenance_images[$slot]="gonka/versiond-router-maintenance-rollback:$operation_id-$slot"
        "$docker_bin" tag "$image" "${maintenance_images[$slot]}"
        for key in "${maintenance_keys[@]}"; do
            maintenance_env["$slot:$key"]=$(container_env_value "$id" "$key") || fail \
                "slot $slot is missing environment $key"
        done
        while read -r route; do
            [[ -n $route ]] && routes[$route]=1
        done < <(printf '%s\n%s\n' \
            "${maintenance_env[$slot:VERSIOND_NON_HA_VERSIONS]}" \
            "${maintenance_env[$slot:VERSIOND_VERSIONS]}" \
            | tr ',;' '  ' | tr -s ' ' '\n')
    done
    for route in "${!routes[@]}"; do
        if (( $(route_ready_count "$route") > 0 )); then
            maintenance_required_routes+=("$route")
        fi
    done
}

wait_required_routes_all() {
    local deadline=$((SECONDS + wait_timeout)) slot route missing
    while ((SECONDS < deadline)); do
        missing=
        for route in "${maintenance_required_routes[@]}"; do
            for slot in "${slots[@]}"; do
                if ! slot_route_ready "$slot" "$route"; then
                    missing="$route on slot $slot"
                    break 2
                fi
            done
        done
        [[ -z $missing ]] && return 0
        sleep 1
    done
    echo "versiond-router-fleet: fleet did not restore required route $missing within ${wait_timeout}s" >&2
    return 1
}

maintenance_rollback() {
    local status=$? slot ok=true
    ((status != 0)) || status=1
    trap - ERR INT TERM HUP
    if [[ $maintenance_active == true ]]; then
        warn "maintenance rollout failed; draining candidates before restoring the exact previous fleet"
        for slot in "${slots[@]}"; do
            slot_compose "$slot" stop --timeout "$drain_timeout" router \
                >/dev/null 2>&1 || true
        done
        for slot in "${slots[@]}"; do
            if ! VERSIOND_ROUTER_IMAGE="${maintenance_images[$slot]}" \
                VERSIOND_POOL_HOST="${maintenance_env[$slot:VERSIOND_POOL_HOST]}" \
                VERSIOND_ROUTER_BACK_NETWORK="${maintenance_env[$slot:VERSIOND_ROUTER_BACK_NETWORK_NAME]}" \
                VERSIOND_LEGACY_HOST="${maintenance_env[$slot:VERSIOND_LEGACY_HOST]}" \
                VERSIOND_NON_HA_VERSIONS="${maintenance_env[$slot:VERSIOND_NON_HA_VERSIONS]}" \
                VERSIOND_VERSIONS="${maintenance_env[$slot:VERSIOND_VERSIONS]}" \
                VERSIOND_ROUTER_ALLOW_COARSE_READINESS="${maintenance_env[$slot:VERSIOND_ROUTER_ALLOW_COARSE_READINESS]}" \
                slot_compose "$slot" up -d --wait \
                    --wait-timeout "$wait_timeout" router; then
                ok=false
            fi
        done
        if [[ $ok == true ]] && wait_required_routes_all; then
            warn "the exact previous router fleet was restored; maintenance remains uncommitted"
        else
            warn "automatic fleet rollback failed; keep the maintenance rollback images and restore ingress manually"
        fi
    fi
    exit "$status"
}

rollback_current() {
    local status=$?
    trap - ERR INT TERM HUP
    if [[ -n $current_slot && -n $rollback_image ]]; then
        warn "restoring slot $current_slot from $rollback_image"
        if VERSIOND_ROUTER_IMAGE=$rollback_image slot_compose "$current_slot" \
            up -d --wait --wait-timeout "$wait_timeout" router && \
            wait_slot_routes "$current_slot"; then
            warn "slot $current_slot restored; rollout stopped"
        else
            warn "automatic rollback of slot $current_slot did not restore the fleet route view"
        fi
    fi
    exit "$status"
}

start_slot() {
    local slot=$1
    [[ -n ${expected[$slot]-} ]] || fail "slot '$slot' is not configured"
    slot_compose "$slot" up -d --wait --wait-timeout "$wait_timeout" router
}

start_existing_or_create_slot() {
    local slot=$1 ids count id state
    [[ -n ${expected[$slot]-} ]] || fail "slot '$slot' is not configured"
    ids=$(slot_ids "$slot")
    count=$(wc -w <<<"$ids")
    ((count <= 1)) || fail "duplicate containers claim router slot '$slot'"
    if ((count == 0)); then
        start_slot "$slot"
        return
    fi

    id=$ids
    state=$($docker_bin inspect --format '{{.State.Status}}' "$id")
    case $state in
        running) ;;
        created | exited)
            # `compose start` deliberately ignores a changed manifest or image.
            # Only rollout may replace an existing slot.
            slot_compose "$slot" start router >/dev/null
            ;;
        *) fail "slot $slot cannot be started from container state '$state'" ;;
    esac
    wait_slot_ready "$slot"
}

stop_slot() {
    local slot=$1
    [[ -n ${expected[$slot]-} ]] || fail "slot '$slot' is not configured"
    require_ready_reserve "$slot"
    slot_compose "$slot" stop --timeout "$drain_timeout" router
}

fleet_status() {
    local id slot state health image seen_slot route
    local -A seen=()
    local bad=0
    printf '%-16s %-12s %-10s %s\n' SLOT STATE HEALTH IMAGE
    while read -r id; do
        [[ -n $id ]] || continue
        slot=$($docker_bin inspect --format '{{index .Config.Labels "ai.gonka.slot"}}' "$id")
        if [[ -z ${expected[$slot]-} ]]; then
            warn "orphan router container $id declares unknown slot '$slot'"
            bad=1
        fi
        if [[ -n ${seen[$slot]-} ]]; then
            warn "duplicate containers claim router slot '$slot'"
            bad=1
        fi
        seen[$slot]=$id
        read -r state health image < <($docker_bin inspect --format \
            '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}} {{.Config.Image}}' "$id")
        printf '%-16s %-12s %-10s %s\n' "$slot" "$state" "$health" "$image"
        if [[ $state != running || $health != healthy ]]; then
            bad=1
        fi
    done < <($docker_bin ps -aq \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id")
    for seen_slot in "${slots[@]}"; do
        if [[ -z ${seen[$seen_slot]-} ]]; then
            printf '%-16s %-12s %-10s %s\n' "$seen_slot" absent - -
            bad=1
            continue
        fi
        slot_id "$seen_slot" >/dev/null || continue
        for route in "${!expected_routes[@]}"; do
            if ! slot_route_declared "$seen_slot" "$route"; then
                warn "slot $seen_slot does not declare expected route $route; run rollout"
                bad=1
            fi
        done
    done
    return "$bad"
}

fleet_up() {
    local slot
    require_networks
    pull_router_image
    candidate_image=${VERSIOND_ROUTER_IMAGE:-ghcr.io/product-science/versiond-router:0.2.15-devshard-v5}
    require_placement_compatible "$candidate_image"
    for slot in "${slots[@]}"; do
        echo "Ensuring versiond-router slot $slot is running"
        start_existing_or_create_slot "$slot"
        wait_slot_routes "$slot"
    done
    fleet_status
}

fleet_rollout() {
    local slot id old_image rollback_tag
    require_networks
    pull_router_image
    candidate_image=${VERSIOND_ROUTER_IMAGE:-ghcr.io/product-science/versiond-router:0.2.15-devshard-v5}
    require_placement_compatible "$candidate_image"
    trap rollback_current ERR INT TERM HUP
    for slot in "${slots[@]}"; do
        require_ready_reserve "$slot"
        id=$(slot_id "$slot") || fail "slot $slot has no unique existing container"
        old_image=$($docker_bin inspect --format '{{.Image}}' "$id")
        rollback_tag="gonka/versiond-router-rollback:$operation_id-$slot"
        "$docker_bin" tag "$old_image" "$rollback_tag"
        current_slot=$slot
        rollback_image=$rollback_tag
        echo "Draining versiond-router slot $slot"
        slot_compose "$slot" stop --timeout "$drain_timeout" router
        echo "Starting replacement for slot $slot"
        start_slot "$slot"
        slot_ready "$slot" || false
        wait_slot_routes "$slot"
        current_slot=
        rollback_image=
        "$docker_bin" image rm "$rollback_tag" >/dev/null 2>&1 || true
    done
    trap - ERR INT TERM HUP
    fleet_status
}

fleet_maintenance_rollout() {
    local slot image ack=${VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE:-false}
    case $ack in
        1 | true | yes) ;;
        *) fail "maintenance-rollout requires VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true" ;;
    esac
    require_networks
    pull_router_image
    image=${VERSIOND_ROUTER_IMAGE:-ghcr.io/product-science/versiond-router:0.2.15-devshard-v5}
    [[ -n $(placement_version_for_image "$image") ]] || fail \
        "candidate image has no placement protocol label"
    capture_maintenance_state
    maintenance_active=true
    trap maintenance_rollback ERR INT TERM HUP

    echo "Draining the complete old router fleet for an atomic placement change"
    for slot in "${slots[@]}"; do
        slot_compose "$slot" stop --timeout "$drain_timeout" router
    done
    for slot in "${slots[@]}"; do
        echo "Starting maintenance replacement for slot $slot"
        start_slot "$slot"
    done
    wait_required_routes_all

    maintenance_active=false
    trap - ERR INT TERM HUP
    for slot in "${slots[@]}"; do
        "$docker_bin" image rm "${maintenance_images[$slot]}" >/dev/null 2>&1 || true
    done
    fleet_status
}

exec 9>"$lock_file"
flock -n 9 || fail "another fleet operation holds $lock_file"

command=${1:-}
case $command in
    up) fleet_up ;;
    rollout) fleet_rollout ;;
    maintenance-rollout) fleet_maintenance_rollout ;;
    status) fleet_status ;;
    stop)
        [[ $# == 2 ]] || fail "stop requires exactly one SLOT"
        stop_slot "$2"
        ;;
    start)
        [[ $# == 2 ]] || fail "start requires exactly one SLOT"
        require_networks
        pull_router_image
        candidate_image=${VERSIOND_ROUTER_IMAGE:-ghcr.io/product-science/versiond-router:0.2.15-devshard-v5}
        require_placement_compatible "$candidate_image"
        start_existing_or_create_slot "$2"
        wait_slot_routes "$2"
        ;;
    -h | --help | help) usage ;;
    *) usage; fail "unknown command '${command:-}'" ;;
esac
