#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
    echo "enable-router-ha_test: $*" >&2
    exit 1
}

cat >"$tmpdir/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail

printf 'docker' >>"$DOCKER_LOG"
printf ' %q' "$@" >>"$DOCKER_LOG"
printf '\n' >>"$DOCKER_LOG"
if [[ -n ${PROXY_POLICY_IMAGE-} ]]; then
    printf 'policy-image=%q\n' "$PROXY_POLICY_IMAGE" >>"$DOCKER_LOG"
fi

if [[ ${1:-} == image && ${2:-} == inspect ]]; then
	if [[ ${4:-} == '{{.Id}}' ]]; then
		case ${!#} in
			candidate-policy) printf 'sha256:candidate-policy\n' ;;
			candidate-proxy) printf 'sha256:candidate-proxy\n' ;;
			*) printf 'sha256:candidate-other\n' ;;
		esac
	elif [[ ${4:-} == *ai.gonka.catalog-cache-protocol-version* ]]; then
		case ${!#} in
			candidate-proxy) printf '%s\n' "${CANDIDATE_PROXY_CACHE_PROTOCOL-2}" ;;
			*) printf '%s\n' "${CANDIDATE_ROUTER_CACHE_PROTOCOL-2}" ;;
		esac
	else
		case ${!#} in
			candidate-policy) printf '%s\n' "${CANDIDATE_POLICY_CONTRACT:-1}" ;;
			candidate-proxy) printf '%s\n' "${CANDIDATE_PROXY_CONTRACT:-1}" ;;
			*) printf '1\n' ;;
		esac
	fi
    exit 0
fi

if [[ ${1:-} == inspect ]]; then
    if [[ ${2:-} == --format ]]; then
			if [[ ${FAIL_PROXY_ENV_INSPECT:-false} == true && \
				${3:-} == '{{range .Config.Env}}{{println .}}{{end}}' && \
				${4:-} == proxy ]]; then
				echo 'simulated proxy environment inspect failure' >&2
				exit 1
			fi
			case ${3:-} in
				'{{.State.Running}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}')
					service=
					case ${4:-} in
						cid-proxy-policy2*) service=proxy-policy2 ;;
						cid-proxy-policy*) service=proxy-policy ;;
					esac
					if [[ -f $STATE_DIR/stopped-${4:-unknown} || \
						(-n $service && -f $STATE_DIR/stopped-$service) ]]; then
						printf 'false none\n'
				else
					printf 'true healthy\n'
				fi
				;;
            '{{json .Config.Labels}}')
                jq -cn --arg workdir "$JOIN_DIR" \
                    --arg files "$JOIN_DIR/docker-compose.yml,$JOIN_DIR/docker-compose.versiond.yml,$JOIN_DIR/docker-compose.edge-api-multi.yml" \
                    '{"com.docker.compose.project":"gonka-test",
                      "com.docker.compose.project.config_files":$files,
                      "com.docker.compose.project.working_dir":$workdir}'
                ;;
            *ai.gonka.component*)
                [[ -f $STATE_DIR/current ]] && cat "$STATE_DIR/current"
                ;;
            *ai.gonka.proxy-policy-contract*)
                printf '%s\n' "${CURRENT_PROXY_CONTRACT:-1}"
                ;;
			*com.docker.compose.config-hash*)
				case ${4:-} in
					cid-proxy-policy2*) printf '%s\n' "${POLICY2_CONFIG_HASH:-hash-proxy-policy2}" ;;
					cid-proxy-policy*) printf '%s\n' "${POLICY_CONFIG_HASH:-hash-proxy-policy}" ;;
					proxy) printf '%s\n' "${PROXY_CONFIG_HASH:-hash-proxy}" ;;
				esac
				;;
			'{{.Image}}')
				case ${4:-} in
					cid-proxy-policy*-new) printf 'sha256:candidate-policy\n' ;;
					cid-proxy-policy*) printf 'sha256:old-policy\n' ;;
                    *) printf 'sha256:old-proxy\n' ;;
                esac
                ;;
			'{{.Id}}')
				case ${4:-} in
					proxy)
						if [[ -f $STATE_DIR/generation-proxy ]]; then
							printf 'cid-proxy-new\n'
						else
							printf 'cid-proxy\n'
						fi
						;;
				esac
				;;
			'{{.Config.Image}}')
				case ${4:-} in
					cid-proxy-policy*-new) printf 'candidate-policy\n' ;;
					cid-proxy-policy*) printf 'old-policy-ref\n' ;;
					*) printf 'old-proxy-ref\n' ;;
				esac
				;;
			*NetworkSettings.Networks*)
				case ${4:-} in
					cid-proxy-policy2-new) printf '172.30.0.22\n' ;;
					cid-proxy-policy-new | cid-proxy-policy-1-new | cid-proxy-policy-2-new)
						printf '172.30.0.21\n'
						;;
					cid-proxy-policy2*) printf '172.30.0.12\n' ;;
                    cid-proxy-policy*) printf '172.30.0.11\n' ;;
                esac
                ;;
            '{{range .Config.Env}}{{println .}}{{end}}')
                case ${4:-} in
                    proxy)
                        if [[ ${OMIT_NGINX_MODE:-false} != true ]]; then
                            printf '%s\n' \
                                "NGINX_MODE=${FAKE_NGINX_MODE:-http}"
                        fi
                        printf '%s\n' \
                            'PROXY_POLICY_POOL_SLOTS=7' \
                            'PROXY_ROUTER_PUBLIC_IDLE_SECONDS=86400' \
                            'HAPROXY_DNS_RESOLVER=127.0.0.11:53' \
                            'VERSIOND_ROUTER_POOL_HOST=versiond-router-fleet' \
                            'VERSIOND_ROUTER_FLEET_CAPACITY=16' \
                            'VERSIOND_NON_HA_VERSIONS=v1' \
                            'VERSIOND_VERSIONS=v4' \
                            'VERSIOND_ROUTING_CATALOG_URL=http://versiond-routing-oracle:9100/versions' \
                            'VERSIOND_ROUTING_CATALOG_POLL_SECONDS=5' \
                            'VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS=3' \
                            'VERSIOND_ROUTING_ACTIVATION_MIN_READY=2' \
                            'VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS=86400' \
                            'PROXY_ROUTER_VERSION_CAPACITY=32' \
                            'EDGE_API_POOL_HOST=edge-api-pool' \
                            'EDGE_API_PORT=18080'
                        ;;
                    versiond | versiond2)
                        printf 'PGHOST=devshard-postgres\nPGDATABASE=devshardd\nPGUSER=devshardd\nVERSIOND_ORACLE_URL=http://api:9100/versions\n'
                        ;;
                    versiond-router)
                        printf 'VERSIOND_NON_HA_VERSIONS=v1\nVERSIOND_VERSIONS=v4\n'
                        ;;
                esac
                ;;
        esac
        exit 0
    fi
    case ${2:-} in
        proxy)
			[[ ${PROXY_EXISTS:-true} == true || -f $STATE_DIR/generation-proxy ]]
            ;;
        versiond | versiond2 | devshard-postgres | versiond-router | \
        edge-api | edge-api2 | edge-api3 | edge-api-router)
            exit 0
            ;;
        *) exit 1 ;;
    esac
fi

if [[ ${1:-} == network ]]; then
	if [[ ${2:-} == ls ]]; then
		if [[ ${MISSING_NETWORKS:-false} != true || -f $STATE_DIR/network-gonka-proxy-policy-front ]]; then
			printf 'network-policy\n'
		fi
		exit 0
	fi
    if [[ ${2:-} == inspect ]]; then
        if [[ ${3:-} == --format ]]; then
            name=${5:-unknown}
            if [[ ${WRONG_NETWORK_OWNERSHIP:-false} == true ]]; then
                printf 'wrong-key|wrong-project\n'
            else
                printf 'proxy-policy-front|gonka-test\n'
            fi
            exit 0
        fi
        if [[ ${MISSING_NETWORKS:-false} == true ]]; then
            [[ -f $STATE_DIR/network-${3:-unknown} ]]
            exit
        fi
        exit 0
    fi
    if [[ ${2:-} == create ]]; then
        name=${!#}
        : >"$STATE_DIR/network-$name"
    fi
    exit 0
fi

if [[ ${1:-} == exec && ${2:-} == versiond-router && \
    ${3:-} == /bin/sh && ${4:-} == -ec && \
    ${7:-} == /usr/local/lib/router-runtime/catalog-status ]]; then
    if [[ ${MIGRATION_CATALOG_DIAGNOSTIC:-false} == true ]]; then
        printf 'present'
    else
        printf 'absent'
    fi
    exit 0
fi

if [[ ${1:-} == compose ]]; then
    compat=false
    action=
    service=
	config_hash=false
	model_file=
	previous=
    for arg in "$@"; do
        [[ $arg == *docker-compose.proxy-v4-compat.yml ]] && compat=true
		[[ $previous == -f ]] && model_file=$arg
		[[ $arg == --hash ]] && config_hash=true
		case $arg in config | pull | up | stop | ps | rm) action=$arg ;; esac
        case $arg in proxy | proxy-policy | proxy-policy2) service=$arg ;; esac
		previous=$arg
    done
    if [[ $action == config ]]; then
		if [[ $config_hash == true ]]; then
			printf '%s hash-%s\n' "$service" "$service"
			exit 0
		fi
        model=$(jq -cn --arg join "$JOIN_DIR" '{name:"gonka-test",networks:{
            default:{name:"gonka-test_default"},
            "proxy-policy-front":{name:"gonka-proxy-policy-front"},
            "versiond-router-front":{name:"gonka-versiond-router-front"},
            "versiond-router-back":{name:"gonka-versiond-router-back"}
        },services:{
            proxy:{container_name:"proxy",image:"candidate-proxy"},
            "proxy-policy":{image:"candidate-policy"},
            "proxy-policy2":{image:"candidate-policy"},
            versiond:{container_name:"versiond",environment:{PGHOST:"devshard-postgres",PGDATABASE:"devshardd",PGUSER:"devshardd",PGPORT:"5432",DEVSHARD_STORAGE_MODE:"postgres"}},
            versiond2:{container_name:"versiond2",environment:{PGHOST:"devshard-postgres",PGDATABASE:"devshardd",PGUSER:"devshardd",PGPORT:"5432",DEVSHARD_STORAGE_MODE:"postgres"}},
            "devshard-postgres":{container_name:"devshard-postgres",volumes:[{type:"bind",source:($join + "/devshards/postgres"),target:"/var/lib/postgresql/gonka"}]},
            "edge-api":{container_name:"edge-api"},
            "edge-api2":{container_name:"edge-api2"},
            "edge-api3":{container_name:"edge-api3"}
        }}')
        if [[ -f $STATE_DIR/model-drift ]]; then
            jq -c '.services.proxy.environment.TEST_MODEL_DRIFT = "true"' \
                <<<"$model"
        else
            printf '%s\n' "$model"
        fi
        exit 0
    fi
	if [[ $action == ps ]]; then
		if [[ $service == "${FAIL_COMPOSE_PS_SERVICE-}" ]]; then
			exit 1
		fi
		if [[ -f $STATE_DIR/present-$service ]]; then
			suffix=
			[[ ! -f $STATE_DIR/generation-$service ]] || suffix=-new
            if [[ $service == proxy-policy && \
                ${INITIAL_POLICY_A_REPLICAS:-1} == 2 ]]; then
				printf 'cid-%s-1%s\ncid-%s-2%s\n' \
					"$service" "$suffix" "$service" "$suffix"
            else
				printf 'cid-%s%s\n' "$service" "$suffix"
            fi
        fi
        exit 0
    fi
	if [[ $action == rm ]]; then
        rm -f "$STATE_DIR/present-$service"
		exit 0
	fi
	if [[ $action == stop && $service == proxy-policy* ]]; then
		if [[ -f $STATE_DIR/stopped-$service ]]; then
			: >"$STATE_DIR/stopped-twice-$service"
		fi
		: >"$STATE_DIR/stopped-$service"
		exit 0
	fi
	if [[ $action == up && $service == proxy-policy* ]]; then
		rm -f "$STATE_DIR/health-down-policy_http-$service" \
			"$STATE_DIR/health-down-policy_https-$service"
			if [[ -f $model_file && $model_file == *.gonka-rollback-model.* ]]; then
			printf 'policy-image=%s\n' \
				"$(jq -r --arg service "$service" '.services[$service].image' "$model_file")" \
				>>"$DOCKER_LOG"
				: >"$STATE_DIR/present-$service"
				rm -f "$STATE_DIR/generation-$service"
				rm -f "$STATE_DIR/stopped-$service"
				rm -f "$STATE_DIR/stopped-twice-$service"
			exit 0
		fi
		if [[ $service == "${KILL_POLICY_SERVICE-}" ]]; then
			kill -KILL "$PPID"
			exit 137
		fi
        if [[ $service == "${FAIL_POLICY_SERVICE-}" && \
            ${PROXY_POLICY_IMAGE-} != gonka/router-ha-policy-rollback:* ]]; then
			: >"$STATE_DIR/generation-$service"
            exit 1
        fi
		: >"$STATE_DIR/present-$service"
			: >"$STATE_DIR/generation-$service"
		rm -f "$STATE_DIR/stopped-$service"
		rm -f "$STATE_DIR/stopped-twice-$service"
        if [[ $service == "${DRIFT_AFTER_POLICY_SERVICE-}" ]]; then
            : >"$STATE_DIR/model-drift"
        fi
        exit 0
    fi
    if [[ $action == up && $service == proxy ]]; then
		if [[ -f $model_file && $model_file == *.gonka-rollback-model.* ]]; then
			printf 'rollback-model-image %s\n' \
				"$(jq -r '.services.proxy.image' "$model_file")" >>"$DOCKER_LOG"
			if jq -e '.services.proxy.environment.VERSIOND_SERVICE_NAME' \
				"$model_file" >/dev/null 2>&1; then
				printf 'proxy-policy\n' >"$STATE_DIR/current"
			else
				printf 'proxy-router\n' >"$STATE_DIR/current"
			fi
			rm -f "$STATE_DIR/generation-proxy"
			exit 0
		fi
		if [[ ${KILL_PROXY_BEFORE_MUTATION:-false} == true ]]; then
			kill -KILL "$PPID"
			exit 137
		fi
        if [[ $compat == true ]]; then
            printf 'rollback-versiond %s\n' \
                "${PROXY_V4_VERSIOND_SERVICE_NAME:-}" >>"$DOCKER_LOG"
            printf 'proxy-policy\n' >"$STATE_DIR/current"
            exit 0
        fi
        if [[ ${PROXY_ROUTER_IMAGE:-} == gonka/router-ha-proxy-rollback:* ]]; then
            printf 'rollback-current slots=%s\n' \
                "${PROXY_POLICY_POOL_SLOTS:-}" >>"$DOCKER_LOG"
            printf 'proxy-router\n' >"$STATE_DIR/current"
            exit 0
        fi
        if [[ ${SIGNAL_CUTOVER:-false} == true ]]; then
            kill -TERM "$PPID"
            sleep 0.1
            exit 0
        fi
        if [[ ${FAIL_CUTOVER:-false} == true ]]; then
			: >"$STATE_DIR/generation-proxy"
            exit 1
        fi
        printf 'proxy-router\n' >"$STATE_DIR/current"
		: >"$STATE_DIR/generation-proxy"
    fi
    exit 0
fi

if [[ ${1:-} == exec && ${2:-} == versiond-router && \
    ${3:-} == test && ${4:-} == -x && \
    ${5:-} == /usr/local/lib/router-runtime/catalog-status ]]; then
    # The migration singleton represents the pre-catalog image. The cutover
    # must retain its mixed-image fallback until that reversible path is gone.
    [[ ${MIGRATION_CATALOG_DIAGNOSTIC:-false} == true ]] && exit 0
    exit 1
fi

if [[ ${1:-} == exec && ${2:-} == versiond-router && \
    ${3:-} == /usr/local/lib/router-runtime/catalog-status ]]; then
    if [[ ${MIGRATION_CATALOG_HANG:-false} == true ]]; then
        sleep 300
    fi
    [[ ${MIGRATION_CATALOG_FAIL_MAP:-} != "${4:-}" ]] || exit 1
    case ${4:-} in
        /etc/haproxy/non_ha.map) printf 'v1\n' ;;
        /etc/haproxy/versions.map) printf 'v4\n' ;;
        *) exit 1 ;;
    esac
    exit 0
fi

if [[ $1 == exec && ($2 == versiond || $2 == versiond2) ]]; then
    last=
    for last; do :; done
    case $last in
        http://127.0.0.1:8080/internal/storage-identity)
            identity=$(printenv POSTGRES_IDENTITY || \
                printf '11111111-1111-1111-1111-111111111111\n')
            if [[ $2 == versiond2 ]] && identity2=$(printenv POSTGRES_IDENTITY2); then
                identity=$identity2
            fi
            printf '{"identity":"%s"}\n' "$identity"
            exit 0
            ;;
        http://api:9100/versions)
            if ! printenv VERSION_CATALOG_JSON; then
                printf '%s\n' '{"versions":[{"name":"v1"},{"name":"v4"}]}'
            fi
            exit 0
            ;;
    esac
fi

if [[ ${1:-} == exec && ${2:-} == proxy && \
    ${3:-} == /bin/sh && ${4:-} == -ec && \
    ${5:-} == *'set server '* ]]; then
	command=${5:-}
	for backend in policy_http policy_https; do
		for service in proxy-policy proxy-policy2; do
			ref=$backend/$service
			if [[ $backend == "${RUNTIME_FAIL_BACKEND-}" && \
				$service == "${RUNTIME_FAIL_SERVICE-}" && \
				$command == *"set server $ref ${RUNTIME_FAIL_COMMAND-}"* ]]; then
				exit 1
			fi
			if [[ $command == *"set server $ref state drain"* ]]; then
				printf 'runtime drain %s\n' "$ref" >>"$DOCKER_LOG"
				: >"$STATE_DIR/drained-$backend-$service"
			elif [[ $command == *"set server $ref health down"* ]]; then
				printf 'runtime down %s\n' "$ref" >>"$DOCKER_LOG"
				: >"$STATE_DIR/health-down-$backend-$service"
			elif [[ $command == *"set server $ref state ready"* ]]; then
				printf 'runtime ready %s\n' "$ref" >>"$DOCKER_LOG"
				rm -f "$STATE_DIR/drained-$backend-$service"
			fi
		done
	done
	exit 0
fi

if [[ ${1:-} == exec && ${2:-} == proxy && \
    ${3:-} == /bin/sh && ${4:-} == -ec && \
    ${5:-} == *'show stat'* ]]; then
	if [[ ${RUNTIME_HANG:-false} == true ]]; then
		sleep 300
	fi
	printf '# pxname,svname,status,check_status,check_rise,check_fall,check_health,addr\n'
	for service in proxy-policy proxy-policy2; do
		[[ -f $STATE_DIR/present-$service ]] || continue
		case $service in
			proxy-policy)
				old_address=172.30.0.11
				new_address=172.30.0.21
				;;
			proxy-policy2)
				old_address=172.30.0.12
				new_address=172.30.0.22
				;;
		esac
		address=$old_address
		[[ ! -f $STATE_DIR/generation-$service ]] || address=$new_address
		for backend in policy_http policy_https; do
			status=UP
			if [[ -f $STATE_DIR/drained-$backend-$service && \
				$service != "${WITHDRAW_FAIL_SERVICE-}" ]]; then
				status=DRAIN
			elif [[ -f $STATE_DIR/stopped-$service ]]; then
				status=DOWN
			fi
			check_status=L7OK
			check_health=3
			if [[ -f $STATE_DIR/health-down-$backend-$service && \
				-f $STATE_DIR/stopped-$service ]]; then
				check_status=L4CON
				check_health=0
			fi
			port=80
			[[ $backend == policy_http ]] || port=443
			printf '%s,%s,%s,%s,2,2,%s,%s:%s\n' "$backend" "$service" \
				"$status" "$check_status" "$check_health" "$address" "$port"
		done
	done
	exit 0
fi

case ${1:-} in
    exec | tag) exit 0 ;;
	rm)
		[[ ${3:-} != proxy ]] || rm -f "$STATE_DIR/generation-proxy"
		exit 0
		;;
    image) exit 0 ;;
esac
exit 0
EOF
chmod +x "$tmpdir/docker"

cat >"$tmpdir/fleet" <<'EOF'
#!/usr/bin/env bash
set -eu
printf 'fleet %s\n' "$*" >>"$DOCKER_LOG"
if [[ ${1:-} == spec-hash ]]; then
    if [[ -f $STATE_DIR/fleet-spec-drift ]]; then
        printf '%064d\n' 2
    else
        printf '%064d\n' 1
    fi
    exit 0
fi
if [[ ${1:-} == verify-admission && \
    ${FLEET_ADMISSION_FAIL:-false} == true ]]; then
    exit 1
fi
if [[ ${1:-} == apply && ${FLEET_COMPOSE_DRIFT:-false} == true ]]; then
	: >"$STATE_DIR/model-drift"
fi
if [[ ${1:-} == apply && ${FLEET_SPEC_DRIFT:-false} == true ]]; then
	: >"$STATE_DIR/fleet-spec-drift"
fi
EOF
chmod +x "$tmpdir/fleet"

cat >"$tmpdir/postgres-preflight" <<'EOF'
#!/usr/bin/env bash
set -eu
printf 'postgres-preflight %s\n' "$*" >>"$DOCKER_LOG"
[[ ${POSTGRES_PREFLIGHT_FAIL:-false} != true ]] || exit 1
	expected=
	while (($#)); do
		case $1 in
			--expected-identity)
				(($# >= 2)) && [[ -n $2 ]] || exit 2
				expected=$2
				shift 2
				;;
			--)
				shift
				(($# > 0)) || exit 2
				break
				;;
			*) exit 2 ;;
		esac
	done
first=${POSTGRES_IDENTITY:-11111111-1111-1111-1111-111111111111}
second=${POSTGRES_IDENTITY2:-$first}
[[ $first == "$second" ]] || exit 1
[[ -z $expected || $first == "$expected" ]] || exit 1
EOF
chmod +x "$tmpdir/postgres-preflight"

cat >"$tmpdir/config.env" <<EOF
ROUTER_HA_PULL_POLICY=missing
GONKA_DEPLOYMENT_LOCK=$tmpdir/deployment.lock
VERSIOND_NON_HA_VERSIONS=v1
VERSIOND_VERSIONS=v4
EOF

run_cutover() {
    local log=$1
    shift
    : >"$log"
    rm -f "$tmpdir/current"
    rm -f "$tmpdir/model-drift"
    rm -f "$tmpdir/fleet-spec-drift"
	rm -f "$tmpdir"/present-proxy-policy*
	rm -f "$tmpdir"/generation-proxy*
	rm -f "$tmpdir"/stopped-proxy-policy*
	rm -f "$tmpdir"/drained-policy_* "$tmpdir"/health-down-policy_*
    if [[ -n ${INITIAL_PROXY_COMPONENT:-} ]]; then
        printf '%s\n' "$INITIAL_PROXY_COMPONENT" >"$tmpdir/current"
    fi
	for service in ${INITIAL_POLICY_SERVICES-}; do
		: >"$tmpdir/present-$service"
		if [[ ${INITIAL_POLICY_GENERATION:-old} == candidate ]]; then
			: >"$tmpdir/generation-$service"
		fi
	done
    env DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        STATE_DIR="$tmpdir" \
        JOIN_DIR="$script_dir" \
        VERSIOND_ROUTER_FLEET_BIN="$tmpdir/fleet" \
        ROUTER_HA_POSTGRES_PREFLIGHT_BIN="$tmpdir/postgres-preflight" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        "$@" "$script_dir/enable-router-ha.sh" \
        --versiond-mode ha --edge-mode multi
}

run_cutover "$tmpdir/success.log" env MISSING_NETWORKS=true
preflight_line=$(grep -n '^postgres-preflight ' "$tmpdir/success.log" | head -n1 | cut -d: -f1)
apply_line=$(grep -n '^fleet apply$' "$tmpdir/success.log" | head -n1 | cut -d: -f1)
[[ -n $preflight_line && -n $apply_line && $preflight_line -lt $apply_line ]] || fail \
    "shared PostgreSQL was not verified before the first fleet mutation"
grep -q '^postgres-preflight -- ' "$tmpdir/success.log" || fail \
	"router cutover did not require live PostgreSQL identity proof"
grep -q '^fleet apply$' "$tmpdir/success.log" || fail \
    "router fleet was not reconciled through its update lifecycle"
grep -q '^fleet verify-admission v1 v4$' "$tmpdir/success.log" || fail \
    "cutover did not verify every route served by the migration singleton"
grep -q 'network create .*com.docker.compose.network=proxy-policy-front' \
    "$tmpdir/success.log" || fail \
    "missing private policy network was not created with Compose ownership"
grep -q 'network connect --alias proxy-policy-ingress' \
    "$tmpdir/success.log" || fail \
    "legacy public proxy was not attached to the private policy network"
policy_line=$(grep -n ' up .*proxy-policy$' "$tmpdir/success.log" | head -n1 | cut -d: -f1)
policy2_line=$(grep -n ' up .*proxy-policy2$' "$tmpdir/success.log" | head -n1 | cut -d: -f1)
proxy_line=$(grep -n ' up .*proxy$' "$tmpdir/success.log" | head -n1 | cut -d: -f1)
[[ -n $policy2_line && -n $policy_line && -n $proxy_line && \
    $policy2_line -lt $policy_line && $policy_line -lt $proxy_line ]] || fail \
    "policy slots were not rolled reserve-first before the public cutover"
grep -q 'docker rm -f versiond-router' "$tmpdir/success.log" || fail \
    "singleton versiond-router was not removed after commit"
if grep -q 'docker rm -f edge-api-router' "$tmpdir/success.log"; then
    fail "versiond router cutover removed the existing edge-api router"
fi
verify_line=$(grep -n '^fleet verify-admission ' "$tmpdir/success.log" | head -n1 | cut -d: -f1)
remove_line=$(grep -n 'docker rm -f versiond-router' "$tmpdir/success.log" | head -n1 | cut -d: -f1)
[[ -n $verify_line && -n $remove_line && $verify_line -lt $remove_line ]] || fail \
    "migration singleton was removed before fleet admission was committed"
jq -e '
    .transaction.ingress.state == "committed" and
    .transaction.ingress.fleet_spec_sha256 ==
        "0000000000000000000000000000000000000000000000000000000000000001" and
    .transaction.ingress.touched ==
        ["network:policy", "policy:proxy-policy2", "policy:proxy-policy", "proxy"] and
    .transaction.ingress.policies["proxy-policy"].container_ids == [] and
    .transaction.ingress.policies["proxy-policy2"].container_ids == [] and
    .transaction.ingress.proxy.container_id == "cid-proxy"
' "$tmpdir/.gonka-router-ha-transaction.json" >/dev/null || fail \
    "successful cutover did not persist its exact mutation order"
[[ $(stat -c '%a' "$tmpdir/.gonka-router-ha-transaction.json") == 600 ]] || fail \
    "ingress transaction journal is not private"

if run_cutover "$tmpdir/failure.log" env FAIL_CUTOVER=true; then
    fail "failed public proxy replacement was reported as successful"
fi
grep -q 'gonka-rollback-model\..* up .*proxy$' \
	"$tmpdir/failure.log" || fail \
    "failed cutover did not use the durable v4 rollback model"
grep -q ' up .*proxy$' "$tmpdir/failure.log" || fail \
    "failed cutover did not recreate the public nginx"

if run_cutover "$tmpdir/admission-failure.log" env FLEET_ADMISSION_FAIL=true; then
    fail "cutover committed while the parent did not admit the router fleet"
fi
grep -q '^fleet verify-admission v1 v4$' \
    "$tmpdir/admission-failure.log" || fail \
    "failed admission scenario did not reach the commit gate"
grep -q 'gonka-rollback-model\..* up .*proxy$' \
	"$tmpdir/admission-failure.log" || fail \
    "admission failure did not preserve the singleton-backed v4 rollback"
if grep -q 'docker rm -f versiond-router' "$tmpdir/admission-failure.log"; then
    fail "admission failure removed the upstream required by v4 rollback"
fi

set +e
run_cutover "$tmpdir/signal.log" env SIGNAL_CUTOVER=true
signal_status=$?
set -e
((signal_status != 0)) || fail \
    "TERM during cutover was reported as success"
jq -e '
    .transaction.ingress.state == "active" or
    .transaction.ingress.state == "rolled_back"
' "$tmpdir/.gonka-router-ha-transaction.json" >/dev/null || fail \
    "TERM did not retain a recoverable ingress transaction"
if grep -q 'gonka-rollback-model\..* up .*proxy$' "$tmpdir/signal.log"; then
	fail "TERM before Docker mutation unnecessarily recreated the public proxy"
fi

INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/idempotent.log" env
policy2_stop_line=$(grep -n ' stop .*proxy-policy2$' "$tmpdir/idempotent.log" | head -n1 | cut -d: -f1)
policy2_up_line=$(grep -n ' up .*proxy-policy2$' "$tmpdir/idempotent.log" | head -n1 | cut -d: -f1)
policy2_drain_line=$(grep -n '^runtime drain policy_http/proxy-policy2$' "$tmpdir/idempotent.log" | head -n1 | cut -d: -f1)
policy2_down_line=$(grep -n '^runtime down policy_http/proxy-policy2$' "$tmpdir/idempotent.log" | head -n1 | cut -d: -f1)
policy2_ready_line=$(grep -n '^runtime ready policy_http/proxy-policy2$' "$tmpdir/idempotent.log" | tail -n1 | cut -d: -f1)
policy_stop_line=$(grep -n ' stop .*proxy-policy$' "$tmpdir/idempotent.log" | head -n1 | cut -d: -f1)
policy_up_line=$(grep -n ' up .*proxy-policy$' "$tmpdir/idempotent.log" | head -n1 | cut -d: -f1)
policy_drain_line=$(grep -n '^runtime drain policy_http/proxy-policy$' "$tmpdir/idempotent.log" | head -n1 | cut -d: -f1)
policy_down_line=$(grep -n '^runtime down policy_http/proxy-policy$' "$tmpdir/idempotent.log" | head -n1 | cut -d: -f1)
policy_ready_line=$(grep -n '^runtime ready policy_http/proxy-policy$' "$tmpdir/idempotent.log" | tail -n1 | cut -d: -f1)
[[ -n $policy2_drain_line && -n $policy2_stop_line && -n $policy2_down_line && \
    -n $policy2_up_line && -n $policy2_ready_line && \
    $policy2_drain_line -lt $policy2_stop_line && \
    $policy2_stop_line -lt $policy2_down_line && \
    $policy2_down_line -lt $policy2_ready_line && \
    $policy2_ready_line -lt $policy2_up_line && \
    -n $policy_drain_line && -n $policy_stop_line && -n $policy_down_line && \
    -n $policy_up_line && -n $policy_ready_line && \
    $policy_drain_line -lt $policy_stop_line && \
    $policy_stop_line -lt $policy_down_line && \
    $policy_down_line -lt $policy_ready_line && \
    $policy_ready_line -lt $policy_up_line && \
    $policy2_up_line -lt $policy_drain_line ]] || fail \
    "day-2 policy replacements did not cross drain, stop, health, and admission stages reserve-first"
grep -q '^docker tag ' "$tmpdir/idempotent.log" || fail \
    "day-2 convergence did not arm public proxy rollback"
grep -q 'docker image rm gonka/router-ha-proxy-rollback:' \
    "$tmpdir/idempotent.log" || fail \
    "successful day-2 convergence retained its temporary rollback image"
grep -q 'docker rm -f versiond-router' "$tmpdir/idempotent.log" || fail \
    "idempotent convergence left the migration singleton behind"
grep -q '^fleet verify-admission$' "$tmpdir/idempotent.log" || fail \
    "idempotent convergence skipped strict parent admission verification"
grep -q '^fleet apply$' "$tmpdir/idempotent.log" || fail \
    "idempotent convergence did not apply router fleet image/config updates"
jq -e '.transaction.ingress.state == "committed"' \
    "$tmpdir/.gonka-router-ha-transaction.json" >/dev/null || fail \
    "rerun after TERM did not commit the recovered ingress transaction"

INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/both-policy-backends.log" env FAKE_NGINX_MODE=both
for backend in policy_http policy_https; do
	for service in proxy-policy proxy-policy2; do
		for transition in drain down ready; do
			grep -q "^runtime $transition $backend/$service$" \
				"$tmpdir/both-policy-backends.log" || fail \
				"$service did not cross $transition in $backend"
		done
	done
done

INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/missing-nginx-mode.log" env OMIT_NGINX_MODE=true
grep -q '^runtime drain policy_http/proxy-policy2$' \
    "$tmpdir/missing-nginx-mode.log" || fail \
    "proven absence of legacy NGINX_MODE did not retain the http default"

if INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
    INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/nginx-mode-inspect-failure.log" env \
        FAIL_PROXY_ENV_INSPECT=true \
        2>"$tmpdir/nginx-mode-inspect-failure.stderr"; then
    fail "proxy environment inspect failure was accepted as http mode"
fi
grep -q 'cannot inspect proxy environment before policy drain' \
    "$tmpdir/nginx-mode-inspect-failure.stderr" || fail \
    "proxy environment inspect failure was not diagnosed"
if grep -Eq ' stop .*proxy-policy2?$' \
    "$tmpdir/nginx-mode-inspect-failure.log"; then
    fail "policy worker was stopped after NGINX_MODE inspect failed"
fi

INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_POLICY_GENERATION=candidate \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/already-converged.log" env
if grep -Eq ' (stop|up) .*proxy-policy2?$' "$tmpdir/already-converged.log"; then
	fail "already-converged policy generations were restarted"
fi

# If only slot B is admitted, update the failed A first. A fixed B->A order
# would stop the final serving policy worker and drop all public traffic.
INITIAL_POLICY_SERVICES=proxy-policy2 \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/degraded-policy.log" env
policy_line=$(grep -n ' up .*proxy-policy$' "$tmpdir/degraded-policy.log" | head -n1 | cut -d: -f1)
policy2_line=$(grep -n ' up .*proxy-policy2$' "$tmpdir/degraded-policy.log" | head -n1 | cut -d: -f1)
[[ -n $policy_line && -n $policy2_line && $policy_line -lt $policy2_line ]] || fail \
    "degraded rollout stopped the only admitted policy worker first"

if INITIAL_POLICY_SERVICES='' INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/no-policy-reserve.log" env; then
    fail "rollout proceeded without an admitted policy reserve"
fi
if grep -q ' up .*proxy-policy' "$tmpdir/no-policy-reserve.log"; then
	fail "missing policy reserve was detected after mutation"
fi

if INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
    INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/withdrawal-failure.log" env \
        ROUTER_HA_CUTOVER_TIMEOUT_SECONDS=1 \
        WITHDRAW_FAIL_SERVICE=proxy-policy2; then
	fail "policy replacement proceeded while the old address remained admitted"
fi
grep -q '^runtime drain policy_http/proxy-policy2$' \
    "$tmpdir/withdrawal-failure.log" || fail \
    "withdrawal failure did not drain the selected old generation"
if grep -q ' stop .*proxy-policy2$' "$tmpdir/withdrawal-failure.log"; then
	fail "withdrawal failure stopped a generation that was still admitted"
fi
grep -q '^runtime ready policy_http/proxy-policy2$' \
    "$tmpdir/withdrawal-failure.log" || fail \
    "withdrawal failure did not restore the old generation admission state"
if grep ' up .*proxy-policy2$' "$tmpdir/withdrawal-failure.log" \
    | grep -vq 'gonka-rollback-model'; then
	fail "replacement started before withdrawal was confirmed"
fi

if INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
    INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/partial-drain-failure.log" env \
        FAKE_NGINX_MODE=both \
        RUNTIME_FAIL_BACKEND=policy_https \
        RUNTIME_FAIL_SERVICE=proxy-policy2 \
        RUNTIME_FAIL_COMMAND='state drain'; then
	fail "policy replacement proceeded after a partial runtime drain"
fi
grep -q '^runtime drain policy_http/proxy-policy2$' \
    "$tmpdir/partial-drain-failure.log" || fail \
    "partial drain failure did not exercise the first policy backend"
grep -q '^runtime ready policy_http/proxy-policy2$' \
    "$tmpdir/partial-drain-failure.log" || fail \
    "partial drain failure left the first policy backend withdrawn"
if grep -q ' stop .*proxy-policy2$' "$tmpdir/partial-drain-failure.log"; then
	fail "partial drain failure stopped the still-serving policy generation"
fi

runtime_timeout_started=$SECONDS
if INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
    INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/runtime-timeout.log" env \
        ROUTER_HA_RUNTIME_TIMEOUT_SECONDS=1 \
        ROUTER_HA_CUTOVER_TIMEOUT_SECONDS=3 RUNTIME_HANG=true; then
	fail "a hung HAProxy Runtime API call was accepted"
fi
((SECONDS - runtime_timeout_started < 60)) || fail \
	"hung HAProxy Runtime API call exceeded its external timeout"

migration_runtime_timeout_started=$SECONDS
if run_cutover "$tmpdir/migration-runtime-timeout.log" env \
    ROUTER_HA_RUNTIME_TIMEOUT_SECONDS=1 \
    MIGRATION_CATALOG_DIAGNOSTIC=true MIGRATION_CATALOG_HANG=true; then
    fail "a hung migration-router catalog diagnostic was accepted"
fi
((SECONDS - migration_runtime_timeout_started < 60)) || fail \
    "hung migration-router catalog diagnostic exceeded its external timeout"

if run_cutover "$tmpdir/partial-migration-catalog.log" env \
    ROUTER_HA_RUNTIME_TIMEOUT_SECONDS=1 \
    MIGRATION_CATALOG_DIAGNOSTIC=true \
    MIGRATION_CATALOG_FAIL_MAP=/etc/haproxy/versions.map; then
    fail "a partial migration-router catalog was accepted"
fi
if grep -q 'docker rm -f versiond-router' \
    "$tmpdir/partial-migration-catalog.log"; then
    fail "partial migration-router catalog crossed the cutover commit point"
fi

if INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
    INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/mid-rollout-drift.log" env \
        DRIFT_AFTER_POLICY_SERVICE=proxy-policy2; then
    fail "ingress rollout mixed two Compose generations"
fi
if grep -q ' up .*proxy$' "$tmpdir/mid-rollout-drift.log"; then
    fail "Compose drift was detected after public proxy mutation"
fi
grep -q 'policy-image=gonka/router-ha-policy-rollback:proxy-policy2-' \
    "$tmpdir/mid-rollout-drift.log" || fail \
    "Compose drift did not restore the policy slot already touched"

if INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
    INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/day2-failure.log" env FAIL_CUTOVER=true; then
    fail "failed day-2 public proxy update was reported as successful"
fi

if INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
    INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/policy-failure.log" env \
        FAIL_POLICY_SERVICE=proxy-policy; then
    fail "failed policy slot update was reported as successful"
fi
grep -q 'policy-image=gonka/router-ha-policy-rollback:proxy-policy-' \
    "$tmpdir/policy-failure.log" || fail \
    "failed policy generation did not restore the captured active slot"
grep -q 'policy-image=gonka/router-ha-policy-rollback:proxy-policy2-' \
    "$tmpdir/policy-failure.log" || fail \
    "failed policy generation did not restore the captured reserve slot"

if INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
    INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/policy-inventory-failure.log" env \
        FAIL_COMPOSE_PS_SERVICE=proxy-policy; then
    fail "failed policy inventory was accepted as an empty rollback baseline"
fi
grep -q ' ps .*proxy-policy' "$tmpdir/policy-inventory-failure.log" || fail \
    "policy inventory failure did not reach the expected Compose query"
if grep -q 'docker rm .*proxy-policy' "$tmpdir/policy-inventory-failure.log"; then
    fail "policy inventory failure removed an existing worker"
fi

if INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
    INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/reserve-failure.log" env \
        FAIL_POLICY_SERVICE=proxy-policy2; then
    fail "failed reserve policy slot update was reported as successful"
fi
grep -q 'policy-image=gonka/router-ha-policy-rollback:proxy-policy2-' \
    "$tmpdir/reserve-failure.log" || fail \
    "failed reserve slot was not restored"
if grep -q 'policy-image=gonka/router-ha-policy-rollback:proxy-policy-' \
    "$tmpdir/reserve-failure.log"; then
    fail "rollback replaced the untouched active policy slot"
fi

# A deferred ingress transaction is still reversible until its owner records
# one durable decision. Recovery must restore the prepared generation rather
# than reporting success without touching it.
prepared_journal="$tmpdir/prepared-ingress.json"
INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/prepared-ingress.log" env \
        ROUTER_HA_TRANSACTION_JOURNAL="$prepared_journal" \
        ROUTER_HA_TRANSACTION_ID=test-prepared-ingress \
        ROUTER_HA_DEFER_COMMIT=true
jq -e '.transaction.ingress.state == "prepared"' \
    "$prepared_journal" >/dev/null || fail \
    "deferred ingress transaction was not prepared"
: >"$tmpdir/prepared-recovery.log"
env DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/prepared-recovery.log" \
    STATE_DIR="$tmpdir" \
    JOIN_DIR="$script_dir" \
    ROUTER_HA_TRANSACTION_JOURNAL="$prepared_journal" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/enable-router-ha.sh" --recover-only
jq -e '.transaction.ingress.state == "rolled_back" and
       (.transaction.ingress.rollback_models? // null) == null' \
    "$prepared_journal" >/dev/null || fail \
    "prepared ingress recovery did not restore and redact its generation"
grep -q 'gonka-rollback-model\..* up .*proxy' \
    "$tmpdir/prepared-recovery.log" || fail \
    "prepared ingress recovery was a successful no-op"

# The same prepared state becomes forward-only once the owner commits. A
# recover-only process must finalize it without invoking any rollback model.
committed_journal="$tmpdir/committed-ingress.json"
INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/committed-ingress.log" env \
        ROUTER_HA_TRANSACTION_JOURNAL="$committed_journal" \
        ROUTER_HA_TRANSACTION_ID=test-committed-ingress \
        ROUTER_HA_DEFER_COMMIT=true
jq '.transaction.decision = "commit"' "$committed_journal" \
    >"$tmpdir/committed-ingress.decision"
mv "$tmpdir/committed-ingress.decision" "$committed_journal"
: >"$tmpdir/committed-recovery.log"
env DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/committed-recovery.log" \
    STATE_DIR="$tmpdir" \
    JOIN_DIR="$script_dir" \
    ROUTER_HA_TRANSACTION_JOURNAL="$committed_journal" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/enable-router-ha.sh" --recover-only
jq -e '.transaction.ingress.state == "committed" and
       (.transaction.ingress.rollback_models? // null) == null' \
    "$committed_journal" >/dev/null || fail \
    "durably committed ingress did not finish forward"
if grep -q 'gonka-rollback-model' "$tmpdir/committed-recovery.log"; then
    fail "durably committed ingress used a rollback model"
fi

set +e
INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/crash.log" env KILL_POLICY_SERVICE=proxy-policy2
crash_status=$?
set -e
[[ $crash_status -ne 0 ]] || fail "SIGKILL during policy mutation returned success"
jq -e '.transaction.ingress.state == "active" and
       .transaction.ingress.touched == ["network:policy", "policy:proxy-policy2"]' \
    "$tmpdir/.gonka-router-ha-transaction.json" >/dev/null || fail \
    "SIGKILL did not leave a replayable touched-resource journal"
cp "$tmpdir/config.env" "$tmpdir/config.env.saved"
printf 'this forward config is intentionally invalid\n' >"$tmpdir/config.env"
: >"$tmpdir/configless-recovery.log"
env DOCKER_BIN="$tmpdir/docker" \
    DOCKER_LOG="$tmpdir/configless-recovery.log" \
    STATE_DIR="$tmpdir" \
    JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/enable-router-ha.sh" --recover-only
mv "$tmpdir/config.env.saved" "$tmpdir/config.env"
jq -e '.transaction.ingress.state == "rolled_back"' \
    "$tmpdir/.gonka-router-ha-transaction.json" >/dev/null || fail \
    "config-independent recovery did not finish the embedded rollback model"
INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/crash-recovery.log" env
apply_line=$(grep -n '^fleet apply$' "$tmpdir/crash-recovery.log" | head -n1 | cut -d: -f1)
[[ -n $apply_line ]] || fail "restart did not continue after journal recovery"
if grep -q 'gonka-rollback-model\..* up .*proxy-policy2$' \
    "$tmpdir/crash-recovery.log"; then
	fail "journal intent without a Docker mutation recreated an unchanged policy slot"
fi

set +e
INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/stopped-crash.log" env KILL_POLICY_SERVICE=proxy-policy2
stopped_crash_status=$?
set -e
[[ $stopped_crash_status -ne 0 ]] || fail \
    "SIGKILL before stopped-generation recovery returned success"
: >"$tmpdir/stopped-cid-proxy-policy2"
INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/stopped-recovery.log" env
rm -f "$tmpdir/stopped-cid-proxy-policy2"
grep -q 'gonka-rollback-model\..* up .*proxy-policy2$' \
    "$tmpdir/stopped-recovery.log" || fail \
    "matching container ID hid a stopped rollback generation"

set +e
INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/legacy-proxy-crash.log" env \
        KILL_PROXY_BEFORE_MUTATION=true
legacy_proxy_status=$?
set -e
[[ $legacy_proxy_status -ne 0 ]] || fail \
    "SIGKILL before proxy mutation returned success"
jq 'del(.transaction.ingress.proxy.container_id)' \
    "$tmpdir/.gonka-router-ha-transaction.json" \
    >"$tmpdir/legacy-ingress.json"
mv "$tmpdir/legacy-ingress.json" "$tmpdir/.gonka-router-ha-transaction.json"
INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/legacy-proxy-recovery.log" env PROXY_EXISTS=false
grep -q 'gonka-rollback-model\..* up .*proxy$' \
    "$tmpdir/legacy-proxy-recovery.log" || fail \
    "legacy journal without generation identity treated an absent proxy as restored"

if INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
    INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/config-drift.log" env POLICY2_CONFIG_HASH=unexpected; then
    fail "policy config drift was accepted without an exact rollback model"
fi
if grep -q ' up .*proxy-policy' "$tmpdir/config-drift.log"; then
    fail "policy config drift was detected after mutation"
fi

if INITIAL_POLICY_SERVICES=proxy-policy INITIAL_POLICY_A_REPLICAS=2 \
    run_cutover "$tmpdir/scaled-policy-failure.log" env \
        FAIL_POLICY_SERVICE=proxy-policy; then
    fail "failed migration from the scaled policy service succeeded"
fi
grep -q -- '--scale proxy-policy=2 proxy-policy$' \
    "$tmpdir/scaled-policy-failure.log" || fail \
    "rollback did not restore the scaled service's original replica count"

if run_cutover "$tmpdir/contract-mismatch.log" env \
    CANDIDATE_PROXY_CONTRACT=2; then
    fail "an incompatible policy/proxy wire contract was accepted"
fi

if run_cutover "$tmpdir/outer-compose-drift.log" env \
	ROUTER_HA_EXPECTED_COMPOSE_SHA256=0000000000000000000000000000000000000000000000000000000000000000; then
	fail "router cutover accepted a different Compose generation than the outer upgrade"
fi
if run_cutover "$tmpdir/outer-fleet-drift.log" env \
    ROUTER_HA_EXPECTED_FLEET_SPEC_SHA256=0000000000000000000000000000000000000000000000000000000000000000; then
    fail "router cutover accepted a different fleet specification than the outer upgrade"
fi
if grep -q '^fleet apply$' "$tmpdir/outer-fleet-drift.log"; then
    fail "outer fleet specification drift was detected after fleet mutation"
fi

outer_compose_sha=$(DOCKER_LOG=/dev/null STATE_DIR="$tmpdir" JOIN_DIR="$script_dir" \
	"$tmpdir/docker" compose config --format json | jq -Sc . | \
	sha256sum | awk '{print $1}')
if run_cutover "$tmpdir/outer-compose-mid-fleet-drift.log" env \
	ROUTER_HA_EXPECTED_COMPOSE_SHA256="$outer_compose_sha" \
	FLEET_COMPOSE_DRIFT=true; then
	fail "router cutover committed Compose drift introduced during fleet rollout"
fi

if run_cutover "$tmpdir/fleet-spec-drift.log" env \
    FLEET_SPEC_DRIFT=true; then
    fail "router cutover committed a fleet specification changed by apply"
fi
grep -q '^fleet apply$' "$tmpdir/fleet-spec-drift.log" || fail \
    "fleet specification drift scenario did not reach fleet apply"
if grep -q ' up .*\(proxy-policy\|proxy\)$' \
    "$tmpdir/fleet-spec-drift.log"; then
    fail "fleet specification drift was detected after ingress mutation"
fi

if run_cutover "$tmpdir/postgres-identity-mismatch.log" env \
    POSTGRES_IDENTITY2=22222222-2222-2222-2222-222222222222; then
    fail "router HA accepted versiond replicas backed by different PostgreSQL databases"
fi

if run_cutover "$tmpdir/postgres-preflight-failure.log" env \
    POSTGRES_PREFLIGHT_FAIL=true; then
    fail "failed PostgreSQL deployment preflight was accepted"
fi
if grep -q '^fleet apply$' "$tmpdir/postgres-preflight-failure.log"; then
    fail "PostgreSQL deployment preflight failed after fleet mutation"
fi
if grep -q '^fleet apply$' "$tmpdir/postgres-identity-mismatch.log"; then
    fail "PostgreSQL identity mismatch was detected after fleet mutation"
fi

printf '%s\n' '{"storage":{"postgres_identity":"33333333-3333-3333-3333-333333333333"}}' \
    >"$tmpdir/.gonka-devshard-v5-upgrade-complete"
if run_cutover "$tmpdir/postgres-marker-identity-mismatch.log" env; then
    fail "standalone router HA accepted a database different from the committed upgrade marker"
fi
rm -f "$tmpdir/.gonka-devshard-v5-upgrade-complete"
if grep -q '^fleet apply$' "$tmpdir/postgres-marker-identity-mismatch.log"; then
    fail "committed PostgreSQL identity mismatch was detected after fleet mutation"
fi

if run_cutover "$tmpdir/incompatible-version-name.log" env \
    VERSION_CATALOG_JSON='{"versions":[{"name":"v4:hotfix"}]}'; then
    fail "router HA accepted a catalog name outside the routing grammar"
fi
if grep -q '^fleet apply$' "$tmpdir/incompatible-version-name.log"; then
    fail "incompatible catalog name was detected after fleet mutation"
fi

if run_cutover "$tmpdir/version-name-terminal-lf.log" env \
    VERSION_CATALOG_JSON='{"versions":[{"name":"v4\n"}]}'; then
    fail "router HA accepted a catalog name with a terminal LF"
fi
if grep -q '^fleet apply$' "$tmpdir/version-name-terminal-lf.log"; then
    fail "terminal-LF catalog name was detected after fleet mutation"
fi
if grep -q ' up .*\(proxy-policy\|proxy\)$' \
	"$tmpdir/outer-compose-mid-fleet-drift.log"; then
	fail "mid-fleet Compose drift was detected after ingress mutation"
fi
if grep -q ' up .*\(proxy-policy\|proxy\)$' "$tmpdir/outer-compose-drift.log"; then
	fail "outer Compose generation drift was detected after ingress mutation"
fi

if run_cutover "$tmpdir/cache-contract-mismatch.log" env \
    CANDIDATE_PROXY_CACHE_PROTOCOL=''; then
    fail "proxy-router without a cache protocol was accepted"
fi
if run_cutover "$tmpdir/cache-contract-old.log" env \
    CANDIDATE_PROXY_CACHE_PROTOCOL=1; then
    fail "proxy-router with an old cache protocol was accepted"
fi
if grep -q ' up .*proxy-policy' "$tmpdir/cache-contract-mismatch.log"; then
    fail "cache protocol mismatch was detected after policy mutation"
fi
if grep -q ' up .*proxy-policy' "$tmpdir/contract-mismatch.log"; then
    fail "contract mismatch was detected after policy mutation"
fi

run_cutover "$tmpdir/missing-proxy.log" env PROXY_EXISTS=false
grep -q ' up .*proxy$' "$tmpdir/missing-proxy.log" || fail \
    "missing committed public proxy was not recreated"
if grep -q '^docker tag sha256:old-proxy ' "$tmpdir/missing-proxy.log"; then
    fail "missing public proxy armed a fictitious image rollback"
fi

if run_cutover "$tmpdir/missing-proxy-failure.log" env \
    PROXY_EXISTS=false FAIL_CUTOVER=true; then
    fail "failed recreation of an absent public proxy succeeded"
fi
grep -q '^docker rm -f proxy$' "$tmpdir/missing-proxy-failure.log" || fail \
    "failed recreation did not restore the committed absent state"
grep -q 'gonka-rollback-model\..* up .*proxy$' \
	"$tmpdir/day2-failure.log" || fail \
    "day-2 rollback did not restore its immutable Compose model"
if grep -q '^fleet verify-admission$' "$tmpdir/day2-failure.log"; then
	fail "day-2 ingress rollback depended on downstream fleet admission"
fi
jq -e '.transaction.ingress.state == "rolled_back"' \
	"$tmpdir/.gonka-router-ha-transaction.json" >/dev/null || fail \
	"day-2 ingress rollback retained an active journal"

if run_cutover "$tmpdir/wrong-network.log" env WRONG_NETWORK_OWNERSHIP=true; then
    fail "cutover accepted an existing network owned by another Compose model"
fi
if grep -q '^fleet apply$' "$tmpdir/wrong-network.log"; then
    fail "router fleet started before network ownership was validated"
fi

journal=$tmpdir/.gonka-router-ha-transaction.json
printf '%s\n' '{broken-json' >"$journal"
cp "$journal" "$journal.expected"
if env DOCKER_BIN="$tmpdir/docker" DOCKER_LOG="$tmpdir/corrupt-recovery.log" \
    STATE_DIR="$tmpdir" JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/enable-router-ha.sh" --recover-only \
    >"$tmpdir/corrupt-recovery.stdout" 2>"$tmpdir/corrupt-recovery.stderr"; then
    fail "corrupt ingress journal was reported as successfully recovered"
fi
cmp -s "$journal" "$journal.expected" || fail \
    "failed recovery modified the corrupt ingress journal"

printf '%s\n' '{"transaction":{"ingress":{"state":"future-state"}}}' \
    >"$journal"
cp "$journal" "$journal.expected"
if env DOCKER_BIN="$tmpdir/docker" DOCKER_LOG="$tmpdir/unknown-recovery.log" \
    STATE_DIR="$tmpdir" JOIN_DIR="$script_dir" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/enable-router-ha.sh" --recover-only \
    >"$tmpdir/unknown-recovery.stdout" 2>"$tmpdir/unknown-recovery.stderr"; then
    fail "unknown ingress journal state was reported as successfully recovered"
fi
cmp -s "$journal" "$journal.expected" || fail \
    "failed recovery modified the unknown ingress journal"

echo "enable-router-ha_test: ok"
