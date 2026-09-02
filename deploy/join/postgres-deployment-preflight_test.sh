#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
preflight="$script_dir/postgres-deployment-preflight.sh"
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
    echo "postgres-deployment-preflight_test: $*" >&2
    exit 1
}

write_config() {
    local first_extra=${1:-} second_extra=${2:-${1:-}}
    local first_host=${3:-pg} second_host=${4:-${3:-pg}}
    local first_pool=${RENDERED_POOL_ONE:-4}
    local second_pool=${RENDERED_POOL_TWO:-$first_pool}
    cat >"$tmpdir/config.json" <<EOF
{"name":"preflight-test","services":{"versiond":{"environment":{"DEVSHARD_STORAGE_MODE":"postgres","PGHOST":"$first_host","PGPORT":"5432","PGDATABASE":"devshardd","PGUSER":"user","PG_POOL_MAX_CONNS":"$first_pool"$first_extra}},"versiond2":{"environment":{"DEVSHARD_STORAGE_MODE":"postgres","PGHOST":"$second_host","PGPORT":"5432","PGDATABASE":"devshardd","PGUSER":"user","PG_POOL_MAX_CONNS":"$second_pool"$second_extra}}}}
EOF
}

cat >"$tmpdir/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%q ' "$@" >>"$DOCKER_LOG"
printf '\n' >>"$DOCKER_LOG"
if [[ $1 == compose && ${*: -3} == "config --format json" ]]; then
    cat "$CONFIG_JSON"
elif [[ $1 == compose && ${*: -3:1} == ps && ${*: -2:1} == -q ]]; then
    service=${*: -1}
    case $LIVE_MODE:$service in
        both:versiond) printf 'container-1\n' ;;
        both:versiond2) printf 'container-2\n' ;;
        three:versiond) printf 'container-1\n' ;;
        three:versiond2) printf 'container-2\n' ;;
        three:versiond3) printf 'container-3\n' ;;
        one:versiond) printf 'container-1\n' ;;
    esac
elif [[ $1 == inspect ]]; then
    container=${*: -1}
    [[ $container != "$INSPECT_FAILURE_CONTAINER" ]] || {
        echo 'simulated Docker daemon inspect failure' >&2
        exit 1
    }
    runtime_host=pg
    [[ $container != container-2 ]] || runtime_host=$RUNTIME_HOST_TWO
    runtime_pool=$RUNTIME_POOL_ONE
    [[ $container != container-2 ]] || runtime_pool=$RUNTIME_POOL_TWO
    jq -cn \
        --arg host "$runtime_host" \
        --arg pool "$runtime_pool" \
        --arg extra "${RUNTIME_EXTRA:-}" '
        ["DEVSHARD_STORAGE_MODE=postgres", "PGHOST=" + $host, "PGPORT=5432",
         "PGDATABASE=devshardd", "PGUSER=user", "PG_POOL_MAX_CONNS=" + $pool]
        + (if $extra == "" then [] else [$extra] end)'
elif [[ $1 == exec ]]; then
    container=$2
    case $PROOF_API_MODE in
        ready) ;;
        404)
            echo '  HTTP/1.1 404 Not Found' >&2
            echo 'wget: server returned error: HTTP/1.1 404 Not Found' >&2
            exit 8
            ;;
        503)
            printf '{"error":"no stable HA children"}\n'
            echo '  HTTP/1.1 503 Service Unavailable' >&2
            echo 'wget: server returned error: HTTP/1.1 503 Service Unavailable' >&2
            exit 8
            ;;
        timeout)
            echo 'wget: download timed out' >&2
            exit 1
            ;;
        *) exit 1 ;;
    esac
    if [[ $container == container-1 ]]; then
        identity=$IDENTITY_ONE
        snapshot=snapshot-1
        generation=generation-1
        database=$DATABASE_ONE
        pool_max_connections=$PROOF_POOL_ONE
        server_max_connections=$SERVER_MAX_ONE
        server_reserved_connections=$SERVER_RESERVED_ONE
    else
        identity=$IDENTITY_TWO
        snapshot=snapshot-2
        generation=generation-2
        database=$DATABASE_TWO
        pool_max_connections=$PROOF_POOL_TWO
        server_max_connections=$SERVER_MAX_TWO
        server_reserved_connections=$SERVER_RESERVED_TWO
    fi
    endpoint=${*: -1}
    case $endpoint in
        */internal/storage-identity)
            if [[ $SNAPSHOT_DRIFT == true && -f $CHALLENGE_STATE_DIR/challenged ]]; then
                snapshot=$snapshot-drift
            fi
            if [[ $INVALID_PROOF == "$container" ]]; then
                printf '{"identity":"%s"}\n' "$identity"
            else
                jq -cn \
                    --arg identity "$identity" \
                    --arg snapshot "$snapshot" \
                    --arg generation_prefix "$generation" \
                    --argjson pool_max_connections "$pool_max_connections" \
                    --argjson server_max_connections "$server_max_connections" \
                    --argjson server_reserved_connections "$server_reserved_connections" \
                    --argjson children "$TARGETS_PER_REPLICA" '
                    {identity:$identity, children:$children, snapshot:$snapshot,
                     targets:[range(1; $children + 1)
                         | {generation:($generation_prefix + "-" + tostring), version:"v5",
                            pool_max_connections:$pool_max_connections,
                            server_max_connections:$server_max_connections,
                            server_reserved_connections:$server_reserved_connections}]}'
            fi
            ;;
        */internal/storage-challenge)
            payload=
            for ((index = 1; index <= $#; index++)); do
                if [[ ${!index} == --post-data ]]; then
                    next=$((index + 1))
                    payload=${!next}
                    break
                fi
            done
            [[ -n $payload ]] || exit 1
            operation=$(jq -er '.operation' <<<"$payload")
            nonce=$(jq -er '.nonce' <<<"$payload")
            [[ $(jq -er '.snapshot' <<<"$payload") == "$snapshot" ]] || exit 1
            generation=$(jq -er '.generation' <<<"$payload")
            found=false
            if [[ $operation == write ]]; then
                printf '%s\n' "$nonce" >"$CHALLENGE_STATE_DIR/$database"
                touch "$CHALLENGE_STATE_DIR/challenged"
                found=true
            elif [[ $operation == read && -f $CHALLENGE_STATE_DIR/$database && \
                $(<"$CHALLENGE_STATE_DIR/$database") == "$nonce" ]]; then
                found=true
            fi
            printf '%s %s %s\n' "$container" "$operation" "$generation" \
                >>"$CHALLENGE_LOG"
            jq -cn \
                --arg identity "$identity" \
                --arg snapshot "$snapshot" \
                --arg generation "$generation" \
                --argjson found "$found" \
                '{identity:$identity,found:$found,children:1,snapshot:$snapshot,generation:$generation}'
            ;;
        *) exit 1 ;;
    esac
else
    exit 1
fi
EOF
chmod +x "$tmpdir/docker"
mkdir "$tmpdir/challenge-state"

run_preflight() {
    : >"$tmpdir/docker.log"
    : >"$tmpdir/challenge.log"
    find "$tmpdir/challenge-state" -type f -delete
    DOCKER_BIN="$tmpdir/docker" DOCKER_LOG="$tmpdir/docker.log" \
        CONFIG_JSON="$tmpdir/config.json" IDENTITY_ONE="${IDENTITY_ONE:-db-1}" \
        IDENTITY_TWO="${IDENTITY_TWO:-db-1}" RUNTIME_EXTRA="${RUNTIME_EXTRA:-}" \
        DATABASE_ONE="${DATABASE_ONE:-shared}" DATABASE_TWO="${DATABASE_TWO:-shared}" \
        RUNTIME_HOST_TWO="${RUNTIME_HOST_TWO:-pg}" \
        RUNTIME_POOL_ONE="${RUNTIME_POOL_ONE:-4}" \
        RUNTIME_POOL_TWO="${RUNTIME_POOL_TWO:-4}" \
        PROOF_POOL_ONE="${PROOF_POOL_ONE:-4}" \
        PROOF_POOL_TWO="${PROOF_POOL_TWO:-4}" \
        SERVER_MAX_ONE="${SERVER_MAX_ONE:-100}" \
        SERVER_MAX_TWO="${SERVER_MAX_TWO:-100}" \
        SERVER_RESERVED_ONE="${SERVER_RESERVED_ONE:-3}" \
        SERVER_RESERVED_TWO="${SERVER_RESERVED_TWO:-3}" \
        LIVE_MODE="${LIVE_MODE:-both}" INVALID_PROOF="${INVALID_PROOF:-none}" \
        SNAPSHOT_DRIFT="${SNAPSHOT_DRIFT:-false}" \
        PROOF_API_MODE="${PROOF_API_MODE:-ready}" \
        INSPECT_FAILURE_CONTAINER="${INSPECT_FAILURE_CONTAINER:-none}" \
        TARGETS_PER_REPLICA="${TARGETS_PER_REPLICA:-1}" \
        CHALLENGE_STATE_DIR="$tmpdir/challenge-state" \
        CHALLENGE_LOG="$tmpdir/challenge.log" \
        "$preflight" "$@" -- -f base.yml -f ha.yml
}

write_config
run_preflight --expected-identity db-1 >"$tmpdir/ok"
grep -qx db-1 "$tmpdir/ok" || fail "matching identity was not returned"
! grep -Eq '(^| )((up|start|stop|rm|down|create|update))( |$)' "$tmpdir/docker.log" ||
    fail "preflight attempted a deployment mutation"
[[ $(grep -c ' write ' "$tmpdir/challenge.log") -eq 2 ]] || fail \
    "preflight did not write through every generation"
[[ $(grep -c ' read ' "$tmpdir/challenge.log") -eq 4 ]] || fail \
    "preflight did not connect every writer and reader to the anchor generation"

if run_preflight --expected-identity '' >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "an explicitly empty expected identity was accepted"
fi
grep -q -- '--expected-identity requires a non-empty value' "$tmpdir/err" || fail \
    "empty expected identity was not diagnosed"
[[ ! -s $tmpdir/docker.log ]] || fail \
    "empty expected identity reached Docker before argument validation"

TARGETS_PER_REPLICA=2
export TARGETS_PER_REPLICA
run_preflight >"$tmpdir/linear-proof"
[[ $(grep -c ' write ' "$tmpdir/challenge.log") -eq 4 ]] || fail \
    "linear proof did not write through every generation"
[[ $(grep -c ' read ' "$tmpdir/challenge.log") -eq 8 ]] || fail \
    "storage proof did not remain linear as generations increased"

SERVER_MAX_ONE=55
SERVER_MAX_TWO=55
export SERVER_MAX_ONE SERVER_MAX_TWO
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "a connection budget covering only one rolling generation per replica was accepted"
fi
grep -q 'connection budget is insufficient: 58 required for 4 current generations' \
    "$tmpdir/err" || fail "all-version rolling overlap was not included in the connection budget"

SERVER_MAX_ONE=61
SERVER_MAX_TWO=61
run_preflight >"$tmpdir/exact-capacity"
grep -qx db-1 "$tmpdir/exact-capacity" || fail \
    "the exact all-version rolling connection budget was rejected"
unset TARGETS_PER_REPLICA SERVER_MAX_ONE SERVER_MAX_TWO

RENDERED_POOL_ONE=0
export RENDERED_POOL_ONE
write_config
if run_preflight --compose-only >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "a non-positive rendered PostgreSQL pool limit was accepted"
fi
unset RENDERED_POOL_ONE
grep -q 'must set PG_POOL_MAX_CONNS to a positive integer' "$tmpdir/err" || fail \
    "invalid rendered PostgreSQL pool limit was not diagnosed"

RENDERED_POOL_TWO=8
export RENDERED_POOL_TWO
write_config
if run_preflight --compose-only >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "different rendered PostgreSQL pool limits were accepted"
fi
unset RENDERED_POOL_TWO
grep -q 'must use the same PG_POOL_MAX_CONNS' "$tmpdir/err" || fail \
    "rendered PostgreSQL pool-limit mismatch was not diagnosed"
write_config

if run_preflight --expected-identity other-database \
    >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "an unexpected live database lineage was accepted"
fi
grep -q 'changed from other-database to db-1' "$tmpdir/err" || fail \
    "expected-identity failure was not diagnosed"

for key in DATABASE_URL PGHOSTADDR PGSERVICE PGSERVICEFILE PGOPTIONS; do
    write_config ",\"$key\":\"override\""
    if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
        fail "rendered $key bypass was accepted"
    fi
    grep -q "$key" "$tmpdir/err" || fail \
        "rendered $key failure was not diagnosed"

    write_config
    RUNTIME_EXTRA="$key=override"
    export RUNTIME_EXTRA
    if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
        fail "running $key bypass was accepted"
    fi
    unset RUNTIME_EXTRA
    grep -q "$key" "$tmpdir/err" || fail \
        "runtime $key failure was not diagnosed"
done

write_config '' '' pg pg-other
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "different rendered PostgreSQL hosts were accepted"
fi
grep -q 'same non-empty PGHOST' "$tmpdir/err" || fail \
    "rendered-host mismatch was not diagnosed"

write_config
RUNTIME_HOST_TWO=pg-other
export RUNTIME_HOST_TWO
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "runtime PostgreSQL host drift was accepted"
fi
unset RUNTIME_HOST_TWO
grep -q "running versiond2 has PGHOST='pg-other'" "$tmpdir/err" || fail \
    "runtime-host mismatch was not diagnosed"

RUNTIME_POOL_TWO=8
export RUNTIME_POOL_TWO
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "runtime PostgreSQL pool-limit drift was accepted"
fi
unset RUNTIME_POOL_TWO
grep -q "running versiond2 has PG_POOL_MAX_CONNS='8'" "$tmpdir/err" || fail \
    "runtime PostgreSQL pool-limit mismatch was not diagnosed"

INSPECT_FAILURE_CONTAINER=container-1
export INSPECT_FAILURE_CONTAINER
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "a Docker inspect failure was accepted as missing runtime variables"
fi
unset INSPECT_FAILURE_CONTAINER
grep -q 'cannot inspect runtime environment for versiond container container-1' \
    "$tmpdir/err" || fail "Docker inspect failure was not diagnosed"

IDENTITY_TWO=db-2
export IDENTITY_TWO
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "different live database identities were accepted"
fi
unset IDENTITY_TWO
grep -q 'different PostgreSQL database lineages' "$tmpdir/err" ||
    fail "identity mismatch was not diagnosed"

PROOF_POOL_TWO=8
export PROOF_POOL_TWO
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "a child pool larger than the rendered contract was accepted"
fi
unset PROOF_POOL_TWO
grep -q 'reports PostgreSQL pool capacity 8' "$tmpdir/err" || fail \
    "live child-pool mismatch was not diagnosed"

SERVER_MAX_TWO=99
export SERVER_MAX_TWO
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "different PostgreSQL server capacities were accepted"
fi
unset SERVER_MAX_TWO
grep -q 'server capacity differs between HA generations' "$tmpdir/err" || fail \
    "PostgreSQL server-capacity mismatch was not diagnosed"

SERVER_MAX_ONE=35
SERVER_MAX_TWO=35
export SERVER_MAX_ONE SERVER_MAX_TWO
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "an insufficient PostgreSQL connection budget was accepted"
fi
unset SERVER_MAX_ONE SERVER_MAX_TWO
grep -q 'connection budget is insufficient: 34 required' "$tmpdir/err" || fail \
    "insufficient PostgreSQL connection budget was not diagnosed"

DATABASE_TWO=clone
export DATABASE_TWO
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "independent databases with a cloned identity passed the live challenge"
fi
unset DATABASE_TWO
grep -q 'cannot observe the challenge' "$tmpdir/err" || fail \
    "independent-database failure was not diagnosed"
grep -q 'serialize preflight runs and retry' "$tmpdir/err" || fail \
    "concurrent-preflight ambiguity was not diagnosed"

SNAPSHOT_DRIFT=true
export SNAPSHOT_DRIFT
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "a generation change during the challenge was accepted"
fi
unset SNAPSHOT_DRIFT
grep -q 'generation snapshot changed' "$tmpdir/err" || fail \
    "generation-snapshot failure was not diagnosed"

INVALID_PROOF=container-2
export INVALID_PROOF
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "a malformed storage proof was accepted"
fi
unset INVALID_PROOF
grep -q 'invalid PostgreSQL storage proof' "$tmpdir/err" || fail \
    "malformed-proof failure was not diagnosed"

for proof_api_mode in 404 503 timeout; do
    PROOF_API_MODE=$proof_api_mode
    export PROOF_API_MODE
    if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
        fail "storage-proof endpoint HTTP $proof_api_mode was accepted"
    fi
    case $proof_api_mode in
        404) expected_error='does not expose the live storage-proof API' ;;
        503) expected_error="not ready (HTTP 503); inspect 'docker logs container-1'" ;;
        timeout) expected_error='storage identity timed out' ;;
    esac
    grep -Fq "$expected_error" "$tmpdir/err" || fail \
        "storage-proof $proof_api_mode was not diagnosed"
    if [[ $proof_api_mode == 503 ]]; then
        grep -Fq 'HTTP/1.1 503 Service Unavailable' "$tmpdir/err" || fail \
            "storage-proof HTTP 503 diagnostics were not preserved"
    fi
done
unset PROOF_API_MODE

LIVE_MODE=none
export LIVE_MODE
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "live preflight passed without versiond replicas"
fi
grep -q "Compose project 'preflight-test' has no running versiond replicas" \
    "$tmpdir/err" || fail "missing-project replicas were not diagnosed"
if run_preflight --expected-identity db-1 >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "--expected-identity passed without a live identity"
fi
grep -q "has no running versiond replicas" "$tmpdir/err" || fail \
    "missing expected identity was not diagnosed"
run_preflight --compose-only >"$tmpdir/compose-only"
grep -q "rendered PostgreSQL contract is valid for Compose project 'preflight-test'" \
    "$tmpdir/compose-only" || fail "explicit Compose-only validation failed"
! grep -q ' ps -q ' "$tmpdir/docker.log" || fail \
    "Compose-only validation inspected runtime containers"
if run_preflight --compose-only --expected-identity db-1 \
    >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "--compose-only accepted --expected-identity"
fi
grep -q -- '--expected-identity requires the live PostgreSQL proof' "$tmpdir/err" ||
    fail "incompatible Compose-only options were not diagnosed"
unset LIVE_MODE

LIVE_MODE=one
export LIVE_MODE
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "preflight passed with only one versiond replica"
fi
unset LIVE_MODE
grep -q 'has only 1 of 2 versiond replicas running' "$tmpdir/err" || fail \
    "partial-replica failure was not diagnosed"

write_config
jq 'del(.services.versiond2)' "$tmpdir/config.json" >"$tmpdir/config-without-versiond2.json"
mv "$tmpdir/config-without-versiond2.json" "$tmpdir/config.json"
if run_preflight --compose-only >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "Compose topology without versiond2 was accepted"
fi
grep -q 'needs at least two versiond services' "$tmpdir/err" || fail \
    "missing versiond2 service was not diagnosed"

# Three local replicas: every service is discovered, checked, and challenged.
write_config
jq '.services.versiond3 = .services.versiond2' "$tmpdir/config.json" >"$tmpdir/config-three.json"
mv "$tmpdir/config-three.json" "$tmpdir/config.json"
LIVE_MODE=three run_preflight >"$tmpdir/out" 2>"$tmpdir/err" || \
    fail "three-replica preflight failed: $(cat "$tmpdir/err")"
unset LIVE_MODE
grep -q '^container-3 write ' "$tmpdir/challenge.log" || fail \
    "the third replica was not challenged"
[[ $(<"$tmpdir/out") == db-1 ]] || fail \
    "three-replica preflight did not print the shared identity"

real_docker_bin=${REAL_DOCKER_BIN:-docker}
command -v "$real_docker_bin" >/dev/null 2>&1 || fail \
    "$real_docker_bin is required for the real Compose render test"
(
    cd "$script_dir"
    DEVSHARD_POSTGRES_PASSWORD=preflight-test \
        DOCKER_BIN="$real_docker_bin" "$preflight" --compose-only -- \
        --project-name postgres-preflight-render-test \
        -f docker-compose.yml -f docker-compose.versiond.yml
) >"$tmpdir/real-compose" 2>"$tmpdir/real-compose-err" || {
    cat "$tmpdir/real-compose-err" >&2
    fail "the real join Compose topology did not pass configuration validation"
}
grep -q "Compose project 'postgres-preflight-render-test'" "$tmpdir/real-compose" ||
    fail "the real Compose project name was not preserved"

echo "postgres-deployment-preflight_test: ok"
