#!/usr/bin/env bash

# Records the command sequence update-devshard.sh runs against a fake docker
# for the single and HA topologies, and checks its refusals.

set -Eeuo pipefail

source_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
    echo "update-devshard_test: $*" >&2
    exit 1
}

# The script resolves Compose files next to itself, so run a copy from a
# directory that holds placeholder files; the fake docker never reads them.
script_dir=$tmpdir/join
mkdir -p "$script_dir"
cp "$source_dir/update-devshard.sh" "$script_dir/"
: >"$script_dir/docker-compose.yml"
: >"$script_dir/docker-compose.versiond.yml"
: >"$script_dir/docker-compose.observability.yml"
: >"$script_dir/docker-compose.versiond3.yml"

# The fake docker answers the read-only queries the script makes and logs
# everything else. Scenario knobs come from the environment.
cat >"$tmpdir/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%s\n' "$*" >>"$FAKE_LOG"
case "$1 ${2:-} ${3:-}" in
    "info  ") exit 0 ;;
    "compose version --short") printf '%s\n' "${FAKE_COMPOSE_VERSION:-2.30.0}"; exit 0 ;;
esac
if [[ $1 == inspect ]]; then
    shift
    format=
    while (($# > 1)); do
        case $1 in
            --format) format=$2; shift 2 ;;
            --type) shift 2 ;;
            *) shift ;;
        esac
    done
    name=$1
    case " ${FAKE_CONTAINERS:-} " in
        *" $name "*) ;;
        *) exit 1 ;;
    esac
    case $format in
        *working_dir*) printf '%s\n' "${FAKE_WORKING_DIR}" ;;
        *config_files*) printf '%s\n' "${FAKE_CONFIG_FILES}" ;;
        *com.docker.compose.project\"*) printf 'gonka\n' ;;
    esac
    exit 0
fi
if [[ $1 == compose ]]; then
    case " $* " in
        *" config --format json "*)
            case " $* " in
                *"docker-compose.versiond.yml"*) cat "$FAKE_RENDERED_HA" ;;
                *) cat "$FAKE_RENDERED_SINGLE" ;;
            esac
            exit 0
            ;;
        *" ps --all --quiet devshard-postgres "*)
            printf '%s\n' "${FAKE_POSTGRES_CONTAINER:-}"
            exit 0
            ;;
    esac
fi
exit 0
EOF
chmod +x "$tmpdir/docker"

cat >"$tmpdir/fleet.sh" <<'EOF'
#!/usr/bin/env bash
printf 'fleet %s\n' "$*" >>"$FAKE_LOG"
EOF
cat >"$tmpdir/preflight.sh" <<'EOF'
#!/usr/bin/env bash
printf 'preflight %s\n' "$*" >>"$FAKE_LOG"
EOF
chmod +x "$tmpdir/fleet.sh" "$tmpdir/preflight.sh"

cat >"$tmpdir/single.json" <<'EOF'
{"services":{
  "versiond":{"image":"ghcr.io/example/versiond:new","environment":{}},
  "proxy":{"image":"ghcr.io/example/proxy-router:new","environment":{}},
  "proxy-policy":{"image":"ghcr.io/example/proxy:new","environment":{}},
  "proxy-policy2":{"image":"ghcr.io/example/proxy:new","environment":{}}
}}
EOF
cat >"$tmpdir/ha.json" <<'EOF'
{"services":{
  "versiond":{"image":"ghcr.io/example/versiond:new","environment":{
    "GONKA_HA":"true","PGHOST":"devshard-postgres","PGDATABASE":"devshardd","PGUSER":"devshardd",
    "DEVSHARD_STORAGE_MODE":"postgres"}},
  "versiond2":{"image":"ghcr.io/example/versiond:new","deploy":{"replicas":1},"environment":{
    "GONKA_HA":"true","PGHOST":"devshard-postgres","PGDATABASE":"devshardd","PGUSER":"devshardd",
    "DEVSHARD_STORAGE_MODE":"postgres"}},
  "devshard-postgres":{"image":"postgres@sha256:abc","environment":{},
    "volumes":[{"type":"bind","source":"/srv/gonka/postgres","target":"/var/lib/postgresql/gonka"}]},
  "proxy":{"image":"ghcr.io/example/proxy-router:new","environment":{}},
  "proxy-policy":{"image":"ghcr.io/example/proxy:new","environment":{}},
  "proxy-policy2":{"image":"ghcr.io/example/proxy:new","environment":{}}
},"networks":{"versiond-router-back":{"name":"gonka-versiond-router-back"}}}
EOF
jq '.services.versiond2.deploy.replicas = 0
  | .services.versiond3 = (.services.versiond2 | .deploy.replicas = 1)' \
    "$tmpdir/ha.json" >"$tmpdir/ha3.json"
cat >"$tmpdir/config.env" <<'EOF'
export KEY_NAME=test
EOF

run_update() {
    : >"$tmpdir/log"
    env PATH="$tmpdir:$PATH" \
        FAKE_LOG="$tmpdir/log" \
        FAKE_WORKING_DIR="$script_dir" \
        FAKE_RENDERED_SINGLE="$tmpdir/single.json" \
        FAKE_RENDERED_HA="$tmpdir/ha.json" \
        GONKA_CONFIG_ENV="$tmpdir/config.env" \
        DOCKER_BIN=docker \
        VERSIOND_ROUTER_FLEET_BIN="$tmpdir/fleet.sh" \
        DEVSHARD_POSTGRES_MIGRATION_PREFLIGHT_BIN="$tmpdir/preflight.sh" \
        "$@" "$script_dir/update-devshard.sh" "${UPDATE_ARGS[@]}" \
        >"$tmpdir/out" 2>"$tmpdir/err"
}

mutations() {
    grep -E '^compose .*(pull|up|rm) |^rm -f|^fleet (prepare-networks|apply)' "$tmpdir/log" | \
        sed -E 's/ --project-directory [^ ]+//; s/ -f [^ ]+\.yml//g; s/ --project-name [^ ]+//'
}

# Single topology, stock files, no running deployment.
UPDATE_ARGS=()
run_update env FAKE_CONTAINERS="" || fail "single update failed: $(cat "$tmpdir/err")"
expected='compose pull versiond proxy proxy-policy proxy-policy2
compose up -d --no-deps --wait --wait-timeout 2100 proxy-policy2 proxy-policy
compose up -d --no-deps --wait --wait-timeout 2100 proxy
compose up -d --no-deps --wait --wait-timeout 2100 versiond'
[[ $(mutations) == "$expected" ]] || fail "single sequence:
$(mutations)"
grep -q 'Topology: single' "$tmpdir/out" || fail "single topology not reported"

# HA topology discovered from the running versiond's Compose labels, with a
# v4 PostgreSQL container and the legacy nginx router still present.
UPDATE_ARGS=()
run_update env \
    FAKE_CONTAINERS="versiond versiond2 devshard-postgres versiond-router proxy" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml,docker-compose.observability.yml" \
    FAKE_POSTGRES_CONTAINER=pg-old || fail "HA update failed: $(cat "$tmpdir/err")"
expected='compose pull versiond versiond2 proxy proxy-policy proxy-policy2 devshard-postgres
compose up -d --no-deps --wait --wait-timeout 2100 devshard-postgres
fleet prepare-networks
fleet apply
compose up -d --no-deps --wait --wait-timeout 2100 proxy-policy2 proxy-policy
compose up -d --no-deps --wait --wait-timeout 2100 proxy
rm -f versiond-router
compose up -d --no-deps --wait --wait-timeout 2100 versiond2
compose up -d --no-deps --wait --wait-timeout 2100 versiond'
[[ $(mutations) == "$expected" ]] || fail "HA sequence:
$(mutations)"
grep -q 'preflight --source-container pg-old --target-dir /srv/gonka/postgres' "$tmpdir/log" || \
    fail "PostgreSQL migration space was not checked"
grep -q -- '--project-name gonka' "$tmpdir/log" || fail "project name from labels was not used"
grep -c 'docker-compose.observability.yml' "$tmpdir/log" >/dev/null || \
    fail "operator overlays from labels were dropped"
grep -q 'fleet status' "$tmpdir/log" || fail "fleet status was not printed"

# Any number of local replicas: a decommissioned versiond2 (0 replicas) is
# skipped, versiond3 is updated before the legacy owner.
UPDATE_ARGS=()
run_update env FAKE_CONTAINERS="versiond versiond3 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml,docker-compose.versiond3.yml" \
    FAKE_RENDERED_HA="$tmpdir/ha3.json" || fail "three-replica update failed: $(cat "$tmpdir/err")"
expected='compose pull versiond versiond3 proxy proxy-policy proxy-policy2 devshard-postgres
compose up -d --no-deps --wait --wait-timeout 2100 devshard-postgres
fleet prepare-networks
fleet apply
compose up -d --no-deps --wait --wait-timeout 2100 proxy-policy2 proxy-policy
compose up -d --no-deps --wait --wait-timeout 2100 proxy
compose up -d --no-deps --wait --wait-timeout 2100 versiond3
compose up -d --no-deps --wait --wait-timeout 2100 versiond'
[[ $(mutations) == "$expected" ]] || fail "three-replica sequence:
$(mutations)"
grep -q 'Topology: ha (versiond versiond2 versiond3)' "$tmpdir/out" || \
    fail "replica discovery: $(grep Topology "$tmpdir/out")"

# --check runs the preflight and changes nothing.
UPDATE_ARGS=(--check)
run_update env FAKE_CONTAINERS="versiond devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" \
    FAKE_POSTGRES_CONTAINER=pg-old || fail "--check failed: $(cat "$tmpdir/err")"
[[ -z $(mutations) ]] || fail "--check mutated the deployment: $(mutations)"
grep -q 'Preflight passed' "$tmpdir/out" || fail "--check did not report success"

# --dry-run prints the sequence without running docker for it.
UPDATE_ARGS=(--dry-run)
run_update env FAKE_CONTAINERS="" || fail "--dry-run failed: $(cat "$tmpdir/err")"
[[ -z $(mutations) ]] || fail "--dry-run mutated the deployment"
grep -q '^+ docker compose .* up -d --no-deps --wait --wait-timeout 2100 versiond$' "$tmpdir/out" || \
    fail "--dry-run did not print the versiond step"

# COMPOSE_FILE wins over container labels.
UPDATE_ARGS=(--dry-run)
run_update env FAKE_CONTAINERS="versiond" FAKE_CONFIG_FILES="docker-compose.yml" \
    COMPOSE_FILE="docker-compose.yml:docker-compose.versiond.yml" || \
    fail "COMPOSE_FILE run failed: $(cat "$tmpdir/err")"
grep -q 'Topology: ha' "$tmpdir/out" || fail "COMPOSE_FILE overlay did not select HA"

# Refusals.
UPDATE_ARGS=(--check)
if run_update env FAKE_CONTAINERS="versiond" FAKE_CONFIG_FILES="docker-compose.yml" \
    FAKE_WORKING_DIR=/elsewhere; then
    fail "a deployment from another directory was accepted"
fi
grep -q 'lives in /elsewhere' "$tmpdir/err" || fail "wrong-directory message: $(cat "$tmpdir/err")"

if run_update env FAKE_CONTAINERS="" FAKE_COMPOSE_VERSION=2.20.0; then
    fail "an old Docker Compose was accepted"
fi
grep -q '2.24.4 or newer' "$tmpdir/err" || fail "compose version message: $(cat "$tmpdir/err")"

jq '.services.versiond2.environment.PGDATABASE = "other"' "$tmpdir/ha.json" >"$tmpdir/ha-drift.json"
if run_update env FAKE_CONTAINERS="versiond devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" \
    FAKE_RENDERED_HA="$tmpdir/ha-drift.json"; then
    fail "replicas pointing at different databases were accepted"
fi
grep -q 'versiond2 disagree on PGDATABASE' "$tmpdir/err" || fail "PG drift message: $(cat "$tmpdir/err")"

jq '.services.versiond2.environment.PGSERVICE = "other"' "$tmpdir/ha.json" >"$tmpdir/ha-libpq.json"
if run_update env FAKE_CONTAINERS="versiond devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" \
    FAKE_RENDERED_HA="$tmpdir/ha-libpq.json"; then
    fail "a libpq override that can redirect one replica was accepted"
fi
grep -q 'versiond2 sets PGSERVICE' "$tmpdir/err" || fail "libpq override message: $(cat "$tmpdir/err")"

UPDATE_ARGS=(--check --topology ha)
if run_update env FAKE_CONTAINERS="" COMPOSE_FILE=docker-compose.yml; then
    fail "HA without the versiond overlay was accepted"
fi
grep -q 'does not declare GONKA_HA=true' "$tmpdir/err" || fail "HA declaration message: $(cat "$tmpdir/err")"

# A model that declares HA but lost the router networks is rejected.
jq 'del(.networks)' "$tmpdir/ha.json" >"$tmpdir/ha-no-net.json"
UPDATE_ARGS=(--check)
if run_update env FAKE_CONTAINERS="versiond devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" \
    FAKE_RENDERED_HA="$tmpdir/ha-no-net.json"; then
    fail "HA model without router networks was accepted"
fi
grep -q 'needs docker-compose.versiond.yml' "$tmpdir/err" || fail "HA overlay message: $(cat "$tmpdir/err")"

echo "update-devshard_test: ok"
