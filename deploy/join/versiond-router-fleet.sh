#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
config_env=${GONKA_CONFIG_ENV:-$script_dir/config.env}
slot_file=$script_dir/versiond-router-slot/docker-compose.yml
current_slot=
# Container ID of the generation a failing replacement puts back.
rollback_generation=
maintenance_active=false
# True while this run holds previous generations on purpose (between drain
# and commit); status then does not report them as an interrupted run.
operation_in_flight=false
declare -A inherited_env=()
# slot -> container ID of the generation a failed maintenance restores.
declare -A maintenance_restore=()
# slot -> container ID of an unproven candidate a maintenance run removes.
declare -A maintenance_stale=()
declare -A legacy_env_defaults=(
    [VERSIOND_ROUTING_CATALOG_URL]=''
    [VERSIOND_ROUTING_CATALOG_POLL_SECONDS]=5
    [VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS]=3
    [VERSIOND_ROUTING_ACTIVATION_MIN_READY]=2
    [VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS]=86400
    [VERSIOND_ROUTER_ALLOW_COARSE_READINESS]=false
    [VERSIOND_ROUTER_TRUST_FORWARDED_HEADERS]=false
    [VERSIOND_ROUTER_VERSION_CAPACITY]=32
    [HAPROXY_DNS_RESOLVER]=127.0.0.11:53
)
maintenance_pending=()
# Slots whose serving candidate still has its previous generation next to it
# from an interrupted run: not replaced again, but rolled back and committed
# together with the pending ones, so a retry never loses their exact record.
maintenance_kept=()
inventory_ids=()
inventory_listing=

fail() {
    echo "versiond-router-fleet: $*" >&2
    exit 1
}

# Every exec into a router is bounded: a wedged Runtime API socket or a hung
# container must not hold the deployment lock forever.
docker_exec() {
    timeout --kill-after=5s "${VERSIOND_ROUTER_EXEC_TIMEOUT_SECONDS:-30}" \
        "$docker_bin" exec "$@"
}

warn() {
    echo "versiond-router-fleet: warning: $*" >&2
}

usage() {
    cat >&2 <<'EOF'
Usage: versiond-router-fleet.sh COMMAND [ARGS]

Commands:
  spec-hash          Print the canonical desired fleet specification SHA-256.
  prepare-networks   Create or validate the fleet-owned front/back networks.
  up                 Create missing slots or start existing containers unchanged.
  apply              Bootstrap an absent fleet or roll changed slots one at a time.
  rollout            Replace slots one at a time, preserving the ready reserve.
  maintenance-rollout
                     Drain the old fleet before a placement-contract change.
                     Rerun it after an interruption: slots already on the new
                     contract are kept, the rest are replaced. Rollback covers
                     the slots captured by the current run only.
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
"0 1 2"; every slot generation is its own Compose project
(<prefix>-<slot>-<generation>). While a replacement is uncommitted, the
previous generation of the slot stays as a stopped container: a rollback
starts exactly what served before, and a rerun after an interruption finds
that record in Docker itself.
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

normalize_versions() {
    printf '%s\n' "$1" | tr ',;' '  ' | tr -s ' ' '\n' | \
        sed '/^$/d' | LC_ALL=C sort -u | paste -sd, -
}

project_prefix=${VERSIOND_ROUTER_PROJECT_PREFIX:-gonka-versiond-router}
fleet_id=${VERSIOND_ROUTER_FLEET_ID:-$project_prefix}
slot_list=${VERSIOND_ROUTER_FLEET_SLOTS:-0 1 2}
min_ready=${VERSIOND_ROUTER_MIN_READY:-2}
drain_timeout=${VERSIOND_ROUTER_DRAIN_TIMEOUT_SECONDS:-1800}
wait_timeout=${VERSIOND_ROUTER_START_TIMEOUT_SECONDS:-60}
version_wait_timeout=${VERSIOND_ROUTING_ACTIVATION_TIMEOUT_SECONDS:-2100}
pull_policy=${VERSIOND_ROUTER_PULL_POLICY:-always}
config_dir=$(cd -- "$(dirname -- "$config_env")" && pwd -P)

command -v "$docker_bin" >/dev/null 2>&1 || fail "$docker_bin is required"
command -v flock >/dev/null 2>&1 || fail "flock is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"
command -v sha256sum >/dev/null 2>&1 || fail "sha256sum is required"
# shellcheck source=deploy/join/deployment-lock.sh
source "$script_dir/deployment-lock.sh"

# Explicit versiond endpoints for bare-metal multi-host pools. A relative path
# in config.env is resolved from the directory of config.env. The list is read
# once and handed to every slot generation as an environment value with its
# SHA-256, so a generation carries its own membership: an edited file changes
# nothing until the fleet is rolled, and a restored generation keeps the list
# it was created with. Membership is part of the placement contract; a
# change goes through maintenance-rollout so that one consistent-hash ring is
# replaced by another instead of two rings serving at once.
endpoints_overlay=$script_dir/versiond-router-slot/docker-compose.endpoints.yml
endpoints_host_file=
if [[ -n ${VERSIOND_POOL_ENDPOINTS_FILE:-} ]]; then
    endpoints_host_file=$VERSIOND_POOL_ENDPOINTS_FILE
    [[ $endpoints_host_file == /* ]] || \
        endpoints_host_file=$config_dir/$endpoints_host_file
    [[ -r $endpoints_host_file ]] || fail \
        "VERSIOND_POOL_ENDPOINTS_FILE is not readable: $endpoints_host_file"
    endpoints_host_file=$(cd -- "$(dirname -- "$endpoints_host_file")" && pwd -P)/$(basename -- "$endpoints_host_file")
    jq -e '
        type == "array" and length > 0 and
        all(.[]; type == "object" and
            (.id | type == "string" and test("^[A-Za-z0-9][A-Za-z0-9._-]*$")) and
            (.host | type == "string" and test("^[A-Za-z0-9][A-Za-z0-9.-]*$")) and
            ((.port == null) or
             (.port | type == "number" and . >= 1 and . <= 65535 and floor == .))) and
        ([.[].id] | length == (unique | length))
    ' "$endpoints_host_file" >/dev/null || fail \
        "invalid versiond endpoint file $endpoints_host_file: expected a non-empty JSON array of {id, host, port} entries with unique ids"
    VERSIOND_POOL_ENDPOINTS=$(jq -c . "$endpoints_host_file") || fail \
        "cannot read the versiond endpoint file $endpoints_host_file"
    export VERSIOND_POOL_ENDPOINTS
    VERSIOND_POOL_ENDPOINTS_SHA256=$(printf '%s' "$VERSIOND_POOL_ENDPOINTS" | sha256sum | awk '{print $1}')
    export VERSIOND_POOL_ENDPOINTS_SHA256
fi

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
    case $slot in '' | *[!0-9]*) fail "invalid router slot '$slot': slots are non-negative integers" ;; esac
    [[ -z ${expected[$slot]-} ]] || fail "router slot '$slot' is declared twice"
    expected[$slot]=1
done
((min_ready < ${#slots[@]})) || fail "VERSIOND_ROUTER_MIN_READY must be smaller than the fleet"

declare -A candidate_routes=()
declare -A expected_routes=()
# HA versions the candidate configuration declares; a pinned non-HA version
# has one owner that may be down on purpose and is never required.
declare -A candidate_ha_routes=()
for version in $(normalize_versions "${VERSIOND_VERSIONS-v4 v5}" | tr ',' ' '); do
    candidate_ha_routes[$version]=1
done
while read -r route; do
    [[ -n $route ]] || continue
    candidate_routes[$route]=1
    expected_routes[$route]=1
done < <(printf '%s\n' \
    "${VERSIOND_NON_HA_VERSIONS-v1 v2 v3}" \
    "${VERSIOND_VERSIONS-v4 v5}" \
    | tr ',;' '  ' | tr -s ' ' '\n')



# Three route sets drive every gate:
#
#   required   HA versions the candidate configuration declares. Every
#              candidate generation must serve them before it counts as
#              ready; a fleet in which one of them has no ready router is
#              reported as broken, because that is the pool or its PostgreSQL
#              being down, not a route to stop caring about.
#   protected  routes that had a ready router when the run started and that
#              the candidate keeps (declared HA versions, and catalog routes).
#              Losing every ready router for one of them mid-run stops the
#              run; a candidate that declares one must serve it.
#   expected   everything the fleet tracks for admission and reserve: the
#              two sets above, pinned non-HA versions, and catalog routes.
#              A tracked route with zero ready routers is visible in status.
#
# A version the configuration adds is served by no running router yet: it is
# the candidate's job, never a reason to refuse the rollout that introduces
# it. A version the configuration withdraws is not protected: the rollout
# that removes it is allowed to stop serving it.
declare -A required_routes=()
declare -A protected_routes=()
for version in "${!candidate_ha_routes[@]}"; do
    required_routes[$version]=1
done
case ${VERSIOND_ROUTER_ALLOW_UNSERVED_STATIC_ROUTES:-false} in
    1 | true | yes) required_routes=() ;;
esac


placement_contract() {
    local pool_host=$1 back_network=$2 legacy_host=$3 legacy_versions=$4
    local ha_versions=$5 catalog_url=$6 coarse_readiness=$7 dns_resolver=$8
    local membership=$9
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
    # Pool membership places escrows on the consistent-hash ring: two rings
    # must never serve at once, so a membership change is a contract change.
    printf 'pool=%s;back-network=%s;legacy-host=%s;legacy-versions=%s;routing-mode=%s;catalog-url=%s;coarse-readiness=%s;dns-resolver=%s;membership=%s\n' \
        "$pool_host" "$back_network" "$legacy_host" "$normalized" \
        "$routing_mode" "$catalog_url" "$coarse_readiness" "$dns_resolver" "$membership"
}

# The membership description of a pool: an explicit endpoint list by its
# hash, a legacy host list by its normalized content, or DNS discovery.
pool_membership() {
    local endpoints_sha=$1 hosts=$2
    if [[ -n $endpoints_sha ]]; then
        printf 'endpoints:%s\n' "$endpoints_sha"
    elif [[ -n $(normalize_versions "$hosts") ]]; then
        printf 'hosts:%s\n' "$(normalize_versions "$hosts")"
    else
        printf 'dns\n'
    fi
}

candidate_placement_contract() {
    placement_contract \
        "${VERSIOND_POOL_HOST:-versiond-pool}" \
        "${VERSIOND_ROUTER_BACK_NETWORK:-gonka-versiond-router-back}" \
        "${VERSIOND_LEGACY_HOST:-versiond}" \
        "${VERSIOND_NON_HA_VERSIONS-v1 v2 v3}" \
        "${VERSIOND_VERSIONS-v4 v5}" \
        "${VERSIOND_ROUTING_CATALOG_URL-http://versiond-routing-oracle:9100/versions}" \
        "${VERSIOND_ROUTER_ALLOW_COARSE_READINESS:-false}" \
        "${HAPROXY_DNS_RESOLVER:-127.0.0.11:53}" \
        "$(pool_membership "${VERSIOND_POOL_ENDPOINTS_SHA256:-}" "${VERSIOND_HOSTS:-}")"
}

# Every generation of a slot is its own Compose project, named
# <prefix>-<slot>-<generation>. The state volume is shared by name across the
# generations of a slot, so a replacement starts with the catalog cache of
# the generation it replaces.
slot_compose() {
    local slot=$1 generation=$2
    local -a slot_files=(-f "$slot_file")
    shift 2
    [[ -n ${VERSIOND_ROUTER_METRICS_NETWORK:-} ]] || resolve_metrics_network
    [[ -z $endpoints_host_file ]] || slot_files+=(-f "$endpoints_overlay")
    VERSIOND_ROUTER_SLOT=$slot VERSIOND_ROUTER_FLEET_ID=$fleet_id \
        VERSIOND_ROUTER_STATE_VOLUME=$(slot_state_volume "$slot") \
        "$docker_bin" compose \
        --project-directory "$script_dir" \
        --project-name "$project_prefix-$slot-$generation" \
        "${slot_files[@]}" "$@"
}

# The volume name Compose gave the slot before generations existed, so an
# updated fleet keeps its catalog caches.
slot_state_volume() {
    printf '%s-%s_router-state\n' "$project_prefix" "$1"
}

# A generation identifier: unique per replacement, valid in a project name.
new_generation() {
    local id
    id=$(tr -d '-' </proc/sys/kernel/random/uuid) || fail "cannot allocate a generation identifier"
    printf '%s\n' "${id:0:12}"
}

# fleet_spec_hash [--without-image]: the canonical specification. The
# commit marker stores the variant without the image reference next to the
# image ID, so a mutable tag pinned to its digest still matches.
fleet_spec_hash() {
    local slot rendered config_hash manifest_hash index=0 filter=.
    [[ ${1:-} != --without-image ]] || filter='del(.services.router.image)'
    manifest_hash=$(sha256sum "$slot_file" | awk '{print $1}')
    {
        printf 'schema=1\n'
        printf 'fleet_id=%s\n' "$fleet_id"
        printf 'project_prefix=%s\n' "$project_prefix"
        printf 'min_ready=%s\n' "$min_ready"
        printf 'slot_manifest_sha256=%s\n' "$manifest_hash"
        printf 'endpoints_sha256=%s\n' "${VERSIOND_POOL_ENDPOINTS_SHA256:-none}"
        for slot in "${slots[@]}"; do
            rendered=$(slot_compose "$slot" render config --format json) || fail \
                "cannot render fleet slot '$slot' for specification hashing"
            config_hash=$(jq -Sc "$filter" <<<"$rendered" | sha256sum | awk '{print $1}')
            printf 'slot.%06d.name=%s\n' "$index" "$slot"
            printf 'slot.%06d.config_sha256=%s\n' "$index" "$config_hash"
            ((index += 1))
        done
    } | sha256sum | awk '{print $1}'
}

slot_ids() {
    local slot=$1
    "$docker_bin" ps -aq --no-trunc \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot"
}

# "created id state" for every container of a slot, newest first.
slot_containers() {
    local slot=$1 ids
    local -a id_list=()
    ids=$(slot_ids "$slot") || return 3
    [[ -n $ids ]] || return 0
    mapfile -t id_list <<<"$ids"
    "$docker_bin" inspect --format '{{.Created}} {{.Id}} {{.State.Status}}' "${id_list[@]}" | sort -r || return 3
}

# Classifies the containers of a slot. slot_current is the newest one: the
# generation that serves, or the candidate of an uncommitted replacement;
# slot_previous is the older generation that replacement would put back.
# Returns 1 for an absent slot, 2 for containers the fleet never produces
# (three generations, or two running at once), 3 when Docker did not answer.
slot_current=
slot_current_state=
slot_previous=
slot_previous_state=
slot_generations() {
    local slot=$1 listing count running=0 created id state
    slot_current=
    slot_current_state=
    slot_previous=
    slot_previous_state=
    listing=$(slot_containers "$slot") || {
        echo "versiond-router-fleet: cannot list router slot $slot" >&2
        return 3
    }
    [[ -n $listing ]] || return 1
    count=$(wc -l <<<"$listing")
    ((count <= 2)) || return 2
    while read -r created id state; do
        [[ -n $created ]] || continue
        [[ $state != running ]] || ((running += 1))
        if [[ -z $slot_current ]]; then
            slot_current=$id
            slot_current_state=$state
        else
            slot_previous=$id
            slot_previous_state=$state
        fi
    done <<<"$listing"
    ((running <= 1)) || return 2
    return 0
}

fleet_ids() {
    "$docker_bin" ps -aq --no-trunc \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id"
}

# Every fleet container ID, or a hard failure when Docker cannot answer. A
# failed listing must never read as an empty fleet: that would bootstrap new
# slots next to the existing ones or skip the drain and admission gates.
fleet_inventory() {
    inventory_listing=$(fleet_ids) || fail "cannot inventory the router fleet"
    inventory_ids=()
    [[ -z $inventory_listing ]] || mapfile -t inventory_ids <<<"$inventory_listing"
}

fleet_volume_ids() {
    "$docker_bin" volume ls -q \
        --filter label=ai.gonka.component=versiond-router-state \
        --filter "label=ai.gonka.fleet=$fleet_id"
}

# Prints the slot's container ID. Returns 1 for an absent slot, 2 for duplicate
# containers, 3 when Docker could not answer. Callers pass a non-zero status to
# slot_lookup_failed so that a Docker failure never reads as an absent slot.
# Prints the ID of the slot's current generation. Returns 1 for an absent
# slot, 2 for an inventory the fleet does not produce, 3 when Docker could
# not answer. Callers pass a non-zero status to slot_lookup_failed so that a
# Docker failure never reads as an absent slot.
slot_id() {
    local status=0
    slot_generations "$1" || status=$?
    ((status == 0)) || return "$status"
    printf '%s\n' "$slot_current"
}

# slot_lookup_failed STATUS [SLOT]: a Docker failure or an inventory the
# fleet never produces stops the command instead of reading as an absent slot.
slot_lookup_failed() {
    local slot=${2:-} named=''
    [[ -z $slot ]] || named=" '$slot'"
    case ${1:-0} in
        3) fail "Docker did not answer a router slot query; refusing to guess the fleet state" ;;
        2) fail "duplicate containers claim router slot$named: more than two generations, or two running; remove the stray container or use down --maintenance" ;;
    esac
}

slot_running() {
    local slot=$1 id state
    id=$(slot_id "$1") || { slot_lookup_failed $? "${slot-}"; return 1; }
    state=$("$docker_bin" inspect --format '{{.State.Status}}' "$id") || fail \
        "cannot inspect router slot $1"
    [[ $state == running ]]
}

slot_ready() {
    local slot=$1 id state health details
    id=$(slot_id "$1") || { slot_lookup_failed $? "${slot-}"; return 1; }
    details=$("$docker_bin" inspect --format \
        '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$id") || fail \
        "cannot inspect router slot $1"
    read -r state health <<<"$details"
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
    id=$(slot_id "$slot") || { slot_lookup_failed $? "${slot-}"; return 1; }
    slot_ready "$slot" || return 1
    encoded=$(urlencode "$route")
    docker_exec "$id" /bin/busybox wget -q -O /dev/null \
        "http://127.0.0.1:8404/readyz?version=$encoded" 2>/dev/null
}

slot_catalog_routes() {
    local slot=$1 id map
    id=$(slot_id "$1") || { slot_lookup_failed $? "${slot-}"; return 1; }
    if docker_exec "$id" test -x \
        /usr/local/lib/router-runtime/catalog-status >/dev/null 2>&1; then
        for map in /etc/haproxy/non_ha.map /etc/haproxy/versions.map; do
            docker_exec "$id" \
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

# 0 declared, 1 not declared, 2 the catalog could not be read.
slot_route_declared() {
    local slot=$1 route=$2 configured declared
    configured=$(slot_catalog_routes "$slot") || return 2
    while IFS= read -r declared; do
        [[ $declared != "$route" ]] || return 0
    done <<<"$configured"
    return 1
}

# Routes the running generations carry in their catalogs. Every one of them
# is tracked (status shows a route with zero ready routers instead of hiding
# it); the ones with a ready router now are protected, unless the candidate
# configuration withdraws them from its static declaration.
discover_expected_routes() {
    local slot id route routes legacy_versions ha_versions lookup
    for slot in "${slots[@]}"; do
        lookup=0
        slot_generations "$slot" 2>/dev/null || lookup=$?
        # Discovery runs before every command; a slot with stray containers
        # is reported by the command itself, not here.
        ((lookup != 3)) || slot_lookup_failed 3 "$slot"
        [[ $lookup == 0 && $slot_current_state == running ]] || continue
        id=$slot_current
        routes=$(slot_catalog_routes "$slot") || fail \
            "cannot read the effective route catalog from slot $slot"
        legacy_versions=$(container_env_value "$id" VERSIOND_NON_HA_VERSIONS) || legacy_versions=
        ha_versions=$(container_env_value "$id" VERSIOND_VERSIONS) || ha_versions=
        while IFS= read -r route; do
            [[ -n $route ]] || continue
            if [[ -z ${candidate_routes[$route]-} ]] && \
                route_in_static_environment "$route" "$legacy_versions" "$ha_versions"; then
                # Declared statically by the running generation only: the
                # candidate withdraws it, the rollout may stop serving it.
                continue
            fi
            expected_routes[$route]=1
            slot_route_ready "$slot" "$route" || continue
            # A pinned non-HA version has one owner that may be down on
            # purpose; it is tracked, not protected.
            if [[ -n ${candidate_ha_routes[$route]-} ]] || \
                ! route_in_static_environment "$route" "$legacy_versions" "${VERSIOND_NON_HA_VERSIONS-v1 v2 v3}"; then
                protected_routes[$route]=1
            fi
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
    local slot=$1 id
    id=$(slot_id "$1") || { slot_lookup_failed $? "${slot-}"; return 1; }
    container_front_ip "$id"
}

container_front_ip() {
    local network
    network=${VERSIOND_ROUTER_FRONT_NETWORK:-gonka-versiond-router-front}
    "$docker_bin" inspect --format \
        "{{with index .NetworkSettings.Networks \"$network\"}}{{.IPAddress}}{{end}}" \
        "$1"
}

parent_proxy_active() {
    local parent=${PROXY_ROUTER_CONTAINER:-proxy} component
    # An absent parent is a real topology (the legacy nginx cutover has not
    # happened). A Docker error is not: treating it as absence would skip the
    # drain and admission gates, so it stops the command instead.
    if ! component=$("$docker_bin" inspect --format \
        '{{or (index .Config.Labels "ai.gonka.component") ""}}' \
        "$parent" 2>&1); then
        case ${component,,} in
            *"no such object"* | *"no such container"*) return 1 ;;
        esac
        fail "cannot inspect the parent proxy $parent: $component"
    fi
    [[ $component == proxy-router ]]
}

parent_diagnostic_available() {
    local parent=${PROXY_ROUTER_CONTAINER:-proxy}
    parent_proxy_active || return 1
    docker_exec "$parent" test -x \
        /usr/local/lib/proxy-router/route-status >/dev/null 2>&1
}

parent_server_refs() {
    local address=$1 status_pattern=${2:-'^(UP|DRAIN)'}
    local parent=${PROXY_ROUTER_CONTAINER:-proxy} stats
    stats=$(docker_exec "$parent" /bin/sh -ec \
        "printf 'show stat\\n' | socat stdio /var/run/haproxy/haproxy.sock") || return 2
    awk -F, -v address="$address" -v status_pattern="$status_pattern" '
        NR == 1 {
            for (i = 1; i <= NF; i++) {
                name = $i
                sub(/^#[[:space:]]*/, "", name)
                column[name] = i
            }
            valid = column["pxname"] && column["svname"] &&
                column["status"] && column["addr"]
            next
        }
        valid {
            backend = $(column["pxname"])
            server_address = $(column["addr"])
            if ((backend == "versiond_router_coarse" ||
                    backend ~ /^versiond_routers_/) &&
                (server_address == address ||
                    index(server_address, address ":") == 1) &&
                $(column["status"]) ~ status_pattern) {
                print backend "/" $(column["svname"])
                found = 1
            }
        }
        END {
            if (!valid) exit 2
            exit found ? 0 : 1
        }
    ' <<<"$stats"
}

repair_stale_parent_drain() {
    local slot=$1 address refs_output status ref
    local -a refs=()

    parent_proxy_active || return 0
    address=$(slot_front_ip "$slot") || return 1
    if refs_output=$(parent_server_refs "$address" '^DRAIN'); then
        mapfile -t refs <<<"$refs_output"
    else
        status=$?
        ((status == 1)) && return 0
        return 1
    fi
    warn "repairing stale parent DRAIN state for slot $slot; fresh L7 admission is required"
    for ref in "${refs[@]}"; do
        parent_runtime_command "set server $ref health down" || return 1
        parent_runtime_command "set server $ref state ready" || return 1
    done
}

parent_address_withdrawal_state() {
    local address=$1 parent=${PROXY_ROUTER_CONTAINER:-proxy} stats
    stats=$(docker_exec "$parent" /bin/sh -ec \
        "printf 'show stat\\n' | socat stdio /var/run/haproxy/haproxy.sock") || return 2
    awk -F, -v address="$address" '
        NR == 1 {
            for (i = 1; i <= NF; i++) {
                name = $i
                sub(/^#[[:space:]]*/, "", name)
                column[name] = i
            }
            valid = column["pxname"] && column["status"] && column["addr"]
            next
        }
        valid {
            backend = $(column["pxname"])
            server_address = $(column["addr"])
            if ((backend == "versiond_router_coarse" ||
                    backend ~ /^versiond_routers_/) &&
                (server_address == address ||
                    index(server_address, address ":") == 1) &&
                $(column["status"]) ~ /^UP/) admitted = 1
        }
        END {
            if (!valid) exit 2
            exit admitted ? 1 : 0
        }
    ' <<<"$stats"
}

parent_runtime_command() {
    local command=$1 parent=${PROXY_ROUTER_CONTAINER:-proxy} response
    [[ $command != *"'"* ]] || return 1
    response=$(docker_exec "$parent" /bin/sh -ec \
        "printf '%s\\n' '$command' | socat stdio /var/run/haproxy/reconciler.sock") || return 1
    [[ -z ${response//[[:space:]]/} ]]
}

ready_parent_refs() {
    local ref restored=true
    for ref; do
        parent_runtime_command "set server $ref state ready" || restored=false
    done
    $restored
}

declare -a parent_drained_refs=()

prepare_parent_slot_stop() {
    local slot=$1 id=${2:-} address refs_output status ref
    local -a refs=()
    parent_drained_refs=()
    parent_proxy_active || return 0
    if [[ -n $id ]]; then
        address=$(container_front_ip "$id") || return 1
    else
        address=$(slot_front_ip "$slot") || return 1
    fi
    if refs_output=$(parent_server_refs "$address"); then
        mapfile -t refs <<<"$refs_output"
    else
        status=$?
        ((status == 1)) && return 0
        return 1
    fi
    for ref in "${refs[@]}"; do
        parent_runtime_command "set server $ref state drain" || {
            ready_parent_refs "${parent_drained_refs[@]}" || true
            parent_drained_refs=()
            return 1
        }
        parent_drained_refs+=("$ref")
    done
    local deadline=$((SECONDS + wait_timeout))
    while ((SECONDS < deadline)); do
        if parent_address_withdrawal_state "$address"; then
            return 0
        else
            status=$?
        fi
        ((status == 1)) || break
        sleep 1
    done
    ready_parent_refs "${parent_drained_refs[@]}" || true
    parent_drained_refs=()
    return 1
}

reset_parent_slot_health() {
    local ref
    for ref in "${parent_drained_refs[@]}"; do
        parent_runtime_command "set server $ref health down" || return 1
    done
    ready_parent_refs "${parent_drained_refs[@]}" || return 1
    parent_drained_refs=()
}

# Drains one container out of the parent proxy and stops it; it stays in
# Docker as the record of what served. Its restart policy becomes
# unless-stopped first: a daemon restart before the stop still brings it
# back (nothing else serves the slot yet), a daemon restart after the stop
# leaves it stopped next to its replacement. restore_generation switches
# `always` back on.
stop_generation() {
    local slot=$1 id=$2 status=0
    prepare_parent_slot_stop "$slot" "$id" || return 1
    "$docker_bin" update --restart=unless-stopped "$id" >/dev/null || {
        reset_parent_slot_health || true
        return 1
    }
    "$docker_bin" stop --time "$drain_timeout" "$id" >/dev/null || status=$?
    reset_parent_slot_health || return 1
    return "$status"
}

stop_slot_generation() {
    local slot=$1 id
    id=$(slot_id "$slot") || { slot_lookup_failed $? "${slot-}"; return 1; }
    stop_generation "$slot" "$id"
}

# The state volume of a slot is shared by its generations and therefore
# external to their Compose projects; it is created here, once, with the
# labels down --maintenance removes it by.
ensure_slot_volume() {
    local slot=$1 volume
    volume=$(slot_state_volume "$slot")
    "$docker_bin" volume inspect "$volume" >/dev/null 2>&1 && return 0
    "$docker_bin" volume create \
        --label ai.gonka.component=versiond-router-state \
        --label "ai.gonka.fleet=$fleet_id" \
        --label "ai.gonka.slot=$slot" \
        "$volume" >/dev/null || fail "cannot create the state volume $volume of slot $slot"
}

# Creates the next generation of a slot on the candidate specification and
# waits for its healthcheck.
create_generation() {
    local slot=$1
    ensure_slot_volume "$slot"
    slot_compose "$slot" "$(new_generation)" up -d --wait --wait-timeout "$wait_timeout" router
}

# The last look at a candidate before its previous generation is removed. A
# plain failure status rather than fail: the caller's ERR trap must run and
# put the previous generation back.
require_slot_routes_now() {
    local slot=$1 route declared
    slot_ready "$slot" || {
        echo "versiond-router-fleet: refusing to commit slot $slot: its candidate is not healthy" >&2
        return 1
    }
    declared=$(slot_catalog_routes "$slot") || {
        echo "versiond-router-fleet: refusing to commit slot $slot: cannot read its route catalog" >&2
        return 1
    }
    for route in "${!required_routes[@]}" "${!protected_routes[@]}"; do
        if ! grep -qx -- "$route" <<<"$declared"; then
            # A route that vanished from the candidate's catalog since its
            # route gate is a failure, unless removals are allowed and the
            # catalog itself removed it.
            if [[ -z ${required_routes[$route]-} ]]; then
                case ${VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS:-false} in
                    1 | true | yes)
                        # The removal took effect: the route stops being
                        # protected for the rest of the run, so the next
                        # slot's reserve check does not stop a rollout whose
                        # first slot is already committed.
                        warn "catalog route $route was removed; slot $slot no longer declares it"
                        unset "protected_routes[$route]" "expected_routes[$route]"
                        continue
                        ;;
                esac
            fi
            echo "versiond-router-fleet: refusing to commit slot $slot: route $route vanished from its catalog before the previous generation was removed" >&2
            return 1
        fi
        slot_route_ready "$slot" "$route" || {
            echo "versiond-router-fleet: refusing to commit slot $slot: route $route stopped being served before the previous generation was removed" >&2
            return 1
        }
    done
}

# The commit marker of a maintenance rollout: one volume, created in a
# single Docker operation once every check passed, removed once every
# previous generation is gone. While it exists the fleet may only roll
# forward: a rerun finishes the cleanup and never restores a previous
# generation, so an interruption halfway cannot leave a mixed placement.
commit_marker_name() {
    printf 'gonka-versiond-router-commit-%s\n' "$fleet_id"
}

# Prints "spec-hash image" of the committed rollout; returns 1 when no
# commit is pending.
commit_marker_record() {
    local output
    if output=$("$docker_bin" volume inspect \
        --format '{{index .Labels "ai.gonka.spec-hash"}} {{index .Labels "ai.gonka.candidate-image"}}' \
        "$(commit_marker_name)" 2>&1); then
        printf '%s\n' "$output"
        return 0
    fi
    case ${output,,} in
        *"no such volume"*) return 1 ;;
    esac
    fail "cannot inspect the commit marker $(commit_marker_name): $output"
}

# The marker carries the complete specification that was committed, so a
# retry can tell a changed configuration apart before it touches anything.
commit_marker_create() {
    local spec slot
    local -a labels=()
    spec=$(fleet_spec_hash --without-image)
    # The Compose configuration hash of every slot as committed: it names
    # the image by reference, so a cleanup finished with the committed
    # digest pinned compares against this record, not against a rendering.
    for slot in "${slots[@]}"; do
        [[ -n ${desired_hashes[$slot]-} ]] || desired_hashes[$slot]=$(desired_slot_config_hash "$slot")
        labels+=(--label "ai.gonka.slot-hash-$slot=${desired_hashes[$slot]}")
    done
    "$docker_bin" volume create \
        --label ai.gonka.component=versiond-router-commit \
        --label "ai.gonka.fleet=$fleet_id" \
        --label "ai.gonka.spec-hash=$spec" \
        --label "ai.gonka.candidate-image=$candidate_image_id" \
        "${labels[@]}" \
        "$(commit_marker_name)" >/dev/null || fail "cannot record the commit point"
}

# The reference an operator can pin an image by: its registry digest when
# the image was pulled, otherwise its local ID.
image_pull_reference() {
    local digest
    digest=$("$docker_bin" image inspect --format '{{range .RepoDigests}}{{println .}}{{end}}' "$1" 2>/dev/null | head -n 1)
    printf '%s\n' "${digest:-$1}"
}

commit_marker_slot_hash() {
    "$docker_bin" volume inspect --format "{{index .Labels \"ai.gonka.slot-hash-$1\"}}" \
        "$(commit_marker_name)" 2>/dev/null
}

# Does a container run the committed candidate of its slot? The image by
# ID; the configuration either as committed or as rendered now, which
# differ only in the image reference once the specification matched.
generation_matches_committed() {
    local id=$1 slot=$2 image hash committed_hash
    [[ -n ${desired_hashes[$slot]-} ]] || desired_hashes[$slot]=$(desired_slot_config_hash "$slot")
    committed_hash=$(commit_marker_slot_hash "$slot") || committed_hash=
    image=$("$docker_bin" inspect --format '{{.Image}}' "$id") || return 2
    hash=$("$docker_bin" inspect --format \
        '{{or (index .Config.Labels "com.docker.compose.config-hash") ""}}' "$id") || return 2
    [[ $image == "$candidate_image_id" ]] || return 1
    [[ $hash == "${desired_hashes[$slot]}" || ( -n $committed_hash && $hash == "$committed_hash" ) ]]
}

commit_marker_remove() {
    "$docker_bin" volume rm "$(commit_marker_name)" >/dev/null || \
        warn "cannot remove the commit marker $(commit_marker_name); rerun apply"
}

# Finishes a committed maintenance rollout: every previous generation is
# removed, a candidate that does not serve is recreated on the committed
# candidate image, nothing is restored.
roll_forward_committed() {
    local record committed_spec committed_image slot lookup
    record=$(commit_marker_record) || return 0
    read -r committed_spec committed_image <<<"$record"
    # Both checked before anything is removed: with another configuration
    # or another image the cleanup would remove records it may still need.
    # The image is compared by ID, so a mutable tag that moved is refused,
    # and pinning the committed digest satisfies both checks.
    [[ $committed_image == "$candidate_image_id" ]] || fail \
        "a committed maintenance cleanup is pending for image $committed_image but $candidate_image now resolves to $candidate_image_id (the tag moved); pin the committed image (VERSIOND_ROUTER_IMAGE=$(image_pull_reference "$committed_image")) and rerun apply to finish the cleanup first"
    [[ $committed_spec == "$(fleet_spec_hash --without-image)" ]] || fail \
        "a committed maintenance cleanup is pending for specification $committed_spec (image $committed_image) and the configuration has changed since; restore that configuration and rerun apply to finish the cleanup first"
    echo "Finishing the committed maintenance rollout"
    for slot in "${slots[@]}"; do
        lookup=0
        slot_generations "$slot" || lookup=$?
        slot_lookup_failed "$lookup" "$slot"
        if ((lookup == 1)); then
            start_slot "$slot"
        else
            # Checked before the record of the slot is removed.
            if ! generation_matches_committed "$slot_current" "$slot"; then
                fail "slot $slot does not run the committed candidate; finish the committed cleanup with the committed configuration before changing it"
            fi
            if [[ -n $slot_previous ]]; then
                "$docker_bin" rm -f "$slot_previous" >/dev/null || fail \
                    "cannot remove the previous generation of slot $slot"
            fi
            if ! slot_ready "$slot"; then
                echo "Recreating the committed candidate of slot $slot (state $slot_current_state)"
                [[ $slot_current_state != running ]] || stop_generation "$slot" "$slot_current"
                "$docker_bin" rm -f "$slot_current" >/dev/null
                start_slot "$slot"
            fi
        fi
        wait_slot_routes "$slot"
        wait_parent_admission "$slot"
    done
    commit_marker_remove
}

# Removes the stopped previous generation of a slot without any further
# check: the cleanup half of a commit, safe to retry.
remove_previous_generation() {
    local slot=$1
    slot_generations "$slot" || { slot_lookup_failed $? "$slot"; return 1; }
    [[ -n $slot_previous ]] || return 0
    [[ $slot_previous_state != running ]] || {
        echo "versiond-router-fleet: cannot remove the previous generation of slot $slot: it is running" >&2
        return 1
    }
    "$docker_bin" rm "$slot_previous" >/dev/null || {
        echo "versiond-router-fleet: cannot remove the previous generation of slot $slot" >&2
        return 1
    }
}

# Removes the stopped previous generation of a slot: the replacement is
# proven and there is nothing left to put back.
commit_slot() {
    local slot=$1
    require_slot_routes_now "$slot" || return 1
    remove_previous_generation "$slot"
}

# Puts a stopped generation back: the candidate (if any) is drained and
# removed, the previous container is started exactly as it was.
restore_generation() {
    local slot=$1 candidate=$2 previous=$3 state
    if [[ -n $candidate ]]; then
        state=$("$docker_bin" inspect --format '{{.State.Status}}' "$candidate") || return 1
        [[ $state != running ]] || stop_generation "$slot" "$candidate" || return 1
        "$docker_bin" rm -f "$candidate" >/dev/null || return 1
    fi
    "$docker_bin" update --restart=always "$previous" >/dev/null || return 1
    "$docker_bin" start "$previous" >/dev/null || return 1
    wait_slot_ready "$slot" || return 1
    repair_stale_parent_drain "$slot" || return 1
}

# Does a container run the candidate image on the desired configuration of
# its slot? The configuration differs per slot (front alias, metrics alias),
# so the desired hash is rendered per slot and cached for the run.
declare -A desired_hashes=()
generation_matches_candidate() {
    local id=$1 slot=$2 image hash
    [[ -n ${desired_hashes[$slot]-} ]] || desired_hashes[$slot]=$(desired_slot_config_hash "$slot")
    image=$("$docker_bin" inspect --format '{{.Image}}' "$id") || return 2
    hash=$("$docker_bin" inspect --format \
        '{{or (index .Config.Labels "com.docker.compose.config-hash") ""}}' "$id") || return 2
    [[ $image == "$candidate_image_id" && $hash == "${desired_hashes[$slot]}" ]]
}

# Settles a slot that carries an uncommitted replacement: a candidate that
# serves is committed, one that does not is removed and the previous
# generation is put back. Both leave the slot with one generation.
converge_interrupted_slot() {
    local slot=$1
    [[ -n $slot_previous ]] || return 0
    if [[ $slot_current_state == running ]] && slot_ready "$slot" && \
        wait_slot_routes "$slot" && wait_parent_admission "$slot"; then
        echo "Committing the interrupted replacement of slot $slot"
        commit_slot "$slot"
    else
        echo "Restoring the previous generation of slot $slot; its replacement never converged"
        restore_generation "$slot" "$slot_current" "$slot_previous" || fail \
            "cannot restore the previous generation of slot $slot"
        wait_restored_routes "$slot" || fail \
            "the restored generation of slot $slot does not serve its routes"
        wait_parent_admission "$slot"
    fi
    slot_generations "$slot" || slot_lookup_failed $? "${slot-}"
}

require_parent_diagnostic() {
    parent_proxy_active || return 1
    parent_diagnostic_available || fail \
        "active parent proxy has no route-status diagnostic; update proxy-router before rolling the inner fleet"
}

parent_route_admitted() {
    local route=$1 address=$2 parent=${PROXY_ROUTER_CONTAINER:-proxy}
    if docker_exec "$parent" \
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
        # DNS may assign the replacement address to a drained server-template
        # slot after this wait has already started. Repair on every pass so the
        # same operation converges without requiring an operator retry.
        repair_stale_parent_drain "$slot" || return 1
        missing=
        for route in --coarse "${!expected_routes[@]}" "${!required_routes[@]}" "${!protected_routes[@]}"; do
            if [[ $route != --coarse ]] && ! slot_route_ready "$slot" "$route"; then
                # A required or protected route that stopped being served
                # while admission was awaited is an outage, not a route to
                # skip; the wait times out and the trap puts the previous
                # generation back.
                if [[ -n ${required_routes[$route]-} || -n ${protected_routes[$route]-} ]] && \
                    slot_route_declared "$slot" "$route"; then
                    missing=$route
                    break
                fi
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
    # Every route the fleet serves must be admitted, and a required or
    # catalog route that nothing serves is a failure, not an exemption. A
    # route the caller named is reported slot by slot instead.
    for route in "${!expected_routes[@]}" "${!required_routes[@]}"; do
        [[ -z ${admission_required_routes[$route]-} ]] || continue
        if (( $(route_ready_count "$route") > 0 )); then
            admission_required_routes[$route]=1
        elif [[ -n ${required_routes[$route]-} ]]; then
            fail "required version $route is served by no ready router; the versiond pool or its PostgreSQL is down"
        elif [[ -z ${candidate_routes[$route]-} ]]; then
            fail "catalog route $route is declared by the fleet but served by no ready router"
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

# Every bootstrap version must be served by at least one router slot before a
# rollout starts. A route with zero ready backends drops out of the protected
# set, so a rollout during a PostgreSQL or versiond outage could otherwise
# finish without ever proving that the routes came back.
# Every HA version the running generations declare and the candidate keeps
# must be served by at least one router before a rollout starts. A fleet in
# which one of them has no ready router has lost its versiond pool or its
# PostgreSQL, and rolling it would only hide that. A pinned non-HA version
# belongs to one host that may be down on purpose and is never a blocker.
require_static_routes_served() {
    local slot id declared version count lookup
    local -A served=()
    case ${VERSIOND_ROUTER_ALLOW_UNSERVED_STATIC_ROUTES:-false} in
        1 | true | yes) return 0 ;;
    esac
    for slot in "${slots[@]}"; do
        lookup=0
        slot_generations "$slot" 2>/dev/null || lookup=$?
        slot_lookup_failed "$lookup" "$slot"
        [[ $lookup == 0 && $slot_current_state == running ]] || continue
        id=$slot_current
        declared=$(container_env_value "$id" VERSIOND_VERSIONS) || continue
        for version in $(normalize_versions "$declared" | tr ',' ' '); do
            # A version the candidate withdraws is allowed to go.
            [[ -n ${candidate_ha_routes[$version]-} ]] || continue
            served[$version]=1
        done
    done
    for version in "${!served[@]}"; do
        count=$(route_ready_count "$version")
        ((count > 0)) || fail \
            "version $version is declared but served by no ready router; the versiond pool or its PostgreSQL is down, refusing to roll the routers (VERSIOND_ROUTER_ALLOW_UNSERVED_STATIC_ROUTES=true overrides)"
        expected_routes[$version]=1
        protected_routes[$version]=1
    done
}

require_ready_reserve() {
    local excluded=$1 route total reserve parent_reserve
    reserve=$(ready_count_except "$excluded")
    ((reserve >= min_ready)) || fail \
        "refusing to stop slot $excluded: only $reserve other routers are ready, need $min_ready"
    for route in "${!expected_routes[@]}"; do
        total=$(route_ready_count "$route")
        if ((total == 0)); then
            # A route that was served when the run started and lost every
            # ready router since is an outage in the pool or its PostgreSQL.
            # A route the configuration only declares is served by the
            # candidate first.
            [[ -z ${protected_routes[$route]-} ]] || fail \
                "refusing to stop slot $excluded: version $route is served by no ready router; the versiond pool or its PostgreSQL is down"
            continue
        fi
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

# A candidate generation must declare every required route and serve every
# required or protected route it declares; any other tracked route must
# converge once a peer serves it. A protected catalog route the candidate
# does not declare was removed from the catalog: with
# VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS=true that is the removal taking
# effect and the route stops being protected for the rest of the run;
# otherwise the runtime map is monotonic and a candidate without the route
# has lost its cache, which is a failure.
wait_slot_routes() {
    local slot=$1 deadline=$((SECONDS + wait_timeout))
    local route missing reason declared_status
    while ((SECONDS < deadline)); do
        missing=
        reason=
        for route in "${!expected_routes[@]}" "${!required_routes[@]}" "${!protected_routes[@]}"; do
            declared_status=0
            slot_route_declared "$slot" "$route" || declared_status=$?
            if ((declared_status == 2)); then
                # An unreadable catalog is not "not declared": keep waiting.
                missing=$route
                reason="cannot show its catalog for"
                break
            fi
            if ((declared_status == 1)); then
                # A candidate must at least declare what its own configuration
                # lists (HA and pinned versions alike): a generation without
                # them is broken, not converging.
                if [[ -n ${required_routes[$route]-} || -n ${candidate_routes[$route]-} ]]; then
                    missing=$route
                    reason="does not declare"
                    break
                fi
                if [[ -n ${protected_routes[$route]-} ]]; then
                    case ${VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS:-false} in
                        1 | true | yes)
                            warn "catalog route $route was removed; slot $slot no longer declares it"
                            unset "protected_routes[$route]" "expected_routes[$route]"
                            ;;
                        *)
                            missing=$route
                            reason="does not declare protected"
                            break
                            ;;
                    esac
                fi
                continue
            fi
            if [[ -n ${required_routes[$route]-} || -n ${protected_routes[$route]-} ]] || \
                (( $(route_ready_count "$route" "$slot") > 0 )); then
                if ! slot_route_ready "$slot" "$route"; then
                    missing=$route
                    reason="did not converge"
                    break
                fi
            fi
        done
        [[ -z $missing ]] && return 0
        sleep 1
    done
    echo "versiond-router-fleet: slot $slot $reason expected route $missing within ${wait_timeout}s" >&2
    return 1
}

# A restored generation serves what it served before: every route it
# declares that a peer serves too must be ready on it again.
wait_restored_routes() {
    local slot=$1 deadline=$((SECONDS + wait_timeout)) route missing routes
    while ((SECONDS < deadline)); do
        missing=
        routes=$(slot_catalog_routes "$slot") || routes=
        while IFS= read -r route; do
            [[ -n $route ]] || continue
            if (( $(route_ready_count "$route" "$slot") > 0 )) && \
                ! slot_route_ready "$slot" "$route"; then
                missing=$route
                break
            fi
        done <<<"$routes"
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
    local image=${VERSIOND_ROUTER_IMAGE:-ghcr.io/product-science/versiond-router:0.2.15-devshard-v5}
    [[ $pull_policy != never ]] || return 0
    if slot_compose "${slots[0]}" render pull --policy "$pull_policy" router; then
        return 0
    fi
    # The registry is unreachable. Recovery from cached images must still
    # work; only a missing candidate image is fatal.
    "$docker_bin" image inspect "$image" >/dev/null 2>&1 || fail \
        "cannot pull $image and it is not cached locally"
    warn "cannot reach the registry; continuing with the cached $image"
}

desired_slot_config_hash() {
    local slot=$1 service hash
    read -r service hash < <(slot_compose "$slot" render config --hash router)
    [[ $service == router && -n $hash ]] || fail \
        "cannot calculate the desired Compose configuration hash for slot $slot"
    printf '%s\n' "$hash"
}

# The candidate image every slot is compared against; the desired
# configuration is rendered per slot on first use.
candidate_image_id=
resolve_candidate() {
    candidate_image=${VERSIOND_ROUTER_IMAGE:-ghcr.io/product-science/versiond-router:0.2.15-devshard-v5}
    candidate_image_id=$($docker_bin image inspect --format '{{.Id}}' "$candidate_image") || fail \
        "candidate router image is not available: $candidate_image"
    desired_hashes=()
}

slot_needs_replacement() {
    local slot=$1 id status=0
    id=$(slot_id "$slot") || { slot_lookup_failed $? "${slot-}"; return 2; }
    generation_matches_candidate "$id" "$slot" || status=$?
    ((status != 2)) || return 2
    ((status == 1))
}

placement_version_for_image() {
    "$docker_bin" image inspect --format \
        '{{index .Config.Labels "ai.gonka.placement-protocol-version"}}' "$1"
}

cache_protocol_for_image() {
    "$docker_bin" image inspect --format \
        '{{index .Config.Labels "ai.gonka.catalog-cache-protocol-version"}}' "$1"
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
            id=$(slot_id "$slot") || { slot_lookup_failed $? "${slot-}"; continue; }
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

require_cache_compatible() {
    local candidate=$1 candidate_protocol slot id running_image running_protocol
    candidate_protocol=$(cache_protocol_for_image "$candidate")
    [[ $candidate_protocol =~ ^[0-9]+$ ]] || fail \
        "candidate image has no numeric catalog cache protocol label"
    for slot in "${slots[@]}"; do
        id=$(slot_id "$slot") || { slot_lookup_failed $? "${slot-}"; continue; }
        running_image=$($docker_bin inspect --format '{{.Image}}' "$id")
        running_protocol=$(cache_protocol_for_image "$running_image")
        [[ $running_protocol =~ ^[0-9]+$ ]] || fail \
            "slot $slot has no numeric catalog cache protocol label"
        # Protocol generations own distinct files in the per-slot state volume.
        # One-step upgrades are safe and automatic; the previous file remains
        # untouched for exact-image rollback. Skipping a generation or an
        # operator-requested downgrade is ambiguous and fails.
        if ((candidate_protocol != running_protocol && \
            candidate_protocol != running_protocol + 1)); then
            fail "catalog cache protocol mismatch: candidate=$candidate_protocol slot-$slot=$running_protocol; roll forward one protocol generation at a time"
        fi
    done
}

running_placement_contract() {
    local id=$1 pool_host back_network legacy_host legacy_versions ha_versions
    local catalog_url coarse_readiness dns_resolver endpoints_sha hosts
    pool_host=$(container_env_value "$id" VERSIOND_POOL_HOST) || return 1
    back_network=$(container_env_value "$id" VERSIOND_ROUTER_BACK_NETWORK_NAME) || return 1
    legacy_host=$(container_env_value "$id" VERSIOND_LEGACY_HOST) || return 1
    legacy_versions=$(container_env_value "$id" VERSIOND_NON_HA_VERSIONS) || return 1
    ha_versions=$(container_env_value "$id" VERSIOND_VERSIONS) || return 1
    catalog_url=$(container_env_value_or_legacy_default "$id" VERSIOND_ROUTING_CATALOG_URL) || return 1
    coarse_readiness=$(container_env_value_or_legacy_default "$id" VERSIOND_ROUTER_ALLOW_COARSE_READINESS) || return 1
    dns_resolver=$(container_env_value_or_legacy_default "$id" HAPROXY_DNS_RESOLVER) || return 1
    endpoints_sha=$(container_env_value "$id" VERSIOND_POOL_ENDPOINTS_SHA256) || endpoints_sha=
    hosts=$(container_env_value "$id" VERSIOND_HOSTS) || hosts=
    placement_contract "$pool_host" "$back_network" "$legacy_host" \
        "$legacy_versions" "$ha_versions" "$catalog_url" "$coarse_readiness" "$dns_resolver" \
        "$(pool_membership "$endpoints_sha" "$hosts")"
}

require_placement_compatible() {
    local candidate=$1 slot id running_image candidate_version running_version
    local candidate_contract running_contract
    candidate_version=$(placement_version_for_image "$candidate")
    [[ -n $candidate_version ]] || fail \
        "candidate image has no placement protocol label; refusing a mixed rollout"
    require_cache_compatible "$candidate"
    candidate_contract=$(candidate_placement_contract)
    for slot in "${slots[@]}"; do
        id=$(slot_id "$slot") || { slot_lookup_failed $? "${slot-}"; continue; }
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
    local id slot lookup
    local -A seen=()

    fleet_inventory
    while IFS= read -r id; do
        [[ -n $id ]] || continue
        slot=$($docker_bin inspect --format \
            '{{or (index .Config.Labels "ai.gonka.slot") ""}}' "$id") || fail \
            "router container $id disappeared while inventory was collected"
        [[ -n ${expected[$slot]-} ]] || fail \
            "orphan router container $id declares unknown slot '$slot'; use down --maintenance to clean the fleet"
        seen[$slot]=1
    done <<<"$inventory_listing"
    for slot in "${!seen[@]}"; do
        lookup=0
        slot_generations "$slot" || lookup=$?
        ((lookup != 2)) || fail "duplicate containers claim router slot '$slot'"
        slot_lookup_failed "$lookup" "$slot"
    done
}

# Brings every slot to one serving generation before any healthy slot is
# considered for replacement. The record of an interrupted replacement is
# the slot's stopped previous generation: a candidate that serves is
# committed, one that does not is removed and the previous generation is
# started again.
repair_fleet_capacity() {
    local slot lookup
    for slot in "${slots[@]}"; do
        lookup=0
        slot_generations "$slot" || lookup=$?
        slot_lookup_failed "$lookup" "$slot"
        if ((lookup == 1)); then
            echo "Recovering absent versiond-router slot $slot"
            start_slot "$slot"
        elif [[ -n $slot_previous ]]; then
            converge_interrupted_slot "$slot"
            continue
        elif slot_ready "$slot"; then
            continue
        else
            echo "Recovering non-ready versiond-router slot $slot (state $slot_current_state)"
            case $slot_current_state in
                created | exited)
                    # The only generation of the slot, stopped by an
                    # interrupted run before its replacement existed. Start
                    # it as it is; the rollout replaces it under its trap.
                    "$docker_bin" update --restart=always "$slot_current" >/dev/null
                    "$docker_bin" start "$slot_current" >/dev/null
                    wait_slot_ready "$slot"
                    wait_restored_routes "$slot"
                    wait_parent_admission "$slot"
                    continue
                    ;;
                running | restarting | paused | dead)
                    # Nothing else is recorded for the slot: recreate it on
                    # the candidate specification.
                    [[ $slot_current_state == dead ]] || stop_generation "$slot" "$slot_current"
                    "$docker_bin" rm -f "$slot_current" >/dev/null
                    start_slot "$slot"
                    ;;
                *) fail "slot $slot cannot be recovered from container state '$slot_current_state'" ;;
            esac
        fi
        wait_slot_routes "$slot"
        wait_parent_admission "$slot"
    done
}

# Classifies every slot against the candidate placement contract so a
# maintenance rollout can be rerun after a hard interruption (SIGKILL, OOM,
# reboot) without a journal: slots already running the candidate contract are
# done, slots on the previous contract are captured for exact rollback and
# replaced, slots left stopped or absent are simply started. Returns 1 when
# nothing is pending.
# Classifies every slot against the candidate specification so a maintenance
# rollout can be rerun after a hard interruption (SIGKILL, OOM, reboot):
# slots already serving the candidate are done (an uncommitted previous
# generation next to them is committed), slots on the previous generation
# keep it as their exact rollback, an unproven candidate is removed before
# a new one is created. Returns with maintenance_pending empty when nothing
# is pending.
capture_maintenance_state() {
    local slot lookup previous_contract='' contract restore
    maintenance_pending=()
    maintenance_kept=()
    maintenance_restore=()
    maintenance_stale=()
    for slot in "${slots[@]}"; do
        lookup=0
        slot_generations "$slot" || lookup=$?
        slot_lookup_failed "$lookup" "$slot"
        if ((lookup == 1)); then
            warn "slot $slot has no container; it will be started on the candidate contract"
            maintenance_pending+=("$slot")
            continue
        fi
        if [[ -n $slot_previous ]] && ! generation_matches_candidate "$slot_current" "$slot"; then
            # An older interrupted replacement with another specification.
            converge_interrupted_slot "$slot"
        fi
        if generation_matches_candidate "$slot_current" "$slot"; then
            if [[ $slot_current_state == running ]] && slot_ready "$slot"; then
                echo "Slot $slot already runs the candidate generation"
                if [[ -n $slot_previous ]]; then
                    # Replaced by an interrupted run and never committed: its
                    # previous generation stays until this run commits.
                    maintenance_restore[$slot]=$slot_previous
                    maintenance_kept+=("$slot")
                fi
                continue
            fi
            warn "slot $slot carries the candidate generation but is not serving; it will be replaced"
            maintenance_stale[$slot]=$slot_current
            restore=$slot_previous
        else
            restore=$slot_current
            if [[ $slot_current_state == running ]]; then
                slot_ready "$slot" || fail "slot $slot is not healthy before maintenance"
            else
                warn "slot $slot is stopped on the previous generation; it is kept for rollback"
            fi
        fi
        if [[ -n $restore ]]; then
            contract=$(running_placement_contract "$restore") || fail \
                "slot $slot does not expose its placement contract"
            if [[ -z $previous_contract ]]; then
                previous_contract=$contract
            elif [[ $contract != "$previous_contract" ]]; then
                fail "fleet has more than one previous placement contract; refusing automated maintenance"
            fi
            maintenance_restore[$slot]=$restore
        else
            warn "slot $slot has no previous generation; a failed maintenance leaves it stopped"
        fi
        maintenance_pending+=("$slot")
    done
}

# Every candidate generation must serve the required routes and every
# protected route it declares.
wait_required_routes_all() {
    local slot
    for slot in "${slots[@]}"; do
        wait_slot_routes "$slot" || return 1
    done
}

# The value the current configuration would give a slot for one maintenance
# key, for slots whose previous environment was not captured by this run.


maintenance_rollback() {
    local status=$? slot ok=true
    ((status != 0)) || status=1
    trap - ERR INT TERM HUP
    if [[ $maintenance_active == true ]]; then
        warn "maintenance rollout failed; draining candidates before restoring the exact previous fleet"
        for slot in "${maintenance_pending[@]}" "${maintenance_kept[@]}"; do
            slot_generations "$slot" || { slot_lookup_failed $? "${slot-}"; continue; }
            [[ -n ${maintenance_restore[$slot]-} ]] || continue
            # The candidate is the newest container unless the previous
            # generation is still the only one (the candidate never started).
            if [[ $slot_current != "${maintenance_restore[$slot]}" ]]; then
                restore_generation "$slot" "$slot_current" "${maintenance_restore[$slot]}" || ok=false
            else
                restore_generation "$slot" "" "${maintenance_restore[$slot]}" || ok=false
            fi
        done
        for slot in "${maintenance_pending[@]}" "${maintenance_kept[@]}"; do
            [[ -n ${maintenance_restore[$slot]-} ]] || continue
            wait_restored_routes "$slot" || ok=false
        done
        if [[ $ok == true ]]; then
            warn "the exact previous router fleet was restored; maintenance remains uncommitted"
        else
            warn "automatic fleet rollback failed; the previous generations are kept in Docker, restore ingress manually"
        fi
    fi
    exit "$status"
}

rollback_current() {
    local status=$?
    trap - ERR INT TERM HUP
    if [[ -n $current_slot && -n $rollback_generation ]]; then
        warn "restoring the previous generation of slot $current_slot"
        slot_generations "$current_slot" || slot_lookup_failed $? "${slot-}"
        local candidate=
        [[ $slot_current == "$rollback_generation" ]] || candidate=$slot_current
        if restore_generation "$current_slot" "$candidate" "$rollback_generation" && \
            wait_restored_routes "$current_slot"; then
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
    create_generation "$slot"
}

start_existing_or_create_slot() {
    local slot=$1 lookup=0
    [[ -n ${expected[$slot]-} ]] || fail "slot '$slot' is not configured"
    slot_generations "$slot" || lookup=$?
    slot_lookup_failed "$lookup" "$slot"
    if ((lookup == 1)); then
        start_slot "$slot"
        return
    fi
    [[ -z $slot_previous ]] || fail \
        "slot $slot carries an uncommitted replacement; run apply to converge it"
    case $slot_current_state in
        running) ;;
        created | exited)
            # A stopped generation is started as it is: only rollout may
            # replace an existing slot.
            "$docker_bin" update --restart=always "$slot_current" >/dev/null
            "$docker_bin" start "$slot_current" >/dev/null
            ;;
        *) fail "slot $slot cannot be started from container state '$slot_current_state'" ;;
    esac
    wait_slot_ready "$slot"
}

stop_slot() {
    local slot=$1
    [[ -n ${expected[$slot]-} ]] || fail "slot '$slot' is not configured"
    require_ready_reserve "$slot"
    stop_slot_generation "$slot"
}

stop_fleet_containers() {
    local id state pid failed=0
    local -a ids=() pids=()
    fleet_inventory
    ids=("${inventory_ids[@]}")
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
    fleet_inventory
    while IFS= read -r id; do
        [[ -n $id ]] && fleet_containers[$id]=1
    done <<<"$inventory_listing"
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
    fleet_inventory
    ids=("${inventory_ids[@]}")
    if ((${#ids[@]} > 0)); then
        "$docker_bin" rm "${ids[@]}" >/dev/null
    fi
    mapfile -t volumes < <(fleet_volume_ids)
    if ((${#volumes[@]} > 0)); then
        "$docker_bin" volume rm "${volumes[@]}" >/dev/null
    fi
    # A pending commit belongs to the fleet that is being removed.
    ! commit_marker_record >/dev/null || commit_marker_remove
    for network in "${cleanup_networks[@]}"; do
        echo "Removing versiond-router fleet network $network"
        "$docker_bin" network rm "$network" >/dev/null
    done
}

fleet_status() {
    local slot lookup route count kind committed
    local bad=0
    printf '%-16s %-12s %-10s %s\n' SLOT STATE HEALTH IMAGE
    if committed=$(commit_marker_record); then
        warn "a maintenance rollout (image ${committed#* }) is committed but its cleanup did not finish; run apply"
        bad=1
    fi
    fleet_inventory
    while read -r id; do
        [[ -n $id ]] || continue
        slot=$($docker_bin inspect --format \
            '{{index .Config.Labels "ai.gonka.slot"}}' "$id" 2>/dev/null) || continue
        if [[ -z ${expected[$slot]-} ]]; then
            warn "orphan router container $id declares unknown slot '$slot'"
            bad=1
        fi
    done <<<"$inventory_listing"
    for slot in "${slots[@]}"; do
        lookup=0
        slot_generations "$slot" || lookup=$?
        case $lookup in
            1)
                printf '%-16s %-12s %-10s %s\n' "$slot" absent - -
                bad=1
                continue
                ;;
            2)
                warn "duplicate containers claim router slot '$slot'"
                bad=1
                continue
                ;;
            3) slot_lookup_failed 3 "$slot" ;;
        esac
        details=$($docker_bin inspect --format \
            '{{.State.Status}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}} {{.Config.Image}}' \
            "$slot_current" 2>/dev/null) || {
                warn "router container $slot_current disappeared while status was collected"
                bad=1
                continue
            }
        read -r state health image <<<"$details"
        printf '%-16s %-12s %-10s %s\n' "$slot" "$state" "$health" "$image"
        if [[ $state != running || $health != healthy ]]; then
            bad=1
        fi
        if [[ -n $slot_previous && $operation_in_flight == false ]]; then
            warn "slot $slot carries an uncommitted replacement (previous generation ${slot_previous:0:12} is kept); run apply to converge it"
            bad=1
        fi
        for route in "${!expected_routes[@]}" "${!required_routes[@]}"; do
            if ! slot_route_declared "$slot" "$route"; then
                warn "slot $slot does not declare expected route $route; run rollout"
                bad=1
            fi
        done
    done
    # A route the fleet declares and nobody serves is an outage of the pool
    # or its PostgreSQL, visible here instead of hidden behind slot health.
    for route in $(printf '%s\n' "${!expected_routes[@]}" "${!required_routes[@]}" | LC_ALL=C sort -u); do
        count=$(route_ready_count "$route")
        if [[ -n ${required_routes[$route]-} ]]; then
            kind=required
        elif [[ -n ${candidate_routes[$route]-} ]]; then
            kind=pinned
        else
            kind=catalog
        fi
        printf 'ROUTE %-12s %-10s ready %s/%s\n' "$route" "$kind" "$count" "${#slots[@]}"
        ((count > 0)) || case $kind in
            required)
                warn "required version $route is served by no ready router; the versiond pool or its PostgreSQL is down"
                bad=1
                ;;
            catalog)
                warn "catalog route $route is declared by the fleet but served by no ready router"
                bad=1
                ;;
        esac
    done
    if parent_proxy_active; then
        if ! parent_diagnostic_available; then
            warn "active parent proxy has no route-status diagnostic"
            bad=1
        else
            if collect_required_admission_routes 2>/dev/null && check_parent_fleet_admission_once; then
                printf 'PARENT_ADMISSION admitted\n'
            else
                warn "parent admission is incomplete: ${parent_admission_error:-a declared route has no ready router}"
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
    resolve_candidate
    require_placement_compatible "$candidate_image"
    for slot in "${slots[@]}"; do
        echo "Ensuring versiond-router slot $slot is running"
        start_existing_or_create_slot "$slot"
        wait_slot_routes "$slot"
        wait_parent_admission "$slot"
    done
    require_static_routes_served
    fleet_status
}

fleet_apply() {
    local -a ids=()
    fleet_inventory
    ids=("${inventory_ids[@]}")
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
    resolve_candidate
    roll_forward_committed
    require_placement_compatible "$candidate_image"
    repair_fleet_capacity
    fleet_rollout
}

fleet_rollout() {
    local slot id replacement_status lookup
    prepare_slot_networks
    pull_router_image
    resolve_candidate
    roll_forward_committed
    require_placement_compatible "$candidate_image"
    require_static_routes_served
    trap rollback_current ERR INT TERM HUP
    for slot in "${slots[@]}"; do
        lookup=0
        slot_generations "$slot" || lookup=$?
        slot_lookup_failed "$lookup" "$slot"
        ((lookup == 0)) || fail "slot $slot has no existing container; run apply"
        [[ -z $slot_previous ]] || converge_interrupted_slot "$slot"
        id=$slot_current
        replacement_status=0
        slot_needs_replacement "$slot" || replacement_status=$?
        if ((replacement_status == 1)); then
            echo "Versiond-router slot $slot already matches the requested image and configuration"
            wait_slot_routes "$slot"
            wait_parent_admission "$slot"
            continue
        fi
        ((replacement_status == 0)) || fail \
            "cannot compare the running and requested contracts for slot $slot"
        require_static_routes_served
        require_ready_reserve "$slot"
        # The generation that serves now stays in Docker, stopped, until the
        # replacement is proven; the trap starts it again on failure.
        current_slot=$slot
        rollback_generation=$id
        echo "Draining versiond-router slot $slot"
        stop_generation "$slot" "$id"
        echo "Starting replacement for slot $slot"
        create_generation "$slot"
        slot_ready "$slot" || false
        wait_slot_routes "$slot"
        wait_parent_admission "$slot"
        commit_slot "$slot"
        current_slot=
        rollback_generation=
    done
    trap - ERR INT TERM HUP
    fleet_status
}

fleet_maintenance_rollout() {
    local slot ack=${VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE:-false}
    case $ack in
        1 | true | yes) ;;
        *) fail "maintenance-rollout requires VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true" ;;
    esac
    prepare_slot_networks
    pull_router_image
    resolve_candidate
    [[ -n $(placement_version_for_image "$candidate_image") ]] || fail \
        "candidate image has no placement protocol label"
    require_cache_compatible "$candidate_image"
    roll_forward_committed
    require_static_routes_served
    capture_maintenance_state
    if ((${#maintenance_pending[@]} == 0 && ${#maintenance_kept[@]} == 0)); then
        echo "Every slot already runs the candidate generation"
        fleet_status
        return 0
    fi
    maintenance_active=true
    trap maintenance_rollback ERR INT TERM HUP

    echo "Draining the previous router generation for an atomic placement change"
    for slot in "${maintenance_pending[@]}"; do
        slot_generations "$slot" || { slot_lookup_failed $? "${slot-}"; continue; }
        [[ $slot_current_state == running ]] && stop_generation "$slot" "$slot_current"
        # An unproven candidate of an interrupted run is removed; its previous
        # generation stays as the rollback record.
        [[ -z ${maintenance_stale[$slot]-} ]] || "$docker_bin" rm -f "${maintenance_stale[$slot]}" >/dev/null
    done
    for slot in "${maintenance_pending[@]}"; do
        echo "Starting maintenance replacement for slot $slot"
        create_generation "$slot"
    done
    wait_required_routes_all

    # Validate the candidate's complete live view before crossing the commit
    # point, while the previous generations are still there to put back.
    select_candidate_route_view
    for slot in "${slots[@]}"; do
        wait_parent_admission "$slot"
    done
    operation_in_flight=true
    fleet_status
    operation_in_flight=false
    # Every check happens while every previous generation still exists;
    # the last one looks at each candidate once more.
    for slot in "${maintenance_pending[@]}" "${maintenance_kept[@]}"; do
        require_slot_routes_now "$slot"
    done

    # Commit point: one Docker operation. From here nothing is rolled back;
    # removing the previous generations is cleanup that a rerun finishes
    # (roll_forward_committed) if it stops halfway, and a slot never loses
    # its serving candidate to a failure in another slot's cleanup.
    commit_marker_create
    maintenance_active=false
    trap - ERR INT TERM HUP
    for slot in "${maintenance_pending[@]}" "${maintenance_kept[@]}"; do
        remove_previous_generation "$slot" || \
            warn "the previous generation of slot $slot is still there; rerun apply to finish the cleanup"
    done
    commit_marker_remove
}

# The deployment transaction fingerprint does not inspect live route health or
# catalog state. It is computable before any slot exists once the main topology
# has supplied the metrics-network identity.
command=${1:-}
if [[ $command == spec-hash ]]; then
    fleet_spec_hash
    exit 0
fi

# Observability and acceptance gates never mutate fleet or parent state. Let
# them run while a deployment owns the exclusive lock so an operator can watch
# convergence. Every command that changes containers, networks, or Runtime API
# state remains serialized.
case $command in
    status | verify-admission | wait-version) ;;
    *) gonka_acquire_deployment_lock "$config_dir" || exit 1 ;;
esac

# Runtime-discovered versions are part of the safety reserve even though they
# are intentionally absent from the host-managed bootstrap environment.
discover_expected_routes

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
        target_slot=$2
        prepare_slot_networks
        pull_router_image
        resolve_candidate
        require_placement_compatible "$candidate_image"
        start_existing_or_create_slot "$target_slot"
        wait_slot_routes "$target_slot"
        wait_parent_admission "$target_slot"
        ;;
    -h | --help | help) usage ;;
    *) usage; fail "unknown command '${command:-}'" ;;
esac
