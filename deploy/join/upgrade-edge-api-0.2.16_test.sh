#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
    echo "upgrade-edge-api-0.2.16_test: $*" >&2
    exit 1
}

cat >"$tmpdir/compose.yml" <<'EOF'
services: {}
EOF
cat >"$tmpdir/config.env" <<EOF
EDGE_API_0_2_16_UPGRADE_MARKER=$tmpdir/complete.json
EDGE_API_0_2_16_ALLOW_UNRELEASED_IMAGES=true
EDGE_API_0_2_16_IMAGE=host-config-must-not-replace-release-contract
EDGE_API_0_2_16_PROXY_POLICY_IMAGE=host-config-must-not-replace-release-contract
EDGE_API_0_2_16_PROXY_ROUTER_IMAGE=host-config-must-not-replace-release-contract
EOF
printf '{"release_id":"0.2.15-devshard-v5"}\n' \
    >"$tmpdir/.gonka-devshard-v5-upgrade-complete"

grep -v '^EDGE_API_0_2_16_ALLOW_UNRELEASED_IMAGES=' "$tmpdir/config.env" \
    >"$tmpdir/release-config.env"
if DOCKER_BIN="$tmpdir/docker" DOCKER_LOG="$tmpdir/release-gate.log" \
    FAKE_STATE_DIR="$tmpdir/release-gate-state" JOIN_DIR="$script_dir" \
    COMPOSE_FILE_PATH="$tmpdir/compose.yml" \
    EXISTING_CONTAINERS="proxy proxy-policy proxy-policy2 versiond edge-api" \
    ROUTER_HA_ENABLE_BIN="$tmpdir/enable-router" \
    GONKA_CONFIG_ENV="$tmpdir/release-config.env" \
    "$script_dir/upgrade-edge-api-0.2.16.sh" --edge-mode single \
    --compose-file "$tmpdir/compose.yml" --preflight-only \
    >"$tmpdir/release-gate.stdout" 2>"$tmpdir/release-gate.stderr"; then
    fail "production preflight accepted mutable release image tags"
fi
grep -q 'must use an immutable sha256 digest' "$tmpdir/release-gate.stderr" || \
    fail "mutable image failure was not actionable"

cat >"$tmpdir/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
state=${FAKE_STATE_DIR:?}
printf 'EDGE_API_IMAGE=%s PROXY_POLICY_IMAGE=%s PROXY_ROUTER_IMAGE=%s :: ' \
    "${EDGE_API_IMAGE-}" "${PROXY_POLICY_IMAGE-}" "${PROXY_ROUTER_IMAGE-}" \
    >>"$DOCKER_LOG"
printf '%q ' "$@" >>"$DOCKER_LOG"
printf '\n' >>"$DOCKER_LOG"
exists() {
    [[ " ${EXISTING_CONTAINERS-} " == *" $1 "* ]] || \
        [[ -f "$state/present-$1" ]]
}
service_image() {
    local service=$1
    if [[ -f $state/image-$service ]]; then
        cat "$state/image-$service"
        return
    fi
    case $service in
        proxy) printf '%s\n' "${OLD_PROXY_IMAGE:-old-proxy}" ;;
        proxy-policy | proxy-policy2) printf '%s\n' "${OLD_POLICY_IMAGE:-old-policy}" ;;
        *) printf 'old-%s\n' "$service" ;;
    esac
}

if [[ ${1:-} == compose && ${2:-} == version ]]; then
    printf '2.24.4\n'
    exit 0
fi
if [[ ${1:-} == inspect ]]; then
    if [[ $# -eq 2 ]]; then
        exists "$2"
        exit
    fi
    format=${3:-}
    container=${4:-}
    service=${container#cid-}
    case $format in
        '{{json .Config.Labels}}')
            jq -cn --arg file "$COMPOSE_FILE_PATH" --arg work "$JOIN_DIR" \
                '{"com.docker.compose.project":"gonka-test",
                  "com.docker.compose.project.config_files":$file,
                  "com.docker.compose.project.working_dir":$work}'
            ;;
        '{{index .Config.Labels "ai.gonka.component"}}')
            printf 'proxy-router\n'
            ;;
        '{{range .Config.Env}}{{println .}}{{end}}')
            if [[ $container == edge-api-router ]]; then
                printf 'EDGE_API_HOSTS=edge-api edge-api2 edge-api3\n'
            fi
            ;;
        '{{.Image}}') printf 'sha256:%s\n' "$service" ;;
        '{{.Config.Image}}') service_image "$service" ;;
        '{{.State.Running}}|{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}')
            if [[ $(service_image "$service") == gonka-upgrade-rollback/* ]]; then
                printf 'true|unhealthy\n'
            else
                printf 'true|healthy\n'
            fi
            ;;
        '{{.State.Running}}') printf 'true\n' ;;
        *) exit 1 ;;
    esac
    exit 0
fi

if [[ ${1:-} == compose ]]; then
    shift
    while (($# > 0)); do
        case $1 in
            --project-directory | --project-name | -f) shift 2 ;;
            *) break ;;
        esac
    done
    case ${1:-} in
        config)
            jq -cn --arg project gonka-test --arg dir "$JOIN_DIR" '
                {
                  name:$project,
                  services:{
                    proxy:{container_name:"proxy"},
                    "proxy-policy":{}, "proxy-policy2":{},
                    versiond:{container_name:"versiond",environment:{}},
                    "edge-api":{container_name:"edge-api"},
                    "edge-api2":{container_name:"edge-api2"},
                    "edge-api3":{container_name:"edge-api3"}
                  },
                  networks:{
                    default:{name:"gonka-test_default"},
                    "proxy-policy-front":{name:"gonka-proxy-policy-front"}
                  }
                }'
            ;;
        ps)
            service=${*: -1}
            exists "$service" && printf 'cid-%s\n' "$service"
            ;;
        pull) ;;
        up)
            service=${*: -1}
            if [[ $service == "${FAIL_SERVICE-}" && \
                ${EDGE_API_IMAGE-} != gonka-upgrade-rollback/* ]]; then
                exit 1
            fi
            case $service in
                edge-api | edge-api2 | edge-api3)
                    printf '%s\n' "$EDGE_API_IMAGE" >"$state/image-$service"
                    ;;
                proxy) printf '%s\n' "$PROXY_ROUTER_IMAGE" >"$state/image-proxy" ;;
                proxy-policy | proxy-policy2)
                    printf '%s\n' "$PROXY_POLICY_IMAGE" >"$state/image-$service"
                    ;;
            esac
            : >"$state/present-$service"
            ;;
        *) exit 1 ;;
    esac
    exit 0
fi

case ${1:-} in
    tag) ;;
    cp) ;;
    exec) ;;
    rm) rm -f "$state/present-${*: -1}" ;;
    image) ;;
    *) exit 1 ;;
esac
EOF
chmod +x "$tmpdir/docker"

cat >"$tmpdir/enable-router" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf 'enable-router ' >>"$DOCKER_LOG"
printf '%q ' "$@" >>"$DOCKER_LOG"
printf '\n' >>"$DOCKER_LOG"
printf '%s\n' "$PROXY_POLICY_IMAGE" >"$FAKE_STATE_DIR/image-proxy-policy2"
printf '%s\n' "$PROXY_POLICY_IMAGE" >"$FAKE_STATE_DIR/image-proxy-policy"
printf '%s\n' "$PROXY_ROUTER_IMAGE" >"$FAKE_STATE_DIR/image-proxy"
EOF
chmod +x "$tmpdir/enable-router"

run_update() {
    local mode=$1 log=$2 state=$3
    shift 3
    mkdir -p "$state"
    : >"$log"
    env DOCKER_BIN="$tmpdir/docker" DOCKER_LOG="$log" \
        FAKE_STATE_DIR="$state" JOIN_DIR="$script_dir" \
        COMPOSE_FILE_PATH="$tmpdir/compose.yml" \
        EXISTING_CONTAINERS="proxy proxy-policy proxy-policy2 versiond edge-api edge-api2 edge-api3 edge-api-router" \
        ROUTER_HA_ENABLE_BIN="$tmpdir/enable-router" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" "$@" \
        "$script_dir/upgrade-edge-api-0.2.16.sh" \
        --edge-mode "$mode" --compose-file "$tmpdir/compose.yml" \
        --acknowledge-maintenance
}

assert_not_mutated() {
    local log=$1 service=$2
    if grep -E -- \
        " :: (compose .* (pull|up|stop|start|rm) |rm |tag |image rm ).*${service}" \
        "$log" >/dev/null; then
        cat "$log" >&2
        fail "$service was mutated"
    fi
}

preflight_log=$tmpdir/preflight.log
mkdir -p "$tmpdir/preflight-state"
mv "$tmpdir/.gonka-devshard-v5-upgrade-complete" \
    "$tmpdir/.gonka-devshard-v5-upgrade-complete.saved"
if DOCKER_BIN="$tmpdir/docker" DOCKER_LOG="$preflight_log" \
    FAKE_STATE_DIR="$tmpdir/preflight-state" JOIN_DIR="$script_dir" \
    COMPOSE_FILE_PATH="$tmpdir/compose.yml" \
    EXISTING_CONTAINERS="proxy proxy-policy proxy-policy2 versiond edge-api" \
    ROUTER_HA_ENABLE_BIN="$tmpdir/enable-router" \
    GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-edge-api-0.2.16.sh" --edge-mode single \
    --compose-file "$tmpdir/compose.yml" --preflight-only \
    >"$tmpdir/missing-prerequisite.stdout" \
    2>"$tmpdir/missing-prerequisite.stderr"; then
    fail "preflight accepted a host without the 0.2.15 completion marker"
fi
grep -q '0.2.15 host update marker is missing' \
    "$tmpdir/missing-prerequisite.stderr" || fail \
    "missing prerequisite failure was not actionable"
mv "$tmpdir/.gonka-devshard-v5-upgrade-complete.saved" \
    "$tmpdir/.gonka-devshard-v5-upgrade-complete"
: >"$preflight_log"
DOCKER_BIN="$tmpdir/docker" DOCKER_LOG="$preflight_log" \
FAKE_STATE_DIR="$tmpdir/preflight-state" JOIN_DIR="$script_dir" \
COMPOSE_FILE_PATH="$tmpdir/compose.yml" \
EXISTING_CONTAINERS="proxy proxy-policy proxy-policy2 versiond edge-api" \
ROUTER_HA_ENABLE_BIN="$tmpdir/enable-router" \
GONKA_CONFIG_ENV="$tmpdir/config.env" \
    "$script_dir/upgrade-edge-api-0.2.16.sh" --edge-mode single \
    --compose-file "$tmpdir/compose.yml" --preflight-only >/dev/null
if grep -E -- ' compose .* (pull|up|stop|rm) ' "$preflight_log" >/dev/null; then
    fail "preflight mutated Compose services"
fi

multi_log=$tmpdir/multi.log
run_update multi "$multi_log" "$tmpdir/multi-state"
grep -q 'EDGE_API_IMAGE=ghcr.io/product-science/edge-api:0.2.16' \
    "$multi_log" || fail "host configuration replaced the release image contract"
for service in edge-api2 edge-api3 edge-api; do
    grep -Eq "up .*${service}[[:space:]]*$" "$multi_log" || fail \
        "$service was not replaced"
done
first=$(grep -nE 'up .*edge-api2[[:space:]]*$' "$multi_log" | head -n1 | cut -d: -f1)
second=$(grep -nE 'up .*edge-api3[[:space:]]*$' "$multi_log" | head -n1 | cut -d: -f1)
third=$(grep -nE 'up .*edge-api[[:space:]]*$' "$multi_log" | head -n1 | cut -d: -f1)
ingress=$(grep -n '^enable-router ' "$multi_log" | head -n1 | cut -d: -f1)
[[ $first -lt $second && $second -lt $third && $third -lt $ingress ]] || fail \
    "edge replicas were not prepared before ingress cutover"
grep -q '^enable-router .*--ingress-only' "$multi_log" || fail \
    "edge update did not isolate ingress work from the versiond fleet"
[[ $(grep -c ' :: cp .*legacy-router-upgrade-barrier.sh edge-api-router:' \
    "$multi_log") -eq 3 ]] || fail \
    "each edge replacement did not persist its nginx traffic barrier"
[[ $(grep -c ' :: exec edge-api-router rm -f /etc/gonka-upgrade-barrier' \
    "$multi_log") -eq 3 ]] || fail \
    "successful edge replacements did not clear their nginx barriers"
grep -q ' :: rm -f edge-api-router' "$multi_log" || fail \
    "retired edge-api-router was not removed"
jq -e '
    .release_id == "0.2.16" and
    .prerequisite_release_id == "0.2.15-devshard-v5" and
    .topology == "multi"
' "$tmpdir/complete.json" >/dev/null || fail \
    "completion marker does not identify the applied release chain"
assert_not_mutated "$multi_log" versiond
assert_not_mutated "$multi_log" devshard-postgres

failed_log=$tmpdir/failed.log
if run_update multi "$failed_log" "$tmpdir/failed-state" \
    FAIL_SERVICE=edge-api3; then
    fail "failed edge-api3 replacement unexpectedly succeeded"
fi
grep -q 'EDGE_API_IMAGE=gonka-upgrade-rollback/edge-api3:' "$failed_log" || fail \
    "failed edge-api3 was not restored"
grep -q 'http://127.0.0.1:18080/healthz' "$failed_log" || fail \
    "rollback did not use the legacy edge-api health contract"
grep -q ' :: image rm gonka-upgrade-rollback/edge-api3:' "$failed_log" || fail \
    "successful rollback retained its temporary image tag"
grep -q ' :: exec edge-api-router rm -f /etc/gonka-upgrade-barrier' \
    "$failed_log" || fail "failed replacement left its nginx barrier installed"
if grep -q '^enable-router ' "$failed_log"; then
    fail "ingress changed after an edge replica failed"
fi
if grep -q ' :: rm -f edge-api-router' "$failed_log"; then
    fail "legacy router was removed after an edge replica failed"
fi

single_log=$tmpdir/single.log
run_update single "$single_log" "$tmpdir/single-state" \
    EXISTING_CONTAINERS="proxy proxy-policy proxy-policy2 versiond edge-api"
grep -Eq 'up .*edge-api[[:space:]]*$' "$single_log" || fail \
    "single edge-api was not replaced"
if grep -q 'edge-api-router' "$single_log"; then
    if grep -E -- \
        ' :: (cp .*edge-api-router|exec .*edge-api-router|rm .*edge-api-router)' \
        "$single_log" >/dev/null; then
        fail "single-edge update mutated edge-api-router"
    fi
fi
assert_not_mutated "$single_log" versiond

echo "upgrade-edge-api-0.2.16_test: ok"
