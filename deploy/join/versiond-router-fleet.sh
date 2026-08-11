#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
config_env=${GONKA_CONFIG_ENV:-$script_dir/config.env}
slot_file=$script_dir/versiond-router-slot/docker-compose.yml
current_slot=
rollback_image=
maintenance_active=false
declare -A inherited_env=()
declare -A rollback_env=()
declare -A rollback_routes=()
declare -A maintenance_images=()
declare -A maintenance_env=()
declare -A legacy_env_defaults=(
    [VERSIOND_ROUTING_CATALOG_URL]=''
    [VERSIOND_ROUTING_CATALOG_POLL_SECONDS]=5
    [VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS]=3
    [VERSIOND_ROUTING_ACTIVATION_MIN_READY]=2
    [VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS]=86400
    [VERSIOND_ROUTER_ALLOW_COARSE_READINESS]=false
    [VERSIOND_ROUTER_VERSION_CAPACITY]=32
    [HAPROXY_DNS_RESOLVER]=127.0.0.11:53
)
maintenance_required_routes=()
maintenance_keys=(
    VERSIOND_POOL_HOST
    VERSIOND_ROUTER_BACK_NETWORK_NAME
    VERSIOND_LEGACY_HOST
    VERSIOND_NON_HA_VERSIONS
    VERSIOND_VERSIONS
    VERSIOND_ROUTER_ALLOW_COARSE_READINESS
    VERSIOND_ROUTING_CATALOG_URL
    VERSIOND_ROUTING_CATALOG_POLL_SECONDS
    VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS
    VERSIOND_ROUTING_ACTIVATION_MIN_READY
    VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS
    VERSIOND_ROUTER_VERSION_CAPACITY
    HAPROXY_DNS_RESOLVER
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
  prepare-networks   Create or validate the fleet-owned front/back networks.
  up                 Create missing slots or start existing containers unchanged.
  apply              Bootstrap an absent fleet or roll changed slots one at a time.
  rollout            Replace slots one at a time, preserving the ready reserve.
  maintenance-rollout
                     Drain the old fleet before a placement-contract change.
  verify-admission [ROUTE ...]
                     Require every slot, and every listed live route, to be
                     admitted by the active parent proxy.
  wait-version VERSION
                     Wait until every slot has learned VERSION and the configured
                     ready reserve is admitted end to end. This is the
                     post-approval acceptance gate for release automation.
  status             Show the expected fleet and reject duplicate/orphan slots.
  stop SLOT          Gracefully stop one slot after checking the ready reserve.
  start SLOT         Start an existing container unchanged, or create it if absent.
  stop-all --maintenance
                     Gracefully stop every fleet container for host maintenance.
  down --maintenance Remove all fleet containers, including orphan slots, and
                     remove fleet-owned networks after the main stack is down.

Configuration comes from config.env. VERSIOND_ROUTER_FLEET_SLOTS defaults to
"0 1 2" and identifies independent Compose projects, not replicas in one model.
EOF
}

[[ -f $config_env ]] || fail "configuration file not found: $config_env"
while IFS= read -r name; do
    case $name in
        GONKA_* | VERSIOND_* | ROUTER_HA_* | PROXY_* | HAPROXY_* | DOCKER_BIN)
            inherited_env[$name]=${!name}
            ;;
    esac
done < <(compgen -e)
set -a
# shellcheck disable=SC1090
source "$config_env"
set +a
for name in "${!inherited_env[@]}"; do
    printf -v "$name" '%s' "${inherited_env[$name]}"
    export "${name?}"
done
docker_bin=${DOCKER_BIN:-docker}

project_prefix=${VERSIOND_ROUTER_PROJECT_PREFIX:-gonka-versiond-router}
fleet_id=${VERSIOND_ROUTER_FLEET_ID:-$project_prefix}
slot_list=${VERSIOND_ROUTER_FLEET_SLOTS:-0 1 2}
min_ready=${VERSIOND_ROUTER_MIN_READY:-2}
drain_timeout=${VERSIOND_ROUTER_DRAIN_TIMEOUT_SECONDS:-1800}
wait_timeout=${VERSIOND_ROUTER_START_TIMEOUT_SECONDS:-60}
version_wait_timeout=${VERSIOND_ROUTING_ACTIVATION_TIMEOUT_SECONDS:-2100}
pull_policy=${VERSIOND_ROUTER_PULL_POLICY:-always}
config_dir=$(cd -- "$(dirname -- "$config_env")" && pwd -P)
lock_file=${VERSIOND_ROUTER_FLEET_LOCK:-$config_dir/.gonka-versiond-router-$fleet_id.lock}
operation_id="$(date +%s%N)-$$"

command -v "$docker_bin" >/dev/null 2>&1 || fail "$docker_bin is required"
command -v flock >/dev/null 2>&1 || fail "flock is required"

case $min_ready in '' | *[!0-9]*) fail "VERSIOND_ROUTER_MIN_READY must be a non-negative integer" ;; esac
case $pull_policy in always | missing | never) ;; *) fail "VERSIOND_ROUTER_PULL_POLICY must be always, missing, or never" ;; esac
case $fleet_id in '' | *[!A-Za-z0-9._-]*) fail "invalid VERSIOND_ROUTER_FLEET_ID '$fleet_id'" ;; esac
for value in "$drain_timeout" "$wait_timeout" "$version_wait_timeout"; do
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

declare -A candidate_routes=()
declare -A expected_routes=()
while read -r route; do
    [[ -n $route ]] || continue
    candidate_routes[$route]=1
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
    local pool_host=$1 back_network=$2 legacy_host=$3 legacy_versions=$4
    local ha_versions=$5 catalog_url=$6 coarse_readiness=$7 dns_resolver=$8
    local normalized routing_mode
    normalized=$(normalize_versions "$legacy_versions")
    [[ -n $normalized ]] || legacy_host=
    if [[ -n $catalog_url ]]; then
        routing_mode=catalog
    elif [[ -n $(normalize_versions "$ha_versions") ]]; then
        routing_mode=static-per-version
    else
        routing_mode=coarse
    fi
    printf 'pool=%s;back-network=%s;legacy-host=%s;legacy-versions=%s;routing-mode=%s;catalog-url=%s;coarse-readiness=%s;dns-resolver=%s\n' \
        "$pool_host" "$back_network" "$legacy_host" "$normalized" \
        "$routing_mode" "$catalog_url" "$coarse_readiness" "$dns_resolver"
}

candidate_placement_contract() {
    placement_contract \
        "${VERSIOND_POOL_HOST:-versiond-pool}" \
        "${VERSIOND_ROUTER_BACK_NETWORK:-gonka-versiond-router-back}" \
        "${VERSIOND_LEGACY_HOST:-versiond}" \
        "${VERSIOND_NON_HA_VERSIONS-v1 v2 v3}" \
        "${VERSIOND_VERSIONS-v4 v5 v6 v7 v8}" \
        "${VERSIOND_ROUTING_CATALOG_URL-http://versiond-routing-oracle:9100/versions}" \
        "${VERSIOND_ROUTER_ALLOW_COARSE_READINESS:-false}" \
        "${HAPROXY_DNS_RESOLVER:-127.0.0.11:53}"
}

slot_compose() {
    local slot=$1
    shift
    [[ -n ${VERSIOND_ROUTER_METRICS_NETWORK:-} ]] || resolve_metrics_network
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

fleet_ids() {
    "$docker_bin" ps -aq --no-trunc \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id"
}

fleet_volume_ids() {
    "$docker_bin" volume ls -q \
        --filter label=ai.gonka.component=versiond-router-state \
        --filter "label=ai.gonka.fleet=$fleet_id"
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

slot_catalog_routes() {
    local id map
    id=$(slot_id "$1") || return 1
    if "$docker_bin" exec "$id" test -x \
        /usr/local/lib/router-runtime/catalog-status >/dev/null 2>&1; then
        for map in /etc/haproxy/non_ha.map /etc/haproxy/versions.map; do
            "$docker_bin" exec "$id" \
                /usr/local/lib/router-runtime/catalog-status "$map"
        done
        return
    fi

    # Mixed-image rollout fallback. Old images know only their container env.
    local legacy_versions ha_versions
    legacy_versions=$(container_env_value "$id" VERSIOND_NON_HA_VERSIONS) || return 1
    ha_versions=$(container_env_value "$id" VERSIOND_VERSIONS) || return 1
    printf '%s\n%s\n' "$legacy_versions" "$ha_versions" | \
        tr ',;' '  ' | tr -s ' ' '\n' | sed '/^$/d'
}

slot_route_declared() {
    local slot=$1 route=$2 configured
    configured=$(slot_catalog_routes "$slot") || return 1
    while IFS= read -r declared; do
        [[ $declared != "$route" ]] || return 0
    done <<<"$configured"
    return 1
}

discover_expected_routes() {
    local slot route routes
    for slot in "${slots[@]}"; do
        slot_id "$slot" >/dev/null 2>&1 || continue
        routes=$(slot_catalog_routes "$slot") || fail \
            "cannot read the effective route catalog from slot $slot"
        while IFS= read -r route; do
            [[ -n $route ]] || continue
            # A removed governance version remains in the monotonic runtime map
            # until replacement. Protect routes that still carry traffic, not
            # stale declarations whose children have already drained.
            slot_route_ready "$slot" "$route" && expected_routes[$route]=1
        done <<<"$routes"
    done
    return 0
}

route_in_static_environment() {
    local route=$1 versions declared
    shift
    for versions in "$@"; do
        while IFS= read -r declared; do
            [[ $declared != "$route" ]] || return 0
        done < <(printf '%s\n' "$versions" | tr ',;' '  ' | tr -s ' ' '\n')
    done
    return 1
}

select_candidate_route_view() {
    local route
    expected_routes=()
    for route in "${!candidate_routes[@]}"; do
        expected_routes[$route]=1
    done
    discover_expected_routes
}

slot_front_ip() {
    local id network
    id=$(slot_id "$1") || return 1
    network=${VERSIOND_ROUTER_FRONT_NETWORK:-gonka-versiond-router-front}
    "$docker_bin" inspect --format \
        "{{with index .NetworkSettings.Networks \"$network\"}}{{.IPAddress}}{{end}}" \
        "$id"
}

parent_proxy_active() {
    local parent=${PROXY_ROUTER_CONTAINER:-proxy} component
    component=$("$docker_bin" inspect --format \
        '{{or (index .Config.Labels "ai.gonka.component") ""}}' \
        "$parent" 2>/dev/null) || return 1
    [[ $component == proxy-router ]]
}

parent_diagnostic_available() {
    local parent=${PROXY_ROUTER_CONTAINER:-proxy}
    parent_proxy_active || return 1
    "$docker_bin" exec "$parent" test -x \
        /usr/local/lib/proxy-router/route-status >/dev/null 2>&1
}

require_parent_diagnostic() {
    parent_proxy_active || return 1
    parent_diagnostic_available || fail \
        "active parent proxy has no route-status diagnostic; update proxy-router before rolling the inner fleet"
}

parent_route_admitted() {
    local route=$1 address=$2 parent=${PROXY_ROUTER_CONTAINER:-proxy}
    if "$docker_bin" exec "$parent" \
        /usr/local/lib/proxy-router/route-status "$route" "$address" \
        >/dev/null 2>&1; then
        return 0
    else
        return $?
    fi
}

parent_admitted_count() {
    local route=$1 excluded=${2:-} slot address status count=0
    for slot in "${slots[@]}"; do
        [[ $slot == "$excluded" ]] && continue
        if [[ $route == --coarse ]]; then
            slot_ready "$slot" || continue
        else
            slot_route_ready "$slot" "$route" || continue
        fi
        address=$(slot_front_ip "$slot") || continue
        if parent_route_admitted "$route" "$address"; then
            ((count += 1))
        else
            status=$?
            if ((status == 3)); then
                printf 'unknown\n'
                return 0
            fi
        fi
    done
    printf '%s\n' "$count"
}

wait_parent_admission() {
    local slot=$1 deadline route address state missing
    parent_proxy_active || return 0
    require_parent_diagnostic
    address=$(slot_front_ip "$slot") || return 1
    deadline=$((SECONDS + wait_timeout))
    while ((SECONDS < deadline)); do
        missing=
        for route in --coarse "${!expected_routes[@]}"; do
            if [[ $route != --coarse ]] && ! slot_route_ready "$slot" "$route"; then
                continue
            fi
            if parent_route_admitted "$route" "$address"; then
                continue
            else
                state=$?
            fi
            ((state == 3)) && continue
            missing=$route
            break
        done
        [[ -z $missing ]] && return 0
        sleep 1
    done
    echo "versiond-router-fleet: parent proxy did not admit slot $slot for route $missing within ${wait_timeout}s" >&2
    return 1
}

declare -A admission_required_routes=()
collect_required_admission_routes() {
    local route

    admission_required_routes=()
    for route in "$@"; do
        [[ -n ${expected_routes[$route]-} ]] || fail \
            "required admission route '$route' is not declared by this fleet"
        admission_required_routes[$route]=1
    done
    # Preserve every route the fleet already serves even when the caller has no
    # external baseline (for example, an idempotent cutover retry). Completely
    # inactive declarations remain optional because VERSIOND_VERSIONS may list
    # protocol versions ahead of governance activation.
    for route in "${!expected_routes[@]}"; do
        if (( $(route_ready_count "$route") > 0 )); then
            admission_required_routes[$route]=1
        fi
    done
}

parent_admission_error=
check_parent_fleet_admission_once() {
    local route slot address state

    parent_admission_error=
    for slot in "${slots[@]}"; do
        if ! slot_ready "$slot"; then
            parent_admission_error="slot $slot is not healthy"
            return 1
        fi
        if ! address=$(slot_front_ip "$slot"); then
            parent_admission_error="slot $slot has no front-network address"
            return 1
        fi
        if parent_route_admitted --coarse "$address"; then
            :
        else
            state=$?
            parent_admission_error="parent does not admit slot $slot for coarse routing (status $state)"
            return 1
        fi
        for route in "${!admission_required_routes[@]}"; do
            if ! slot_route_ready "$slot" "$route"; then
                parent_admission_error="slot $slot cannot serve required route $route"
                return 1
            fi
            if parent_route_admitted "$route" "$address"; then
                continue
            else
                state=$?
                parent_admission_error="parent does not admit slot $slot for required route $route (status $state)"
                return 1
            fi
        done
    done
}

verify_parent_fleet_admission() {
    local deadline

    collect_required_admission_routes "$@"

    parent_proxy_active || fail \
        "cannot verify fleet admission: the active parent is not proxy-router"
    require_parent_diagnostic
    deadline=$((SECONDS + wait_timeout))
    while ((SECONDS < deadline)); do
        check_parent_fleet_admission_once && return 0
        sleep 1
    done
    echo "versiond-router-fleet: admission verification timed out: $parent_admission_error" >&2
    return 1
}

wait_version() {
    local route=$1 deadline missing slot ready parent_ready
    case $route in
        '' | *[/?#%]* | *[[:space:]]* | *\\* | *\"* | *\'* | . | ..)
            fail "version '$route' cannot be represented as a routed path segment"
            ;;
    esac
    expected_routes[$route]=1
    deadline=$((SECONDS + version_wait_timeout))
    while ((SECONDS < deadline)); do
        missing=
        for slot in "${slots[@]}"; do
            if ! slot_route_declared "$slot" "$route"; then
                missing="slot $slot has not learned version $route"
                break
            fi
        done
        if [[ -z $missing ]]; then
            ready=$(route_ready_count "$route")
            if ((ready < min_ready)); then
                missing="only $ready router slots can serve version $route; need $min_ready"
            fi
        fi
        if [[ -z $missing ]]; then
            if ! parent_proxy_active; then
                missing="active parent is not proxy-router"
            else
                require_parent_diagnostic
                parent_ready=$(parent_admitted_count "$route")
                if [[ $parent_ready == unknown ]]; then
                    missing="parent proxy has not learned version $route"
                elif ((parent_ready < min_ready)); then
                    missing="parent proxy admits only $parent_ready router slots for version $route; need $min_ready"
                fi
            fi
        fi
        if [[ -z $missing ]]; then
            printf 'versiond-router-fleet: version %s is ready on %s router slots and admitted end to end\n' \
                "$route" "$ready"
            return 0
        fi
        sleep 1
    done
    fail "version $route did not become ready within ${version_wait_timeout}s: $missing"
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
    local excluded=$1 route total reserve parent_reserve
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
    parent_proxy_active || return 0
    require_parent_diagnostic
    parent_reserve=$(parent_admitted_count --coarse "$excluded")
    [[ $parent_reserve == unknown ]] || ((parent_reserve >= min_ready)) || fail \
        "refusing to stop slot $excluded: parent proxy admits only $parent_reserve other coarse routers, need $min_ready"
    for route in "${!expected_routes[@]}"; do
        total=$(route_ready_count "$route")
        ((total > 0)) || continue
        parent_reserve=$(parent_admitted_count "$route" "$excluded")
        [[ $parent_reserve == unknown ]] && continue
        ((parent_reserve >= min_ready)) || fail \
            "refusing to stop slot $excluded: parent proxy admits only $parent_reserve other routers for version $route, need $min_ready"
    done
}

wait_slot_routes() {
    local slot=$1 deadline=$((SECONDS + wait_timeout))
    local route missing reason
    while ((SECONDS < deadline)); do
        missing=
        reason=
        for route in "${!expected_routes[@]}"; do
            if ! slot_route_declared "$slot" "$route"; then
                missing=$route
                reason="does not declare"
                break
            fi
            if (( $(route_ready_count "$route" "$slot") > 0 )) && \
                ! slot_route_ready "$slot" "$route"; then
                missing=$route
                reason="did not converge"
                break
            fi
        done
        [[ -z $missing ]] && return 0
        sleep 1
    done
    echo "versiond-router-fleet: slot $slot $reason expected route $missing within ${wait_timeout}s" >&2
    return 1
}

wait_rollback_routes() {
    local slot=$1 deadline=$((SECONDS + wait_timeout)) route missing
    while ((SECONDS < deadline)); do
        missing=
        for route in "${!rollback_routes[@]}"; do
            if (( $(route_ready_count "$route" "$slot") > 0 )) && \
                ! slot_route_ready "$slot" "$route"; then
                missing=$route
                break
            fi
        done
        [[ -z $missing ]] && return 0
        sleep 1
    done
    echo "versiond-router-fleet: restored slot $slot did not recover route $missing within ${wait_timeout}s" >&2
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

ensure_network() {
    local role=$1 network=$2 legacy_key=$3 ownership
    if "$docker_bin" network inspect "$network" >/dev/null 2>&1; then
        ownership=$("$docker_bin" network inspect --format \
            '{{or (index .Labels "ai.gonka.component") ""}}|{{or (index .Labels "ai.gonka.fleet") ""}}|{{or (index .Labels "ai.gonka.network-role") ""}}|{{or (index .Labels "com.docker.compose.network") ""}}' \
            "$network")
        case $ownership in
            "versiond-router-network|$fleet_id|$role|"*) return 0 ;;
            "|||$legacy_key")
                warn "adopting legacy Compose network $network as an external fleet resource"
                return 0
                ;;
            *) fail "network $network has unexpected ownership '$ownership'" ;;
        esac
    fi
    "$docker_bin" network create \
        --label ai.gonka.component=versiond-router-network \
        --label "ai.gonka.fleet=$fleet_id" \
        --label "ai.gonka.network-role=$role" \
        "$network" >/dev/null
}

prepare_networks() {
    ensure_network front \
        "${VERSIOND_ROUTER_FRONT_NETWORK:-gonka-versiond-router-front}" \
        versiond-router-front
    ensure_network back \
        "${VERSIOND_ROUTER_BACK_NETWORK:-gonka-versiond-router-back}" \
        versiond-router-back
}

prepare_slot_networks() {
    prepare_networks
    resolve_metrics_network
    ensure_metrics_network
}

pull_router_image() {
    [[ $pull_policy == never ]] || \
        slot_compose "${slots[0]}" pull --policy "$pull_policy" router
}

desired_slot_config_hash() {
    local slot=$1 service hash
    read -r service hash < <(slot_compose "$slot" config --hash router)
    [[ $service == router && -n $hash ]] || fail \
        "cannot calculate the desired Compose configuration hash for slot $slot"
    printf '%s\n' "$hash"
}

slot_needs_replacement() {
    local slot=$1 candidate_image_id=$2 id running_image running_hash desired_hash
    id=$(slot_id "$slot") || return 2
    running_image=$($docker_bin inspect --format '{{.Image}}' "$id") || return 2
    running_hash=$($docker_bin inspect --format \
        '{{or (index .Config.Labels "com.docker.compose.config-hash") ""}}' \
        "$id") || return 2
    desired_hash=$(desired_slot_config_hash "$slot") || return 2
    [[ $running_image != "$candidate_image_id" || $running_hash != "$desired_hash" ]]
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

resolve_metrics_network() {
    local slot id recorded parent project network ownership
    local resolved=${VERSIOND_ROUTER_METRICS_NETWORK:-}

    if [[ -z $resolved ]]; then
        for slot in "${slots[@]}"; do
            id=$(slot_id "$slot") || continue
            recorded=$(container_env_value \
                "$id" VERSIOND_ROUTER_METRICS_NETWORK_NAME) || continue
            [[ -z $resolved || $resolved == "$recorded" ]] || fail \
                "router slots record different metrics networks: $resolved and $recorded"
            resolved=$recorded
        done
    fi

    if [[ -z $resolved ]]; then
        parent=${PROXY_ROUTER_CONTAINER:-proxy}
        project=$("$docker_bin" inspect --format \
            '{{or (index .Config.Labels "com.docker.compose.project") ""}}' \
            "$parent" 2>/dev/null) || project=
        if [[ -n $project ]]; then
            # Docker's Go-template variables are literals for the Docker CLI.
            # shellcheck disable=SC2016
            while IFS= read -r network; do
                [[ -n $network ]] || continue
                ownership=$("$docker_bin" network inspect --format \
                    '{{or (index .Labels "com.docker.compose.network") ""}}|{{or (index .Labels "com.docker.compose.project") ""}}' \
                    "$network" 2>/dev/null) || continue
                [[ $ownership == "default|$project" ]] || continue
                [[ -z $resolved ]] || fail \
                    "parent proxy is attached to multiple Compose default networks"
                resolved=$network
            done < <("$docker_bin" inspect --format \
                '{{range $name, $network := .NetworkSettings.Networks}}{{println $name}}{{end}}' \
                "$parent")
        fi
    fi

    case $resolved in
        '' )
            fail "cannot identify the main Compose default network for router metrics; set VERSIOND_ROUTER_METRICS_NETWORK explicitly"
            ;;
        *[!A-Za-z0-9_.-]*)
            fail "invalid VERSIOND_ROUTER_METRICS_NETWORK '$resolved'"
            ;;
    esac
    VERSIOND_ROUTER_METRICS_NETWORK=$resolved
    export VERSIOND_ROUTER_METRICS_NETWORK
}

ensure_metrics_network() {
    local network=$VERSIOND_ROUTER_METRICS_NETWORK
    local parent=${PROXY_ROUTER_CONTAINER:-proxy} attached ownership

    "$docker_bin" network inspect "$network" >/dev/null 2>&1 || fail \
        "main Compose metrics network $network does not exist"
    attached=$("$docker_bin" inspect --format \
        "{{with index .NetworkSettings.Networks \"$network\"}}{{.IPAddress}}{{end}}" \
        "$parent" 2>/dev/null) || attached=
    if [[ -n $attached ]]; then
        return 0
    fi
    ownership=$("$docker_bin" network inspect --format \
        '{{or (index .Labels "com.docker.compose.network") ""}}' \
        "$network")
    [[ $ownership == default ]] || fail \
        "metrics network $network is neither attached to $parent nor a Compose default network"
}

container_env_value_or_legacy_default() {
    local id=$1 key=$2
    if container_env_value "$id" "$key"; then
        return 0
    fi
    [[ -v legacy_env_defaults[$key] ]] || return 1
    printf '%s\n' "${legacy_env_defaults[$key]}"
}

running_placement_contract() {
    local id=$1 pool_host back_network legacy_host legacy_versions ha_versions
    local catalog_url coarse_readiness dns_resolver
    pool_host=$(container_env_value "$id" VERSIOND_POOL_HOST) || return 1
    back_network=$(container_env_value "$id" VERSIOND_ROUTER_BACK_NETWORK_NAME) || return 1
    legacy_host=$(container_env_value "$id" VERSIOND_LEGACY_HOST) || return 1
    legacy_versions=$(container_env_value "$id" VERSIOND_NON_HA_VERSIONS) || return 1
    ha_versions=$(container_env_value "$id" VERSIOND_VERSIONS) || return 1
    catalog_url=$(container_env_value_or_legacy_default "$id" VERSIOND_ROUTING_CATALOG_URL) || return 1
    coarse_readiness=$(container_env_value_or_legacy_default "$id" VERSIOND_ROUTER_ALLOW_COARSE_READINESS) || return 1
    dns_resolver=$(container_env_value_or_legacy_default "$id" HAPROXY_DNS_RESOLVER) || return 1
    placement_contract "$pool_host" "$back_network" "$legacy_host" \
        "$legacy_versions" "$ha_versions" "$catalog_url" "$coarse_readiness" "$dns_resolver"
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

validate_inventory_structure() {
    local id slot
    local -A seen=()

    while IFS= read -r id; do
        [[ -n $id ]] || continue
        slot=$($docker_bin inspect --format \
            '{{or (index .Config.Labels "ai.gonka.slot") ""}}' "$id") || fail \
            "router container $id disappeared while inventory was collected"
        [[ -n ${expected[$slot]-} ]] || fail \
            "orphan router container $id declares unknown slot '$slot'; use down --maintenance to clean the fleet"
        [[ -z ${seen[$slot]-} ]] || fail \
            "duplicate containers claim router slot '$slot'"
        seen[$slot]=$id
    done < <(fleet_ids)

}

repair_fleet_capacity() {
    local slot id state
    for slot in "${slots[@]}"; do
        if ! id=$(slot_id "$slot"); then
            echo "Recovering absent versiond-router slot $slot"
            start_slot "$slot"
        elif slot_ready "$slot"; then
            continue
        else
            state=$($docker_bin inspect --format '{{.State.Status}}' "$id") || fail \
                "router slot $slot disappeared during recovery"
            echo "Recovering non-ready versiond-router slot $slot (state $state)"
            case $state in
                running | restarting | paused)
                    # This slot is already outside the ready reserve, but it may
                    # still own accepted SSE responses. Drain it without
                    # consuming any healthy peer.
                    slot_compose "$slot" stop --timeout "$drain_timeout" router
                    ;;
                created | exited | dead) ;;
                *) fail "slot $slot cannot be recovered from container state '$state'" ;;
            esac
            slot_compose "$slot" up -d --force-recreate --wait \
                --wait-timeout "$wait_timeout" router
        fi
        wait_slot_routes "$slot"
        wait_parent_admission "$slot"
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
            maintenance_env["$slot:$key"]=$(container_env_value_or_legacy_default "$id" "$key") || fail \
                "slot $slot is missing environment $key"
        done
        while read -r route; do
            [[ -n $route ]] || continue
            if [[ -n ${candidate_routes[$route]-} ]] || \
                ! route_in_static_environment "$route" \
                    "${maintenance_env[$slot:VERSIOND_NON_HA_VERSIONS]}" \
                    "${maintenance_env[$slot:VERSIOND_VERSIONS]}"; then
                routes[$route]=1
            fi
        done < <(slot_catalog_routes "$slot")
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
                VERSIOND_ROUTING_CATALOG_URL="${maintenance_env[$slot:VERSIOND_ROUTING_CATALOG_URL]}" \
                VERSIOND_ROUTING_CATALOG_POLL_SECONDS="${maintenance_env[$slot:VERSIOND_ROUTING_CATALOG_POLL_SECONDS]}" \
                VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS="${maintenance_env[$slot:VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS]}" \
                VERSIOND_ROUTING_ACTIVATION_MIN_READY="${maintenance_env[$slot:VERSIOND_ROUTING_ACTIVATION_MIN_READY]}" \
                VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS="${maintenance_env[$slot:VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS]}" \
                VERSIOND_ROUTER_VERSION_CAPACITY="${maintenance_env[$slot:VERSIOND_ROUTER_VERSION_CAPACITY]}" \
                HAPROXY_DNS_RESOLVER="${maintenance_env[$slot:HAPROXY_DNS_RESOLVER]}" \
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
        if VERSIOND_ROUTER_IMAGE=$rollback_image \
            VERSIOND_POOL_HOST="${rollback_env[VERSIOND_POOL_HOST]}" \
            VERSIOND_ROUTER_BACK_NETWORK="${rollback_env[VERSIOND_ROUTER_BACK_NETWORK_NAME]}" \
            VERSIOND_LEGACY_HOST="${rollback_env[VERSIOND_LEGACY_HOST]}" \
            VERSIOND_NON_HA_VERSIONS="${rollback_env[VERSIOND_NON_HA_VERSIONS]}" \
            VERSIOND_VERSIONS="${rollback_env[VERSIOND_VERSIONS]}" \
            VERSIOND_ROUTER_ALLOW_COARSE_READINESS="${rollback_env[VERSIOND_ROUTER_ALLOW_COARSE_READINESS]}" \
            VERSIOND_ROUTING_CATALOG_URL="${rollback_env[VERSIOND_ROUTING_CATALOG_URL]}" \
            VERSIOND_ROUTING_CATALOG_POLL_SECONDS="${rollback_env[VERSIOND_ROUTING_CATALOG_POLL_SECONDS]}" \
            VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS="${rollback_env[VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS]}" \
            VERSIOND_ROUTING_ACTIVATION_MIN_READY="${rollback_env[VERSIOND_ROUTING_ACTIVATION_MIN_READY]}" \
            VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS="${rollback_env[VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS]}" \
            VERSIOND_ROUTER_VERSION_CAPACITY="${rollback_env[VERSIOND_ROUTER_VERSION_CAPACITY]}" \
            HAPROXY_DNS_RESOLVER="${rollback_env[HAPROXY_DNS_RESOLVER]}" \
            slot_compose "$current_slot" up -d --wait \
                --wait-timeout "$wait_timeout" router && \
            wait_rollback_routes "$current_slot"; then
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

stop_fleet_containers() {
    local id state pid failed=0
    local -a ids=() pids=()
    mapfile -t ids < <(fleet_ids)
    if ((${#ids[@]} == 0)); then
        echo "No versiond-router fleet containers are present"
        return 0
    fi

    # All routers receive their soft-stop together. The maintenance deadline is
    # therefore one fleet-wide wall-clock budget rather than N sequential waits.
    for id in "${ids[@]}"; do
        state=$($docker_bin inspect --format '{{.State.Status}}' "$id") || fail \
            "router container $id disappeared before maintenance stop"
        case $state in
            running | restarting | paused)
                "$docker_bin" stop --time "$drain_timeout" "$id" >/dev/null &
                pids+=("$!")
                ;;
            created | exited | dead) ;;
            *) fail "router container $id has unsupported state '$state'" ;;
        esac
    done
    for pid in "${pids[@]}"; do
        wait "$pid" || failed=1
    done
    ((failed == 0)) || fail "one or more router containers did not stop cleanly"
}

declare -a cleanup_networks=()

add_cleanup_network() {
    local network=$1 role=${2:-} legacy_key=${3:-} ownership
    local existing
    "$docker_bin" network inspect "$network" >/dev/null 2>&1 || return 0
    for existing in "${cleanup_networks[@]}"; do
        [[ $existing != "$network" ]] || return 0
    done
    ownership=$($docker_bin network inspect --format \
        '{{or (index .Labels "ai.gonka.component") ""}}|{{or (index .Labels "ai.gonka.fleet") ""}}|{{or (index .Labels "ai.gonka.network-role") ""}}|{{or (index .Labels "com.docker.compose.network") ""}}' \
        "$network")
    case $ownership in
        "versiond-router-network|$fleet_id|"*)
            if [[ -n $role && $ownership != "versiond-router-network|$fleet_id|$role|"* ]]; then
                fail "network $network has unexpected ownership '$ownership'"
            fi
            ;;
        "|||$legacy_key")
            [[ -n $role && -n $legacy_key ]] || fail \
                "network $network has legacy ownership outside the current fleet topology"
            ;;
        *) fail "network $network has unexpected ownership '$ownership'" ;;
    esac
    cleanup_networks+=("$network")
}

collect_cleanup_networks() {
    local network_id network
    cleanup_networks=()
    add_cleanup_network \
        "${VERSIOND_ROUTER_FRONT_NETWORK:-gonka-versiond-router-front}" \
        front versiond-router-front
    add_cleanup_network \
        "${VERSIOND_ROUTER_BACK_NETWORK:-gonka-versiond-router-back}" \
        back versiond-router-back
    while IFS= read -r network_id; do
        [[ -n $network_id ]] || continue
        network=$($docker_bin network inspect --format '{{.Name}}' "$network_id") || fail \
            "fleet network $network_id disappeared while cleanup was prepared"
        add_cleanup_network "$network"
    done < <("$docker_bin" network ls -q \
        --filter label=ai.gonka.component=versiond-router-network \
        --filter "label=ai.gonka.fleet=$fleet_id")
}

require_networks_detached_from_main_stack() {
    local id network attachment name
    local -A fleet_containers=()
    while IFS= read -r id; do
        [[ -n $id ]] && fleet_containers[$id]=1
    done < <(fleet_ids)
    for network in "${cleanup_networks[@]}"; do
        # Docker's Go template variables are literals for the Docker CLI.
        # shellcheck disable=SC2016
        while IFS= read -r attachment; do
            [[ -n $attachment ]] || continue
            [[ -n ${fleet_containers[$attachment]-} ]] && continue
            name=$($docker_bin inspect --format '{{.Name}}' "$attachment" 2>/dev/null || printf '%s' "$attachment")
            fail "network $network is still attached to non-fleet container ${name#/}; run the main Compose down before fleet down"
        done < <("$docker_bin" network inspect --format \
            '{{range $id, $container := .Containers}}{{println $id}}{{end}}' \
            "$network")
    done
}

fleet_down() {
    local network
    local -a ids=() volumes=()
    collect_cleanup_networks
    require_networks_detached_from_main_stack
    stop_fleet_containers
    mapfile -t ids < <(fleet_ids)
    if ((${#ids[@]} > 0)); then
        "$docker_bin" rm "${ids[@]}" >/dev/null
    fi
    mapfile -t volumes < <(fleet_volume_ids)
    if ((${#volumes[@]} > 0)); then
        "$docker_bin" volume rm "${volumes[@]}" >/dev/null
    fi
    for network in "${cleanup_networks[@]}"; do
        echo "Removing versiond-router fleet network $network"
        "$docker_bin" network rm "$network" >/dev/null
    done
}

fleet_status() {
    local id slot state health image seen_slot route details
    local -A seen=()
    local bad=0
    printf '%-16s %-12s %-10s %s\n' SLOT STATE HEALTH IMAGE
    while read -r id; do
        [[ -n $id ]] || continue
        if ! slot=$($docker_bin inspect --format \
            '{{index .Config.Labels "ai.gonka.slot"}}' "$id" 2>/dev/null); then
            warn "router container $id disappeared while status was collected"
            bad=1
            continue
        fi
        if [[ -z ${expected[$slot]-} ]]; then
            warn "orphan router container $id declares unknown slot '$slot'"
            bad=1
        fi
        if [[ -n ${seen[$slot]-} ]]; then
            warn "duplicate containers claim router slot '$slot'"
            bad=1
        fi
        seen[$slot]=$id
        details=$($docker_bin inspect --format \
            '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}} {{.Config.Image}}' \
            "$id" 2>/dev/null) || {
                warn "router container $id disappeared while status was collected"
                bad=1
                continue
            }
        read -r state health image <<<"$details"
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
    if parent_proxy_active; then
        if ! parent_diagnostic_available; then
            warn "active parent proxy has no route-status diagnostic"
            bad=1
        else
            collect_required_admission_routes
            if check_parent_fleet_admission_once; then
                printf 'PARENT_ADMISSION admitted\n'
            else
                warn "parent admission is incomplete: $parent_admission_error"
                bad=1
            fi
        fi
    else
        printf 'PARENT_ADMISSION not-applicable (active parent is not proxy-router)\n'
    fi
    return "$bad"
}

fleet_up() {
    local slot
    prepare_slot_networks
    pull_router_image
    candidate_image=${VERSIOND_ROUTER_IMAGE:-ghcr.io/product-science/versiond-router:0.2.15-devshard-v5}
    require_placement_compatible "$candidate_image"
    for slot in "${slots[@]}"; do
        echo "Ensuring versiond-router slot $slot is running"
        start_existing_or_create_slot "$slot"
        wait_slot_routes "$slot"
        wait_parent_admission "$slot"
    done
    fleet_status
}

fleet_apply() {
    local -a ids=()
    mapfile -t ids < <(fleet_ids)
    if ((${#ids[@]} == 0)); then
        echo "Bootstrapping absent versiond-router fleet"
        fleet_up
        return
    fi

    # Repair missing capacity before touching a known-good slot. Retries
    # converge a partial fleet, while duplicates and unknown slots still fail
    # before the first mutation.
    validate_inventory_structure
    prepare_slot_networks
    pull_router_image
    candidate_image=${VERSIOND_ROUTER_IMAGE:-ghcr.io/product-science/versiond-router:0.2.15-devshard-v5}
    require_placement_compatible "$candidate_image"
    repair_fleet_capacity
    fleet_rollout
}

fleet_rollout() {
    local slot id old_image rollback_tag key route candidate_image_id replacement_status
    prepare_slot_networks
    pull_router_image
    candidate_image=${VERSIOND_ROUTER_IMAGE:-ghcr.io/product-science/versiond-router:0.2.15-devshard-v5}
    candidate_image_id=$($docker_bin image inspect --format '{{.Id}}' "$candidate_image") || fail \
        "candidate router image is not available: $candidate_image"
    require_placement_compatible "$candidate_image"
    trap rollback_current ERR INT TERM HUP
    for slot in "${slots[@]}"; do
        id=$(slot_id "$slot") || fail "slot $slot has no unique existing container"
        replacement_status=0
        slot_needs_replacement "$slot" "$candidate_image_id" || replacement_status=$?
        if ((replacement_status == 1)); then
            echo "Versiond-router slot $slot already matches the requested image and configuration"
            wait_slot_routes "$slot"
            wait_parent_admission "$slot"
            continue
        fi
        ((replacement_status == 0)) || fail \
            "cannot compare the running and requested contracts for slot $slot"
        require_ready_reserve "$slot"
        old_image=$($docker_bin inspect --format '{{.Image}}' "$id")
        rollback_tag="gonka/versiond-router-rollback:$operation_id-$slot"
        "$docker_bin" tag "$old_image" "$rollback_tag"
        rollback_env=()
        rollback_routes=()
        for key in "${maintenance_keys[@]}"; do
            rollback_env[$key]=$(container_env_value_or_legacy_default "$id" "$key") || fail \
                "slot $slot is missing environment $key"
        done
        while IFS= read -r route; do
            [[ -n $route ]] && rollback_routes[$route]=1
        done < <(slot_catalog_routes "$slot")
        current_slot=$slot
        rollback_image=$rollback_tag
        echo "Draining versiond-router slot $slot"
        slot_compose "$slot" stop --timeout "$drain_timeout" router
        echo "Starting replacement for slot $slot"
        start_slot "$slot"
        slot_ready "$slot" || false
        wait_slot_routes "$slot"
        wait_parent_admission "$slot"
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
    prepare_slot_networks
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

    # Validate the candidate's complete live view before crossing the commit
    # point. This catches partial catalog convergence while exact rollback
    # images and environments are still available.
    select_candidate_route_view
    fleet_status

    maintenance_active=false
    trap - ERR INT TERM HUP
    for slot in "${slots[@]}"; do
        "$docker_bin" image rm "${maintenance_images[$slot]}" >/dev/null 2>&1 || true
    done
}

if [[ ! -e $lock_file ]]; then
    (umask 000; : >"$lock_file") || fail "cannot create fleet lock $lock_file"
fi
exec 9<"$lock_file"
flock -n 9 || fail "another fleet operation holds $lock_file"

# Runtime-discovered versions are part of the safety reserve even though they
# are intentionally absent from the host-managed bootstrap environment.
discover_expected_routes

command=${1:-}
case $command in
    prepare-networks) prepare_networks ;;
    up) fleet_up ;;
    apply) fleet_apply ;;
    rollout) fleet_rollout ;;
    maintenance-rollout) fleet_maintenance_rollout ;;
    verify-admission)
        shift
        verify_parent_fleet_admission "$@"
        ;;
    wait-version)
        [[ $# == 2 ]] || fail "wait-version requires exactly one VERSION"
        wait_version "$2"
        ;;
    status) fleet_status ;;
    stop)
        [[ $# == 2 ]] || fail "stop requires exactly one SLOT"
        stop_slot "$2"
        ;;
    stop-all)
        [[ $# == 2 && $2 == --maintenance ]] || fail \
            "stop-all requires the explicit --maintenance acknowledgement"
        stop_fleet_containers
        ;;
    down)
        [[ $# == 2 && $2 == --maintenance ]] || fail \
            "down requires the explicit --maintenance acknowledgement"
        fleet_down
        ;;
    start)
        [[ $# == 2 ]] || fail "start requires exactly one SLOT"
        prepare_slot_networks
        pull_router_image
        candidate_image=${VERSIOND_ROUTER_IMAGE:-ghcr.io/product-science/versiond-router:0.2.15-devshard-v5}
        require_placement_compatible "$candidate_image"
        start_existing_or_create_slot "$2"
        wait_slot_routes "$2"
        wait_parent_admission "$2"
        ;;
    -h | --help | help) usage ;;
    *) usage; fail "unknown command '${command:-}'" ;;
esac
