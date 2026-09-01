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
postgres_deployment_preflight_bin=${DEVSHARD_V5_POSTGRES_PREFLIGHT_BIN:-$script_dir/postgres-deployment-preflight.sh}
upgrade_marker=${DEVSHARD_V5_UPGRADE_MARKER:-$config_dir/.gonka-devshard-v5-upgrade-complete}
upgrade_journal=${DEVSHARD_V5_UPGRADE_JOURNAL:-$upgrade_marker.in-progress}
[[ $(dirname -- "$upgrade_marker") == "$(dirname -- "$upgrade_journal")" ]] || fail \
	"upgrade marker and journal must share one directory for atomic commit"
transaction_id=$operation_id
base_fingerprint=none
saved_desired_fingerprint=
saved_compose_config_sha=
saved_fleet_spec_sha=
saved_base_fingerprint=

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
# shellcheck source=deploy/join/deployment-lock.sh
source "$script_dir/deployment-lock.sh"
gonka_acquire_deployment_lock "$config_dir" || exit 1

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
        versiond-router) printf '60\n' ;;
        *) fail "internal error: no startup timeout for $1" ;;
    esac
}

rollback_timeout_for_service() {
    if [[ -n $rollback_verify_timeout_override ]]; then
        printf '%s\n' "$rollback_verify_timeout_override"
        return 0
    fi
    case $1 in
        devshard-postgres) printf '120\n' ;;
        versiond | versiond2 | versiond-router) printf '60\n' ;;
        *) printf '60\n' ;;
    esac
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
    if [[ $versiond_mode == ha ]]; then
        service_instances_match_release versiond2 "$DEVSHARD_V5_VERSIOND_IMAGE" || return 1
        verify_shared_postgres_identity || return 1
        if [[ $GONKA_COMPOSE_POSTGRES_MODE == local ]]; then
            service_instances_match_release devshard-postgres \
                "$DEVSHARD_V5_POSTGRES_IMAGE" || return 1
        fi
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
    if [[ -f $upgrade_journal ]] && jq -e 'type == "object"' \
		"$upgrade_journal" >/dev/null 2>&1; then
		committed=$(jq -r '.transaction.postgres_identity // ""' \
			"$upgrade_journal")
	fi
    if [[ -z $committed && -f $upgrade_marker ]] && jq -e 'type == "object"' \
        "$upgrade_marker" >/dev/null 2>&1; then
        committed=$(jq -r '.storage.postgres_identity // ""' "$upgrade_marker")
    fi
    if [[ -n $committed && $first != "$committed" ]]; then
        warn "PostgreSQL identity changed from committed $committed to $first"
        return 1
    fi
    verified_postgres_identity=$first
}

run_postgres_deployment_preflight() {
    local mode=$1
    local -a args=()

    [[ -x $postgres_deployment_preflight_bin ]] || fail \
        "PostgreSQL deployment preflight is not executable: $postgres_deployment_preflight_bin"
    case $mode in
        compose) args+=(--compose-only) ;;
        runtime) args+=(--runtime-contract-only) ;;
        live)
            [[ -z $verified_postgres_identity ]] || \
                args+=(--expected-identity "$verified_postgres_identity")
            ;;
        *) fail "internal error: unknown PostgreSQL preflight mode $mode" ;;
    esac
    env DOCKER_BIN="$docker_bin" "$postgres_deployment_preflight_bin" \
        "${args[@]}" -- "${GONKA_COMPOSE_COMMAND[@]:2}"
}

verify_release_ingress_state() {
    service_instances_match_release proxy "$DEVSHARD_V5_PROXY_ROUTER_IMAGE" || return 1
    service_instances_match_release proxy-policy "$DEVSHARD_V5_PROXY_POLICY_IMAGE" || return 1
    service_instances_match_release proxy-policy2 "$DEVSHARD_V5_PROXY_POLICY_IMAGE"
}

converge_release_service() {
    local service=$1

	verify_compose_model_unchanged
    "${compose[@]}" up -d --no-deps --wait \
        --wait-timeout "$(service_startup_timeout "$service")" \
        "$service"
}

current_compose_config_sha() {
	local rendered
	rendered=$("${GONKA_COMPOSE_COMMAND[@]}" config --format json) || return 1
	jq -Sc . <<<"$rendered" | sha256sum | awk '{print $1}'
}

router_fleet_spec_sha() {
	GONKA_CONFIG_ENV=$config_env \
	GONKA_COMPOSE_PROJECT=$GONKA_COMPOSE_PROJECT \
	VERSIOND_ROUTER_FRONT_NETWORK=$GONKA_COMPOSE_ROUTER_FRONT_NETWORK \
	VERSIOND_ROUTER_BACK_NETWORK=$GONKA_COMPOSE_ROUTER_BACK_NETWORK \
	VERSIOND_ROUTER_METRICS_NETWORK=$GONKA_COMPOSE_DEFAULT_NETWORK \
		"$fleet_bin" spec-hash
}

verify_router_fleet_spec() {
	local current
	[[ $versiond_mode == ha ]] || return 0
	current=$(router_fleet_spec_sha) || fail \
		"cannot compute the canonical router fleet specification"
	[[ $current == "$fleet_spec_sha" ]] || fail \
		"router fleet specification changed during the upgrade transaction ($fleet_spec_sha -> $current)"
}

verify_compose_model_unchanged() {
	local current
	current=$(current_compose_config_sha) || fail \
		"effective Compose model no longer renders"
	[[ $current == "$compose_config_sha" ]] || fail \
		"effective Compose model changed during this upgrade; restore the recorded files and rerun"
}

write_upgrade_journal() {
    local phase=$1 existing_transaction='{}' journal
    case $phase in
        prepared | applications_verified | ingress_verified) ;;
        *) fail "internal error: invalid upgrade phase $phase" ;;
    esac
    if [[ -f $upgrade_journal ]]; then
		existing_transaction=$(jq -c '.transaction // {}' "$upgrade_journal") || fail \
			"cannot preserve active transaction metadata"
	fi
	journal=$(jq -c \
		--arg id "$transaction_id" --arg phase "$phase" \
		--arg base "$base_fingerprint" \
		--arg desired "$desired_fingerprint" \
		--arg compose_sha "$compose_config_sha" \
		--arg fleet_spec_sha "$fleet_spec_sha" \
		--arg postgres_identity "$verified_postgres_identity" \
		--argjson existing "$existing_transaction" '
		. + {transaction: ($existing + {
			id: $id,
			phase: $phase,
			base_fingerprint: $base,
			desired_fingerprint: $desired,
			compose_config_sha256: $compose_sha,
			fleet_spec_sha256: $fleet_spec_sha,
			updated_at_unix: (now | floor)
		})}
		| if $postgres_identity == "" then .
		  else .transaction.postgres_identity = $postgres_identity end
	' <<<"$desired_upgrade_marker") || fail "cannot encode upgrade journal"
	atomic_write_upgrade_state "$upgrade_journal" "$journal"
}

atomic_write_upgrade_state() {
	local path=$1 payload=$2 directory tmp
	directory=$(dirname -- "$path")
	mkdir -p "$directory"
	tmp=$(mktemp "$directory/.gonka-upgrade-state.XXXXXX")
	printf '%s\n' "$payload" >"$tmp"
	chmod 600 "$tmp"
	sync -f "$tmp"
	mv -f "$tmp" "$path"
	sync -f "$directory"
}

write_upgrade_marker() {
    local marker directory
	[[ -f $upgrade_journal ]] || fail \
		"active upgrade journal disappeared before commit"
    if [[ $versiond_mode == ha ]]; then
        [[ -n $verified_postgres_identity ]] || verify_shared_postgres_identity
		marker=$(jq -c --arg identity "$verified_postgres_identity" '
			.storage = {postgres_identity: $identity}
		' "$upgrade_journal") || fail "cannot finalize PostgreSQL identity"
		atomic_write_upgrade_state "$upgrade_journal" "$marker"
    fi
	jq -e '
		.transaction.phase == "ingress_verified" and
		((.transaction.ingress.rollback_models? // null) == null)
	' "$upgrade_journal" >/dev/null || fail \
		"upgrade journal is not safe to commit"
	directory=$(dirname -- "$upgrade_marker")
	mv -f "$upgrade_journal" "$upgrade_marker"
	sync -f "$directory"
}

upgrade_state_release() {
    local state_file=$1
    if jq -e 'type == "object"' "$state_file" >/dev/null 2>&1; then
        jq -er '.release_id | strings | select(length > 0)' "$state_file"
        return
    fi
    head -n1 "$state_file"
}

upgrade_marker_release() {
    upgrade_state_release "$upgrade_marker"
}

restore_saved_topology() {
	local state_file='' state_release state_versiond_mode state_edge_mode

    # An active transaction always outranks the last committed marker. Falling
    # back to the marker here would silently forget a partially applied newer
    # desired state after a crash.
    if [[ -f $upgrade_journal ]]; then
        jq -e '
            type == "object" and .schema == 2 and
            (.transaction.phase == "prepared" or
             .transaction.phase == "applications_verified" or
             .transaction.phase == "ingress_verified") and
			(.transaction.id | type == "string" and length > 0) and
			(.transaction.base_fingerprint | type == "string" and length > 0) and
			(.transaction.desired_fingerprint | type == "string" and length == 64) and
			(.transaction.compose_config_sha256 | type == "string" and length == 64) and
			(.transaction.fleet_spec_sha256 | type == "string") and
			((.topology.versiond == "ha" and
			  (.transaction.fleet_spec_sha256 | length) == 64) or
			 (.topology.versiond == "single" and
			  .transaction.fleet_spec_sha256 == "none")) and
            (.topology.versiond == "single" or .topology.versiond == "ha") and
            (.topology.edge_api == "single" or .topology.edge_api == "multi") and
            (.compose.files | type == "array" and length > 0) and
            (.compose.project | type == "string" and length > 0) and
            (.compose.project_directory | type == "string" and length > 0)
        ' "$upgrade_journal" >/dev/null 2>&1 || fail \
            "invalid interrupted-upgrade journal $upgrade_journal"
        state_release=$(upgrade_state_release "$upgrade_journal") || fail \
            "cannot read release identity from $upgrade_journal"
        [[ $state_release == "$release_id" ]] || fail \
            "unfinished upgrade journal $upgrade_journal belongs to $state_release, not $release_id"
        state_file=$upgrade_journal
        interrupted_upgrade_loaded=true
		transaction_id=$(jq -er '.transaction.id' "$state_file")
		operation_id=$transaction_id
		saved_base_fingerprint=$(jq -er '.transaction.base_fingerprint' "$state_file")
		saved_desired_fingerprint=$(jq -er '.transaction.desired_fingerprint' "$state_file")
		saved_compose_config_sha=$(jq -er '.transaction.compose_config_sha256' "$state_file")
		saved_fleet_spec_sha=$(jq -er '.transaction.fleet_spec_sha256' "$state_file")
    elif [[ -f $upgrade_marker ]] && jq -e '
		type == "object" and (.schema == 1 or .schema == 2) and
		(.topology.versiond == "single" or .topology.versiond == "ha") and
		(.topology.edge_api == "single" or .topology.edge_api == "multi") and
		(.compose.files | type == "array" and length > 0) and
        (.compose.project | type == "string" and length > 0) and
        (.compose.project_directory | type == "string" and length > 0)
	' "$upgrade_marker" >/dev/null 2>&1 &&
        [[ $(upgrade_marker_release) == "$release_id" ]]; then
        state_file=$upgrade_marker
        committed_marker_loaded=true
    else
        return 0
    fi

	state_versiond_mode=$(jq -er '.topology.versiond' "$state_file")
	state_edge_mode=$(jq -er '.topology.edge_api' "$state_file")
	if [[ $versiond_mode == auto ]]; then
		versiond_mode=$state_versiond_mode
	fi
	if [[ $edge_mode == auto ]]; then
		edge_mode=$state_edge_mode
	fi

    if ((${#compose_file_args[@]} == 0)) && [[ -z ${COMPOSE_FILE-} ]]; then
        mapfile -t compose_file_args < <(jq -er '.compose.files[]' "$state_file")
        export GONKA_COMPOSE_USE_COMMITTED_TOPOLOGY=true
        if [[ $committed_marker_loaded == true ]]; then
            echo "Using the committed Compose topology from $state_file"
        else
            echo "Resuming the saved Compose topology from $state_file"
        fi
    fi
    [[ -n $compose_project_name ]] || \
        compose_project_name=$(jq -er '.compose.project' "$state_file")
	[[ -n $compose_project_directory ]] || \
		compose_project_directory=$(jq -er \
			'.compose.project_directory' "$state_file")
}

committed_marker_loaded=false
interrupted_upgrade_loaded=false
restore_saved_topology

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

# Pin the services owned by this operation to the exact release. Edge API is
# intentionally left on its current image and topology for the 0.2.15 update.
export VERSIOND_IMAGE=$DEVSHARD_V5_VERSIOND_IMAGE
export VERSIOND_ROUTER_IMAGE=$DEVSHARD_V5_VERSIOND_ROUTER_IMAGE
export PROXY_POLICY_IMAGE=$DEVSHARD_V5_PROXY_POLICY_IMAGE
export PROXY_ROUTER_IMAGE=$DEVSHARD_V5_PROXY_ROUTER_IMAGE
export DEVSHARD_POSTGRES_IMAGE=$DEVSHARD_V5_POSTGRES_IMAGE

runtime_compose_containers=(proxy versiond edge-api)
for container in proxy-policy proxy-policy2 versiond2 versiond-router devshard-postgres \
    edge-api2 edge-api3 edge-api-router; do
    container_exists "$container" && runtime_compose_containers+=("$container")
done
gonka_compose_resolve \
    "$docker_bin" "$script_dir" "$versiond_mode" "$edge_mode" \
    "$compose_project_name" "$compose_project_directory" \
    compose_file_args runtime_compose_containers

# Managed PostgreSQL is inferred from the effective, operator-supplied model.
# Add the no-local-DB projection automatically and persist it in the committed
# Compose file list, so the host does not need another upgrade option or manual
# edit to avoid starting the bundled database later.
if [[ $versiond_mode == ha && $GONKA_COMPOSE_POSTGRES_MODE == external ]]; then
	external_postgres_overlay=$script_dir/docker-compose.versiond-external-postgres.yml
	if [[ " ${GONKA_COMPOSE_FILES[*]} " != *" $external_postgres_overlay "* ]]; then
		GONKA_COMPOSE_FILES+=("$external_postgres_overlay")
		GONKA_COMPOSE_COMMAND+=(-f "$external_postgres_overlay")
		GONKA_COMPOSE_FORWARD_ARGS+=(--compose-file "$external_postgres_overlay")
	fi
fi
compose=("${GONKA_COMPOSE_COMMAND[@]}")
if [[ $versiond_mode == ha ]]; then
    compose+=(-f "$script_dir/docker-compose.versiond-v5-compat.yml")
fi
# An ingress transaction may have been interrupted after the public listener
# changed. Recover it as soon as the saved topology can be rendered, before
# capacity checks, PostgreSQL preflight, image pulls, or application startup.
# The ingress journal carries exact rollback models, so recovery does not wait
# for the slow forward path to become healthy again.
if [[ -f $upgrade_journal ]] && jq -e \
	'.transaction.ingress.state == "active" or .transaction.ingress.state == "prepared"' "$upgrade_journal" \
	>/dev/null 2>&1; then
	[[ $preflight_only == false ]] || fail \
		"an interrupted ingress transaction requires a normal run for automatic rollback; --preflight-only never mutates the deployment"
	echo "Recovering interrupted ingress transaction before upgrade preflight"
	ROUTER_HA_TRANSACTION_JOURNAL=$upgrade_journal \
	ROUTER_HA_TRANSACTION_ID=$transaction_id \
	"$enable_router_bin" --recover-only \
		--versiond-mode "$versiond_mode" --edge-mode "$edge_mode" \
		"${GONKA_COMPOSE_FORWARD_ARGS[@]}"
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
fleet_spec_sha=none
fleet_spec_expectation=
if [[ $versiond_mode == ha ]]; then
	fleet_spec_sha=$(router_fleet_spec_sha) || fail \
		"cannot compute the canonical router fleet specification"
	[[ $fleet_spec_sha =~ ^[0-9a-f]{64}$ ]] || fail \
		"router fleet returned an invalid specification fingerprint"
	fleet_spec_expectation=$fleet_spec_sha
fi
desired_upgrade_state=$(jq -cn \
    --arg release_id "$release_id" \
    --arg release_commit "$DEVSHARD_V5_RELEASE_COMMIT" \
    --arg versiond_mode "$versiond_mode" \
    --arg edge_mode "$edge_mode" \
    --arg project "$GONKA_COMPOSE_PROJECT" \
    --arg project_directory "$GONKA_COMPOSE_PROJECT_DIRECTORY" \
    --arg config_sha256 "$compose_config_sha" \
    --arg fleet_spec_sha256 "$fleet_spec_sha" \
    --argjson files "$compose_files_json" \
    --arg versiond "$DEVSHARD_V5_VERSIOND_IMAGE" \
    --arg versiond_router "$DEVSHARD_V5_VERSIOND_ROUTER_IMAGE" \
    --arg proxy_policy "$DEVSHARD_V5_PROXY_POLICY_IMAGE" \
    --arg proxy_router "$DEVSHARD_V5_PROXY_ROUTER_IMAGE" \
    --arg postgres "$DEVSHARD_V5_POSTGRES_IMAGE" \
    '{
        schema: 2,
        release_id: $release_id,
        release_commit: $release_commit,
        topology: {versiond: $versiond_mode, edge_api: $edge_mode},
        compose: {
            project: $project,
            project_directory: $project_directory,
            files: $files,
            config_sha256: $config_sha256
        },
        router_fleet: {spec_sha256: $fleet_spec_sha256},
        images: {
            versiond: $versiond,
            versiond_router: $versiond_router,
            proxy_policy: $proxy_policy,
            proxy_router: $proxy_router,
            postgres: $postgres
        }
    }')
desired_fingerprint=$(
    printf '%s' "$desired_upgrade_state" | sha256sum | awk '{print $1}'
)
desired_upgrade_marker=$(jq -c --arg fingerprint "$desired_fingerprint" \
    '. + {fingerprint: $fingerprint}' <<<"$desired_upgrade_state")
verified_postgres_identity=
verified_postgres_identity_committed=false
if [[ $interrupted_upgrade_loaded == true ]]; then
	verified_postgres_identity=$(jq -r \
		'.transaction.postgres_identity // ""' "$upgrade_journal")
elif [[ -f $upgrade_marker ]] && jq -e 'type == "object"' \
	"$upgrade_marker" >/dev/null 2>&1; then
	verified_postgres_identity=$(jq -r \
		'.storage.postgres_identity // ""' "$upgrade_marker")
fi
[[ -z $verified_postgres_identity ]] || verified_postgres_identity_committed=true
current_base_fingerprint=none
if [[ -f $upgrade_marker ]]; then
	current_base_fingerprint=$(jq -er \
		'.fingerprint | strings | select(length == 64)' \
		"$upgrade_marker" 2>/dev/null || printf 'none\n')
fi
if [[ $interrupted_upgrade_loaded == true ]]; then
	[[ $saved_base_fingerprint == "$current_base_fingerprint" ]] || fail \
		"active transaction $transaction_id was based on $saved_base_fingerprint, but committed base is now $current_base_fingerprint; explicit recovery is required"
	[[ $saved_compose_config_sha == "$compose_config_sha" ]] || fail \
		"effective Compose model changed during active transaction $transaction_id; restore the saved override files before resuming"
	[[ $saved_fleet_spec_sha == "$fleet_spec_sha" ]] || fail \
		"router fleet specification changed during active transaction $transaction_id; restore the saved fleet configuration before resuming"
	[[ $saved_desired_fingerprint == "$desired_fingerprint" ]] || fail \
		"active transaction $transaction_id targets a different immutable release state; restore the saved Compose inputs and rerun the transaction (the journal is retained because discarding partially applied state is unsafe)"
	base_fingerprint=$saved_base_fingerprint
else
	base_fingerprint=$current_base_fingerprint
fi

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
if [[ $versiond_mode == ha ]]; then
    run_postgres_deployment_preflight compose
    # Credential and endpoint drift must be rejected while every current
    # generation and the original database are still untouched. In
    # particular, Compose cannot rotate an already initialized POSTGRES_PASSWORD.
    run_postgres_deployment_preflight runtime
fi
if [[ $versiond_mode == ha && $GONKA_COMPOSE_POSTGRES_MODE == local ]]; then
    postgres_container=$("${compose[@]}" ps --all --quiet devshard-postgres)
    postgres_target_dir=$GONKA_COMPOSE_POSTGRES_DATA_DIR
    if [[ -n $postgres_container && \
        $("$docker_bin" inspect --format '{{.State.Running}}' "$postgres_container") == true ]]; then
        env DOCKER_BIN="$docker_bin" \
            "$script_dir/devshard-postgres-migration-preflight.sh" \
            --source-container "$postgres_container" \
            --target-dir "$postgres_target_dir"
    elif [[ -n $postgres_container ]]; then
        postgres_source_volume=$("$docker_bin" inspect --format \
            '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}' \
            "$postgres_container") || fail \
            "cannot inspect stopped devshard-postgres storage"
        [[ -n $postgres_source_volume ]] || fail \
            "stopped devshard-postgres does not expose a named legacy volume; start it for a bounded live migration proof"
        env DOCKER_BIN="$docker_bin" \
            "$script_dir/devshard-postgres-migration-preflight.sh" \
            --source-volume "$postgres_source_volume" \
            --target-dir "$postgres_target_dir"
    else
        warn "devshard-postgres container is absent; verifying the published persistent PGDATA lineage"
        env DOCKER_BIN="$docker_bin" \
            "$script_dir/devshard-postgres-migration-preflight.sh" \
            --target-dir "$postgres_target_dir"
    fi
fi
if [[ $versiond_mode == ha ]]; then
	gonka_compose_validate_ha_version_catalog "$docker_bin" versiond
	verify_router_fleet_spec
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
	$committed_marker_loaded != true && $interrupted_upgrade_loaded != true && \
    $maintenance_ack != true ]]; then
    fail "the one-time v5 cutover restarts shared PostgreSQL when local HA storage is used and replaces the public listener, terminating existing ingress connections; schedule the maintenance window, run --preflight-only, then rerun with --acknowledge-maintenance"
fi
if [[ $existing_proxy_component == proxy-router && -f $upgrade_marker && \
    $(upgrade_marker_release) != "$release_id" ]]; then
    fail "router HA is active, but commit marker $upgrade_marker belongs to another release"
fi
write_upgrade_journal prepared
day2_reconcile=false
if [[ $existing_proxy_component == proxy-router || \
    $committed_marker_loaded == true ]]; then
    if [[ ! -f $upgrade_marker ]]; then
        warn "resuming the saved release transaction and reconciling the complete router HA state"
    elif [[ $existing_proxy_component != proxy-router ]]; then
        warn "the committed public proxy is absent; rebuilding it through the router HA recovery path"
    elif ! jq -e --arg fingerprint "$desired_fingerprint" \
		'(.schema == 1 or .schema == 2) and .fingerprint == $fingerprint' \
        "$upgrade_marker" >/dev/null 2>&1; then
        warn "the committed release state differs from the requested state; reconciling it"
    fi

    day2_reconcile=true
fi

if [[ $versiond_mode == ha ]]; then
    GONKA_CONFIG_ENV=$config_env \
    GONKA_COMPOSE_PROJECT=$GONKA_COMPOSE_PROJECT \
    VERSIOND_ROUTER_FRONT_NETWORK=$GONKA_COMPOSE_ROUTER_FRONT_NETWORK \
    VERSIOND_ROUTER_BACK_NETWORK=$GONKA_COMPOSE_ROUTER_BACK_NETWORK \
    VERSIOND_ROUTER_METRICS_NETWORK=$GONKA_COMPOSE_DEFAULT_NETWORK \
        "$fleet_bin" prepare-networks
fi

declare -A rollback_images=()
declare -A rollback_version_baselines=()
declare -A rollback_service_was_running=()
declare -A rollback_service_was_absent=()
declare -A rollback_service_touched=()
declare -A rollback_runtime_models=()
declare -a rollback_touch_order=()

persist_application_rollback() {
    local service=$1
    local image=${rollback_images[$service]-}
    local running=${rollback_service_was_running[$service]-false}
    local absent=${rollback_service_was_absent[$service]-false}
    local touched=${rollback_service_touched[$service]-false}
    local baseline=${rollback_version_baselines[$service]-null}
    local runtime_model=${rollback_runtime_models[$service]-null}
    local updated touch_order

    touch_order=$(printf '%s\n' "${rollback_touch_order[@]}" | \
        jq -Rsc 'split("\n") | map(select(length > 0))')

    updated=$(jq -c \
        --arg service "$service" --arg image "$image" \
        --argjson running "$running" --argjson absent "$absent" \
        --argjson touched "$touched" --argjson baseline "$baseline" \
		--argjson runtime_model "$runtime_model" \
        --argjson touch_order "$touch_order" '
        .transaction.application_rollback.services[$service] = {
            image: $image,
            was_running: $running,
            was_absent: $absent,
            touched: $touched,
			version_baseline: $baseline,
			runtime_model: $runtime_model
        }
        | .transaction.application_rollback.touch_order = $touch_order
    ' "$upgrade_journal") || fail \
        "cannot persist the $service application rollback baseline"
    atomic_write_upgrade_state "$upgrade_journal" "$updated"
}

load_application_rollback() {
    local service=$1 record image running absent touched baseline runtime_model

    record=$(jq -cer --arg service "$service" '
        .transaction.application_rollback.services[$service]
        | select((.image | type == "string") and
                 ((.was_absent // false) | type == "boolean") and
                 ((.touched // false) | type == "boolean") and
                 (.was_running | type == "boolean") and
                 (.version_baseline == null or
                  (.version_baseline | type == "array")) and
				 (.runtime_model == null or (.runtime_model | type == "object")) and
                 ((.was_absent // false) or (.image | length > 0)))
    ' "$upgrade_journal" 2>/dev/null) || return 1
    if ((${#rollback_touch_order[@]} == 0)); then
        mapfile -t rollback_touch_order < <(jq -r \
            '.transaction.application_rollback.touch_order[]? // empty' \
            "$upgrade_journal")
    fi
    image=$(jq -r '.image' <<<"$record")
    absent=$(jq -r '.was_absent // false' <<<"$record")
    touched=$(jq -r '.touched // false' <<<"$record")
    if [[ $absent != true ]]; then
        "$docker_bin" image inspect "$image" >/dev/null 2>&1 || fail \
            "saved rollback image $image for $service is unavailable; restore it before resuming"
    fi
    running=$(jq -r '.was_running' <<<"$record")
    baseline=$(jq -c '.version_baseline' <<<"$record")
	runtime_model=$(jq -c '.runtime_model' <<<"$record")
    rollback_images[$service]=$image
    rollback_service_was_running[$service]=$running
    rollback_service_was_absent[$service]=$absent
    rollback_service_touched[$service]=$touched
    if [[ $touched == true && " ${rollback_touch_order[*]} " != *" $service "* ]]; then
        rollback_touch_order+=("$service")
    fi
    [[ $baseline == null ]] || rollback_version_baselines[$service]=$baseline
	[[ $runtime_model == null ]] || rollback_runtime_models[$service]=$runtime_model
    if [[ $absent == true ]]; then
        echo "Reusing durable absent rollback baseline for $service"
    else
        echo "Reusing durable $service rollback baseline $image"
    fi
}

clear_application_rollback_metadata() {
    local updated
    updated=$(jq -c 'del(.transaction.application_rollback)' \
        "$upgrade_journal") || fail \
        "cannot clear committed application rollback metadata"
    atomic_write_upgrade_state "$upgrade_journal" "$updated"
}

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

    # Mutation children inherit fd 9. If the parent is interrupted, the shared
    # deployment lock therefore remains held until the active child is reaped.
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
    persist_application_rollback versiond-router
    display=$(jq -c '.' <<<"$versions")
    echo "Captured versiond-router route baseline: $display"
}

capture_rollback_image() {
    local service=$1
    local container_id image_id rollback_image was_running

    if load_application_rollback "$service"; then
        return 0
    fi

    container_id=$("${compose[@]}" ps --all --quiet "$service")
    if [[ -z $container_id ]]; then
        warn "$service has no existing container; rollback will restore its absence"
        rollback_images[$service]=
        rollback_service_was_running[$service]=false
        rollback_service_was_absent[$service]=true
        rollback_service_touched[$service]=false
        persist_application_rollback "$service"
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
    rollback_service_was_absent[$service]=false
    rollback_service_touched[$service]=false
	rollback_runtime_models[$service]=$("$docker_bin" inspect "$container_id" | \
		jq -ce '.[0] | {
			name: (.Name | ltrimstr("/")), config: .Config,
			host_config: .HostConfig, mounts: (.Mounts // []),
			networks: .NetworkSettings.Networks
		}') || fail "cannot capture the exact runtime model for $service"
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
    persist_application_rollback "$service"
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
        devshard-postgres)
            [[ $("$docker_bin" inspect --format \
                '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' \
                "$container_id") == healthy ]]
            return
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
    local container_id

    if [[ ${rollback_service_was_absent[$service]-false} == true ]]; then
        echo "Restoring absent $service baseline" >&2
        "${compose[@]}" rm -f -s "$service" >/dev/null 2>&1 || {
            warn "could not remove failed $service replacement"
            return 1
        }
        return 0
    fi
    if [[ -z $rollback_image ]]; then
        warn "$service has no captured image; refusing an inexact rollback"
        return 1
    fi

    [[ -n ${rollback_runtime_models[$service]-} ]] || {
		warn "$service has no exact saved runtime model"
		return 1
	}

    echo "Restoring $service from $rollback_image" >&2
	if restore_runtime_container "$service" "$rollback_image"; then
		if [[ ${rollback_service_was_running[$service]-true} == false ]]; then
			return 0
		fi
        if wait_for_rollback_availability "$service"; then
            return 0
        fi
        container_id=$("${compose[@]}" ps --all --quiet "$service")
        if [[ -n $container_id && \
            $("$docker_bin" inspect --format '{{.State.Running}}' "$container_id") == true ]]; then
            warn "restored $service is running but its external dependency is not yet available; leaving restart-policy recovery armed"
            return 0
        fi
    fi

    warn "rollback of $service could not be restored as a running container"
    return 1
}

restore_runtime_container() {
	local service=$1 image=$2 model
	local name value key restart health_cmd network network_id current
	local -a create_args=(create) command_args=() connect_args=()
	model=${rollback_runtime_models[$service]}

	name=$(jq -er '.name' <<<"$model") || return 1
	current=$("${compose[@]}" ps --all --quiet "$service") || return 1
	[[ -z $current ]] || "$docker_bin" rm -f "$current" >/dev/null || return 1
	create_args+=(--name "$name" --network none)
	value=$(jq -r '.config.Hostname // ""' <<<"$model"); [[ -z $value ]] || create_args+=(--hostname "$value")
	value=$(jq -r '.config.User // ""' <<<"$model"); [[ -z $value ]] || create_args+=(--user "$value")
	value=$(jq -r '.config.WorkingDir // ""' <<<"$model"); [[ -z $value ]] || create_args+=(--workdir "$value")
	value=$(jq -r '.config.StopSignal // ""' <<<"$model"); [[ -z $value ]] || create_args+=(--stop-signal "$value")
	value=$(jq -r '.config.StopTimeout // .host_config.StopTimeout // 0' <<<"$model"); ((value == 0)) || create_args+=(--stop-timeout "$value")
	value=$(jq -r '.config.Entrypoint // [] | length' <<<"$model")
	((value <= 1)) || { warn "$service uses a multi-element entrypoint that Docker CLI cannot restore exactly"; return 1; }
	if ((value == 1)); then
		create_args+=(--entrypoint "$(jq -r '.config.Entrypoint[0]' <<<"$model")")
	fi
	while IFS= read -r value; do create_args+=(--env "$value"); done < <(jq -r '.config.Env[]?' <<<"$model")
	while IFS=$'\t' read -r key value; do create_args+=(--label "$key=$value"); done < <(
		jq -r '.config.Labels // {} | to_entries[] | [.key,.value] | @tsv' <<<"$model")
	restart=$(jq -r '.host_config.RestartPolicy.Name // "no"' <<<"$model")
	if [[ $restart == on-failure ]]; then
		value=$(jq -r '.host_config.RestartPolicy.MaximumRetryCount // 0' <<<"$model")
		((value == 0)) || restart="$restart:$value"
	fi
	create_args+=(--restart "$restart")
	[[ $(jq -r '.host_config.ReadonlyRootfs // false' <<<"$model") == false ]] || create_args+=(--read-only)
	[[ $(jq -r '.host_config.Privileged // false' <<<"$model") == false ]] || create_args+=(--privileged)
	while IFS= read -r value; do create_args+=(--cap-add "$value"); done < <(jq -r '.host_config.CapAdd[]?' <<<"$model")
	while IFS= read -r value; do create_args+=(--cap-drop "$value"); done < <(jq -r '.host_config.CapDrop[]?' <<<"$model")
	while IFS= read -r value; do create_args+=(--security-opt "$value"); done < <(jq -r '.host_config.SecurityOpt[]?' <<<"$model")
	while IFS= read -r value; do create_args+=(--mount "$value"); done < <(jq -r '
		.host_config.Mounts[]? |
		"type=" + .Type + ",src=" + .Source + ",dst=" + .Target +
		(if .ReadOnly then ",readonly" else "" end) +
		(if .BindOptions.Propagation? then ",bind-propagation=" + .BindOptions.Propagation else "" end)
	' <<<"$model")
	# HostConfig.Binds retains the original Compose source/destination/options
	# form, including the named volume identity.
	if ! jq -e '.host_config.Mounts | length > 0' <<<"$model" >/dev/null; then
		while IFS= read -r value; do create_args+=(--volume "$value"); done < <(jq -r '.host_config.Binds[]?' <<<"$model")
	fi
	# Some Docker versions omit both HostConfig mount representations. The
	# top-level inspect model still lets us restore the same named volume or
	# bind source without falling back to the new Compose definition.
	if ! jq -e '((.host_config.Mounts // []) | length > 0) or
		((.host_config.Binds // []) | length > 0)' <<<"$model" >/dev/null; then
		while IFS= read -r value; do create_args+=(--mount "$value"); done < <(jq -r '
			.mounts[]? |
			(if .Type == "tmpfs" then
				"type=tmpfs,dst=" + .Destination
			elif .Type == "volume" then
				"type=volume,src=" + .Name + ",dst=" + .Destination
			elif .Type == "bind" then
				"type=bind,src=" + .Source + ",dst=" + .Destination
			else error("unsupported rollback mount type: " + .Type)
			end) +
			(if .RW then "" else ",readonly" end) +
			(if .Type == "bind" and (.Propagation // "") != "" then
				",bind-propagation=" + .Propagation else "" end)
		' <<<"$model")
	fi
	if jq -e '.config.Healthcheck.Test? | length > 0' <<<"$model" >/dev/null; then
		health_cmd=$(jq -r '
			.config.Healthcheck.Test as $test |
			if $test[0] == "CMD-SHELL" then $test[1]
			else ($test[1:] | map(@sh) | join(" ")) end
		' <<<"$model")
		create_args+=(--health-cmd "$health_cmd")
		value=$(jq -r '.config.Healthcheck.Interval // 0' <<<"$model"); ((value == 0)) || create_args+=(--health-interval "${value}ns")
		value=$(jq -r '.config.Healthcheck.Timeout // 0' <<<"$model"); ((value == 0)) || create_args+=(--health-timeout "${value}ns")
		value=$(jq -r '.config.Healthcheck.StartPeriod // 0' <<<"$model"); ((value == 0)) || create_args+=(--health-start-period "${value}ns")
		value=$(jq -r '.config.Healthcheck.Retries // 0' <<<"$model"); ((value == 0)) || create_args+=(--health-retries "$value")
	fi
	mapfile -t command_args < <(jq -r '.config.Cmd[]?' <<<"$model")
	"$docker_bin" "${create_args[@]}" "$image" "${command_args[@]}" >/dev/null || return 1
	while IFS= read -r network; do
		connect_args=(network connect)
		while IFS= read -r value; do [[ -z $value ]] || connect_args+=(--alias "$value"); done < <(
			jq -r --arg network "$network" '.networks[$network].Aliases[]?' <<<"$model")
		network_id=$(jq -r --arg network "$network" '.networks[$network].NetworkID // ""' <<<"$model")
		[[ -z $network_id ]] || "$docker_bin" network inspect "$network_id" >/dev/null || return 1
		connect_args+=("$network" "$name")
		"$docker_bin" "${connect_args[@]}" >/dev/null || return 1
	done < <(jq -r '.networks | keys[]' <<<"$model")
	[[ ${rollback_service_was_running[$service]-true} == false ]] || "$docker_bin" start "$name" >/dev/null
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
    rollback_service_touched[$service]=false
    return "$status"
}

handle_signal() {
    local signal=$1
    local status=$2
    local interrupted_pid

    trap - HUP INT TERM
    warn "received $signal; aborting the active upgrade step"
    if [[ -n $foreground_pid ]]; then
        interrupted_pid=$foreground_pid
        kill -TERM "$interrupted_pid" 2>/dev/null || true
        # The child owns compensation for the operation it is executing. Let it
        # finish that rollback after the first signal; a second operator signal
        # is the explicit force-stop request.
        trap 'warn "received a second signal; force-stopping the active upgrade step"; kill -KILL "$interrupted_pid" 2>/dev/null || true' \
            HUP INT TERM
        wait "$interrupted_pid" 2>/dev/null || true
        trap - HUP INT TERM
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
	if [[ -f $upgrade_journal ]] && jq -e \
		'.transaction.ingress.state == "active" or .transaction.ingress.state == "prepared"' \
		"$upgrade_journal" >/dev/null 2>&1; then
		warn "rolling back the ingress transaction before application dependencies"
		if ! ROUTER_HA_TRANSACTION_JOURNAL=$upgrade_journal \
			ROUTER_HA_TRANSACTION_ID=$transaction_id \
			"$enable_router_bin" --recover-only; then
			compensation_ok=false
			warn "automatic ingress rollback failed; operator action is required"
		fi
	fi
    if [[ -n $active_service ]]; then
		if [[ $active_failure_strategy == stop ]]; then
			warn "stopping interrupted replacement of $active_service"
			stop_failed_service "$active_service" || compensation_ok=false
		fi
		active_service=
		active_failure_strategy=
    fi
    local service
	# Restore dependencies before their consumers. Application health is
	# allowed to converge later when an external PostgreSQL endpoint is merely
	# unavailable; restored containers are never manually stopped for that.
	for service in devshard-postgres versiond2 versiond versiond-router; do
        [[ ${rollback_service_touched[$service]-false} == true ]] || continue
        warn "rolling back completed replacement of $service"
        if ! rollback_service "$service"; then
            compensation_ok=false
            warn "automatic rollback of $service failed; operator action is required"
        fi
        rollback_service_touched[$service]=false
    done
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
	verify_compose_model_unchanged
	if ! service_needs_replacement "$service"; then
		echo "$service already matches the requested image and Compose contract"
		return 0
	fi
	echo "Replacing $service"
	if [[ ${rollback_service_touched[$service]-false} != true ]]; then
		rollback_touch_order+=("$service")
	fi
	rollback_service_touched[$service]=true
	persist_application_rollback "$service"
    active_service=$service
    active_failure_strategy=$failure_strategy
    if run_interruptible "${compose[@]}" \
        up -d --no-deps --force-recreate --wait \
        --wait-timeout "$wait_timeout" "$service"; then
		if [[ $versiond_mode == ha && \
			($service == versiond || $service == versiond2) ]]; then
			local observed_identity
			observed_identity=$(versiond_storage_identity "$service") || {
				warn "$service became ready but did not expose its PostgreSQL identity"
				return 1
			}
			if [[ -n $verified_postgres_identity && \
				$observed_identity != "$verified_postgres_identity" ]]; then
				if [[ $verified_postgres_identity_committed == true ]]; then
					warn "PostgreSQL identity changed from committed $verified_postgres_identity to $observed_identity through $service"
				else
					warn "$service uses PostgreSQL identity $observed_identity, expected $verified_postgres_identity"
				fi
				return 1
			fi
			verified_postgres_identity=$observed_identity
			# Persist the storage fence before another supervisor or ingress
			# resource can be replaced. A resumed transaction must prove the
			# same database identity before it can commit.
			write_upgrade_journal prepared
		fi
        active_service=
        active_failure_strategy=
        return 0
    fi
    warn "$service did not become ready; aborting the upgrade"
    return 1
}

service_needs_replacement() {
	local service=$1 container desired_ref desired_image actual_image desired_hash actual_hash state
	container=$("${compose[@]}" ps --all --quiet "$service") || return 0
	[[ -n $container ]] || return 0
	desired_ref=$(jq -er --arg service "$service" \
		'.services[$service].image' <<<"$effective_compose_config") || return 0
	desired_image=$("$docker_bin" image inspect --format '{{.Id}}' "$desired_ref" 2>/dev/null) || return 0
	read -r actual_image actual_hash state < <("$docker_bin" inspect --format \
		'{{.Image}} {{or (index .Config.Labels "com.docker.compose.config-hash") ""}} {{.State.Running}}/{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' \
		"$container") || return 0
	desired_hash=$("${compose[@]}" config --hash "$service" | \
		awk -v service="$service" '$1 == service {print $2; found=1} END {if (!found) exit 1}') || return 0
	[[ $actual_image != "$desired_image" || $actual_hash != "$desired_hash" || \
		$state != true/healthy ]]
}

cleanup_rollback_tags() {
    local service rollback_image

    for service in "${!rollback_images[@]}"; do
        rollback_image=${rollback_images[$service]}
        [[ -n $rollback_image ]] || continue
        if ! "$docker_bin" image rm "$rollback_image" >/dev/null; then
            warn "could not remove temporary image tag $rollback_image"
        fi
    done
}

trap 'handle_signal HUP 129' HUP
trap 'handle_signal INT 130' INT
trap 'handle_signal TERM 143' TERM
trap 'handle_exit $?' EXIT

services=(versiond)
if [[ $versiond_mode == ha ]]; then
    services+=(versiond2 versiond-router)
fi
if [[ $day2_reconcile == true && $versiond_mode == ha && \
    $GONKA_COMPOSE_POSTGRES_MODE == local ]]; then
    services=(devshard-postgres "${services[@]}")
fi
for service in "${services[@]}"; do
    capture_rollback_image "$service"
done

pull_services=(versiond)
if [[ $versiond_mode == ha ]]; then
    pull_services+=(versiond2 versiond-router)
    [[ $GONKA_COMPOSE_POSTGRES_MODE != local ]] || \
        pull_services+=(devshard-postgres)
fi
missing_image_services=()
for service in "${pull_services[@]}"; do
	desired_ref=$(jq -er --arg service "$service" \
		'.services[$service].image' <<<"$effective_compose_config") || fail \
		"cannot resolve the desired image for $service"
	"$docker_bin" image inspect "$desired_ref" >/dev/null 2>&1 || \
		missing_image_services+=("$service")
done
if ((${#missing_image_services[@]} > 0)); then
	run_interruptible "${compose[@]}" pull "${missing_image_services[@]}"
fi

if [[ $day2_reconcile == true ]]; then
    echo "The v5 router HA topology is active; reconciling it transactionally"
    if [[ $versiond_mode == ha && $GONKA_COMPOSE_POSTGRES_MODE == local ]]; then
        replace_service devshard-postgres \
            "$(service_startup_timeout devshard-postgres)" rollback
    fi
    if [[ $versiond_mode == ha ]]; then
        replace_service versiond2 "$(service_startup_timeout versiond2)" rollback
        replace_service versiond "$(service_startup_timeout versiond)" rollback
    else
        replace_service versiond "$(service_startup_timeout versiond)" rollback
    fi
    verify_release_application_state || fail \
        "application state did not converge to $release_id"
    if [[ $versiond_mode == ha ]]; then
        run_postgres_deployment_preflight live || fail \
            "live PostgreSQL deployment proof failed while application rollback baselines are retained"
    fi
    write_upgrade_journal applications_verified
    verify_compose_model_unchanged
    run_interruptible env \
        ROUTER_HA_TRANSACTION_JOURNAL="$upgrade_journal" \
        ROUTER_HA_TRANSACTION_ID="$transaction_id" \
		ROUTER_HA_DEFER_COMMIT=true \
        ROUTER_HA_EXPECTED_POSTGRES_IDENTITY="$verified_postgres_identity" \
        ROUTER_HA_EXPECTED_COMPOSE_SHA256="$compose_config_sha" \
        ROUTER_HA_EXPECTED_FLEET_SPEC_SHA256="$fleet_spec_expectation" \
        "$enable_router_bin" \
        --versiond-mode "$versiond_mode" --edge-mode "$edge_mode" \
        "${GONKA_COMPOSE_FORWARD_ARGS[@]}"
    verify_release_ingress_state || fail \
        "ingress state did not converge to $release_id"
    verify_router_fleet_spec
    write_upgrade_journal ingress_verified
    verify_compose_model_unchanged
	run_interruptible env \
		ROUTER_HA_TRANSACTION_JOURNAL="$upgrade_journal" \
		ROUTER_HA_TRANSACTION_ID="$transaction_id" \
		"$enable_router_bin" --finalize-transaction
    clear_application_rollback_metadata
    write_upgrade_marker
    cleanup_rollback_tags
    echo "Devshard v5 release state is converged"
    exit 0
fi

if [[ $versiond_mode == ha && $GONKA_COMPOSE_POSTGRES_MODE == local ]]; then
	verify_compose_model_unchanged
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

verify_release_application_state || fail \
    "application state did not converge to $release_id"
if [[ $versiond_mode == ha ]]; then
    run_postgres_deployment_preflight live || fail \
        "live PostgreSQL deployment proof failed while application rollback baselines are retained"
fi
write_upgrade_journal applications_verified
case ${UPGRADE_ENABLE_ROUTER_HA:-true} in
    true | 1 | yes)
		verify_compose_model_unchanged
		run_interruptible env \
			ROUTER_HA_TRANSACTION_JOURNAL="$upgrade_journal" \
			ROUTER_HA_TRANSACTION_ID="$transaction_id" \
			ROUTER_HA_DEFER_COMMIT=true \
			ROUTER_HA_EXPECTED_POSTGRES_IDENTITY="$verified_postgres_identity" \
			ROUTER_HA_EXPECTED_COMPOSE_SHA256="$compose_config_sha" \
			ROUTER_HA_EXPECTED_FLEET_SPEC_SHA256="$fleet_spec_expectation" \
			"$enable_router_bin" \
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
verify_release_ingress_state || fail \
    "ingress state did not converge to $release_id"
verify_router_fleet_spec
write_upgrade_journal ingress_verified
verify_compose_model_unchanged
run_interruptible env \
	ROUTER_HA_TRANSACTION_JOURNAL="$upgrade_journal" \
	ROUTER_HA_TRANSACTION_ID="$transaction_id" \
	"$enable_router_bin" --finalize-transaction
clear_application_rollback_metadata
write_upgrade_marker
cleanup_rollback_tags
echo "Devshard v5 upgrade completed"
