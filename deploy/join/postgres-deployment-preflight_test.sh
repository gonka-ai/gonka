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
    cat >"$tmpdir/config.json" <<EOF
{"services":{"versiond":{"environment":{"DEVSHARD_STORAGE_MODE":"postgres","PGHOST":"$first_host","PGPORT":"5432","PGDATABASE":"devshardd","PGUSER":"user"$first_extra}},"versiond2":{"environment":{"DEVSHARD_STORAGE_MODE":"postgres","PGHOST":"$second_host","PGPORT":"5432","PGDATABASE":"devshardd","PGUSER":"user"$second_extra}}}}
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
        one:versiond) printf 'container-1\n' ;;
    esac
elif [[ $1 == inspect ]]; then
    container=${*: -1}
    runtime_host=pg
    [[ $container != container-2 ]] || runtime_host=$RUNTIME_HOST_TWO
    printf '%s\n' \
        DEVSHARD_STORAGE_MODE=postgres "PGHOST=$runtime_host" PGPORT=5432 \
        PGDATABASE=devshardd PGUSER=user "${RUNTIME_EXTRA:-}"
elif [[ $1 == exec ]]; then
    container=$2
    if [[ $container == container-1 ]]; then
        identity=$IDENTITY_ONE
        snapshot=snapshot-1
        generation=generation-1
        database=$DATABASE_ONE
    else
        identity=$IDENTITY_TWO
        snapshot=snapshot-2
        generation=generation-2
        database=$DATABASE_TWO
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
                    --arg generation "$generation" \
                    '{identity:$identity,children:1,snapshot:$snapshot,targets:[{generation:$generation,version:"v5"}]}'
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
            [[ $(jq -er '.generation' <<<"$payload") == "$generation" ]] || exit 1
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
        LIVE_MODE="${LIVE_MODE:-both}" INVALID_PROOF="${INVALID_PROOF:-none}" \
        SNAPSHOT_DRIFT="${SNAPSHOT_DRIFT:-false}" \
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
    "preflight did not read every writer challenge through every generation"

if run_preflight --expected-identity other-database \
    >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "an unexpected live database lineage was accepted"
fi
grep -q 'changed from other-database to db-1' "$tmpdir/err" || fail \
    "expected-identity failure was not diagnosed"

for key in DATABASE_URL PGSERVICE PGSERVICEFILE PGOPTIONS; do
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

IDENTITY_TWO=db-2
export IDENTITY_TWO
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "different live database identities were accepted"
fi
unset IDENTITY_TWO
grep -q 'different PostgreSQL database lineages' "$tmpdir/err" ||
    fail "identity mismatch was not diagnosed"

DATABASE_TWO=clone
export DATABASE_TWO
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "independent databases with a cloned identity passed the live challenge"
fi
unset DATABASE_TWO
grep -q 'cannot observe the challenge' "$tmpdir/err" || fail \
    "independent-database failure was not diagnosed"

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

LIVE_MODE=none
export LIVE_MODE
run_preflight >"$tmpdir/no-live"
grep -q 'no live replicas to compare' "$tmpdir/no-live" || fail \
    "configuration-only preflight did not explain the missing replicas"
if run_preflight --require-live >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "--require-live passed without versiond replicas"
fi
unset LIVE_MODE
grep -q 'cannot prove shared PostgreSQL storage' "$tmpdir/err" || fail \
    "required-live failure was not diagnosed"

LIVE_MODE=one
export LIVE_MODE
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "preflight passed with only one versiond replica"
fi
unset LIVE_MODE
grep -q 'only one versiond replica is running' "$tmpdir/err" || fail \
    "partial-replica failure was not diagnosed"

echo "postgres-deployment-preflight_test: ok"
