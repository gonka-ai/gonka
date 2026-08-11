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
rollback_image_ref=
rollback_config_hash=
rollback_kind=
rollback_proxy_model=null
policy_rollback_pending=false
declare -A policy_rollback_images=()
declare -A policy_rollback_image_refs=()
declare -A policy_rollback_config_hashes=()
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
# shellcheck source=deploy/join/deployment-lock.sh
source "$script_dir/deployment-lock.sh"

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
transaction_journal=${ROUTER_HA_TRANSACTION_JOURNAL:-$config_dir/.gonka-router-ha-transaction.json}
transaction_id=${ROUTER_HA_TRANSACTION_ID:-router-ha-$(date +%s%N)-$$}

container_config_hash() {
	"$docker_bin" inspect --format \
		'{{index .Config.Labels "com.docker.compose.config-hash"}}' "$1"
}

compose_service_hash() {
	local service=$1
	shift
	"$@" config --hash "$service" | awk -v service="$service" \
		'$1 == service {print $2; found = 1} END {if (!found) exit 1}'
}

atomic_write_json() {
	local path=$1 payload=$2 directory tmp
	directory=$(dirname -- "$path")
	mkdir -p "$directory"
	tmp=$(mktemp "$directory/.gonka-transaction.XXXXXX")
	printf '%s\n' "$payload" >"$tmp"
	chmod 600 "$tmp"
	sync -f "$tmp"
	mv -f "$tmp" "$path"
	sync -f "$directory"
}

write_ingress_record() {
	local ingress=$1 base updated
	if [[ -f $transaction_journal ]]; then
		base=$(cat "$transaction_journal")
	else
		base=$(jq -cn --arg id "$transaction_id" \
			'{schema: 2, transaction: {id: $id, phase: "ingress"}}')
	fi
	updated=$(jq -c --arg id "$transaction_id" --argjson ingress "$ingress" '
		if type != "object" then error("transaction journal is not an object") else . end
		| .transaction = (.transaction // {id: $id, phase: "ingress"})
		| if .transaction.phase != "ingress" and .transaction.id != $id then
			error("ingress transaction id does not match the owning release transaction")
		  elif .transaction.phase == "ingress" then
			.transaction.id = $id
		  else . end
		| .transaction.ingress = $ingress
	' <<<"$base") || fail "cannot update ingress transaction journal"
	atomic_write_json "$transaction_journal" "$updated"
}

update_ingress_record() {
	local filter=$1 updated
	[[ -f $transaction_journal ]] || fail \
		"ingress transaction journal disappeared: $transaction_journal"
	updated=$(jq -c "$filter" "$transaction_journal") || fail \
		"cannot advance ingress transaction journal"
	atomic_write_json "$transaction_journal" "$updated"
}

record_ingress_touch() {
	local resource=$1 filter
	filter=$(jq -rn --arg resource "$resource" '
		".transaction.ingress.touched += [" + ($resource | @json) + "]"')
	update_ingress_record "$filter"
}

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

compose_model_service_hash() {
	local service=$1 model=$2 model_file hash status=0
	model_file=$(mktemp "$config_dir/.gonka-compose-model.XXXXXX.json")
	printf '%s\n' "$model" >"$model_file"
	chmod 600 "$model_file"
	hash=$("$docker_bin" compose \
		--project-name "$GONKA_COMPOSE_PROJECT" \
		--project-directory "$GONKA_COMPOSE_PROJECT_DIRECTORY" \
		-f "$model_file" config --hash "$service" | \
		awk -v service="$service" \
			'$1 == service {print $2; found = 1} END {if (!found) exit 1}') || status=$?
	rm -f "$model_file"
	((status == 0)) || return "$status"
	printf '%s\n' "$hash"
}

render_v4_proxy_model() {
	local image=$1 current_model compat_model extra_environment
	local versiond_service=versiond edge_service=''
	[[ $versiond_mode == ha ]] && versiond_service=versiond-router
	[[ $edge_mode == multi ]] && edge_service=edge-api-router

	current_model=$("${compose[@]}" --profile '*' config --format json) || return
	compat_model=$(PROXY_V4_IMAGE="$image" \
		PROXY_V4_VERSIOND_SERVICE_NAME="$versiond_service" \
		PROXY_V4_EDGE_API_SERVICE_NAME="$edge_service" \
		"${compose[@]}" -f "$script_dir/docker-compose.proxy-v4-compat.yml" \
		--profile '*' config --format json) || return
	# Preserve operator-owned additions such as observability routes. The listed
	# keys belong to the v5 HAProxy contract and never existed in the v4 nginx
	# service, so carrying them into rollback would create a hybrid generation.
	extra_environment=$(jq -c '
		.services.proxy.environment
		| del(
			.EDGE_API_POOL_HOST,
			.HAPROXY_DNS_RESOLVER,
			.PROXY_EDGE_API_PORT,
			.PROXY_POLICY_POOL_HOST,
			.PROXY_POLICY_POOL_SLOTS,
			.PROXY_ROUTER_CATALOG_BIND_HOST,
			.PROXY_ROUTER_CATALOG_PORT,
			.PROXY_ROUTER_CATALOG_UPSTREAM_HOST,
			.PROXY_ROUTER_CATALOG_UPSTREAM_PORT,
			.PROXY_ROUTER_METRICS_BIND_HOST,
			.PROXY_ROUTER_POLICY_BIND_HOST,
			.PROXY_ROUTER_PUBLIC_IDLE_SECONDS,
			.PROXY_ROUTER_VERSION_CAPACITY,
			.PROXY_VERSIOND_PORT,
			.VERSIOND_NON_HA_VERSIONS,
			.VERSIOND_ROUTER_ADMIN_PORT,
			.VERSIOND_ROUTER_FLEET_CAPACITY,
			.VERSIOND_ROUTER_POOL_HOST,
			.VERSIOND_ROUTER_PORT,
			.VERSIOND_ROUTING_ACTIVATION_MIN_READY,
			.VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS,
			.VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS,
			.VERSIOND_ROUTING_CATALOG_POLL_SECONDS,
			.VERSIOND_ROUTING_CATALOG_URL,
			.VERSIOND_VERSIONS
		)
	' <<<"$current_model") || return
	compat_model=$(jq -c --arg image "$image" \
		--argjson extra_environment "$extra_environment" '
		.services.proxy.image = $image
		| .services.proxy.environment += $extra_environment
	' <<<"$compat_model") || return
	if [[ $versiond_mode == single ]]; then
		compat_model=$(jq -c '
			del(.services.proxy.environment.VERSIOND_SERVICE_NAME)
			| del(.services.proxy.environment.VERSIOND_PORT)
		' <<<"$compat_model") || return
	fi
	printf '%s\n' "$compat_model"
}

arm_proxy_rollback() {
    local kind=$1 old_image old_ref actual_hash expected_hash

	if [[ $kind == absent ]]; then
		rollback_kind=$kind
		rollback_image=
		rollback_image_ref=
		rollback_config_hash=
		rollback_proxy_model=null
		return 0
	fi
    old_image=$("$docker_bin" inspect --format '{{.Image}}' proxy)
    old_ref=$("$docker_bin" inspect --format '{{.Config.Image}}' proxy)
    actual_hash=$(container_config_hash proxy) || fail \
		"cannot read the running proxy Compose config hash"
    [[ -n $actual_hash ]] || fail \
		"running proxy has no Compose config hash; exact automatic rollback is impossible"
	rollback_kind=$kind
	if [[ $kind == current ]]; then
		rollback_proxy_model=$(PROXY_ROUTER_IMAGE="$old_ref" \
			"${compose[@]}" --profile '*' config --format json) || fail \
			"cannot render the previous proxy service contract"
	else
		rollback_proxy_model=$(render_v4_proxy_model "$old_ref") || fail \
			"cannot render the previous v4 proxy service contract"
	fi
	expected_hash=$(compose_model_service_hash proxy "$rollback_proxy_model") || fail \
		"cannot hash the previous proxy service contract"
	[[ $expected_hash == "$actual_hash" ]] || fail \
		"running proxy config hash $actual_hash differs from rollback model $expected_hash; use an explicit maintenance migration"
	rollback_image="gonka/router-ha-proxy-rollback:$(date +%s%N)-$$"
	"$docker_bin" tag "$old_image" "$rollback_image"
	rollback_image_ref=$old_ref
	rollback_config_hash=$actual_hash
}

capture_policy_rollback() {
	local service id image image_ref rollback_tag first_image first_ref
	local actual_hash expected_hash
	local -a ids=()

	for service in "${policy_services[@]}"; do
		mapfile -t ids < <("${compose[@]}" ps --all --quiet "$service")
		policy_rollback_replicas[$service]=${#ids[@]}
		((${#ids[@]} > 0)) || continue
		first_image=
		first_ref=
		for id in "${ids[@]}"; do
			image=$("$docker_bin" inspect --format '{{.Image}}' "$id")
			image_ref=$("$docker_bin" inspect --format '{{.Config.Image}}' "$id")
			if [[ -n $first_image && $image != "$first_image" ]]; then
				fail "$service has mixed image generations; refusing an ambiguous rollback"
			fi
			if [[ -n $first_ref && $image_ref != "$first_ref" ]]; then
				fail "$service has mixed image references; refusing an ambiguous rollback"
			fi
			first_image=$image
			first_ref=$image_ref
		done
		actual_hash=$(container_config_hash "${ids[0]}") || fail \
			"cannot read $service Compose config hash"
		[[ -n $actual_hash ]] || fail \
			"$service has no Compose config hash; exact automatic rollback is impossible"
		expected_hash=$(PROXY_POLICY_IMAGE="$first_ref" \
			compose_service_hash "$service" "${compose[@]}") || fail \
			"cannot render the previous $service contract"
		[[ $expected_hash == "$actual_hash" ]] || fail \
			"running $service config hash $actual_hash differs from rollback model $expected_hash; use an explicit maintenance migration"
		rollback_tag="gonka/router-ha-policy-rollback:$service-$(date +%s%N)-$$"
		"$docker_bin" tag "$first_image" "$rollback_tag"
		policy_rollback_images[$service]=$rollback_tag
		policy_rollback_image_refs[$service]=$first_ref
		policy_rollback_config_hashes[$service]=$actual_hash
	done
}

begin_ingress_transaction() {
	local current_model policy_model proxy_model=null policies='{}' proxy
	local service replicas image image_ref config_hash desired_sha
	current_model=$("${compose[@]}" --profile '*' config --format json) || fail \
		"cannot render the immutable ingress transaction model"
	policy_model=$current_model
	for service in "${policy_services[@]}"; do
		replicas=${policy_rollback_replicas[$service]:-0}
		image=${policy_rollback_images[$service]-}
		image_ref=${policy_rollback_image_refs[$service]-}
		config_hash=${policy_rollback_config_hashes[$service]-}
		policies=$(jq -c \
			--arg service "$service" --argjson replicas "$replicas" \
			--arg image "$image" --arg image_ref "$image_ref" \
			--arg config_hash "$config_hash" \
			'. + {($service): {replicas: $replicas, rollback_image: $image,
				image_ref: $image_ref, config_hash: $config_hash}}' \
			<<<"$policies")
		if [[ -n $image ]]; then
			policy_model=$(jq -c --arg service "$service" --arg image "$image" \
				'.services[$service].image = $image' <<<"$policy_model")
		fi
	done

	proxy=$(jq -cn --arg kind "$rollback_kind" \
		--arg image "$rollback_image" --arg image_ref "$rollback_image_ref" \
		--arg config_hash "$rollback_config_hash" \
		'{kind: $kind, rollback_image: $image, image_ref: $image_ref,
		  config_hash: $config_hash}')
	case $rollback_kind in
		absent) ;;
		current | v4)
			proxy_model=$(jq -c --arg image "$rollback_image" \
				'.services.proxy.image = $image' <<<"$rollback_proxy_model") || fail \
				"cannot freeze the immutable proxy rollback model"
			;;
		*) fail "internal error: invalid proxy rollback kind $rollback_kind" ;;
	esac
	desired_sha=$(printf '%s' "$current_model" | sha256sum | awk '{print $1}')
	write_ingress_record "$(jq -cn \
		--arg id "$transaction_id" --arg desired_sha "$desired_sha" \
		--argjson policies "$policies" --argjson proxy "$proxy" \
		--argjson policy_model "$policy_model" \
		--argjson proxy_model "$proxy_model" \
		'{schema: 1, id: $id, state: "active", desired_compose_sha256: $desired_sha,
		  touched: [], policies: $policies, proxy: $proxy,
		  rollback_models: {policy: $policy_model, proxy: $proxy_model}}')"
	rollback_pending=true
	policy_rollback_pending=true
	install_rollback_traps
}

write_journal_model() {
	local expression=$1 output=$2
	jq -c "$expression" "$transaction_journal" >"$output" || return 1
	chmod 600 "$output"
}

rollback_compose() {
	local model_expression=$1
	shift
	local model status=0
	model=$(mktemp "$config_dir/.gonka-rollback-model.XXXXXX.json")
	write_journal_model "$model_expression" "$model" || return 1
	"$docker_bin" compose \
		--project-name "$GONKA_COMPOSE_PROJECT" \
		--project-directory "$GONKA_COMPOSE_PROJECT_DIRECTORY" \
		-f "$model" "$@" || status=$?
	rm -f "$model"
	return "$status"
}

restore_policy_slot() {
	local service=$1 replicas
	replicas=$(jq -er --arg service "$service" \
		'.transaction.ingress.policies[$service].replicas' \
		"$transaction_journal") || return 1
	if ((replicas == 0)); then
		rollback_compose '.transaction.ingress.rollback_models.policy' \
			rm -s -f "$service" >/dev/null
		return
	fi
	rollback_compose '.transaction.ingress.rollback_models.policy' \
		up -d --no-deps --force-recreate --wait \
		--wait-timeout "$cutover_timeout" --scale "$service=$replicas" \
		"$service" || return 1
	if [[ $(proxy_component) == proxy-router ]]; then
		wait_policy_admission "$service"
	fi
}

restore_public_proxy() {
	local kind
	kind=$(jq -er '.transaction.ingress.proxy.kind' "$transaction_journal") || return 1
	if [[ $kind == absent ]]; then
		"$docker_bin" rm -f proxy >/dev/null 2>&1 || true
		return 0
	fi
	rollback_compose '.transaction.ingress.rollback_models.proxy' \
		up -d --no-deps --force-recreate --wait \
		--wait-timeout "$cutover_timeout" proxy || return 1
	if [[ $kind == current ]]; then
		[[ $versiond_mode != ha ]] || run_fleet verify-admission || return 1
		[[ $edge_mode != multi ]] || wait_component edge-api || return 1
	fi
}

cleanup_ingress_rollback_images() {
	local image
	[[ -f $transaction_journal ]] || return 0
	while IFS= read -r image; do
		[[ -n $image ]] || continue
		"$docker_bin" image rm "$image" >/dev/null 2>&1 || true
	done < <(jq -r '
		.transaction.ingress
		| [.proxy.rollback_image, (.policies[]?.rollback_image)]
		| .[] // empty
	' "$transaction_journal")
}

redact_ingress_rollback_models() {
	[[ -f $transaction_journal ]] || return 0
	update_ingress_record 'del(.transaction.ingress.rollback_models)'
}

rollback_ingress_transaction() {
	local resource restored=true
	local -a touched=()
	[[ -f $transaction_journal ]] || return 0
	[[ $(jq -r '.transaction.ingress.state // ""' "$transaction_journal") == active ]] || return 0
	mapfile -t touched < <(jq -r '.transaction.ingress.touched | reverse[]' \
		"$transaction_journal")
	for resource in "${touched[@]}"; do
		case $resource in
			proxy) restore_public_proxy || restored=false ;;
			policy:*) restore_policy_slot "${resource#policy:}" || restored=false ;;
			*) warn "unknown touched ingress resource '$resource'"; restored=false ;;
		esac
	done
	$restored || return 1
	update_ingress_record \
		'.transaction.ingress.state = "rolled_back" | .transaction.ingress.completed_at_unix = (now | floor)'
	cleanup_ingress_rollback_images
	redact_ingress_rollback_models
}

recover_interrupted_ingress() {
	[[ -f $transaction_journal ]] || return 0
	case $(jq -r '.transaction.ingress.state // ""' "$transaction_journal" 2>/dev/null) in
		active)
			warn "recovering interrupted ingress transaction from $transaction_journal"
			rollback_ingress_transaction || fail \
				"interrupted ingress rollback failed; journal retained at $transaction_journal"
			;;
		committed | rolled_back)
			cleanup_ingress_rollback_images
			if jq -e '.transaction.ingress.rollback_models? != null' \
				"$transaction_journal" >/dev/null 2>&1; then
				redact_ingress_rollback_models
			fi
			;;
	esac
}

commit_ingress_transaction() {
	update_ingress_record \
		'.transaction.ingress.state = "committed" | .transaction.ingress.completed_at_unix = (now | floor)'
	rollback_pending=false
	policy_rollback_pending=false
	trap - EXIT INT TERM HUP
	cleanup_ingress_rollback_images
	redact_ingress_rollback_models
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
		record_ingress_touch "policy:$service"
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
	local status=${1:-$?}
    trap - EXIT INT TERM HUP
	if [[ $rollback_pending == true || $policy_rollback_pending == true ]]; then
		warn "ingress update failed; restoring only resources recorded as touched"
		if rollback_ingress_transaction; then
			warn "the previous ingress generation was restored"
		else
			status=1
			warn "automatic ingress rollback failed; journal retained at $transaction_journal"
		fi
	fi
    exit "$status"
}

compose_project=$GONKA_COMPOSE_PROJECT
policy_network=$GONKA_COMPOSE_POLICY_NETWORK
gonka_acquire_deployment_lock "$config_dir" || exit 1
recover_interrupted_ingress

echo "Preparing router HA topology: versiond=$versiond_mode edge-api=$edge_mode"
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
begin_ingress_transaction

# The reserve slot is always reconciled first. The fixed service dependency
# preserves the same order for an ordinary `docker compose up`.
roll_policy_slots

# A repeated run is also a real update path. Keep both the exact previous proxy
# and the policy generation armed until the complete public path is admitted.
if [[ $current_proxy_component == proxy-router ]]; then
	record_ingress_touch proxy
    "${compose[@]}" up -d --no-deps --wait \
        --wait-timeout "$cutover_timeout" proxy
    [[ $versiond_mode != ha ]] || run_fleet verify-admission
    [[ $edge_mode != multi ]] || wait_component edge-api
	commit_ingress_transaction
    remove_migration_routers
    echo "Router HA topology is already active"
    exit 0
fi

echo "Switching the public listener to proxy-router"
record_ingress_touch proxy
if ! "${compose[@]}" up -d --no-deps --force-recreate --wait \
    --wait-timeout "$cutover_timeout" proxy; then
    fail "proxy-router did not become ready"
fi
[[ $versiond_mode != ha ]] || \
    run_fleet verify-admission "${migration_routes[@]}" || fail \
        "proxy-router did not admit the complete router fleet and migration route baseline"
[[ $edge_mode != multi ]] || wait_component edge-api || fail \
    "proxy-router cannot reach a ready edge-api"

commit_ingress_transaction

# These singleton migration bridges are outside the steady-state model. Remove
# them only after the new public path has passed all component checks.
remove_migration_routers

echo "Router HA cutover completed"
