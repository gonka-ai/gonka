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
    local postgres_service=
    if [[ ${INCLUDE_POSTGRES:-false} == true ]]; then
        postgres_service=',"devshard-postgres":{"environment":{'\
'"POSTGRES_DB":"'"${RENDERED_POSTGRES_DB:-devshardd}"'",'\
'"POSTGRES_USER":"'"${RENDERED_POSTGRES_USER:-user}"'",'\
'"POSTGRES_PASSWORD":"'"${RENDERED_POSTGRES_PASSWORD:-secret}"'"}}'
    fi
    cat >"$tmpdir/config.json" <<EOF
{"name":"preflight-test","services":{"versiond":{"deploy":{"replicas":${VERSIOND_REPLICAS:-1}},"environment":{"DEVSHARD_STORAGE_MODE":"postgres","PGHOST":"$first_host","PGPORT":"${RENDERED_PORT_ONE:-5432}","PGDATABASE":"devshardd","PGUSER":"user","PGPASSWORD":"${RENDERED_PASSWORD_ONE:-secret}","PG_POOL_MAX_CONNS":"$first_pool"$first_extra}},"versiond2":{"deploy":{"replicas":${VERSIOND2_REPLICAS:-1}},"environment":{"DEVSHARD_STORAGE_MODE":"postgres","PGHOST":"$second_host","PGPORT":"${RENDERED_PORT_TWO:-${RENDERED_PORT_ONE:-5432}}","PGDATABASE":"devshardd","PGUSER":"user","PGPASSWORD":"${RENDERED_PASSWORD_TWO:-${RENDERED_PASSWORD_ONE:-secret}}","PG_POOL_MAX_CONNS":"$second_pool"$second_extra}}$postgres_service}}
EOF
}

cat >"$tmpdir/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%q ' "$@" >>"$DOCKER_LOG"
printf '\n' >>"$DOCKER_LOG"
if [[ $1 == compose && ${*: -3} == "config --format json" ]]; then
    cat "$CONFIG_JSON"
elif [[ $1 == compose && " $* " == *" ps "* && " $* " == *" -q "* ]]; then
    service=${*: -1}
    case $LIVE_MODE:$service in
        both:versiond) printf 'container-1\n' ;;
        both:versiond2) printf 'container-2\n' ;;
        both:devshard-postgres) printf 'postgres-container\n' ;;
        one:versiond) printf 'container-1\n' ;;
        one:devshard-postgres) printf 'postgres-container\n' ;;
        postgres:devshard-postgres) printf 'postgres-container\n' ;;
    esac
elif [[ $1 == inspect ]]; then
    container=${*: -1}
    [[ $container != "$INSPECT_FAILURE_CONTAINER" ]] || {
        echo 'simulated Docker daemon inspect failure' >&2
        exit 1
    }
    if [[ ${2:-} == --format && ${3:-} == '{{.State.Running}}' && \
        $container == postgres-container ]]; then
        printf '%s\n' "${RUNTIME_POSTGRES_RUNNING:-true}"
    elif [[ $container == postgres-container ]]; then
        jq -cn \
            --arg database "$RUNTIME_POSTGRES_DB" \
            --arg user "$RUNTIME_POSTGRES_USER" \
            --arg password "$RUNTIME_POSTGRES_PASSWORD" '
            ["POSTGRES_DB=" + $database, "POSTGRES_USER=" + $user,
             "POSTGRES_PASSWORD=" + $password]'
    else
        runtime_host=$RUNTIME_HOST_ONE
        [[ $container != container-2 ]] || runtime_host=$RUNTIME_HOST_TWO
        runtime_pool=$RUNTIME_POOL_ONE
        [[ $container != container-2 ]] || runtime_pool=$RUNTIME_POOL_TWO
        runtime_password=$RUNTIME_PASSWORD_ONE
        [[ $container != container-2 ]] || runtime_password=$RUNTIME_PASSWORD_TWO
        jq -cn \
            --arg host "$runtime_host" \
            --arg pool "$runtime_pool" \
            --arg password "$runtime_password" \
            --arg extra "${RUNTIME_EXTRA:-}" '
            ["DEVSHARD_STORAGE_MODE=postgres", "PGHOST=" + $host,
             "PGPORT=5432", "PGDATABASE=devshardd", "PGUSER=user",
             "PGPASSWORD=" + $password]
            + (if $pool == "" then [] else ["PG_POOL_MAX_CONNS=" + $pool] end)
            + (if $extra == "" then [] else [$extra] end)'
    fi
elif [[ $1 == exec ]]; then
    container=$2
    if [[ $container == --env ]]; then
        container=
        for argument in "$@"; do
            [[ $argument != postgres-container ]] || container=$argument
        done
    fi
    if [[ $container == postgres-container ]]; then
        [[ ${FRESH_PSQL_FAIL:-false} != true ]] || exit 1
        printf '%s|%s|%s|%s|%s\n' "$RUNTIME_POSTGRES_DB" \
            "$RUNTIME_POSTGRES_USER" "${RUNTIME_POSTGRES_PORT:-5432}" \
            "${SERVER_MAX_ONE:-100}" "${SERVER_RESERVED_ONE:-3}"
        exit 0
    fi
    if [[ ${*: -1} == nproc ]]; then
        printf '%s\n' "${RUNTIME_CPU_COUNT:-4}"
        exit 0
    fi
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
    if [[ $endpoint == */internal/storage-* ]]; then
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
    fi
    case $endpoint in
        */healthz)
            printf '[{"name":"v3","status":"running"},'\
'{"name":"v4","status":"running"}]\n'
            ;;
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
        RUNTIME_HOST_ONE="${RUNTIME_HOST_ONE:-pg}" \
        RUNTIME_POOL_ONE="${RUNTIME_POOL_ONE-4}" \
        RUNTIME_POOL_TWO="${RUNTIME_POOL_TWO-4}" \
        RUNTIME_PASSWORD_ONE="${RUNTIME_PASSWORD_ONE:-secret}" \
        RUNTIME_PASSWORD_TWO="${RUNTIME_PASSWORD_TWO:-secret}" \
        RUNTIME_POSTGRES_DB="${RUNTIME_POSTGRES_DB:-devshardd}" \
        RUNTIME_POSTGRES_USER="${RUNTIME_POSTGRES_USER:-user}" \
        RUNTIME_POSTGRES_PASSWORD="${RUNTIME_POSTGRES_PASSWORD:-secret}" \
        RUNTIME_POSTGRES_PORT="${RUNTIME_POSTGRES_PORT:-5432}" \
        RUNTIME_POSTGRES_RUNNING="${RUNTIME_POSTGRES_RUNNING:-true}" \
        RUNTIME_CPU_COUNT="${RUNTIME_CPU_COUNT:-4}" \
        FRESH_PSQL_FAIL="${FRESH_PSQL_FAIL:-false}" \
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

RENDERED_PASSWORD_TWO=other-secret
export RENDERED_PASSWORD_TWO
write_config
if run_preflight --compose-only >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "different rendered PostgreSQL passwords were accepted"
fi
unset RENDERED_PASSWORD_TWO
grep -q 'same non-empty PGPASSWORD' "$tmpdir/err" || fail \
    "rendered PostgreSQL password mismatch was not diagnosed"
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

RUNTIME_POOL_ONE=
RUNTIME_POOL_TWO=
export RUNTIME_POOL_ONE RUNTIME_POOL_TWO
run_preflight --runtime-contract-only >"$tmpdir/out" 2>"$tmpdir/err" || fail \
    "v4 replicas without PG_POOL_MAX_CONNS were rejected"

INCLUDE_POSTGRES=true
RUNTIME_HOST_ONE=devshard-postgres
RUNTIME_HOST_TWO=devshard-postgres
PROOF_API_MODE=404
export INCLUDE_POSTGRES RUNTIME_HOST_ONE RUNTIME_HOST_TWO PROOF_API_MODE
write_config '' '' devshard-postgres
run_preflight --runtime-contract-only \
    >"$tmpdir/legacy-runtime" 2>"$tmpdir/legacy-runtime-err" || fail \
    "official v4 runtime without the storage-proof API was rejected"
grep -q 'supported legacy runtime contract' \
    "$tmpdir/legacy-runtime-err" || fail \
    "legacy storage-proof fallback was not reported"
grep -q 'exec container-1 /bin/busybox nproc' \
    "$tmpdir/docker.log" || fail \
    "legacy pool capacity was not derived from the running container"
unset INCLUDE_POSTGRES RUNTIME_HOST_ONE RUNTIME_HOST_TWO PROOF_API_MODE
write_config

PROOF_POOL_ONE=16
PROOF_POOL_TWO=16
SERVER_MAX_ONE=60
SERVER_MAX_TWO=60
export PROOF_POOL_ONE PROOF_POOL_TWO SERVER_MAX_ONE SERVER_MAX_TWO
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "a CPU-sized v4 pool was undercounted as the v5 default"
fi
grep -q 'connection budget is insufficient: 58 required' "$tmpdir/err" || fail \
    "CPU-sized v4 pool was not included in rolling overlap capacity"
if run_preflight --runtime-contract-only \
    >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "runtime preflight skipped a CPU-sized overlap budget failure"
fi
grep -q 'connection budget is insufficient: 58 required' "$tmpdir/err" || fail \
    "runtime contract did not enforce the rolling overlap budget"
SERVER_MAX_ONE=61
SERVER_MAX_TWO=61
run_preflight >"$tmpdir/cpu-sized-pool" || fail \
    "exact capacity for CPU-sized v4 pools was rejected"
unset PROOF_POOL_ONE PROOF_POOL_TWO SERVER_MAX_ONE SERVER_MAX_TWO
unset RUNTIME_POOL_ONE RUNTIME_POOL_TWO

INCLUDE_POSTGRES=true
RUNTIME_HOST_ONE=devshard-postgres
RUNTIME_HOST_TWO=devshard-postgres
export INCLUDE_POSTGRES RUNTIME_HOST_ONE RUNTIME_HOST_TWO
write_config '' '' devshard-postgres
run_preflight --runtime-contract-only >"$tmpdir/local-contract" || fail \
    "matching bundled PostgreSQL identity and credentials were rejected"

LIVE_MODE=postgres
export LIVE_MODE
run_preflight --runtime-contract-only >"$tmpdir/postgres-only-contract" || fail \
    "fresh bundled login was not accepted as recovery evidence"
FRESH_PSQL_FAIL=true
export FRESH_PSQL_FAIL
if run_preflight --runtime-contract-only >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "environment-only PostgreSQL credentials passed without a fresh login"
fi
unset FRESH_PSQL_FAIL LIVE_MODE
grep -q 'cannot open a fresh bundled PostgreSQL session' "$tmpdir/err" || fail \
    "failed bundled credential proof was not diagnosed"

RENDERED_PORT_ONE=5433
RENDERED_PORT_TWO=5433
export RENDERED_PORT_ONE RENDERED_PORT_TWO
write_config '' '' devshard-postgres
if run_preflight --compose-only >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "a non-server bundled PostgreSQL port was accepted"
fi
unset RENDERED_PORT_ONE RENDERED_PORT_TWO
grep -q 'PGPORT must be 5432' "$tmpdir/err" || fail \
    "bundled PostgreSQL port mismatch was not diagnosed"
write_config '' '' devshard-postgres

RUNTIME_POSTGRES_USER=other-user
export RUNTIME_POSTGRES_USER
if run_preflight --runtime-contract-only >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "runtime POSTGRES_USER drift was accepted"
fi
unset RUNTIME_POSTGRES_USER
grep -q "has POSTGRES_USER='other-user'" "$tmpdir/err" || fail \
    "runtime PostgreSQL user drift was not diagnosed"

RENDERED_POSTGRES_PASSWORD=other-secret
export RENDERED_POSTGRES_PASSWORD
write_config '' '' devshard-postgres
if run_preflight --compose-only >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "bundled PostgreSQL password different from versiond was accepted"
fi
unset RENDERED_POSTGRES_PASSWORD INCLUDE_POSTGRES
unset RUNTIME_HOST_ONE RUNTIME_HOST_TWO
grep -q 'POSTGRES_PASSWORD must match versiond PGPASSWORD' "$tmpdir/err" || fail \
    "rendered bundled PostgreSQL password mismatch was not diagnosed"
write_config

RUNTIME_PASSWORD_TWO=old-secret
export RUNTIME_PASSWORD_TWO
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "a PostgreSQL credential change was accepted inside the HA upgrade"
fi
unset RUNTIME_PASSWORD_TWO
grep -q 'rotate credentials separately before the HA upgrade' "$tmpdir/err" || fail \
    "runtime PostgreSQL credential drift was not diagnosed"

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
grep -q 'has no running versiond2 replica' "$tmpdir/err" || fail \
    "partial-replica failure was not diagnosed"

write_config
jq 'del(.services.versiond2)' "$tmpdir/config.json" >"$tmpdir/config-without-versiond2.json"
mv "$tmpdir/config-without-versiond2.json" "$tmpdir/config.json"
if run_preflight --compose-only >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "Compose topology without versiond2 was accepted"
fi
grep -q 'Compose topology has no versiond2 service' "$tmpdir/err" || fail \
    "missing versiond2 service was not diagnosed"

write_config
VERSIOND2_REPLICAS=0
export VERSIOND2_REPLICAS
write_config
run_preflight --compose-only >"$tmpdir/decommissioned-compose" || fail \
    "documented VERSIOND2_REPLICAS=0 topology was rejected"
run_preflight >"$tmpdir/decommissioned-live" || fail \
    "live proof rejected a permanently decommissioned versiond2"
[[ $(grep -c ' write ' "$tmpdir/challenge.log") -eq 1 ]] || fail \
    "decommissioned versiond2 was included as a challenge writer"
[[ $(grep -c ' read ' "$tmpdir/challenge.log") -eq 2 ]] || fail \
    "single-member storage challenge did not complete"
unset VERSIOND2_REPLICAS

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
