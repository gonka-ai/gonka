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

The command is read-only. It validates the rendered HA PostgreSQL contract and,
when both versiond containers exist, proves that they reach the same database.
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
        "no live versiond replicas; cannot prove shared PostgreSQL identity"
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

read_identity() {
    "$docker_bin" exec "$1" wget -qO- -T 5 \
        http://127.0.0.1:8080/internal/storage-identity |
        jq -er '.identity | strings | select(length > 0)'
}

first=$(read_identity "${containers[0]}") || fail "cannot read identity through versiond"
second=$(read_identity "${containers[1]}") || fail "cannot read identity through versiond2"
[[ $first == "$second" ]] || fail "versiond replicas use different PostgreSQL databases"
[[ -z $expected_identity || $first == "$expected_identity" ]] || fail \
    "live PostgreSQL identity changed from $expected_identity to $first"
printf '%s\n' "$first"
