#!/usr/bin/env bash

set -Eeuo pipefail

docker_bin=${DOCKER_BIN:-docker}
expected_identity=
require_live=false
compose_args=()

fail() {
    echo "postgres-deployment-preflight: $*" >&2
    exit 1
}

usage() {
    cat >&2 <<'EOF'
Usage: postgres-deployment-preflight.sh [--expected-identity UUID] [--require-live] -- COMPOSE_ARGS...

Example:
  postgres-deployment-preflight.sh -- \
    -f docker-compose.yml -f docker-compose.versiond.yml

The command does not change the deployment. It validates the rendered HA
PostgreSQL contract and, when both versiond containers exist, exchanges a
short-lived challenge through every running HA devshard generation.
EOF
}

while (($#)); do
    case $1 in
        --expected-identity)
            (($# >= 2)) || fail "--expected-identity requires a value"
            expected_identity=$2
            shift 2
            ;;
        --require-live)
            require_live=true
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
command -v "$docker_bin" >/dev/null 2>&1 || fail "$docker_bin is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

compose=("$docker_bin" compose "${compose_args[@]}")
config=$("${compose[@]}" config --format json) || fail "cannot render Compose topology"

for service in versiond versiond2; do
    jq -e --arg service "$service" '.services | has($service)' \
        >/dev/null <<<"$config" || fail "Compose topology has no $service service"
    mode=$(jq -r --arg service "$service" \
        '.services[$service].environment.DEVSHARD_STORAGE_MODE // ""' <<<"$config")
    [[ $mode == postgres ]] || fail "$service must use DEVSHARD_STORAGE_MODE=postgres"
    for key in DATABASE_URL PGSERVICE PGSERVICEFILE PGOPTIONS; do
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

container_env() {
    local container=$1 name=$2 line
    while IFS= read -r line; do
        case $line in
            "$name="*) printf '%s\n' "${line#*=}"; return 0 ;;
        esac
    done < <("$docker_bin" inspect --format \
        '{{range .Config.Env}}{{println .}}{{end}}' "$container")
    return 1
}

containers=()
for service in versiond versiond2; do
    container=$("${compose[@]}" ps -q "$service") || fail "cannot inspect $service"
    containers+=("$container")
done
if [[ -z ${containers[0]} && -z ${containers[1]} ]]; then
    [[ $require_live == false ]] || fail \
        "no live versiond replicas; cannot prove shared PostgreSQL storage"
    echo "postgres-deployment-preflight: rendered contract is valid; no live replicas to compare"
    exit 0
fi
[[ -n ${containers[0]} && -n ${containers[1]} ]] || fail \
    "only one versiond replica is running; cannot prove shared PostgreSQL identity"

for index in 0 1; do
    service=versiond
    ((index == 0)) || service=versiond2
    container=${containers[index]}
    for key in DATABASE_URL PGSERVICE PGSERVICEFILE PGOPTIONS; do
        value=$(container_env "$container" "$key") || value=
        [[ -z $value ]] || fail "running $service sets forbidden $key"
    done
    for key in PGHOST PGDATABASE PGUSER; do
        expected=$(jq -r --arg service "$service" --arg key "$key" \
            '.services[$service].environment[$key] // ""' <<<"$config")
        actual=$(container_env "$container" "$key") || actual=
        [[ -n $actual && $actual == "$expected" ]] || fail \
            "running $service has $key='$actual', rendered topology expects '$expected'"
    done
    expected=$(jq -r --arg service "$service" \
        '.services[$service].environment.PGPORT // "5432"' <<<"$config")
    actual=$(container_env "$container" PGPORT) || actual=5432
    [[ -n $actual ]] || actual=5432
    [[ $actual == "$expected" ]] || fail \
        "running $service has PGPORT='$actual', rendered topology expects '$expected'"
done

read_storage_identity() {
    "$docker_bin" exec "$1" wget -qO- -T 5 \
        http://127.0.0.1:8080/internal/storage-identity
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
              and (.version | type == "string" and length > 0))
    ' >/dev/null
}

run_storage_challenge() {
    local container=$1 snapshot=$2 generation=$3 operation=$4 nonce=$5
    local request
    request=$(jq -cn \
        --arg operation "$operation" \
        --arg nonce "$nonce" \
        --arg snapshot "$snapshot" \
        --arg generation "$generation" \
        '{operation:$operation, nonce:$nonce, snapshot:$snapshot, generation:$generation}')
    "$docker_bin" exec "$container" wget -qO- -T 5 \
        --header 'Content-Type: application/json' \
        --post-data "$request" \
        http://127.0.0.1:8080/internal/storage-challenge
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
    proof=$(read_storage_identity "${containers[index]}") || fail \
        "cannot read PostgreSQL storage proof through $service"
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

for writer_index in "${!containers[@]}"; do
    while IFS= read -r writer_generation; do
        nonce=$(new_challenge_nonce)
        response=$(run_storage_challenge \
            "${containers[writer_index]}" "${snapshots[writer_index]}" \
            "$writer_generation" write "$nonce") || fail \
            "cannot write PostgreSQL storage challenge through generation $writer_generation"
        validate_challenge_response \
            "$identity" "${snapshots[writer_index]}" "$writer_generation" \
            <<<"$response" || fail \
            "generation $writer_generation did not confirm its PostgreSQL storage challenge write"

        for reader_index in "${!containers[@]}"; do
            while IFS= read -r reader_generation; do
                response=$(run_storage_challenge \
                    "${containers[reader_index]}" "${snapshots[reader_index]}" \
                    "$reader_generation" read "$nonce") || fail \
                    "cannot read PostgreSQL storage challenge through generation $reader_generation"
                validate_challenge_response \
                    "$identity" "${snapshots[reader_index]}" "$reader_generation" \
                    <<<"$response" || fail \
                    "generation $reader_generation cannot observe the challenge written by generation $writer_generation"
            done < <(jq -r '.targets[].generation' <<<"${proofs[reader_index]}")
        done
    done < <(jq -r '.targets[].generation' <<<"${proofs[writer_index]}")
done

for index in "${!containers[@]}"; do
    final_proof=$(read_storage_identity "${containers[index]}") || fail \
        "cannot re-read PostgreSQL storage proof after the challenge"
    [[ $(jq -Sc . <<<"$final_proof") == "${proofs[index]}" ]] || fail \
        "PostgreSQL storage generation snapshot changed during the challenge"
done

printf '%s\n' "$identity"
