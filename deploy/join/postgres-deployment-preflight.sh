#!/usr/bin/env bash

set -Eeuo pipefail

docker_bin=${DOCKER_BIN:-docker}
expected_identity=
compose_only=false
compose_args=()
forbidden_libpq=(DATABASE_URL PGHOSTADDR PGSERVICE PGSERVICEFILE PGOPTIONS)
versiond_lookup_pool_max_connections=4
schema_initializer_connections=1

fail() {
    echo "postgres-deployment-preflight: $*" >&2
    exit 1
}

usage() {
    cat >&2 <<'EOF'
Usage: postgres-deployment-preflight.sh [--expected-identity UUID] [--compose-only] -- COMPOSE_ARGS...

Example:
  postgres-deployment-preflight.sh -- \
    -f docker-compose.yml -f docker-compose.versiond.yml

The default mode requires both versiond replicas and connects every stable HA
devshard generation to one live PostgreSQL proof anchor. --compose-only checks
only the rendered target topology and cannot be combined with
--expected-identity.
EOF
}

while (($#)); do
    case $1 in
        --expected-identity)
            (($# >= 2)) || fail "--expected-identity requires a value"
            [[ -n $2 ]] || fail "--expected-identity requires a non-empty value"
            expected_identity=$2
            shift 2
            ;;
        --compose-only)
            compose_only=true
            shift
            ;;
        --)
            shift
            compose_args=("$@")
            break
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            usage
            fail "unknown option: $1"
            ;;
    esac
done

((${#compose_args[@]} > 0)) || fail "Compose arguments are required after --"
[[ $compose_only == false || -z $expected_identity ]] || fail \
    "--expected-identity requires the live PostgreSQL proof"
command -v "$docker_bin" >/dev/null 2>&1 || fail "$docker_bin is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

is_positive_int32() {
    local value=$1
    [[ $value =~ ^[1-9][0-9]{0,9}$ ]] && ((value <= 2147483647))
}

compose=("$docker_bin" compose "${compose_args[@]}")
config=$("${compose[@]}" config --format json) || fail "cannot render Compose topology"
project_name=$(jq -er '.name | strings | select(length > 0)' <<<"$config") || fail \
    "rendered Compose topology has no project name"

for service in versiond versiond2; do
    jq -e --arg service "$service" '.services | has($service)' \
        >/dev/null <<<"$config" || fail "Compose topology has no $service service"
    mode=$(jq -r --arg service "$service" \
        '.services[$service].environment.DEVSHARD_STORAGE_MODE // ""' <<<"$config")
    [[ $mode == postgres ]] || fail "$service must use DEVSHARD_STORAGE_MODE=postgres"
    for key in "${forbidden_libpq[@]}"; do
        value=$(jq -r --arg service "$service" --arg key "$key" \
            '.services[$service].environment[$key] // ""' <<<"$config")
        [[ -z $value ]] || fail \
            "$service must not set $key; it can bypass the verified PostgreSQL identity"
    done
done

for key in PGHOST PGDATABASE PGUSER; do
    first=$(jq -r --arg key "$key" \
        '.services.versiond.environment[$key] // ""' <<<"$config")
    second=$(jq -r --arg key "$key" \
        '.services.versiond2.environment[$key] // ""' <<<"$config")
    [[ -n $first && $first == "$second" ]] || fail \
        "versiond services must use the same non-empty $key"
done
first=$(jq -r '.services.versiond.environment.PGPORT // "5432"' <<<"$config")
second=$(jq -r '.services.versiond2.environment.PGPORT // "5432"' <<<"$config")
[[ $first == "$second" ]] || fail "versiond services must use the same PGPORT"
pool_max_connections=$(jq -r \
    '.services.versiond.environment.PG_POOL_MAX_CONNS // ""' <<<"$config")
second=$(jq -r \
    '.services.versiond2.environment.PG_POOL_MAX_CONNS // ""' <<<"$config")
is_positive_int32 "$pool_max_connections" || fail \
    "versiond must set PG_POOL_MAX_CONNS to a positive integer"
[[ $pool_max_connections == "$second" ]] || fail \
    "versiond services must use the same PG_POOL_MAX_CONNS"

if [[ $compose_only == true ]]; then
    echo "postgres-deployment-preflight: rendered PostgreSQL contract is valid for Compose project '$project_name'"
    exit 0
fi

container_env() {
    local environment=$1 name=$2
    jq -er --arg prefix "$name=" '
        map(select(startswith($prefix)))
        | if length == 0 then empty else (last | ltrimstr($prefix)) end
    ' <<<"$environment"
}

containers=()
for service in versiond versiond2; do
    container=$("${compose[@]}" ps -q "$service") || fail "cannot inspect $service"
    containers+=("$container")
done
if [[ -z ${containers[0]} && -z ${containers[1]} ]]; then
    fail "Compose project '$project_name' has no running versiond replicas;" \
        "use the deployment's exact project arguments or select --compose-only explicitly"
fi
[[ -n ${containers[0]} && -n ${containers[1]} ]] || fail \
    "Compose project '$project_name' has only one running versiond replica;" \
    "cannot prove shared PostgreSQL storage"

for index in 0 1; do
    service=versiond
    ((index == 0)) || service=versiond2
    container=${containers[index]}
    runtime_environment=$("$docker_bin" inspect --format \
        '{{json .Config.Env}}' "$container") || fail \
        "cannot inspect runtime environment for $service container $container"
    jq -e 'type == "array" and all(.[]; type == "string")' \
        >/dev/null <<<"$runtime_environment" || fail \
        "$service container returned an invalid runtime environment"
    for key in "${forbidden_libpq[@]}"; do
        value=$(container_env "$runtime_environment" "$key") || value=
        [[ -z $value ]] || fail "running $service sets forbidden $key"
    done
    for key in PGHOST PGDATABASE PGUSER; do
        expected=$(jq -r --arg service "$service" --arg key "$key" \
            '.services[$service].environment[$key] // ""' <<<"$config")
        actual=$(container_env "$runtime_environment" "$key") || actual=
        [[ -n $actual && $actual == "$expected" ]] || fail \
            "running $service has $key='$actual', rendered topology expects '$expected'"
    done
    expected=$(jq -r --arg service "$service" \
        '.services[$service].environment.PGPORT // "5432"' <<<"$config")
    actual=$(container_env "$runtime_environment" PGPORT) || actual=5432
    [[ -n $actual ]] || actual=5432
    [[ $actual == "$expected" ]] || fail \
        "running $service has PGPORT='$actual', rendered topology expects '$expected'"
    actual=$(container_env "$runtime_environment" PG_POOL_MAX_CONNS) || actual=
    [[ $actual == "$pool_max_connections" ]] || fail \
        "running $service has PG_POOL_MAX_CONNS='$actual', rendered topology expects '$pool_max_connections'"
done

versiond_http() {
    local service=$1 container=$2 description=$3 method=$4 path=$5 payload=${6:-}
    local diagnostics_file response status details lower_details
    local -a wget=(/bin/busybox wget -qO- -S -T 5)

    if [[ $method == POST ]]; then
        wget+=(--header 'Content-Type: application/json' --post-data "$payload")
    fi
    wget+=("http://127.0.0.1:8080$path")
    diagnostics_file=$(mktemp "${TMPDIR:-/tmp}/gonka-postgres-preflight.XXXXXX") || {
        echo "postgres-deployment-preflight: cannot create HTTP diagnostics file" >&2
        return 1
    }
    if response=$("$docker_bin" exec "$container" "${wget[@]}" \
        2>"$diagnostics_file"); then
        rm -f "$diagnostics_file"
        printf '%s\n' "$response"
        return 0
    fi

    status=$(sed -nE \
        's/.*HTTP\/[^ ]+[[:space:]]+([0-9]{3}).*/\1/p' \
        "$diagnostics_file" | tail -n 1)
    details=$(
        {
            printf '%s\n' "$response"
            cat "$diagnostics_file"
        } | tr '\r\n' '  ' | sed -E 's/[[:space:]]+/ /g; s/^ //; s/ $//' | cut -c1-400
    )
    rm -f "$diagnostics_file"

    case $status in
        404)
            echo "postgres-deployment-preflight: $service $description returned HTTP 404; the running versiond image does not expose the live storage-proof API" >&2
            ;;
        503)
            echo "postgres-deployment-preflight: $service $description is not ready (HTTP 503); inspect 'docker logs $container' for HA child and PostgreSQL state${details:+; details: $details}" >&2
            ;;
        *)
            lower_details=${details,,}
            if [[ $lower_details == *"timed out"* || $lower_details == *timeout* ]]; then
                echo "postgres-deployment-preflight: $service $description timed out; inspect 'docker logs $container' and PostgreSQL availability" >&2
            else
                echo "postgres-deployment-preflight: $service $description request failed${status:+ (HTTP $status)}${details:+: $details}" >&2
            fi
            ;;
    esac
    return 1
}

read_storage_identity() {
    versiond_http "$1" "$2" "storage identity" GET \
        /internal/storage-identity
}

validate_storage_identity() {
    jq -e '
        . as $proof
        | ($proof.identity | type == "string" and length > 0)
          and ($proof.snapshot | type == "string" and length > 0)
          and ($proof.children | type == "number" and . > 0 and floor == .)
          and ($proof.targets | type == "array" and length == $proof.children)
          and ([$proof.targets[].generation] | length == $proof.children)
          and ([$proof.targets[].generation] | unique | length == $proof.children)
          and all($proof.targets[];
              (.generation | type == "string" and length > 0)
              and (.version | type == "string" and length > 0)
              and (.pool_max_connections | type == "number" and . > 0 and floor == .)
              and (.server_max_connections | type == "number" and . > 0 and floor == .)
              and (.server_reserved_connections | type == "number" and . >= 0 and floor == .)
              and (.server_reserved_connections < .server_max_connections))
    ' >/dev/null
}

validate_connection_budget() {
    local proof generation target_pool target_server_max target_server_reserved
    local server_max='' server_reserved='' total_targets=0

    for proof in "${proofs[@]}"; do
        while IFS=$'\t' read -r generation target_pool target_server_max target_server_reserved; do
            [[ $target_pool == "$pool_max_connections" ]] || fail \
                "generation $generation reports PostgreSQL pool capacity $target_pool; rendered topology expects $pool_max_connections"
            if [[ -z $server_max ]]; then
                server_max=$target_server_max
                server_reserved=$target_server_reserved
            elif [[ $target_server_max != "$server_max" || \
                $target_server_reserved != "$server_reserved" ]]; then
                fail "PostgreSQL server capacity differs between HA generations"
            fi
            ((total_targets += 1))
        done < <(jq -r '.targets[] | [
            .generation,
            .pool_max_connections,
            .server_max_connections,
            .server_reserved_connections
        ] | @tsv' <<<"$proof")
    done

    local supervisors=${#containers[@]}
    local per_child=$((pool_max_connections + 2))
    local per_supervisor=$((
        per_child + versiond_lookup_pool_max_connections + schema_initializer_connections
    ))
    local required=$((total_targets * per_child + supervisors * per_supervisor))
    local available=$((server_max - server_reserved))
    ((required <= available)) || fail \
        "PostgreSQL connection budget is insufficient: $required required for $total_targets current generations, rolling replacements, readiness/fence sessions, versiond lookup, and schema initialization; $available non-reserved connections available"
}

run_storage_challenge() {
    local service=$1 container=$2 snapshot=$3 generation=$4 operation=$5 nonce=$6
    local request
    request=$(jq -cn \
        --arg operation "$operation" \
        --arg nonce "$nonce" \
        --arg snapshot "$snapshot" \
        --arg generation "$generation" \
        '{operation:$operation, nonce:$nonce, snapshot:$snapshot, generation:$generation}')
    versiond_http "$service" "$container" \
        "storage challenge for generation $generation" POST \
        /internal/storage-challenge "$request"
}

validate_challenge_response() {
    local identity=$1 snapshot=$2 generation=$3
    jq -e \
        --arg identity "$identity" \
        --arg snapshot "$snapshot" \
        --arg generation "$generation" '
        .identity == $identity
        and .snapshot == $snapshot
        and .generation == $generation
        and .found == true
    ' >/dev/null
}

new_challenge_nonce() {
    local nonce
    [[ -r /proc/sys/kernel/random/uuid ]] || fail \
        "cannot generate PostgreSQL storage challenge nonce"
    IFS= read -r nonce </proc/sys/kernel/random/uuid
    [[ $nonce =~ ^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$ ]] || fail \
        "kernel returned an invalid PostgreSQL storage challenge nonce"
    printf '%s\n' "$nonce"
}

proofs=()
snapshots=()
identity=
for index in "${!containers[@]}"; do
    service=versiond
    ((index == 0)) || service=versiond2
    proof=$(read_storage_identity "$service" "${containers[index]}") || exit 1
    validate_storage_identity <<<"$proof" || fail \
        "$service returned an invalid PostgreSQL storage proof"
    observed_identity=$(jq -r '.identity' <<<"$proof")
    if [[ -z $identity ]]; then
        identity=$observed_identity
    elif [[ $observed_identity != "$identity" ]]; then
        fail "versiond replicas use different PostgreSQL database lineages"
    fi
    proofs+=("$(jq -Sc . <<<"$proof")")
    snapshots+=("$(jq -r '.snapshot' <<<"$proof")")
done

[[ -z $expected_identity || $identity == "$expected_identity" ]] || fail \
    "live PostgreSQL identity changed from $expected_identity to $identity"
validate_connection_budget

anchor_container=${containers[0]}
anchor_snapshot=${snapshots[0]}
anchor_generation=$(jq -r '.targets[0].generation' <<<"${proofs[0]}")
final_nonce=
final_writer_generation=
for writer_index in "${!containers[@]}"; do
    writer_service=versiond
    ((writer_index == 0)) || writer_service=versiond2
    while IFS= read -r writer_generation; do
        nonce=$(new_challenge_nonce)
        response=$(run_storage_challenge \
            "$writer_service" "${containers[writer_index]}" \
            "${snapshots[writer_index]}" "$writer_generation" write "$nonce") || exit 1
        validate_challenge_response \
            "$identity" "${snapshots[writer_index]}" "$writer_generation" \
            <<<"$response" || fail \
            "generation $writer_generation did not confirm its PostgreSQL storage challenge write"

        response=$(run_storage_challenge \
            versiond "$anchor_container" "$anchor_snapshot" \
            "$anchor_generation" read "$nonce") || exit 1
        validate_challenge_response \
            "$identity" "$anchor_snapshot" "$anchor_generation" \
            <<<"$response" || fail \
            "anchor generation $anchor_generation cannot observe the challenge written by generation $writer_generation; serialize preflight runs and retry because another run can replace the nonce before diagnosing separate databases"
        final_nonce=$nonce
        final_writer_generation=$writer_generation
    done < <(jq -r '.targets[].generation' <<<"${proofs[writer_index]}")
done

for reader_index in "${!containers[@]}"; do
    reader_service=versiond
    ((reader_index == 0)) || reader_service=versiond2
    while IFS= read -r reader_generation; do
        response=$(run_storage_challenge \
            "$reader_service" "${containers[reader_index]}" \
            "${snapshots[reader_index]}" "$reader_generation" read "$final_nonce") || exit 1
        validate_challenge_response \
            "$identity" "${snapshots[reader_index]}" "$reader_generation" \
            <<<"$response" || fail \
            "generation $reader_generation cannot observe the final challenge written by generation $final_writer_generation; serialize preflight runs and retry because another run can replace the nonce before diagnosing separate databases"
    done < <(jq -r '.targets[].generation' <<<"${proofs[reader_index]}")
done

for index in "${!containers[@]}"; do
    service=versiond
    ((index == 0)) || service=versiond2
    final_proof=$(read_storage_identity "$service" "${containers[index]}") || exit 1
    [[ $(jq -Sc . <<<"$final_proof") == "${proofs[index]}" ]] || fail \
        "PostgreSQL storage generation snapshot changed during the challenge"
done

printf '%s\n' "$identity"
