#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
config_env=${GONKA_CONFIG_ENV:-$script_dir/config.env}
release_contract=$script_dir/devshard-v5-release.env
# shellcheck source=deploy/join/devshard-v5-release-contract.sh
source "$script_dir/devshard-v5-release-contract.sh"
devshard_v5_load_release_contract "$release_contract"
release_id=$DEVSHARD_V5_RELEASE_ID
operation_id="$(date +%s%N)-$$"
versiond_mode=auto
edge_mode=auto
preflight_only=false
strict_capacity=false
maintenance_ack=false
compose_project_name=
compose_project_directory=
declare -a compose_file_args=()
declare -A inherited_env=()

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
  --compose-file FILE              repeat for the complete ordered model
  --compose-project-name NAME      must match the running Compose project
  --compose-project-directory DIR  must match the running Compose project
  --preflight-only                 verify release and host without mutation
  --strict-capacity                enforce Quickstart capacity recommendations
  --acknowledge-maintenance        allow the one-time ingress/storage cutover

Without --compose-file or COMPOSE_FILE, the script recovers the ordered file
list and project identity from Docker Compose labels on the running services.
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
        --compose-file)
            [[ $# -ge 2 ]] || fail "--compose-file requires a value"
            compose_file_args+=("$2")
            shift 2
            ;;
        --compose-project-name)
            [[ $# -ge 2 ]] || fail "--compose-project-name requires a value"
            compose_project_name=$2
            shift 2
            ;;
        --compose-project-directory)
            [[ $# -ge 2 ]] || fail \
                "--compose-project-directory requires a value"
            compose_project_directory=$2
            shift 2
            ;;
        --preflight-only)
            preflight_only=true
            shift
            ;;
        --strict-capacity)
            strict_capacity=true
            shift
            ;;
        --acknowledge-maintenance)
            maintenance_ack=true
            shift
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
while IFS= read -r name; do
    case $name in
        COMPOSE_* | GONKA_* | DEVSHARD_V5_* | VERSIOND_* | ROUTER_HA_* | \
            PROXY_* | DOCKER_BIN)
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
# These two values are release/test controls, not node configuration. Do not
# let a persistent config.env silently disable source verification or remember
# a maintenance acknowledgement from an earlier operation.
for name in DEVSHARD_V5_ALLOW_UNRELEASED_SOURCE DEVSHARD_V5_MAINTENANCE_ACK; do
    if [[ -z ${inherited_env[$name]+present} ]]; then
        unset "$name"
    fi
done
# config.env is operator input, while release identity and image references are
# immutable distribution metadata. Reload the contract so similarly named
# config keys cannot replace the tagged release artifacts.
devshard_v5_load_release_contract "$release_contract"
config_dir=$(cd -- "$(dirname -- "$config_env")" && pwd -P)
docker_bin=${DOCKER_BIN:-docker}
fleet_bin=${VERSIOND_ROUTER_FLEET_BIN:-$script_dir/versiond-router-fleet.sh}
enable_router_bin=${ROUTER_HA_ENABLE_BIN:-$script_dir/enable-router-ha.sh}
upgrade_marker=${DEVSHARD_V5_UPGRADE_MARKER:-$config_dir/.gonka-devshard-v5-upgrade-complete}
upgrade_lock=${DEVSHARD_V5_UPGRADE_LOCK:-$config_dir/.gonka-devshard-v5-upgrade.lock}

if [[ $strict_capacity == false ]]; then
    case ${DEVSHARD_V5_STRICT_CAPACITY:-false} in
        true | 1 | yes) strict_capacity=true ;;
        false | 0 | no) ;;
        *) fail "DEVSHARD_V5_STRICT_CAPACITY must be true or false" ;;
    esac
fi
if [[ $maintenance_ack == false ]]; then
    case ${DEVSHARD_V5_MAINTENANCE_ACK:-false} in
        true | 1 | yes) maintenance_ack=true ;;
        false | 0 | no) ;;
        *) fail "DEVSHARD_V5_MAINTENANCE_ACK must be true or false" ;;
    esac
fi
devshard_v5_verify_dependencies "$docker_bin"
devshard_v5_verify_release_source "$script_dir"
# shellcheck disable=SC1091 # Runtime path is anchored to this script.
source "$script_dir/compose-topology.sh"

if [[ ! -e $upgrade_lock ]]; then
    (umask 000; : >"$upgrade_lock") || fail \
        "cannot create deployment lock $upgrade_lock"
fi
exec 8<"$upgrade_lock"
flock -n 8 || fail "another devshard upgrade holds $upgrade_lock"

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

service_instances_match_release() {
    local service=$1 expected_image=$2 id details actual_image running health
    local -a ids=()

    mapfile -t ids < <("${GONKA_COMPOSE_COMMAND[@]}" ps --all --quiet "$service")
    ((${#ids[@]} > 0)) || {
        warn "release verification found no $service container"
        return 1
    }
    for id in "${ids[@]}"; do
        details=$("$docker_bin" inspect --format \
            '{{.Config.Image}}|{{.State.Running}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' \
            "$id") || return 1
        IFS='|' read -r actual_image running health <<<"$details"
        if [[ -n $expected_image && $actual_image != "$expected_image" ]]; then
            warn "$service container $id uses $actual_image, expected $expected_image"
            return 1
        fi
        if [[ $running != true || $health != healthy ]]; then
            warn "$service container $id is running=$running health=$health"
            return 1
        fi
    done
}

verify_release_application_state() {
    service_instances_match_release versiond "$DEVSHARD_V5_VERSIOND_IMAGE" || return 1
    service_instances_match_release edge-api "$DEVSHARD_V5_EDGE_API_IMAGE" || return 1
    if [[ $versiond_mode == ha ]]; then
        service_instances_match_release versiond2 "$DEVSHARD_V5_VERSIOND_IMAGE" || return 1
        verify_shared_postgres_identity || return 1
        if [[ $GONKA_COMPOSE_POSTGRES_MODE == local ]]; then
            service_instances_match_release devshard-postgres '' || return 1
        fi
    fi
    if [[ $edge_mode == multi ]]; then
        service_instances_match_release edge-api2 "$DEVSHARD_V5_EDGE_API_IMAGE" || return 1
        service_instances_match_release edge-api3 "$DEVSHARD_V5_EDGE_API_IMAGE" || return 1
    fi
}

versiond_storage_identity() {
    local service=$1 container identity
    container=$("${GONKA_COMPOSE_COMMAND[@]}" ps --quiet "$service")
    [[ -n $container ]] || return 1
    identity=$("$docker_bin" exec "$container" wget -qO- -T 5 \
        http://127.0.0.1:8080/internal/storage-identity | \
        jq -er '.identity | strings | select(length > 0)') || return 1
    printf '%s\n' "$identity"
}

verify_shared_postgres_identity() {
    local first second committed=
    first=$(versiond_storage_identity versiond) || {
        warn "cannot read the database identity through versiond"
        return 1
    }
    second=$(versiond_storage_identity versiond2) || {
        warn "cannot read the database identity through versiond2"
        return 1
    }
    if [[ $first != "$second" ]]; then
        warn "versiond replicas use different PostgreSQL databases ($first != $second)"
        return 1
    fi
    if [[ -f $upgrade_marker ]] && jq -e 'type == "object"' \
        "$upgrade_marker" >/dev/null 2>&1; then
        committed=$(jq -r '.storage.postgres_identity // ""' "$upgrade_marker")
    fi
    if [[ -n $committed && $first != "$committed" ]]; then
        warn "PostgreSQL identity changed from committed $committed to $first"
        return 1
    fi
    verified_postgres_identity=$first
}

verify_release_ingress_state() {
    service_instances_match_release proxy "$DEVSHARD_V5_PROXY_ROUTER_IMAGE" || return 1
    service_instances_match_release proxy-policy "$DEVSHARD_V5_PROXY_POLICY_IMAGE"
}

converge_release_service() {
    local service=$1

    "${compose[@]}" up -d --no-deps --wait \
        --wait-timeout "$(service_startup_timeout "$service")" \
        "$service" 8<&-
}

write_upgrade_marker() {
    local marker=$desired_upgrade_marker marker_tmp=$upgrade_marker.tmp.$$
    if [[ $versiond_mode == ha ]]; then
        [[ -n $verified_postgres_identity ]] || verify_shared_postgres_identity
        marker=$(jq -c --arg identity "$verified_postgres_identity" \
            '. + {storage: {postgres_identity: $identity}}' <<<"$marker")
    fi
    mkdir -p "$(dirname -- "$upgrade_marker")"
    printf '%s\n' "$marker" >"$marker_tmp"
    mv -f "$marker_tmp" "$upgrade_marker"
}

upgrade_marker_release() {
    if jq -e 'type == "object"' "$upgrade_marker" >/dev/null 2>&1; then
        jq -er '.release_id | strings | select(length > 0)' "$upgrade_marker"
        return
    fi
    head -n1 "$upgrade_marker"
}

restore_marker_topology() {
	local marker_release marker_versiond_mode marker_edge_mode

    [[ -f $upgrade_marker ]] || return 0
	jq -e '
		type == "object" and .schema == 1 and
		(.topology.versiond == "single" or .topology.versiond == "ha") and
		(.topology.edge_api == "single" or .topology.edge_api == "multi") and
		(.compose.files | type == "array" and length > 0) and
        (.compose.project | type == "string" and length > 0) and
        (.compose.project_directory | type == "string" and length > 0)
    ' "$upgrade_marker" >/dev/null 2>&1 || return 0
    marker_release=$(upgrade_marker_release) || fail \
        "cannot read release identity from $upgrade_marker"
	[[ $marker_release == "$release_id" ]] || return 0
	marker_versiond_mode=$(jq -er '.topology.versiond' "$upgrade_marker")
	marker_edge_mode=$(jq -er '.topology.edge_api' "$upgrade_marker")
	if [[ $versiond_mode == auto ]]; then
		versiond_mode=$marker_versiond_mode
	fi
	if [[ $edge_mode == auto ]]; then
		edge_mode=$marker_edge_mode
	fi

    if ((${#compose_file_args[@]} == 0)) && [[ -z ${COMPOSE_FILE-} ]]; then
        mapfile -t compose_file_args < <(jq -er '.compose.files[]' "$upgrade_marker")
        export GONKA_COMPOSE_USE_COMMITTED_TOPOLOGY=true
        echo "Using the committed Compose topology from $upgrade_marker"
    fi
    [[ -n $compose_project_name ]] || \
        compose_project_name=$(jq -er '.compose.project' "$upgrade_marker")
	[[ -n $compose_project_directory ]] || \
		compose_project_directory=$(jq -er \
			'.compose.project_directory' "$upgrade_marker")
	committed_marker_loaded=true
}

committed_marker_loaded=false
restore_marker_topology

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
export EDGE_API_IMAGE=$DEVSHARD_V5_EDGE_API_IMAGE
export VERSIOND_IMAGE=$DEVSHARD_V5_VERSIOND_IMAGE
export EDGE_API_ROUTER_IMAGE=$DEVSHARD_V5_EDGE_API_ROUTER_IMAGE
export VERSIOND_ROUTER_IMAGE=$DEVSHARD_V5_VERSIOND_ROUTER_IMAGE
export PROXY_POLICY_IMAGE=$DEVSHARD_V5_PROXY_POLICY_IMAGE
export PROXY_ROUTER_IMAGE=$DEVSHARD_V5_PROXY_ROUTER_IMAGE

runtime_compose_containers=(proxy versiond edge-api)
for container in proxy-policy versiond2 versiond-router devshard-postgres \
    edge-api2 edge-api3 edge-api-router; do
    container_exists "$container" && runtime_compose_containers+=("$container")
done
gonka_compose_resolve \
    "$docker_bin" "$script_dir" "$versiond_mode" "$edge_mode" \
    "$compose_project_name" "$compose_project_directory" \
    compose_file_args runtime_compose_containers
compose=("${GONKA_COMPOSE_COMMAND[@]}")
if [[ $versiond_mode == ha ]]; then
    compose+=(-f "$script_dir/docker-compose.versiond-v5-compat.yml")
fi
if [[ $edge_mode == multi ]]; then
    compose+=(-f "$script_dir/docker-compose.edge-api-v5-compat.yml")
fi

devshard_v5_report_compose_files "$script_dir" "${GONKA_COMPOSE_FILES[@]}"
devshard_v5_verify_capacity "$config_dir" "$strict_capacity"
effective_compose_config=$("${GONKA_COMPOSE_COMMAND[@]}" config --format json)
compose_config_sha=$(
    jq -Sc . <<<"$effective_compose_config" | sha256sum | awk '{print $1}'
)
compose_files_json=$(
    printf '%s\n' "${GONKA_COMPOSE_FILES[@]}" | jq -Rsc \
        'split("\n") | map(select(length > 0))'
)
desired_upgrade_state=$(jq -cn \
    --arg release_id "$release_id" \
    --arg release_commit "$DEVSHARD_V5_RELEASE_COMMIT" \
    --arg versiond_mode "$versiond_mode" \
    --arg edge_mode "$edge_mode" \
    --arg project "$GONKA_COMPOSE_PROJECT" \
    --arg project_directory "$GONKA_COMPOSE_PROJECT_DIRECTORY" \
    --arg config_sha256 "$compose_config_sha" \
    --argjson files "$compose_files_json" \
    --arg edge_api "$DEVSHARD_V5_EDGE_API_IMAGE" \
    --arg versiond "$DEVSHARD_V5_VERSIOND_IMAGE" \
    --arg edge_api_router "$DEVSHARD_V5_EDGE_API_ROUTER_IMAGE" \
    --arg versiond_router "$DEVSHARD_V5_VERSIOND_ROUTER_IMAGE" \
    --arg proxy_policy "$DEVSHARD_V5_PROXY_POLICY_IMAGE" \
    --arg proxy_router "$DEVSHARD_V5_PROXY_ROUTER_IMAGE" \
    '{
        schema: 1,
        release_id: $release_id,
        release_commit: $release_commit,
        topology: {versiond: $versiond_mode, edge_api: $edge_mode},
        compose: {
            project: $project,
            project_directory: $project_directory,
            files: $files,
            config_sha256: $config_sha256
        },
        images: {
            edge_api: $edge_api,
            versiond: $versiond,
            edge_api_router: $edge_api_router,
            versiond_router: $versiond_router,
            proxy_policy: $proxy_policy,
            proxy_router: $proxy_router
        }
    }')
desired_fingerprint=$(
    printf '%s' "$desired_upgrade_state" | sha256sum | awk '{print $1}'
)
desired_upgrade_marker=$(jq -c --arg fingerprint "$desired_fingerprint" \
    '. + {fingerprint: $fingerprint}' <<<"$desired_upgrade_state")
verified_postgres_identity=
public_http_port=$(jq -er '
    [.services.proxy.ports[]?
     | select(.target == 80 and (.protocol // "tcp") == "tcp")
     | (.published | tostring)]
    | unique | select(length == 1) | .[0]
' <<<"$effective_compose_config") || fail \
    "effective Compose model must publish proxy port 80 on exactly one host port"
public_https_port=$(jq -er '
    [.services.proxy.ports[]?
     | select(.target == 443 and (.protocol // "tcp") == "tcp")
     | (.published | tostring)]
    | unique | select(length <= 1) | (.[0] // "")
' <<<"$effective_compose_config") || fail \
    "effective Compose model publishes proxy port 443 on multiple host ports"
if container_exists proxy; then
	devshard_v5_verify_public_ports \
		"$docker_bin" proxy "$public_http_port" "$public_https_port"
else
	warn "public proxy is absent; the committed Compose model will recreate it and Docker will enforce exclusive ownership of ports $public_http_port${public_https_port:+ and $public_https_port}"
fi

postgres_container=
postgres_target_dir=
if [[ $versiond_mode == ha && $GONKA_COMPOSE_POSTGRES_MODE == local ]]; then
    postgres_container=$("${compose[@]}" ps --all --quiet devshard-postgres)
    [[ -n $postgres_container ]] || fail \
        "cannot preflight PostgreSQL migration: the existing container is missing; use the detached-volume recovery procedure"
    postgres_target_dir=$GONKA_COMPOSE_POSTGRES_DATA_DIR
    env DOCKER_BIN="$docker_bin" \
        "$script_dir/devshard-postgres-migration-preflight.sh" \
        --source-container "$postgres_container" \
        --target-dir "$postgres_target_dir"
fi

echo "Devshard v5 release preflight passed"
if [[ $preflight_only == true ]]; then
    exit 0
fi

existing_proxy_component=$(
    "$docker_bin" inspect --format \
        '{{index .Config.Labels "ai.gonka.component"}}' proxy 2>/dev/null || true
)
if [[ $existing_proxy_component != proxy-router && \
	$committed_marker_loaded != true && $maintenance_ack != true ]]; then
    fail "the one-time v5 cutover restarts shared PostgreSQL when local HA storage is used and replaces the public listener, terminating existing ingress connections; schedule the maintenance window, run --preflight-only, then rerun with --acknowledge-maintenance"
fi
if [[ $existing_proxy_component == proxy-router ]]; then
    if [[ -f $upgrade_marker && \
        $(upgrade_marker_release) != "$release_id" ]]; then
        fail "router HA is active, but commit marker $upgrade_marker belongs to another release"
    fi
    if [[ ! -f $upgrade_marker ]]; then
        warn "router HA is active without its commit marker; reconciling the complete release state"
    elif ! jq -e --arg fingerprint "$desired_fingerprint" \
        '.schema == 1 and .fingerprint == $fingerprint' \
        "$upgrade_marker" >/dev/null 2>&1; then
        warn "the committed release state differs from the requested state; reconciling it"
    fi

    echo "The v5 router HA topology is active; converging every release service idempotently"
    if [[ $versiond_mode == ha && $GONKA_COMPOSE_POSTGRES_MODE == local ]]; then
        converge_release_service devshard-postgres
    fi
    if [[ $versiond_mode == ha ]]; then
        for service in versiond2 versiond; do
            converge_release_service "$service"
        done
    else
        converge_release_service versiond
    fi
    if [[ $edge_mode == multi ]]; then
        for service in edge-api2 edge-api3 edge-api; do
            converge_release_service "$service"
        done
    else
        converge_release_service edge-api
    fi
    "$enable_router_bin" \
        --versiond-mode "$versiond_mode" --edge-mode "$edge_mode" \
        "${GONKA_COMPOSE_FORWARD_ARGS[@]}" 8<&-
    verify_release_application_state || fail \
        "application state did not converge to $release_id"
    verify_release_ingress_state || fail \
        "ingress state did not converge to $release_id"
    write_upgrade_marker
    echo "Devshard v5 release state is converged"
    exit 0
fi

if [[ $versiond_mode == ha ]]; then
    GONKA_CONFIG_ENV=$config_env \
    VERSIOND_ROUTER_FRONT_NETWORK=$GONKA_COMPOSE_ROUTER_FRONT_NETWORK \
    VERSIOND_ROUTER_BACK_NETWORK=$GONKA_COMPOSE_ROUTER_BACK_NETWORK \
    VERSIOND_ROUTER_METRICS_NETWORK=$GONKA_COMPOSE_DEFAULT_NETWORK \
        "$fleet_bin" prepare-networks
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
declare -A rollback_service_was_running=()

foreground_pid=
active_service=
active_failure_strategy=
traffic_barrier_router=
traffic_barrier_env=
traffic_barrier_original_hosts=
traffic_barrier_entrypoint=
traffic_barrier_target=
last_captured_version_baseline=

run_interruptible() {
    local status=0

    # The parent keeps the deployment lock. Children must not inherit its file
    # description, otherwise a short-lived CLI that forks can retain the lock
    # after this process has handled a signal and exited.
    "$@" 8<&- &
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

capture_versiond_rollback_baseline() {
    local service=$1
    local container_id=$2
    local commit=${3:-true}
    local timeout deadline snapshot versions display

    timeout=$(service_startup_timeout "$service")
    deadline=$((SECONDS + timeout))
    echo "Waiting up to ${timeout}s for the existing $service baseline"
    while :; do
        if snapshot=$(versiond_health_snapshot "$container_id") &&
            jq -e '.settled and (.running | length > 0)' \
                <<<"$snapshot" >/dev/null; then
            versions=$(jq -ce '.running' <<<"$snapshot")
            if versiond_routes_are_available "$container_id" "$versions"; then
                last_captured_version_baseline=$versions
                if [[ $commit == true ]]; then
                    rollback_version_baselines[$service]=$versions
                fi
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
    local second_override=${1:-}
    local container_id first second versions display

    [[ -n ${rollback_images[versiond-router]-} ]] || return 0
    container_id=$("${compose[@]}" ps --all --quiet versiond-router)
    [[ -n $container_id ]] || fail \
        "cannot find versiond-router while capturing its rollback baseline"
    first=${rollback_version_baselines[versiond]-[]}
    second=${second_override:-${rollback_version_baselines[versiond2]-[]}}
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
    local container_id image_id rollback_image was_running

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
    was_running=$("$docker_bin" inspect --format '{{.State.Running}}' \
        "$container_id")
    case $was_running in
        true | false) ;;
        *) fail "cannot determine whether the current $service is running" ;;
    esac
    rollback_service_was_running[$service]=$was_running
    echo "Captured $service rollback image as $rollback_image"
    case $service in
        versiond | versiond2)
            if [[ $was_running == true ]]; then
                capture_versiond_rollback_baseline "$service" "$container_id"
            else
                echo "Preserving the stopped state of existing $service"
            fi
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

versiond_production_routes_are_available() {
    local service=$1
    local runtime router_id required

    [[ $versiond_mode == ha ]] || return 0
    case $service in
        versiond | versiond2) ;;
        *) return 0 ;;
    esac
    runtime=$(router_runtime versiond-router) || return 1
    [[ $runtime == haproxy ]] || return 0
    required=${rollback_version_baselines[$service]-}
    [[ -n $required ]] || return 1
    router_id=$("${compose[@]}" ps --all --quiet versiond-router)
    [[ -n $router_id ]] || return 1
    versiond_routes_are_available "$router_id" "$required"
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
            rollback_versiond_is_available \
                "$service" "$container_id" || return 1
            ;;
        edge-api | edge-api2 | edge-api3 | edge-api-router)
            rollback_edge_is_available "$container_id" || return 1
            ;;
        *)
            return 1
            ;;
    esac
    versiond_production_routes_are_available "$service"
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
    local container_id

    if [[ -z $rollback_image ]]; then
        warn "$service has no captured image; stopping it instead"
        stop_failed_service "$service"
        return 1
    fi

    if [[ ${rollback_service_was_running[$service]-true} == false ]]; then
        echo "Restoring stopped $service from $rollback_image" >&2
        if env "$image_variable=$rollback_image" \
            "${compose[@]}" up --no-start --no-deps --force-recreate \
                "$service"; then
            container_id=$("${compose[@]}" ps --all --quiet "$service")
            if [[ -n $container_id && \
                $("$docker_bin" inspect --format '{{.State.Running}}' \
                    "$container_id") == false ]]; then
                return 0
            fi
        fi
        warn "could not restore the stopped state of $service"
        stop_failed_service "$service" || true
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
            exec 8<&-
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

pull_services=(versiond edge-api)
if [[ $versiond_mode == ha ]]; then
    pull_services+=(versiond2 versiond-router)
    [[ $GONKA_COMPOSE_POSTGRES_MODE != local ]] || \
        pull_services+=(devshard-postgres)
fi
if [[ $edge_mode == multi ]]; then
    pull_services+=(edge-api2 edge-api3 edge-api-router)
fi
run_interruptible "${compose[@]}" pull "${pull_services[@]}"

if [[ $versiond_mode == ha && $GONKA_COMPOSE_POSTGRES_MODE == local ]]; then
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

fi

if [[ $versiond_mode == ha ]]; then
    # Remove the first replacement from the v4 nginx config before Docker gives
    # its new container the old DNS name. Once one v5 replica is ready, install
    # HAProxy so active checks protect the remaining replacement.
    begin_traffic_barrier \
        versiond-router VERSIOND_HOSTS versiond2 \
        /docker-entrypoint.d/40-render-versiond-upstream.sh
    replace_service versiond2 "$(service_startup_timeout versiond2)" rollback
    # Compose readiness and the route baseline are one replacement commit.
    # Keep compensation armed across the postcondition so a target that passed
    # its coarse healthcheck but cannot serve every route remains isolated.
    active_service=versiond2
    active_failure_strategy=rollback
    versiond2_container=$("${compose[@]}" ps --all --quiet versiond2)
    [[ -n $versiond2_container ]] || fail \
        "versiond2 disappeared after its successful replacement"
    capture_versiond_rollback_baseline \
        versiond2 "$versiond2_container" false
    versiond2_target_baseline=$last_captured_version_baseline
    restore_traffic_barrier || fail \
        "cannot restore the complete legacy versiond-router upstream"
    capture_versiond_router_rollback_baseline "$versiond2_target_baseline"
    # Commit only after the replacement and the production route union both
    # satisfy their postconditions. Until here rollback still uses the immutable
    # source baseline and restores the source running/stopped state.
    active_service=
    active_failure_strategy=
    rollback_version_baselines[versiond2]=$versiond2_target_baseline
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
case ${UPGRADE_ENABLE_ROUTER_HA:-true} in
    true | 1 | yes)
        run_interruptible "$enable_router_bin" \
            --versiond-mode "$versiond_mode" --edge-mode "$edge_mode" \
            "${GONKA_COMPOSE_FORWARD_ARGS[@]}"
        ;;
    false | 0 | no)
        warn "router HA cutover was skipped by UPGRADE_ENABLE_ROUTER_HA"
        ;;
    *) fail "UPGRADE_ENABLE_ROUTER_HA must be true or false" ;;
esac
if [[ $versiond_mode == ha ]]; then
    verify_shared_postgres_identity || fail \
        "versiond replicas did not converge on one PostgreSQL identity"
fi
write_upgrade_marker
echo "Devshard v5 upgrade completed"
