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
cp "$source_dir/update-devshard.sh" "$source_dir/deployment-lock.sh" "$source_dir/updater-rollback.sh" \
    "$source_dir/updater-container-state.py" "$script_dir/"
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
for var in VERSIOND_IMAGE DEVSHARD_POSTGRES_IMAGE PROXY_ROUTER_IMAGE PROXY_POLICY_IMAGE; do
    [[ -z ${!var:-} ]] || printf 'env %s=%s\n' "$var" "${!var}" >>"$FAKE_LOG"
done
case "$1 ${2:-} ${3:-}" in
    "network inspect "*) exit 0 ;;
    "network connect "*) exit 0 ;;
    "run --rm "*)
        case " $* " in
            *"SELECT challenge"*)
                [[ -f $FAKE_STATE/nonce && ${FAKE_TARGET_IS_CLONE:-false} != true ]] && cat "$FAKE_STATE/nonce"
                ;;
            *"current_setting"*) printf '%s|3\n' "${FAKE_MAX_CONNECTIONS:-1000}" ;;
            *"SELECT identity"*) printf '%s\n' "${FAKE_TARGET_IDENTITY:-db-1}" ;;
            *"pg_is_in_recovery"*) printf 'f\n' ;;
        esac
        exit 0
        ;;
    "exec "*)
        knob=FAKE_PROOF_${2#cid-}
        mode=${!knob:-${FAKE_PROOF_MODE:-valid}}
        case $mode in
            404) printf '  HTTP/1.1 404 Not Found\n' >&2; exit 1 ;;
            503) printf '  HTTP/1.1 503 Service Unavailable\n' >&2; exit 1 ;;
            timeout) printf 'wget: download timed out\n' >&2; exit 1 ;;
            exec-failed) printf 'permission denied\n' >&2; exit 126 ;;
            malformed) printf '{bad json\n'; exit 0 ;;
            identity-only) printf '{"identity":"db-1"}\n'; exit 0 ;;
            empty-targets) printf '{"identity":"db-1","snapshot":"snap-1","children":0,"targets":[]}\n'; exit 0 ;;
        esac
        case " $* " in
            *storage-challenge*)
                payload=
                for ((i = 1; i <= $#; i++)); do
                    [[ ${!i} == --post-data ]] && { j=$((i + 1)); payload=${!j}; }
                done
                printf '%s' "$payload" | jq -r .nonce >"$FAKE_STATE/nonce"
                jq -cn --arg identity "${FAKE_RUNNING_IDENTITY:-db-1}" --argjson request "$payload" \
                    '{identity:$identity,snapshot:$request.snapshot,generation:$request.generation,found:true}'
                ;;
            *) printf '{"identity":"%s","snapshot":"snap-1","children":1,"targets":[{"generation":"gen-1","version":"v5","pool_max_connections":4}]}\n' "${FAKE_RUNNING_IDENTITY:-db-1}" ;;
        esac
        exit 0
        ;;
    "tag "*) exit 0 ;;
    "ps -a "*)
        for name in ${FAKE_CONTAINERS:-}; do printf '%s\n' "$name"; done
        exit 0
        ;;
    "image inspect "*)
        case " ${FAKE_MISSING_IMAGES:-} " in
            *" ${*: -1} "*) [[ -f $FAKE_STATE/pulled ]] || exit 1 ;;
        esac
        printf 'id-%s\n' "${*: -1}"
        exit 0
        ;;
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
    name=${1#cid-}
    case " ${FAKE_CONTAINERS:-} " in
        *" $name "*) ;;
        *) echo "Error response from daemon: No such object: $name" >&2; exit 1 ;;
    esac
    case $format in
        "") jq -cn --arg name "$name" --arg health "${FAKE_HEALTH:-healthy}" \
            '[{Id:("cid-"+$name), Name:("/"+$name), Image:("old-"+$name),
              Config:{Labels:{"com.docker.compose.project":"gonka"}},
              State:{Running:true,Status:"running",Health:{Status:$health}}}]' ;;
        *Config.Env*) printf '["PGHOST=devshard-postgres","PGDATABASE=devshardd"]\n' ;;
        *.Image}}*) printf 'old-%s\n' "${name#cid-}" ;;
        *Health*) printf '%s\n' "${FAKE_HEALTH:-healthy}" ;;
        *working_dir*) printf '%s\n' "${FAKE_WORKING_DIR}" ;;
        *config_files*)
            override=FAKE_CONFIG_FILES_${name//-/_}
            printf '%s\n' "${!override:-${FAKE_CONFIG_FILES}}"
            ;;
        *com.docker.compose.project\"*) printf 'gonka\n' ;;
        *NetworkSettings.Networks*)
            case " ${FAKE_ON_BACK_NETWORK:-} " in
                *" $name "*) printf '{"join_default":{},"gonka-versiond-router-back":{}}\n' ;;
                *) printf '{"join_default":{}}\n' ;;
            esac
            ;;
    esac
    exit 0
fi
if [[ $1 == compose ]]; then
    case " $* " in
        *" pull "*) touch "$FAKE_STATE/pulled"; exit 0 ;;
        *" config --format json "*)
            case " $* " in
                *"docker-compose.versiond.yml"*) cat "$FAKE_RENDERED_HA" ;;
                *) cat "$FAKE_RENDERED_SINGLE" ;;
            esac
            exit 0
            ;;
        *" ps --all --quiet "*)
            [[ ${FAKE_FAIL_PS_ALL:-} != "${*: -1}" ]] || { echo 'permission denied' >&2; exit 1; }
            service=${*: -1}
            case " ${FAKE_CONTAINERS:-} " in
                *" $service "*) printf 'cid-%s\n' "$service" ;;
            esac
            exit 0
            ;;
        *" ps --quiet "*)
            service=${*: -1}
            case " ${FAKE_STOPPED:-} " in *" $service "*) exit 0 ;; esac
            case " ${FAKE_CONTAINERS:-} " in
                *" $service "*) printf 'cid-%s\n' "$service" ;;
            esac
            exit 0
            ;;
        *" up -d "*)
            service=${*: -1}
            if [[ $service == "${FAKE_FAIL_UP:-}" ]]; then
                for var in VERSIOND_IMAGE DEVSHARD_POSTGRES_IMAGE PROXY_ROUTER_IMAGE PROXY_POLICY_IMAGE; do
                    [[ -z ${!var:-} ]] || exit 0
                done
                echo "simulated unhealthy $service" >&2
                exit 1
            fi
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
{"name":"gonka","services":{
  "versiond":{"image":"ghcr.io/example/versiond:new","environment":{}},
  "proxy":{"image":"ghcr.io/example/proxy-router:new","environment":{}},
  "proxy-policy":{"image":"ghcr.io/example/proxy:new","environment":{}},
  "proxy-policy2":{"image":"ghcr.io/example/proxy:new","environment":{}}
}}
EOF
cat >"$tmpdir/ha.json" <<'EOF'
{"name":"gonka","services":{
  "versiond":{"image":"ghcr.io/example/versiond:new","environment":{
    "GONKA_HA":"true","PGHOST":"devshard-postgres","PGDATABASE":"devshardd","PGUSER":"devshardd",
    "DEVSHARD_STORAGE_MODE":"postgres"},
    "networks":{"default":{},"versiond-router-back":{"aliases":["versiond-pool"]}}},
  "versiond2":{"image":"ghcr.io/example/versiond:new","deploy":{"replicas":1},"environment":{
    "GONKA_HA":"true","PGHOST":"devshard-postgres","PGDATABASE":"devshardd","PGUSER":"devshardd",
    "DEVSHARD_STORAGE_MODE":"postgres"},
    "networks":{"default":{},"versiond-router-back":{"aliases":["versiond-pool"]}}},
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
    rm -rf "$tmpdir/state"; mkdir -p "$tmpdir/state"
    env PATH="$tmpdir:$PATH" \
        FAKE_LOG="$tmpdir/log" \
        UPDATE_STATE_DIR="$tmpdir/state/updater" \
        GONKA_DEPLOYMENT_LOCK="$tmpdir/deployment.lock" \
        FAKE_STATE="$tmpdir/state" \
        FAKE_MISSING_IMAGES="${FAKE_MISSING_IMAGES:-ghcr.io/example/versiond:new ghcr.io/example/proxy-router:new ghcr.io/example/proxy:new postgres@sha256:abc}" \
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
    grep -E '^compose .*(pull|up|rm|stop) |^rm -f|^network connect |^fleet (prepare-networks|apply|verify-admission)' "$tmpdir/log" | \
        sed -E 's/ --project-directory [^ ]+//; s/ -f [^ ]+\.yml//g; s/ --project-name [^ ]+//'
}

# Single topology, stock files, no running deployment.
UPDATE_ARGS=()
run_update env FAKE_CONTAINERS="" || fail "single update failed: $(cat "$tmpdir/err")"
expected='compose pull versiond proxy proxy-policy proxy-policy2
compose up -d --no-deps proxy
compose up -d --no-deps --wait --wait-timeout 2100 proxy-policy2
compose up -d --no-deps --wait --wait-timeout 2100 proxy-policy
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
    || fail "HA update failed: $(cat "$tmpdir/err")"
expected='compose pull versiond versiond2 proxy proxy-policy proxy-policy2 devshard-postgres
compose up -d --no-deps --wait --wait-timeout 2100 devshard-postgres
fleet prepare-networks
network connect --alias versiond-pool gonka-versiond-router-back cid-versiond
network connect --alias versiond-pool gonka-versiond-router-back cid-versiond2
fleet apply
compose up -d --no-deps proxy
compose up -d --no-deps --wait --wait-timeout 2100 proxy-policy2
compose up -d --no-deps --wait --wait-timeout 2100 proxy-policy
compose up -d --no-deps --wait --wait-timeout 2100 proxy
fleet verify-admission
rm -f versiond-router
compose up -d --no-deps --wait --wait-timeout 2100 versiond2
compose up -d --no-deps --wait --wait-timeout 2100 versiond'
[[ $(mutations) == "$expected" ]] || fail "HA sequence:
$(mutations)"
grep -q 'preflight --source-container cid-devshard-postgres --target-dir /srv/gonka/postgres' "$tmpdir/log" || \
    fail "PostgreSQL migration space was not checked"
grep -q "^run --rm --network .* psql -q -w -v ON_ERROR_STOP=1 -Atc .*pg_is_in_recovery()" "$tmpdir/log" || \
    fail "PostgreSQL was not probed before the first mutation"
[[ $(grep -n 'psql' "$tmpdir/log" | head -1 | cut -d: -f1) -lt $(grep -n '^compose .* pull ' "$tmpdir/log" | head -1 | cut -d: -f1) ]] || \
    fail "PostgreSQL probe must run before the pull"
grep -q -- '--project-name gonka' "$tmpdir/log" || fail "project name from labels was not used"
grep -c 'docker-compose.observability.yml' "$tmpdir/log" >/dev/null || \
    fail "operator overlays from labels were dropped"
grep -q 'fleet status' "$tmpdir/log" || fail "fleet status was not printed"

# Any number of local replicas: a decommissioned versiond2 (0 replicas) is
# skipped, versiond3 is updated before the legacy owner.
UPDATE_ARGS=()
run_update env FAKE_CONTAINERS="versiond versiond2 versiond3 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml,docker-compose.versiond3.yml" \
    FAKE_RENDERED_HA="$tmpdir/ha3.json" || fail "three-replica update failed: $(cat "$tmpdir/err")"
expected='compose pull versiond versiond3 proxy proxy-policy proxy-policy2 devshard-postgres
compose up -d --no-deps --wait --wait-timeout 2100 devshard-postgres
fleet prepare-networks
network connect --alias versiond-pool gonka-versiond-router-back cid-versiond
network connect --alias versiond-pool gonka-versiond-router-back cid-versiond2
network connect --alias versiond-pool gonka-versiond-router-back cid-versiond3
fleet apply
compose up -d --no-deps proxy
compose up -d --no-deps --wait --wait-timeout 2100 proxy-policy2
compose up -d --no-deps --wait --wait-timeout 2100 proxy-policy
compose up -d --no-deps --wait --wait-timeout 2100 proxy
fleet verify-admission
compose up -d --no-deps --wait --wait-timeout 2100 versiond3
compose up -d --no-deps --wait --wait-timeout 2100 versiond
compose stop versiond2
compose rm -f versiond2'
[[ $(mutations) == "$expected" ]] || fail "three-replica sequence:
$(mutations)"
grep -q 'Topology: ha (versiond versiond2 versiond3)' "$tmpdir/out" || \
    fail "replica discovery: $(grep Topology "$tmpdir/out")"

# A replaced service that never becomes healthy is put back on its previous
# image and the run stops there.
UPDATE_ARGS=()
if run_update env FAKE_CONTAINERS="versiond proxy" FAKE_CONFIG_FILES="docker-compose.yml" \
    FAKE_FAIL_UP=versiond; then
    fail "an unhealthy replacement was reported as success"
fi
grep -q 'restoring the previous service specifications' "$tmpdir/err" || \
    fail "rollback message: $(cat "$tmpdir/err")"
grep -q '^start cid-versiond$' "$tmpdir/log" || fail "saved versiond was not restored"
grep -Eq '^tag old-versiond gonka-previous/[0-9a-f]+/versiond$' "$tmpdir/log" || \
    fail "the previous image was not retained under a deployment tag"
[[ ! -d $tmpdir/state/updater/pending ]] || fail "successful recovery left a pending transaction"

# An unhealthy candidate never overwrites the previous tag.
UPDATE_ARGS=(--dry-run)
run_update env FAKE_CONTAINERS="versiond proxy" FAKE_CONFIG_FILES="docker-compose.yml" FAKE_HEALTH=unhealthy || \
    fail "dry run with an unhealthy container failed: $(cat "$tmpdir/err")"
! grep -q '^+ docker tag old-versiond gonka-previous/versiond' "$tmpdir/out" || \
    fail "an unhealthy container overwrote the previous tag"

# A physical clone with the same identity is caught by the write challenge.
UPDATE_ARGS=(--check)
if run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" FAKE_TARGET_IS_CLONE=true; then
    fail "a clone with the same identity was accepted"
fi
grep -q 'did not receive the challenge' "$tmpdir/err" || fail "clone message: $(cat "$tmpdir/err")"
run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" || \
    fail "same-database check failed: $(cat "$tmpdir/err")"
grep -q 'storage-challenge' "$tmpdir/log" || fail "no write challenge was issued"
grep -q 'CREATE TEMP TABLE' "$tmpdir/log" || fail "the probe did not exercise the write path"

# A stopped local PostgreSQL is not a fresh install.
UPDATE_ARGS=(--check)
if run_update env FAKE_CONTAINERS="versiond devshard-postgres" FAKE_STOPPED="devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml"; then
    fail "a stopped PostgreSQL was treated as absent"
fi
grep -q 'exists but is not running' "$tmpdir/err" || fail "stopped PostgreSQL message: $(cat "$tmpdir/err")"

# A replica named outside any fixed list is still found through the labels.
UPDATE_ARGS=(--check)
run_update env FAKE_CONTAINERS="versiond12 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" \
    FAKE_CONFIG_FILES_versiond12="docker-compose.yml,docker-compose.versiond.yml,docker-compose.versiond3.yml" || \
    fail "discovery through versiond12 failed: $(cat "$tmpdir/err")"
grep -q 'docker-compose.versiond3.yml' "$tmpdir/out" || \
    fail "the file list recorded by versiond12 was not used: $(grep -A4 'Compose files' "$tmpdir/out")"

# A model that points at another database than the running replicas is refused.
UPDATE_ARGS=(--check)
if run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" \
    FAKE_RUNNING_IDENTITY=db-1 FAKE_TARGET_IDENTITY=db-2; then
    fail "a database lineage change was accepted"
fi
grep -q 'split the pool between two histories' "$tmpdir/err" || \
    fail "lineage message: $(cat "$tmpdir/err")"
UPDATE_ARGS=(--check)
run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" \
    FAKE_RUNNING_IDENTITY=db-1 FAKE_TARGET_IDENTITY=db-1 || fail "same lineage refused: $(cat "$tmpdir/err")"
grep -q 'database lineage db-1 unchanged' "$tmpdir/out" || fail "lineage was not confirmed"

# The legacy owner of pinned versions cannot be decommissioned.
jq '.services.versiond.deploy.replicas = 0
  | .services.versiond.environment.VERSIOND_NON_HA_VERSIONS = "v1 v2 v3"' \
    "$tmpdir/ha.json" >"$tmpdir/ha-no-owner.json"
UPDATE_ARGS=()
if run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" \
    FAKE_RENDERED_HA="$tmpdir/ha-no-owner.json"; then
    fail "decommissioning the legacy owner was accepted"
fi
grep -q 'versiond is VERSIOND_LEGACY_HOST and still owns the pinned versions' "$tmpdir/err" || \
    fail "legacy owner message: $(cat "$tmpdir/err")"

# The longest Compose file list recorded by any container wins when the others
# are ordered subsets of it (a replica added later carries the extra overlay).
UPDATE_ARGS=(--check)
run_update env FAKE_CONTAINERS="versiond versiond2 versiond3 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" \
    FAKE_CONFIG_FILES_versiond3="docker-compose.yml,docker-compose.versiond.yml,docker-compose.versiond3.yml" \
    FAKE_RENDERED_HA="$tmpdir/ha3.json" || fail "superset file list failed: $(cat "$tmpdir/err")"
grep -q 'docker-compose.versiond3.yml' "$tmpdir/out" || fail "the newer replica's overlay was dropped"
UPDATE_ARGS=(--check)
if run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" \
    FAKE_CONFIG_FILES_versiond2="docker-compose.yml,docker-compose.observability.yml"; then
    fail "conflicting Compose file lists were accepted"
fi
grep -q 'record different Compose file lists' "$tmpdir/err" || fail "file list conflict message: $(cat "$tmpdir/err")"

# The deployment is recognised through any of its containers when versiond
# itself is missing after an interrupted run.
UPDATE_ARGS=(--check)
run_update env FAKE_CONTAINERS="versiond2 devshard-postgres proxy" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" || \
    fail "discovery through versiond2 failed: $(cat "$tmpdir/err")"
grep -q 'Compose files (running .* container)' "$tmpdir/out" || fail "labels were not read from a running container"
grep -q 'Topology: ha' "$tmpdir/out" || fail "HA topology lost without the versiond container"

# HA containers next to a single-versiond model are refused.
UPDATE_ARGS=(--check)
if run_update env FAKE_CONTAINERS="versiond devshard-postgres" FAKE_CONFIG_FILES="docker-compose.yml"; then
    fail "a single model was accepted next to HA containers"
fi
grep -q 'container devshard-postgres exists but the Compose model is a single-versiond one' "$tmpdir/err" || \
    fail "partial-model message: $(cat "$tmpdir/err")"

# --check runs database probes without replacing services.
UPDATE_ARGS=(--check)
run_update env FAKE_CONTAINERS="versiond devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" \
    || fail "--check failed: $(cat "$tmpdir/err")"
[[ -z $(mutations) ]] || fail "--check mutated the deployment: $(mutations)"
grep -q 'Preflight passed' "$tmpdir/out" || fail "--check did not report success"

# --dry-run prints the sequence without running docker for it.
UPDATE_ARGS=(--dry-run)
run_update env FAKE_CONTAINERS="" || fail "--dry-run failed: $(cat "$tmpdir/err")"
[[ -z $(mutations) ]] || fail "--dry-run mutated the deployment"
grep -q '^+ docker compose .* up -d --no-deps --wait --wait-timeout 2100 versiond$' "$tmpdir/out" || \
    fail "--dry-run did not print the versiond step"

# A dry run need not have the candidate PostgreSQL image cached locally.
UPDATE_ARGS=(--dry-run)
run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" || \
    fail "HA dry run failed: $(cat "$tmpdir/err")"
[[ -z $(mutations) ]] || fail "HA dry run replaced services"

# COMPOSE_FILE wins over container labels.
UPDATE_ARGS=(--dry-run)
run_update env FAKE_CONTAINERS="versiond" FAKE_CONFIG_FILES="docker-compose.yml" \
    COMPOSE_FILE="docker-compose.yml:docker-compose.versiond.yml" UPDATE_SKIP_POSTGRES_PROBE=true || \
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
grep -q 'update remote versiond containers on their own hosts' "$tmpdir/err" || fail "HA overlay message: $(cat "$tmpdir/err")"

# Invalid timeout settings must be rejected before even querying Docker.
UPDATE_ARGS=(--check)
for duration in 0 -1 1s 08 86401; do
    if run_update env UPDATE_WAIT_TIMEOUT_SECONDS="$duration"; then fail "accepted invalid duration $duration"; fi
    grep -q 'must be an integer' "$tmpdir/err" || fail "duration refusal missing"
    [[ ! -s $tmpdir/log ]] || fail "invalid duration reached Docker"
done

# Host requirements are checked before the first Docker call, including timeout.
mkdir -p "$tmpdir/restricted-path"
for tool in bash dirname env jq timeout flock sha256sum python3; do
    ln -s "$(command -v "$tool")" "$tmpdir/restricted-path/$tool"
done
ln -s "$tmpdir/docker" "$tmpdir/restricted-path/docker"
for tool in docker jq timeout flock sha256sum python3; do
    mv "$tmpdir/restricted-path/$tool" "$tmpdir/missing-tool"
    if run_update env PATH="$tmpdir/restricted-path"; then fail "missing $tool accepted"; fi
    grep -q "$tool is required" "$tmpdir/err" || fail "missing utility message: $(cat "$tmpdir/err")"
    [[ ! -s $tmpdir/log ]] || fail "missing $tool reached Docker"
    mv "$tmpdir/missing-tool" "$tmpdir/restricted-path/$tool"
done

# The pin owner is a setting, not a fixed service name.
UPDATE_ARGS=()
run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" VERSIOND_LEGACY_HOST=versiond2 || \
    fail "non-default owner failed: $(cat "$tmpdir/err")"
[[ $(grep 'up -d .* versiond' "$tmpdir/log" | tail -1) == *' versiond2' ]] || fail "legacy owner was not last"

# A failed Compose listing must never be interpreted as an absent container.
if run_update env FAKE_CONTAINERS="versiond proxy" FAKE_CONFIG_FILES=docker-compose.yml FAKE_FAIL_PS_ALL=proxy; then
    fail "failed Compose ps was accepted"
fi
grep -q 'cannot list proxy for rollback' "$tmpdir/err" || fail "listing failure was swallowed"
! grep -q 'up -d .*proxy' "$tmpdir/log" || fail "proxy replaced after failed listing"

# A valid first replica cannot mask an unavailable or malformed second proof.
UPDATE_ARGS=(--check)
for mode in 503 timeout exec-failed malformed identity-only empty-targets; do
    if run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
        FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" FAKE_PROOF_versiond2="$mode"; then
        fail "accepted $mode proof from the second replica"
    fi
    [[ -z $(mutations) ]] || fail "mutated services after a failed proof"
    grep -Eq 'storage (lineage|proof)' "$tmpdir/err" || fail "unexpected $mode refusal: $(cat "$tmpdir/err")"
done

# All-legacy proofs are safe only for the known bundled cluster migration.
run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" FAKE_PROOF_MODE=404 || \
    fail "bundled legacy migration refused: $(cat "$tmpdir/err")"
jq '.services.versiond.environment.PGHOST = "external" | .services.versiond2.environment.PGHOST = "external"' \
    "$tmpdir/ha.json" >"$tmpdir/ha-external.json"
if run_update env FAKE_CONTAINERS="versiond versiond2" FAKE_PROOF_MODE=404 \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" FAKE_RENDERED_HA="$tmpdir/ha-external.json"; then
    fail "unproven external legacy database accepted"
fi
grep -q 'cannot prove' "$tmpdir/err" || fail "legacy external refusal: $(cat "$tmpdir/err")"

jq '.services.versiond.environment.PGPORT = "15432" | .services.versiond2.environment.PGPORT = "15432"' \
    "$tmpdir/ha.json" >"$tmpdir/ha-port.json"
if run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" FAKE_PROOF_MODE=404 \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" FAKE_RENDERED_HA="$tmpdir/ha-port.json"; then
    fail "legacy database port change accepted"
fi
grep -q 'existing database differs' "$tmpdir/err" || fail "legacy port change refusal missing"

# A database-change override cannot bypass live writers, even with probes disabled.
if run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" \
    UPDATE_ACCEPT_DATABASE_CHANGE=true UPDATE_SKIP_POSTGRES_PROBE=true; then fail "live database change accepted"; fi
grep -q 'requires all versiond writers stopped' "$tmpdir/err" || fail "offline guard missing"
run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" FAKE_STOPPED="versiond versiond2" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" UPDATE_ACCEPT_DATABASE_CHANGE=true || \
    fail "offline database change refused: $(cat "$tmpdir/err")"

# The budget is enforced against usable server slots: R=2, N=3, P=4 needs 82.
run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" VERSIOND_VERSIONS="v4 v5 v6" \
    FAKE_MAX_CONNECTIONS=85 || fail "exact capacity refused: $(cat "$tmpdir/err")"
grep -q 'budget 82 fits 82' "$tmpdir/out" || fail "capacity formula mismatch"
if run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" VERSIOND_VERSIONS="v4 v5 v6" \
    FAKE_MAX_CONNECTIONS=84; then fail "insufficient capacity accepted"; fi
grep -q 'need 82, available 81' "$tmpdir/err" || fail "capacity refusal missing"
[[ -z $(mutations) ]] || fail "capacity checked after replacing services"

# Running replicas marked for removal still consume connections until stopped.
if run_update env FAKE_CONTAINERS="versiond versiond2 versiond3 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" FAKE_RENDERED_HA="$tmpdir/ha3.json" \
    VERSIOND_VERSIONS="v4 v5 v6" FAKE_MAX_CONNECTIONS=125; then fail "decommissioned live replica omitted from capacity"; fi
grep -q 'need 123, available 122' "$tmpdir/err" || fail "decommissioned capacity mismatch"

if run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" VERSIOND_VERSIONS="v4 v5 v6" \
    VERSIOND_HOSTS="versiond versiond2 remote" UPDATE_POSTGRES_CONNECTION_RESERVE=10 FAKE_MAX_CONNECTIONS=135; then
    fail "remote membership or operator reserve omitted from capacity"
fi
grep -q 'need 133, available 132' "$tmpdir/err" || fail "remote capacity mismatch"

# Two local writers plus three distinct remote addresses need 205, not 123.
# Router ids do not prove that a remote address is a local replica.
cat >"$tmpdir/endpoints.json" <<'EOF'
[{"id":"versiond","host":"remote-a"}, {"id":"versiond2","host":"remote-b"},
 {"id":"third","host":"remote-c"}]
EOF
for membership in VERSIOND_POOL_ENDPOINTS_FILE="$tmpdir/endpoints.json" VERSIOND_HOSTS="remote-a remote-b remote-c"; do
    if run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
        FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" VERSIOND_VERSIONS="v4 v5 v6" \
        "$membership" FAKE_MAX_CONNECTIONS=126; then fail "disjoint remote writers omitted"; fi
    grep -q 'need 205, available 123' "$tmpdir/err" || fail "mixed capacity mismatch: $(cat "$tmpdir/err")"
    [[ -z $(mutations) ]] || fail "mixed capacity checked after replacement"
done

# Local membership and duplicate remote addresses are not counted twice;
# identical hosts on different ports still represent distinct writers.
cat >"$tmpdir/endpoints.json" <<'EOF'
[{"id":"local","host":"versiond"}, {"id":"local2","host":"versiond2","port":8080},
 {"id":"remote","host":"remote-a"}, {"id":"alias","host":"REMOTE-A","port":8080}]
EOF
run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" VERSIOND_VERSIONS="v4 v5 v6" \
    VERSIOND_POOL_ENDPOINTS_FILE="$tmpdir/endpoints.json" FAKE_MAX_CONNECTIONS=126 || \
    fail "overlapping membership refused: $(cat "$tmpdir/err")"
grep -q 'budget 123 fits 123' "$tmpdir/out" || fail "membership double-counted"
if run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" VERSIOND_VERSIONS="v4 v5 v6" \
    VERSIOND_HOSTS="versiond:8080 versiond2 remote-a:8080 remote-a:8081" FAKE_MAX_CONNECTIONS=126; then
    fail "distinct endpoint port omitted"
fi
grep -q 'need 164, available 123' "$tmpdir/err" || fail "host/port capacity mismatch"

# Explicit TLS configuration is outside the updater's supported scope. Reject
# it before replacement, even if all replicas agree or probes are disabled.
for key in PGSSLMODE PGSSLROOTCERT PGSSLCERT PGSSLKEY PGSSLSNI PGSSLPASSWORD PGSSLCRL; do
    for replicas in versiond2 'versiond versiond2'; do
        jq --arg key "$key" --arg replicas "$replicas" '
            reduce ($replicas | split(" ")[]) as $s (.; .services[$s].environment[$key] = "require")' \
            "$tmpdir/ha.json" >"$tmpdir/ha-tls.json"
        if run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
            FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" \
            FAKE_RENDERED_HA="$tmpdir/ha-tls.json" UPDATE_SKIP_POSTGRES_PROBE=true; then
            fail "unsupported TLS setting $key accepted"
        fi
        grep -q "unsupported TLS settings: $key" "$tmpdir/err" || fail "unsupported TLS diagnostic missing"
        [[ -z $(mutations) ]] || fail "unsupported TLS checked after replacement"
    done
done
jq '.services.versiond.environment.PGSSLMODE = "disable" |
    .services.versiond2.environment.PGSSLMODE = "disable"' "$tmpdir/ha.json" >"$tmpdir/ha-no-tls.json"
run_update env FAKE_CONTAINERS="versiond versiond2 devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" FAKE_RENDERED_HA="$tmpdir/ha-no-tls.json" || \
    fail "explicit non-TLS configuration refused: $(cat "$tmpdir/err")"
! grep -q -- '--volumes-from' "$tmpdir/log" || fail "PostgreSQL probe borrowed application mounts"

jq '.services.versiond2.environment.PGHOSTADDR = "127.0.0.2"' "$tmpdir/ha.json" >"$tmpdir/ha-hostaddr.json"
if run_update env FAKE_CONTAINERS="versiond devshard-postgres" \
    FAKE_CONFIG_FILES="docker-compose.yml,docker-compose.versiond.yml" FAKE_RENDERED_HA="$tmpdir/ha-hostaddr.json"; then
    fail "unsupported PGHOSTADDR accepted"
fi
grep -q 'versiond2 sets PGHOSTADDR' "$tmpdir/err" || fail "PGHOSTADDR refusal missing"
echo "update-devshard_test: ok"
