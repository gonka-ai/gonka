#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
config_env=${GONKA_CONFIG_ENV:-$script_dir/config.env}
versiond_mode=auto
edge_mode=auto
compose_project_name=
compose_project_directory=
rollback_pending=false
rollback_image=
rollback_kind=
policy_rollback_pending=false
declare -A rollback_env=()
declare -A policy_rollback_images=()
declare -A policy_rollback_replicas=()
declare -A inherited_env=()
declare -a migration_routes=()
declare -a compose_file_args=()
policy_services=(proxy-policy2 proxy-policy)
policy_contract_version=1

fail() {
    echo "enable-router-ha: $*" >&2
    exit 1
}

warn() {
    echo "enable-router-ha: warning: $*" >&2
}

usage() {
    cat >&2 <<'EOF'
Usage: enable-router-ha.sh [OPTIONS]

Performs the one-time cutover from the public nginx + singleton-router layout
to the public HAProxy, private nginx policy workers, and versiond-router fleet.

Options:
  --versiond-mode auto|single|ha
  --edge-mode     auto|single|multi
  --compose-file FILE              repeat for the complete ordered model
  --compose-project-name NAME      must match the running Compose project
  --compose-project-directory DIR  must match the running Compose project

Without --compose-file or COMPOSE_FILE, the script recovers the ordered file
list and project identity from Docker Compose labels on the running services.
EOF
}

while (($# > 0)); do
    case $1 in
        --versiond-mode)
            (($# >= 2)) || fail "--versiond-mode requires a value"
            versiond_mode=$2
            shift 2
            ;;
        --edge-mode)
            (($# >= 2)) || fail "--edge-mode requires a value"
            edge_mode=$2
            shift 2
            ;;
        --compose-file)
            (($# >= 2)) || fail "--compose-file requires a value"
            compose_file_args+=("$2")
            shift 2
            ;;
        --compose-project-name)
            (($# >= 2)) || fail "--compose-project-name requires a value"
            compose_project_name=$2
            shift 2
            ;;
        --compose-project-directory)
            (($# >= 2)) || fail \
                "--compose-project-directory requires a value"
            compose_project_directory=$2
            shift 2
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *) fail "unknown argument: $1" ;;
    esac
done

case $versiond_mode in auto | single | ha) ;; *) fail "invalid versiond mode" ;; esac
case $edge_mode in auto | single | multi) ;; *) fail "invalid edge-api mode" ;; esac
[[ -f $config_env ]] || fail "configuration file not found: $config_env"
while IFS= read -r name; do
    case $name in
        COMPOSE_* | GONKA_* | VERSIOND_* | ROUTER_HA_* | PROXY_* | DOCKER_BIN)
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
fleet_bin=${VERSIOND_ROUTER_FLEET_BIN:-$script_dir/versiond-router-fleet.sh}

command -v "$docker_bin" >/dev/null 2>&1 || fail "$docker_bin is required"
command -v flock >/dev/null 2>&1 || fail "flock is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"
# shellcheck source=deploy/join/compose-topology.sh
source "$script_dir/compose-topology.sh"

container_exists() {
    "$docker_bin" inspect "$1" >/dev/null 2>&1
}

if [[ $versiond_mode == auto ]]; then
    if container_exists devshard-postgres || container_exists versiond2 || \
        container_exists versiond-router; then
        versiond_mode=ha
    else
        versiond_mode=single
    fi
fi
if [[ $edge_mode == auto ]]; then
    if container_exists edge-api2 || container_exists edge-api3 || \
        container_exists edge-api-router; then
        edge_mode=multi
    else
        edge_mode=single
    fi
fi

runtime_compose_containers=(versiond edge-api)
container_exists proxy && runtime_compose_containers+=(proxy)
for container in versiond2 devshard-postgres edge-api2 edge-api3; do
    container_exists "$container" && runtime_compose_containers+=("$container")
done
gonka_compose_resolve \
    "$docker_bin" "$script_dir" "$versiond_mode" "$edge_mode" \
    "$compose_project_name" "$compose_project_directory" \
    compose_file_args runtime_compose_containers
compose=("${GONKA_COMPOSE_COMMAND[@]}")

ensure_compose_network() {
    local key=$1 name=$2 project=$3 ownership
    if "$docker_bin" network inspect "$name" >/dev/null 2>&1; then
        ownership=$("$docker_bin" network inspect --format \
            '{{or (index .Labels "com.docker.compose.network") ""}}|{{or (index .Labels "com.docker.compose.project") ""}}' \
            "$name")
        [[ $ownership == "$key|$project" ]] || fail \
            "network $name exists with ownership '$ownership', expected '$key|$project'"
        return 0
    fi
    "$docker_bin" network create --internal \
        --label "com.docker.compose.network=$key" \
        --label "com.docker.compose.project=$project" \
        "$name" >/dev/null
}
pull_policy=${ROUTER_HA_PULL_POLICY:-always}
case $pull_policy in always | missing | never) ;; *) fail "ROUTER_HA_PULL_POLICY must be always, missing, or never" ;; esac
cutover_timeout=${ROUTER_HA_CUTOVER_TIMEOUT_SECONDS:-60}
case $cutover_timeout in '' | *[!0-9]* | 0) fail "ROUTER_HA_CUTOVER_TIMEOUT_SECONDS must be positive" ;; esac
config_dir=$(cd -- "$(dirname -- "$config_env")" && pwd -P)
lock_file=${ROUTER_HA_CUTOVER_LOCK:-$config_dir/.gonka-router-ha-cutover.lock}

proxy_component() {
    "$docker_bin" inspect --format '{{index .Config.Labels "ai.gonka.component"}}' proxy 2>/dev/null || true
}

proxy_env_value() {
    local name=$1 line

    while IFS= read -r line; do
        case $line in
            "$name="*) printf '%s\n' "${line#*=}"; return 0 ;;
        esac
    done < <("$docker_bin" inspect --format \
        '{{range .Config.Env}}{{println .}}{{end}}' proxy)
    return 1
}

install_rollback_traps() {
	trap 'restore_proxy "$?"' EXIT
	trap 'exit 129' HUP
	trap 'exit 130' INT
	trap 'exit 143' TERM
}

arm_proxy_rollback() {
    local kind=$1 old_image key

	if [[ $kind == absent ]]; then
		rollback_kind=$kind
		rollback_image=
		rollback_pending=true
		install_rollback_traps
		return 0
	fi
    old_image=$("$docker_bin" inspect --format '{{.Image}}' proxy)
    rollback_image="gonka/router-ha-proxy-rollback:$(date +%s%N)-$$"
    "$docker_bin" tag "$old_image" "$rollback_image"
    rollback_kind=$kind
    rollback_env=()
    if [[ $kind == current ]]; then
        for key in \
            NGINX_MODE PROXY_POLICY_POOL_SLOTS \
            PROXY_ROUTER_PUBLIC_IDLE_SECONDS HAPROXY_DNS_RESOLVER \
            VERSIOND_ROUTER_POOL_HOST VERSIOND_ROUTER_FLEET_CAPACITY \
            VERSIOND_NON_HA_VERSIONS VERSIOND_VERSIONS \
            VERSIOND_ROUTING_CATALOG_URL \
            VERSIOND_ROUTING_CATALOG_POLL_SECONDS \
            VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS \
            VERSIOND_ROUTING_ACTIVATION_MIN_READY \
            VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS \
            PROXY_ROUTER_VERSION_CAPACITY EDGE_API_POOL_HOST EDGE_API_PORT; do
            rollback_env[$key]=$(proxy_env_value "$key" || true)
        done
    fi
    rollback_pending=true
	install_rollback_traps
}

capture_policy_rollback() {
	local service id image rollback_tag first_image
	local -a ids=()

	for service in "${policy_services[@]}"; do
		mapfile -t ids < <("${compose[@]}" ps --all --quiet "$service")
		policy_rollback_replicas[$service]=${#ids[@]}
		((${#ids[@]} > 0)) || continue
		first_image=
		for id in "${ids[@]}"; do
			image=$("$docker_bin" inspect --format '{{.Image}}' "$id")
			if [[ -n $first_image && $image != "$first_image" ]]; then
				fail "$service has mixed image generations; refusing an ambiguous rollback"
			fi
			first_image=$image
		done
		rollback_tag="gonka/router-ha-policy-rollback:$service-$(date +%s%N)-$$"
		"$docker_bin" tag "$first_image" "$rollback_tag"
		policy_rollback_images[$service]=$rollback_tag
	done
	policy_rollback_pending=true
	install_rollback_traps
}

restore_policy_slots() {
	local service replicas rollback_tag restored=true

	# Restore the original scaled service before removing/restoring the reserve.
	# This preserves at least one healthy generation throughout compensation.
	for service in proxy-policy proxy-policy2; do
		replicas=${policy_rollback_replicas[$service]:-0}
		rollback_tag=${policy_rollback_images[$service]-}
		if ((replicas == 0)); then
			"${compose[@]}" rm -s -f "$service" >/dev/null || restored=false
			continue
		fi
		if [[ -z $rollback_tag ]] || ! PROXY_POLICY_IMAGE=$rollback_tag \
			"${compose[@]}" up -d --no-deps --force-recreate --wait \
			--wait-timeout "$cutover_timeout" --scale "$service=$replicas" \
			"$service"; then
			restored=false
		fi
	done
	$restored
}

cleanup_policy_rollback() {
	local image
	for image in "${policy_rollback_images[@]}"; do
		"$docker_bin" image rm "$image" >/dev/null 2>&1 || true
	done
}

image_policy_contract() {
	"$docker_bin" image inspect --format \
		'{{index .Config.Labels "ai.gonka.proxy-policy-contract"}}' "$1"
}

verify_policy_contract() {
	local config policy_image proxy_image policy_contract proxy_contract current_contract
	config=$("${compose[@]}" config --format json)
	policy_image=$(jq -er '.services["proxy-policy"].image' <<<"$config")
	proxy_image=$(jq -er '.services.proxy.image' <<<"$config")
	[[ $(jq -er '.services["proxy-policy2"].image' <<<"$config") == "$policy_image" ]] ||
		fail "proxy-policy slots do not use one candidate image"
	policy_contract=$(image_policy_contract "$policy_image") || fail \
		"cannot read proxy-policy contract from $policy_image"
	proxy_contract=$(image_policy_contract "$proxy_image") || fail \
		"cannot read proxy-policy contract from $proxy_image"
	[[ $policy_contract == "$policy_contract_version" && \
		$policy_contract == "$proxy_contract" ]] || fail \
		"candidate proxy-policy contract '$policy_contract' does not match proxy-router '$proxy_contract'"
	if [[ $(proxy_component) == proxy-router ]]; then
		current_contract=$("$docker_bin" inspect --format \
			'{{index .Config.Labels "ai.gonka.proxy-policy-contract"}}' proxy)
		[[ $current_contract == "$policy_contract" ]] || fail \
			"rolling policy update changes ingress contract $current_contract -> $policy_contract; use a maintenance migration"
	fi
}

policy_address_admitted() {
	local backend=$1 address=$2
	"$docker_bin" exec proxy /bin/sh -ec \
		"printf 'show servers state $backend\\n' | socat stdio /var/run/haproxy/haproxy.sock" |
		awk -v address="$address" '
			NR > 2 && $5 == address {
				found = 1
				if ($6 == 2 && $7 == 0) exit 0
				exit 1
			}
			END { if (!found) exit 1 }
		'
}

wait_policy_admission() {
	local service=$1 deadline=$((SECONDS + cutover_timeout)) mode id address ready
	local -a ids=()
	mode=$(proxy_env_value NGINX_MODE || true)
	mode=${mode:-http}
	while ((SECONDS < deadline)); do
		mapfile -t ids < <("${compose[@]}" ps --quiet "$service")
		ready=true
		((${#ids[@]} > 0)) || ready=false
		for id in "${ids[@]}"; do
			address=$("$docker_bin" inspect --format \
				"{{with index .NetworkSettings.Networks \"$policy_network\"}}{{.IPAddress}}{{end}}" \
				"$id")
			[[ $address =~ ^[0-9]+(\.[0-9]+){3}$ ]] || {
				ready=false
				continue
			}
			if [[ $mode != https ]] && ! policy_address_admitted policy_http "$address"; then
				ready=false
			fi
			if [[ $mode != http ]] && ! policy_address_admitted policy_https "$address"; then
				ready=false
			fi
		done
		[[ $ready == true ]] && return 0
		sleep 1
	done
	return 1
}

roll_policy_slots() {
	local service
	for service in "${policy_services[@]}"; do
		"${compose[@]}" up -d --no-deps --wait \
			--wait-timeout "$cutover_timeout" "$service"
		if [[ $(proxy_component) == proxy-router ]]; then
			wait_policy_admission "$service" || fail \
				"$service is healthy but was not admitted by the public proxy"
		fi
	done
}

wait_component() {
    local component=$1 deadline=$((SECONDS + cutover_timeout))
    while ((SECONDS < deadline)); do
        if "$docker_bin" exec proxy /bin/busybox wget -q -T 3 -O /dev/null \
            "http://127.0.0.1:8404/readyz?component=$component"; then
            return 0
        fi
        sleep 1
    done
    return 1
}

run_fleet() {
    GONKA_CONFIG_ENV=$config_env \
    VERSIOND_ROUTER_FRONT_NETWORK=$GONKA_COMPOSE_ROUTER_FRONT_NETWORK \
    VERSIOND_ROUTER_BACK_NETWORK=$GONKA_COMPOSE_ROUTER_BACK_NETWORK \
    VERSIOND_ROUTER_METRICS_NETWORK=$GONKA_COMPOSE_DEFAULT_NETWORK \
        "$fleet_bin" "$@"
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

capture_migration_route_baseline() {
    local route encoded
    local -A seen=()

    container_exists versiond-router || fail \
        "the transitional versiond-router is missing; refusing a cutover whose v4 rollback would have no upstream"
    migration_routes=()
    while IFS= read -r route; do
        [[ -n $route && -z ${seen[$route]-} ]] || continue
        seen[$route]=1
        encoded=$(urlencode "$route")
        if "$docker_bin" exec versiond-router /bin/busybox wget -q -T 3 \
            -O /dev/null "http://127.0.0.1:8404/readyz?version=$encoded" \
            >/dev/null 2>&1; then
            migration_routes+=("$route")
        fi
    done < <(migration_router_routes)
    ((${#migration_routes[@]} > 0)) || fail \
        "the transitional versiond-router serves none of the declared routes; refusing to commit an unverified fleet"
    echo "Captured migration route baseline: ${migration_routes[*]}"
}

migration_router_routes() {
    local diagnostic=/usr/local/lib/router-runtime/catalog-status map
    if "$docker_bin" exec versiond-router test -x "$diagnostic" \
        >/dev/null 2>&1; then
        for map in /etc/haproxy/non_ha.map /etc/haproxy/versions.map; do
            "$docker_bin" exec versiond-router "$diagnostic" "$map"
        done
        return
    fi

    # Transitional images from before runtime catalog projection expose only
    # their startup environment.
    printf '%s\n%s\n' \
        "${VERSIOND_NON_HA_VERSIONS-v1 v2 v3}" \
        "${VERSIOND_VERSIONS-v4 v5 v6 v7 v8}" \
        | tr ',;' '  ' | tr -s ' ' '\n'
}

remove_migration_container() {
    local container=$1 output
    container_exists "$container" || return 0
    if ! output=$("$docker_bin" rm -f "$container" 2>&1); then
        warn "could not remove migration container $container: $output"
    fi
}

remove_migration_routers() {
    [[ $versiond_mode != ha ]] || remove_migration_container versiond-router
    [[ $edge_mode != multi ]] || remove_migration_container edge-api-router
}

restore_proxy() {
	local status=${1:-$?} proxy_restored=true policy_restored=true
    trap - EXIT INT TERM HUP
    if [[ $rollback_pending == true ]]; then
        warn "public ingress update failed; restoring the captured proxy"
        local versiond_service=versiond edge_service='' restored=false
		if [[ $rollback_kind == absent ]]; then
			"$docker_bin" rm -f proxy >/dev/null 2>&1 || true
			restored=true
		else
			"$docker_bin" rm -f proxy >/dev/null 2>&1 || true
			if [[ $rollback_kind == v4 ]]; then
				[[ $versiond_mode == ha ]] && versiond_service=versiond-router
				[[ $edge_mode == multi ]] && edge_service=edge-api-router
				if PROXY_V4_IMAGE=$rollback_image \
					PROXY_V4_VERSIOND_SERVICE_NAME=$versiond_service \
					PROXY_V4_EDGE_API_SERVICE_NAME=$edge_service \
					"${compose[@]}" -f "$script_dir/docker-compose.proxy-v4-compat.yml" \
						up -d --no-deps --force-recreate --wait \
						--wait-timeout "$cutover_timeout" proxy; then
					restored=true
				fi
			elif NGINX_MODE="${rollback_env[NGINX_MODE]}" \
				PROXY_POLICY_POOL_SLOTS="${rollback_env[PROXY_POLICY_POOL_SLOTS]}" \
				PROXY_ROUTER_PUBLIC_IDLE_SECONDS="${rollback_env[PROXY_ROUTER_PUBLIC_IDLE_SECONDS]}" \
				HAPROXY_DNS_RESOLVER="${rollback_env[HAPROXY_DNS_RESOLVER]}" \
				VERSIOND_ROUTER_POOL_HOST="${rollback_env[VERSIOND_ROUTER_POOL_HOST]}" \
				VERSIOND_ROUTER_FLEET_CAPACITY="${rollback_env[VERSIOND_ROUTER_FLEET_CAPACITY]}" \
				VERSIOND_NON_HA_VERSIONS="${rollback_env[VERSIOND_NON_HA_VERSIONS]}" \
				VERSIOND_VERSIONS="${rollback_env[VERSIOND_VERSIONS]}" \
				VERSIOND_ROUTING_CATALOG_URL="${rollback_env[VERSIOND_ROUTING_CATALOG_URL]}" \
				VERSIOND_ROUTING_CATALOG_POLL_SECONDS="${rollback_env[VERSIOND_ROUTING_CATALOG_POLL_SECONDS]}" \
				VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS="${rollback_env[VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS]}" \
				VERSIOND_ROUTING_ACTIVATION_MIN_READY="${rollback_env[VERSIOND_ROUTING_ACTIVATION_MIN_READY]}" \
				VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS="${rollback_env[VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS]}" \
				PROXY_ROUTER_VERSION_CAPACITY="${rollback_env[PROXY_ROUTER_VERSION_CAPACITY]}" \
				EDGE_API_POOL_HOST="${rollback_env[EDGE_API_POOL_HOST]}" \
				EDGE_API_PORT="${rollback_env[EDGE_API_PORT]}" \
				PROXY_ROUTER_IMAGE=$rollback_image \
				"${compose[@]}" up -d --no-deps --force-recreate --wait \
					--wait-timeout "$cutover_timeout" proxy; then
				restored=true
			fi
		fi
        if [[ $restored == true && $rollback_kind == current ]]; then
            [[ $versiond_mode != ha ]] || run_fleet verify-admission || \
                restored=false
            [[ $edge_mode != multi || $restored != true ]] || \
                wait_component edge-api || restored=false
        fi
        if [[ $restored == true ]]; then
			if [[ $rollback_kind == absent ]]; then
				warn "the failed public proxy was removed"
			else
				warn "the previous public proxy was restored"
			fi
        else
			proxy_restored=false
            warn "automatic public proxy rollback failed; immediate operator action is required"
        fi
    fi
	if [[ $policy_rollback_pending == true ]]; then
		warn "ingress update failed; restoring the captured proxy-policy slots"
		if restore_policy_slots; then
			warn "the previous proxy-policy generation was restored"
		else
			policy_restored=false
			warn "automatic proxy-policy rollback failed; immediate operator action is required"
		fi
	fi
	[[ $proxy_restored == true && $policy_restored == true ]] || status=1
    exit "$status"
}

if [[ ! -e $lock_file ]]; then
    (umask 000; : >"$lock_file") || fail "cannot create router HA lock $lock_file"
fi
exec 9<"$lock_file"
flock -n 9 || fail "another router HA cutover holds $lock_file"

echo "Preparing router HA topology: versiond=$versiond_mode edge-api=$edge_mode"
compose_project=$GONKA_COMPOSE_PROJECT
policy_network=$GONKA_COMPOSE_POLICY_NETWORK
ensure_compose_network proxy-policy-front "$policy_network" "$compose_project"
if [[ $versiond_mode == ha ]]; then
    # `apply` is the lifecycle bridge between the main Compose project and the
    # independent router projects. It bootstraps an absent fleet, but on an
    # existing deployment it pulls and rolls only slots whose image or rendered
    # Compose contract changed.
    run_fleet apply
fi

# During the one-time cutover the old public nginx still owns the `proxy`
# container name. Attach it to the private policy network before starting the
# workers so they can derive a stable trusted subnet from the future ingress
# alias. Recreating `proxy` below replaces this endpoint with proxy-router.
if container_exists proxy; then
    policy_aliases=$("$docker_bin" inspect --format \
        "{{with index .NetworkSettings.Networks \"$policy_network\"}}{{range .Aliases}}{{println .}}{{end}}{{end}}" \
        proxy)
    if ! grep -Fxq proxy-policy-ingress <<<"$policy_aliases"; then
        if [[ -n $policy_aliases ]]; then
            "$docker_bin" network disconnect "$policy_network" proxy
        fi
        "$docker_bin" network connect --alias proxy-policy-ingress \
            "$policy_network" proxy
    fi
fi

current_proxy_component=$(proxy_component)
proxy_was_absent=false
if ! container_exists proxy; then
	proxy_was_absent=true
	warn "public proxy is absent; rebuilding it from the resolved Compose topology"
elif [[ $current_proxy_component != proxy-router ]]; then
	[[ $versiond_mode != ha ]] || capture_migration_route_baseline
fi

if [[ $pull_policy != never ]]; then
	"${compose[@]}" pull --policy "$pull_policy" \
		proxy-policy2 proxy-policy proxy
fi
verify_policy_contract
capture_policy_rollback
if [[ $proxy_was_absent == true ]]; then
	arm_proxy_rollback absent
elif [[ $current_proxy_component == proxy-router ]]; then
	arm_proxy_rollback current
else
	arm_proxy_rollback v4
fi

# The reserve slot is always reconciled first. The fixed service dependency
# preserves the same order for an ordinary `docker compose up`.
roll_policy_slots

# A repeated run is also a real update path. Keep both the exact previous proxy
# and the policy generation armed until the complete public path is admitted.
if [[ $current_proxy_component == proxy-router ]]; then
    "${compose[@]}" up -d --no-deps --wait \
        --wait-timeout "$cutover_timeout" proxy
    [[ $versiond_mode != ha ]] || run_fleet verify-admission
    [[ $edge_mode != multi ]] || wait_component edge-api
    rollback_pending=false
	policy_rollback_pending=false
    trap - EXIT INT TERM HUP
	[[ -z $rollback_image ]] || \
		"$docker_bin" image rm "$rollback_image" >/dev/null 2>&1 || true
	cleanup_policy_rollback
    remove_migration_routers
    echo "Router HA topology is already active"
    exit 0
fi

echo "Switching the public listener to proxy-router"
if ! "${compose[@]}" up -d --no-deps --force-recreate --wait \
    --wait-timeout "$cutover_timeout" proxy; then
    fail "proxy-router did not become ready"
fi
[[ $versiond_mode != ha ]] || \
    run_fleet verify-admission "${migration_routes[@]}" || fail \
        "proxy-router did not admit the complete router fleet and migration route baseline"
[[ $edge_mode != multi ]] || wait_component edge-api || fail \
    "proxy-router cannot reach a ready edge-api"

rollback_pending=false
policy_rollback_pending=false
trap - EXIT INT TERM HUP
[[ -z $rollback_image ]] || \
	"$docker_bin" image rm "$rollback_image" >/dev/null 2>&1 || true
cleanup_policy_rollback

# These singleton migration bridges are outside the steady-state model. Remove
# them only after the new public path has passed all component checks.
remove_migration_routers

echo "Router HA cutover completed"
