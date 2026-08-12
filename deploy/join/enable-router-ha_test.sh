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
	if [[ ${4:-} == *ai.gonka.catalog-cache-protocol-version* ]]; then
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
		case ${3:-} in
			'{{.State.Running}} {{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}')
				if [[ -f $STATE_DIR/stopped-${4:-unknown} ]]; then
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
					cid-proxy-policy*) printf 'old-policy-ref\n' ;;
					*) printf 'old-proxy-ref\n' ;;
				esac
				;;
            *NetworkSettings.Networks*)
                case ${4:-} in
                    cid-proxy-policy2*) printf '172.30.0.12\n' ;;
                    cid-proxy-policy*) printf '172.30.0.11\n' ;;
                esac
                ;;
            '{{range .Config.Env}}{{println .}}{{end}}')
                case ${4:-} in
                    proxy)
                        printf '%s\n' \
                            'NGINX_MODE=http' \
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
                        printf 'PGHOST=devshard-postgres\nPGDATABASE=devshardd\nPGUSER=devshardd\n'
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
        case $arg in config | pull | up | ps | rm) action=$arg ;; esac
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
    if [[ $action == up && $service == proxy-policy* ]]; then
		if [[ -f $model_file && $model_file == *.gonka-rollback-model.* ]]; then
			printf 'policy-image=%s\n' \
				"$(jq -r --arg service "$service" '.services[$service].image' "$model_file")" \
				>>"$DOCKER_LOG"
			: >"$STATE_DIR/present-$service"
			rm -f "$STATE_DIR/generation-$service"
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
    exit 1
fi

if [[ ${1:-} == exec && ${2:-} == proxy && \
    ${3:-} == /bin/sh && ${4:-} == -ec && \
    ${5:-} == *'show servers state'* ]]; then
    printf '# header\n# header2\n'
    printf '1 policy 1 policy1 172.30.0.11 2 0\n'
    printf '1 policy 2 policy2 172.30.0.12 2 0\n'
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
if [[ ${1:-} == verify-admission && \
    ${FLEET_ADMISSION_FAIL:-false} == true ]]; then
    exit 1
fi
if [[ ${1:-} == apply && ${FLEET_COMPOSE_DRIFT:-false} == true ]]; then
	: >"$STATE_DIR/model-drift"
fi
EOF
chmod +x "$tmpdir/fleet"

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
    rm -f "$tmpdir"/present-proxy-policy*
	rm -f "$tmpdir"/generation-proxy*
    if [[ -n ${INITIAL_PROXY_COMPONENT:-} ]]; then
        printf '%s\n' "$INITIAL_PROXY_COMPONENT" >"$tmpdir/current"
    fi
    for service in ${INITIAL_POLICY_SERVICES-}; do
        : >"$tmpdir/present-$service"
    done
    env DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$log" \
        STATE_DIR="$tmpdir" \
        JOIN_DIR="$script_dir" \
        VERSIOND_ROUTER_FLEET_BIN="$tmpdir/fleet" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        "$@" "$script_dir/enable-router-ha.sh" \
        --versiond-mode ha --edge-mode multi
}

run_cutover "$tmpdir/success.log" env MISSING_NETWORKS=true
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
grep -q 'docker rm -f edge-api-router' "$tmpdir/success.log" || fail \
    "singleton edge-api-router was not removed after commit"
verify_line=$(grep -n '^fleet verify-admission ' "$tmpdir/success.log" | head -n1 | cut -d: -f1)
remove_line=$(grep -n 'docker rm -f versiond-router' "$tmpdir/success.log" | head -n1 | cut -d: -f1)
[[ -n $verify_line && -n $remove_line && $verify_line -lt $remove_line ]] || fail \
    "migration singleton was removed before fleet admission was committed"
jq -e '
    .transaction.ingress.state == "committed" and
    .transaction.ingress.touched ==
        ["policy:proxy-policy2", "policy:proxy-policy", "proxy"] and
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
[[ $signal_status -eq 143 ]] || fail \
    "TERM during cutover returned $signal_status instead of 143"
if grep -q 'gonka-rollback-model\..* up .*proxy$' "$tmpdir/signal.log"; then
	fail "TERM before Docker mutation unnecessarily recreated the public proxy"
fi

INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/idempotent.log" env
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

set +e
INITIAL_POLICY_SERVICES="proxy-policy proxy-policy2" \
INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/crash.log" env KILL_POLICY_SERVICE=proxy-policy2
crash_status=$?
set -e
[[ $crash_status -ne 0 ]] || fail "SIGKILL during policy mutation returned success"
jq -e '.transaction.ingress.state == "active" and
       .transaction.ingress.touched == ["policy:proxy-policy2"]' \
    "$tmpdir/.gonka-router-ha-transaction.json" >/dev/null || fail \
    "SIGKILL did not leave a replayable touched-resource journal"
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

outer_compose_sha=$(DOCKER_LOG=/dev/null STATE_DIR="$tmpdir" JOIN_DIR="$script_dir" \
	"$tmpdir/docker" compose config --format json | jq -Sc . | \
	sha256sum | awk '{print $1}')
if run_cutover "$tmpdir/outer-compose-mid-fleet-drift.log" env \
	ROUTER_HA_EXPECTED_COMPOSE_SHA256="$outer_compose_sha" \
	FLEET_COMPOSE_DRIFT=true; then
	fail "router cutover committed Compose drift introduced during fleet rollout"
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
grep -q '^fleet verify-admission$' "$tmpdir/day2-failure.log" || fail \
    "day-2 rollback did not verify parent admission"

if run_cutover "$tmpdir/wrong-network.log" env WRONG_NETWORK_OWNERSHIP=true; then
    fail "cutover accepted an existing network owned by another Compose model"
fi
if grep -q '^fleet apply$' "$tmpdir/wrong-network.log"; then
    fail "router fleet started before network ownership was validated"
fi

echo "enable-router-ha_test: ok"
