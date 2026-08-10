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

if [[ ${1:-} == inspect ]]; then
    if [[ ${2:-} == --format ]]; then
        case ${3:-} in
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
            '{{.Image}}') printf 'sha256:old-proxy\n' ;;
            '{{range .Config.Env}}{{println .}}{{end}}')
                case ${4:-} in
                    versiond | versiond2)
                        printf 'PGHOST=devshard-postgres\nPGDATABASE=devshardd\nPGUSER=devshardd\n'
                        ;;
                esac
                ;;
        esac
        exit 0
    fi
    case ${2:-} in
        proxy | versiond | versiond2 | devshard-postgres | versiond-router | \
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
    for arg in "$@"; do
        [[ $arg == *docker-compose.proxy-v4-compat.yml ]] && compat=true
        case $arg in config | pull | up) action=$arg ;; esac
        case $arg in proxy | proxy-policy) service=$arg ;; esac
    done
    if [[ $action == config ]]; then
        jq -cn --arg join "$JOIN_DIR" '{name:"gonka-test",networks:{
            "proxy-policy-front":{name:"gonka-proxy-policy-front"},
            "versiond-router-front":{name:"gonka-versiond-router-front"},
            "versiond-router-back":{name:"gonka-versiond-router-back"}
        },services:{
            proxy:{container_name:"proxy"},
            "proxy-policy":{},
            versiond:{container_name:"versiond",environment:{PGHOST:"devshard-postgres",PGDATABASE:"devshardd",PGUSER:"devshardd",PGPORT:"5432",DEVSHARD_STORAGE_MODE:"postgres"}},
            versiond2:{container_name:"versiond2",environment:{PGHOST:"devshard-postgres",PGDATABASE:"devshardd",PGUSER:"devshardd",PGPORT:"5432",DEVSHARD_STORAGE_MODE:"postgres"}},
            "devshard-postgres":{container_name:"devshard-postgres",volumes:[{type:"bind",source:($join + "/devshards/postgres"),target:"/var/lib/postgresql/gonka"}]},
            "edge-api":{container_name:"edge-api"},
            "edge-api2":{container_name:"edge-api2"},
            "edge-api3":{container_name:"edge-api3"}
        }}'
        exit 0
    fi
    if [[ $action == up && $service == proxy ]]; then
        if [[ $compat == true ]]; then
            printf 'rollback-versiond %s\n' \
                "${PROXY_V4_VERSIOND_SERVICE_NAME:-}" >>"$DOCKER_LOG"
            printf 'proxy-policy\n' >"$STATE_DIR/current"
            exit 0
        fi
        if [[ ${FAIL_CUTOVER:-false} == true ]]; then
            exit 1
        fi
        printf 'proxy-router\n' >"$STATE_DIR/current"
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

case ${1:-} in
    exec | tag | rm) exit 0 ;;
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
EOF
chmod +x "$tmpdir/fleet"

cat >"$tmpdir/config.env" <<EOF
ROUTER_HA_PULL_POLICY=missing
ROUTER_HA_CUTOVER_LOCK=$tmpdir/cutover.lock
VERSIOND_NON_HA_VERSIONS=v1
VERSIOND_VERSIONS=v4
EOF

run_cutover() {
    local log=$1
    shift
    : >"$log"
    rm -f "$tmpdir/current"
    if [[ -n ${INITIAL_PROXY_COMPONENT:-} ]]; then
        printf '%s\n' "$INITIAL_PROXY_COMPONENT" >"$tmpdir/current"
    fi
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
policy_line=$(grep -n ' up .*proxy-policy' "$tmpdir/success.log" | head -n1 | cut -d: -f1)
proxy_line=$(grep -n ' up .*proxy$' "$tmpdir/success.log" | head -n1 | cut -d: -f1)
[[ -n $policy_line && -n $proxy_line && $policy_line -lt $proxy_line ]] || fail \
    "policy workers were not ready before the public cutover"
grep -q 'docker rm -f versiond-router' "$tmpdir/success.log" || fail \
    "singleton versiond-router was not removed after commit"
grep -q 'docker rm -f edge-api-router' "$tmpdir/success.log" || fail \
    "singleton edge-api-router was not removed after commit"
verify_line=$(grep -n '^fleet verify-admission ' "$tmpdir/success.log" | head -n1 | cut -d: -f1)
remove_line=$(grep -n 'docker rm -f versiond-router' "$tmpdir/success.log" | head -n1 | cut -d: -f1)
[[ -n $verify_line && -n $remove_line && $verify_line -lt $remove_line ]] || fail \
    "migration singleton was removed before fleet admission was committed"

if run_cutover "$tmpdir/failure.log" env FAIL_CUTOVER=true; then
    fail "failed public proxy replacement was reported as successful"
fi
grep -q 'docker-compose.proxy-v4-compat.yml' "$tmpdir/failure.log" || fail \
    "failed cutover did not use the v4 rollback model"
grep -q ' up .*proxy$' "$tmpdir/failure.log" || fail \
    "failed cutover did not recreate the public nginx"

if run_cutover "$tmpdir/admission-failure.log" env FLEET_ADMISSION_FAIL=true; then
    fail "cutover committed while the parent did not admit the router fleet"
fi
grep -q '^fleet verify-admission v1 v4$' \
    "$tmpdir/admission-failure.log" || fail \
    "failed admission scenario did not reach the commit gate"
grep -q '^rollback-versiond versiond-router$' \
    "$tmpdir/admission-failure.log" || fail \
    "admission failure did not preserve the singleton-backed v4 rollback"
if grep -q 'docker rm -f versiond-router' "$tmpdir/admission-failure.log"; then
    fail "admission failure removed the upstream required by v4 rollback"
fi

INITIAL_PROXY_COMPONENT=proxy-router \
    run_cutover "$tmpdir/idempotent.log" env
if grep -q '^docker tag ' "$tmpdir/idempotent.log"; then
    fail "idempotent convergence manufactured a public proxy rollback tag"
fi
grep -q 'docker rm -f versiond-router' "$tmpdir/idempotent.log" || fail \
    "idempotent convergence left the migration singleton behind"
grep -q '^fleet verify-admission$' "$tmpdir/idempotent.log" || fail \
    "idempotent convergence skipped strict parent admission verification"
grep -q '^fleet apply$' "$tmpdir/idempotent.log" || fail \
    "idempotent convergence did not apply router fleet image/config updates"

if run_cutover "$tmpdir/wrong-network.log" env WRONG_NETWORK_OWNERSHIP=true; then
    fail "cutover accepted an existing network owned by another Compose model"
fi
if grep -q '^fleet apply$' "$tmpdir/wrong-network.log"; then
    fail "router fleet started before network ownership was validated"
fi

echo "enable-router-ha_test: ok"
