#!/usr/bin/env bash

# Focused regression reproductions for updater failure modes that are not yet
# part of the normal green suite.
#
#   --repro  succeeds only while the selected bug is reproducible.
#   --gate   succeeds only when the acceptance invariant is satisfied.
#
# The gate is deliberately RED on affected revisions. Do not add it to CI
# until the corresponding production fix lands. Both modes execute the same
# scenario; only the expected outcome is inverted.

set -uo pipefail

source_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_dir=$(cd -- "$source_dir/../.." && pwd -P)

usage() {
    cat <<'EOF'
Usage: update-devshard-regression_test.sh --repro|--gate [SCENARIO ...]
       update-devshard-regression_test.sh --list

Modes:
  --repro  PASS when the current defect is reproduced
  --gate   PASS when the desired acceptance invariant holds

Scenarios:
  UPD-PROOF-HEADERS      storage proof accepts a BusyBox wget -S HTTP 200
  UPD-PROOF-FATAL        a v5 proof timeout/503 stops the updater
  UPD-DISCOVERY-SLOTS    router slot labels do not poison autodiscovery
  UPD-POLICY-BOOTSTRAP   the first policy worker has a resolvable proxy peer
  UPD-OFFLINE-RERUN      cached router images permit retry without a registry
  OPS-MAX-CONNECTIONS    the documented R=2,N=3,P=4 budget is at least 82

With no SCENARIO argument all scenarios run. The BusyBox scenario uses an
already-cached busybox:1.36 or busybox:latest image and never pulls it.
Override the choice with BUSYBOX_IMAGE.
EOF
}

scenario_ids=(
    UPD-PROOF-HEADERS
    UPD-PROOF-FATAL
    UPD-DISCOVERY-SLOTS
    UPD-POLICY-BOOTSTRAP
    UPD-OFFLINE-RERUN
    OPS-MAX-CONNECTIONS
)

if [[ ${1:-} == --list ]]; then
    printf '%s\n' "${scenario_ids[@]}"
    exit 0
fi
case ${1:-} in
    --repro) mode=repro ;;
    --gate) mode=gate ;;
    *) usage >&2; exit 2 ;;
esac
shift
selected=("$@")
if ((${#selected[@]} == 0)); then
    selected=("${scenario_ids[@]}")
fi
for requested in "${selected[@]}"; do
    [[ " ${scenario_ids[*]} " == *" $requested "* ]] || {
        echo "unknown scenario: $requested" >&2
        usage >&2
        exit 2
    }
done

tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

# Run the production updater from an isolated directory. Its Compose files are
# placeholders because fake Docker returns a rendered model directly.
script_dir=$tmpdir/join
mkdir -p "$script_dir/versiond-router-slot"
cp "$source_dir/update-devshard.sh" "$source_dir/deployment-lock.sh" "$script_dir/"
: >"$script_dir/docker-compose.yml"
: >"$script_dir/docker-compose.versiond.yml"
: >"$script_dir/versiond-router-slot/docker-compose.yml"
printf 'export KEY_NAME=regression\n' >"$tmpdir/config.env"

cat >"$tmpdir/single.json" <<'EOF'
{"name":"gonka-regression","services":{
  "versiond":{"image":"example/versiond:fixed","environment":{"GONKA_HA":"false"}},
  "proxy":{"image":"example/proxy-router:fixed","environment":{}},
  "proxy-policy":{"image":"example/proxy-policy:fixed","environment":{}},
  "proxy-policy2":{"image":"example/proxy-policy:fixed","environment":{}}
}}
EOF

cat >"$tmpdir/ha.json" <<'EOF'
{"name":"gonka-regression","services":{
  "versiond":{"image":"example/versiond:fixed","environment":{
    "GONKA_HA":"true","DEVSHARD_STORAGE_MODE":"postgres",
    "PGHOST":"postgres.example.invalid","PGDATABASE":"devshardd","PGUSER":"devshardd"
  }},
  "proxy":{"image":"example/proxy-router:fixed","environment":{}},
  "proxy-policy":{"image":"example/proxy-policy:fixed","environment":{}},
  "proxy-policy2":{"image":"example/proxy-policy:fixed","environment":{}}
},"networks":{
  "default":{"name":"gonka-regression-default"},
  "versiond-router-back":{"name":"gonka-regression-router-back"}
}}
EOF

# The fake is intentionally stateful for UPD-POLICY-BOOTSTRAP: a policy
# service cannot become healthy until starting/attaching the public proxy has
# made proxy-policy-ingress resolvable. Other scenarios use it only to answer
# deterministic Docker/Compose queries made by the real updater.
cat >"$tmpdir/docker" <<'EOF'
#!/usr/bin/env bash
set -uo pipefail

printf '%q ' "$@" >>"$FAKE_LOG"
printf '\n' >>"$FAKE_LOG"

known_container() {
    case $FAKE_SCENARIO:$1 in
        proof-headers:versiond | proof-fatal:versiond | discovery:versiond | discovery:gonka-versiond-router-0-router-1)
            return 0
            ;;
    esac
    return 1
}

case ${1:-} in
    info)
        exit 0
        ;;
    ps)
        if [[ $FAKE_SCENARIO == discovery ]]; then
            printf 'versiond\ngonka-versiond-router-0-router-1\n'
        elif [[ $FAKE_SCENARIO == proof-* ]]; then
            printf 'versiond\n'
        fi
        exit 0
        ;;
    inspect)
        name=${!#}
        known_container "$name" || {
            echo "Error response from daemon: No such object: $name" >&2
            exit 1
        }
        format=
        for ((i = 1; i <= $#; i++)); do
            if [[ ${!i} == --format && $i -lt $# ]]; then
                j=$((i + 1))
                format=${!j}
            fi
        done
        case $format in
            *working_dir*) printf '%s\n' "$FAKE_SCRIPT_DIR" ;;
            *config_files*)
                if [[ $name == gonka-versiond-router-0-router-1 ]]; then
                    printf 'versiond-router-slot/docker-compose.yml\n'
                else
                    printf 'docker-compose.yml\n'
                fi
                ;;
            *com.docker.compose.project\"*)
                if [[ $name == gonka-versiond-router-0-router-1 ]]; then
                    printf 'gonka-versiond-router-0\n'
                else
                    printf 'gonka-regression\n'
                fi
                ;;
            *com.docker.compose.service\"*)
                [[ $name == gonka-versiond-router-0-router-1 ]] && printf 'router\n' || printf 'versiond\n'
                ;;
            *ai.gonka.component\"*)
                [[ $name == gonka-versiond-router-0-router-1 ]] && printf 'versiond-router\n'
                ;;
            *ai.gonka.fleet\"*)
                [[ $name == gonka-versiond-router-0-router-1 ]] && printf 'gonka-versiond-router\n'
                ;;
            *.Image* | *Health*) printf 'healthy-image\n' ;;
        esac
        exit 0
        ;;
    network)
        case ${2:-} in
            inspect) exit 0 ;;
            connect)
                [[ " $* " == *" proxy-policy-ingress "* ]] && : >"$FAKE_STATE/proxy-alias"
                exit 0
                ;;
        esac
        ;;
    run)
        case " $* " in
            *"SELECT identity"*) printf 'db-1\n' ;;
            *"SELECT challenge"*) printf '%s\n' "${FAKE_NONCE:-}" ;;
            *) printf 'f\n' ;;
        esac
        exit 0
        ;;
    exec)
        case $FAKE_SCENARIO in
            proof-headers)
                if [[ " $* " == *storage-identity* ]]; then
                    if [[ " $* " == *" -S "* ]]; then
                        cat "$FAKE_PROOF_HEADER_FILE" >&2
                    fi
                    cat "$FAKE_PROOF_BODY_FILE"
                    exit 0
                fi
                ;;
            proof-fatal)
                if [[ " $* " == *storage-identity* ]]; then
                    printf '  HTTP/1.1 503 Service Unavailable\n' >&2
                    exit 1
                fi
                ;;
        esac
        exit 0
        ;;
    image)
        [[ ${2:-} == inspect ]] && exit 0
        ;;
    compose)
        case " $* " in
            *" version --short "*) printf '2.30.0\n'; exit 0 ;;
            *" config --format json "*)
                if [[ $FAKE_SCENARIO == proof-* ]]; then
                    cat "$FAKE_RENDERED_HA"
                else
                    cat "$FAKE_RENDERED_SINGLE"
                fi
                exit 0
                ;;
            *" ps --all --quiet "* | *" ps --quiet "*)
                service=${*: -1}
                if [[ $service == versiond && $FAKE_SCENARIO != policy ]]; then
                    printf 'cid-versiond\n'
                fi
                exit 0
                ;;
            *" up -d "*)
                service=${*: -1}
                if [[ $FAKE_SCENARIO == policy ]]; then
                    if [[ $service == proxy ]]; then
                        : >"$FAKE_STATE/proxy-alias"
                    elif [[ $service == proxy-policy || $service == proxy-policy2 ]]; then
                        if [[ ! -f $FAKE_STATE/proxy-alias ]]; then
                            echo "$service cannot resolve proxy-policy-ingress" >&2
                            exit 1
                        fi
                    fi
                fi
                exit 0
                ;;
        esac
        exit 0
        ;;
esac
exit 0
EOF
chmod +x "$tmpdir/docker"

LAST_STATUS=0
LAST_OUT=
LAST_ERR=
run_updater() {
    local scenario=$1 topology=$2 compose_source=$3
    local -a compose_env=() args=(--topology "$topology")
    : >"$tmpdir/log"
    rm -rf "$tmpdir/state"
    mkdir -p "$tmpdir/state"
    [[ $scenario == policy ]] || args=(--check "${args[@]}")
    if [[ $compose_source == explicit ]]; then
        compose_env=(
            "COMPOSE_FILE=$script_dir/docker-compose.yml:$script_dir/docker-compose.versiond.yml"
        )
    fi
    env -u COMPOSE_FILE -u COMPOSE_PATH_SEPARATOR \
        FAKE_SCENARIO="$scenario" \
        FAKE_LOG="$tmpdir/log" \
        FAKE_STATE="$tmpdir/state" \
        FAKE_SCRIPT_DIR="$script_dir" \
        FAKE_RENDERED_SINGLE="$tmpdir/single.json" \
        FAKE_RENDERED_HA="$tmpdir/ha.json" \
        FAKE_PROOF_HEADER_FILE="$tmpdir/proof-headers" \
        FAKE_PROOF_BODY_FILE="$tmpdir/proof-body" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        DOCKER_BIN="$tmpdir/docker" \
        UPDATE_POSTGRES_PROBE_SECONDS=5 \
        "${compose_env[@]}" \
        "$script_dir/update-devshard.sh" "${args[@]}" \
        >"$tmpdir/out" 2>"$tmpdir/err"
    LAST_STATUS=$?
    LAST_OUT=$(<"$tmpdir/out")
    LAST_ERR=$(<"$tmpdir/err")
}

SCENARIO_DETAIL=

# Detector return values: 0 = defect reproduced, 1 = invariant holds,
# 2 = harness/prerequisite error. The mode maps those observations to PASS.
detect_proof_headers() {
    local body headers image='' wire status
    if ! command -v docker >/dev/null 2>&1 || ! docker info >/dev/null 2>&1; then
        SCENARIO_DETAIL='Docker is required for the real BusyBox wire-format check'
        return 2
    fi
    if [[ -n ${BUSYBOX_IMAGE:-} ]]; then
        docker image inspect "$BUSYBOX_IMAGE" >/dev/null 2>&1 || {
            SCENARIO_DETAIL="BUSYBOX_IMAGE=$BUSYBOX_IMAGE is not cached (the test never pulls)"
            return 2
        }
        image=$BUSYBOX_IMAGE
    else
        for image in busybox:1.36 busybox:latest; do
            docker image inspect "$image" >/dev/null 2>&1 && break
            image=
        done
        [[ -n $image ]] || {
            SCENARIO_DETAIL='cache busybox:1.36 first, or set BUSYBOX_IMAGE (the test never pulls)'
            return 2
        }
    fi
    docker run --rm --pull=never "$image" sh -c '
        mkdir -p /tmp/www
        printf '\''{"identity":"db-1"}\n'\'' >/tmp/www/index.html
        /bin/busybox httpd -p 127.0.0.1:18080 -h /tmp/www
        /bin/busybox wget -qO- -S -T 5 http://127.0.0.1:18080/index.html
    ' >"$tmpdir/proof-body" 2>"$tmpdir/proof-headers"
    status=$?
    body=$(<"$tmpdir/proof-body")
    headers=$(<"$tmpdir/proof-headers")
    wire=$headers$'\n'$body
    if ((status != 0)) || [[ $headers != *"HTTP/1.1 200 OK"* ]] || [[ $body != *'{"identity":"db-1"}'* ]]; then
        SCENARIO_DETAIL="cached $image did not produce the expected BusyBox header-plus-body response"
        return 2
    fi
    if jq -e . <<<"$wire" >/dev/null 2>&1; then
        SCENARIO_DETAIL='BusyBox wire fixture unexpectedly parsed as one JSON document'
        return 2
    fi
    run_updater proof-headers ha explicit
    if ((LAST_STATUS == 0)); then
        SCENARIO_DETAIL="updater accepted the real $image header-plus-JSON response"
        return 1
    fi
    if [[ $LAST_ERR == *'returned a storage proof without an identity'* ]]; then
        SCENARIO_DETAIL='HTTP 200 headers were passed to jq and rejected before mutation'
        return 0
    fi
    SCENARIO_DETAIL="unexpected updater failure: ${LAST_ERR//$'\n'/ }"
    return 2
}

detect_proof_fatal() {
    local result
    run_updater proof-fatal ha explicit
    if ((LAST_STATUS == 0)) && [[ $LAST_OUT == *'Preflight passed; nothing was changed'* ]] && \
        [[ $LAST_ERR == *'cannot read the storage lineage'* ]]; then
        SCENARIO_DETAIL='proof HTTP 503 called fail in a subshell, then || continue allowed success'
        return 0
    fi
    if ((LAST_STATUS != 0)) && [[ $LAST_ERR == *'cannot read the storage lineage'* ]]; then
        SCENARIO_DETAIL='proof HTTP 503 stopped the updater as required'
        return 1
    fi
    result=${LAST_ERR:-$LAST_OUT}
    SCENARIO_DETAIL="unexpected updater result status=$LAST_STATUS: ${result//$'\n'/ }"
    return 2
}

detect_discovery_slots() {
    run_updater discovery auto labels
    if ((LAST_STATUS == 0)); then
        SCENARIO_DETAIL='main Compose model was discovered while router-slot labels were ignored'
        return 1
    fi
    if [[ $LAST_ERR == *'running containers record different Compose file lists'* ]] && \
        [[ $LAST_ERR == *'versiond-router-slot/docker-compose.yml'* ]]; then
        SCENARIO_DETAIL='router slot was treated as a main deployment container and made labels ambiguous'
        return 0
    fi
    SCENARIO_DETAIL="unexpected updater failure: ${LAST_ERR//$'\n'/ }"
    return 2
}

detect_policy_bootstrap() {
    run_updater policy single labels
    if ((LAST_STATUS == 0)) && [[ -f $tmpdir/state/proxy-alias ]]; then
        SCENARIO_DETAIL='proxy-policy-ingress existed before policy health was required'
        return 1
    fi
    if ((LAST_STATUS != 0)) && [[ $LAST_ERR == *'cannot resolve proxy-policy-ingress'* ]] && \
        [[ ! -f $tmpdir/state/proxy-alias ]]; then
        SCENARIO_DETAIL='updater started proxy-policy2 before anything published proxy-policy-ingress'
        return 0
    fi
    SCENARIO_DETAIL="unexpected updater result status=$LAST_STATUS: ${LAST_ERR//$'\n'/ }"
    return 2
}

detect_offline_rerun() {
    local fleet=$source_dir/versiond-router-fleet.sh updater=$source_dir/update-devshard.sh
    # This is deliberately a fast source-contract check. Runtime pull failure
    # coverage belongs in the fleet's stateful Docker harness: the host updater
    # must either choose a cache-safe policy or the fleet's default must be
    # cache-safe before it delegates `apply`.
    if grep -Eq 'pull_policy=\$\{VERSIOND_ROUTER_PULL_POLICY:-always\}' "$fleet" && \
        ! grep -Eq 'VERSIOND_ROUTER_PULL_POLICY=(missing|never)' "$updater"; then
        SCENARIO_DETAIL='updater delegates apply without an override and fleet defaults to pull --policy always'
        return 0
    fi
    if grep -Eq 'pull_policy=\$\{VERSIOND_ROUTER_PULL_POLICY:-(missing|never)\}' "$fleet" || \
        grep -Eq 'VERSIOND_ROUTER_PULL_POLICY=(missing|never)' "$updater"; then
        SCENARIO_DETAIL='default updater-to-fleet path uses cached images when the registry is unavailable'
        return 1
    fi
    SCENARIO_DETAIL='could not recognize an explicit updater/fleet pull-policy contract'
    return 2
}

detect_max_connections() {
    local doc=$repo_dir/devshard/docs/release-0.2.15-v5.md
    local corrected_formula='R \* \(2 \* N \* \(P \+ 2\) \+ 5\)|2 \* R \* N \* \(P \+ 2\) \+ R \* 5'
    if grep -Eq 'default pool that is 46 connections|2 \* N \* \(P \+ 2\) \+ R \* 5' "$doc"; then
        SCENARIO_DETAIL='documented example says 46; R*(2*N*(P+2)+5) requires 82'
        return 0
    fi
    if grep -Eq "$corrected_formula" "$doc" && \
        grep -Eq '82 (non-reserved )?connections|default pool that is (at least )?82' "$doc"; then
        SCENARIO_DETAIL='R=2,N=3,P=4 example budgets at least 82 connections'
        return 1
    fi
    SCENARIO_DETAIL='could not recognize either the defective or corrected capacity example'
    return 2
}

run_detector() {
    case $1 in
        UPD-PROOF-HEADERS) detect_proof_headers ;;
        UPD-PROOF-FATAL) detect_proof_fatal ;;
        UPD-DISCOVERY-SLOTS) detect_discovery_slots ;;
        UPD-POLICY-BOOTSTRAP) detect_policy_bootstrap ;;
        UPD-OFFLINE-RERUN) detect_offline_rerun ;;
        OPS-MAX-CONNECTIONS) detect_max_connections ;;
    esac
}

failures=0
for id in "${selected[@]}"; do
    SCENARIO_DETAIL=
    run_detector "$id"
    observed=$?
    if ((observed == 2)); then
        printf 'ERROR %-24s %s\n' "$id" "$SCENARIO_DETAIL" >&2
        failures=$((failures + 1))
    elif [[ $mode == repro && $observed == 0 ]]; then
        printf 'PASS  %-24s REPRODUCED: %s\n' "$id" "$SCENARIO_DETAIL"
    elif [[ $mode == gate && $observed == 1 ]]; then
        printf 'PASS  %-24s FIXED: %s\n' "$id" "$SCENARIO_DETAIL"
    elif [[ $mode == repro ]]; then
        printf 'FAIL  %-24s bug no longer reproduces: %s\n' "$id" "$SCENARIO_DETAIL" >&2
        failures=$((failures + 1))
    else
        printf 'FAIL  %-24s acceptance invariant violated: %s\n' "$id" "$SCENARIO_DETAIL" >&2
        failures=$((failures + 1))
    fi
done

if ((failures > 0)); then
    printf '%s: %d scenario(s) failed\n' "$mode" "$failures" >&2
    exit 1
fi
printf '%s: all %d selected scenario(s) passed\n' "$mode" "${#selected[@]}"
