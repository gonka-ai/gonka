#!/usr/bin/env bash

# Update the devshard services of one Gonka join deployment to the release in
# this checkout.
#
# This script only sequences ordinary `docker compose` commands in the order
# that keeps the deployment serving, and prints every command before running
# it. Nothing here cannot be done by hand; it exists so the order does not have
# to be remembered. State lives in Docker and config.env only: rerunning the
# script is safe, and rolling back is setting the previous image tags in
# config.env and running it again.
#
# Order (HA topology):
#   1. shared PostgreSQL          (entrypoint migrates a v4 anonymous volume)
#   2. versiond-router fleet      (HAProxy routers also serve pre-v5 versiond)
#   3. private policy nginx, then the public proxy (the one connection cut)
#   4. the legacy versiond-router singleton is removed
#   5. every other versiond replica, then versiond (the legacy owner), each
#      behind active router checks with --wait
#
# The single-versiond topology performs steps 3 and 5 only. Any number of
# local versiond services (versiond, versiond2, versiond3, ...) is handled;
# the shipped overlay defines two, docker-compose.versiond3.yml adds a third.
#
# Failure model: every step is one service behind a healthcheck. When a
# replaced service does not become healthy, its previous image is put back
# with the same `up` and the script stops; the deployment keeps serving the
# previous release of that service and the new release of the services
# replaced before it, which the HA design tolerates (routers and replicas roll
# one at a time by design). Fix the cause and rerun; Compose skips the services
# that already match. The whole run holds the deployment lock shared with
# versiond-router-fleet.sh, so two operators cannot interleave.

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
config_env=${GONKA_CONFIG_ENV:-$script_dir/config.env}
docker_bin=${DOCKER_BIN:-docker}
fleet_bin=${VERSIOND_ROUTER_FLEET_BIN:-$script_dir/versiond-router-fleet.sh}
migration_preflight_bin=${DEVSHARD_POSTGRES_MIGRATION_PREFLIGHT_BIN:-$script_dir/devshard-postgres-migration-preflight.sh}
min_compose_version=2.24.4
wait_timeout=${UPDATE_WAIT_TIMEOUT_SECONDS:-2100}
topology=auto
check_only=false
dry_run=false

fail() {
    echo "update-devshard: $*" >&2
    exit 1
}

usage() {
    cat >&2 <<'EOF'
Usage: update-devshard.sh [--check] [--dry-run] [--topology auto|single|ha]

Run from deploy/join after `git fetch` and checking out the release.
config.env is read from this directory (or GONKA_CONFIG_ENV).

  --check      detect the topology and run the preflight; change nothing
  --dry-run    print every command without running it
  --topology   override detection (auto: HA when the versiond service
               declares GONKA_HA=true, which the HA overlay sets)

Compose files come from COMPOSE_FILE when set, otherwise from the labels of
the running versiond container, otherwise from the stock files.
EOF
}

while (($# > 0)); do
    case $1 in
        --check) check_only=true; shift ;;
        --dry-run) dry_run=true; shift ;;
        --topology)
            (($# >= 2)) || fail "--topology requires a value"
            topology=$2
            shift 2
            ;;
        -h | --help) usage; exit 0 ;;
        *) usage; fail "unknown argument: $1" ;;
    esac
done
case $topology in auto | single | ha) ;; *) fail "--topology must be auto, single, or ha" ;; esac

# --- configuration ----------------------------------------------------------

[[ -f $config_env ]] || fail "configuration file not found: $config_env (copy config.env.template)"
set -a
# shellcheck disable=SC1090
source "$config_env"
set +a
config_dir=$(cd -- "$(dirname -- "$config_env")" && pwd -P)

for tool in "$docker_bin" jq; do
    command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
done
"$docker_bin" info >/dev/null 2>&1 || fail "cannot reach the Docker daemon with $docker_bin"
compose_version=$("$docker_bin" compose version --short 2>/dev/null) || \
    fail "Docker Compose v2 is required"
compose_core=${compose_version#v}
compose_core=${compose_core%%[-+]*}
[[ $(printf '%s\n%s\n' "$min_compose_version" "$compose_core" | sort -V | head -n1) == "$min_compose_version" ]] || \
    fail "Docker Compose $min_compose_version or newer is required; found $compose_version"

# One deployment, one operator at a time. The fleet script takes the same lock
# and inherits it from this process, so its steps below run under this hold.
if [[ $check_only == false ]]; then
    # shellcheck source=deploy/join/deployment-lock.sh
    source "$script_dir/deployment-lock.sh"
    gonka_acquire_deployment_lock "$config_dir" || exit 1
fi

# --- what to run and how ----------------------------------------------------

run() {
    printf '+'
    printf ' %q' "$@"
    printf '\n'
    if [[ $dry_run == false ]]; then
        "$@"
    fi
}

# "No such container" is the only Docker answer that means absent. Any other
# failure (daemon hiccup, permission) stops the run: guessing the topology
# from a failed query could turn an HA host into a single one.
container_exists() {
    local output
    if output=$("$docker_bin" inspect --type container "$1" 2>&1 >/dev/null); then
        return 0
    fi
    case ${output,,} in
        *"no such object"* | *"no such container"*) return 1 ;;
    esac
    fail "cannot inspect container $1: $output"
}

container_label() {
    "$docker_bin" inspect --format "{{index .Config.Labels \"$2\"}}" "$1" 2>/dev/null
}

# --- Compose files and project ---------------------------------------------

compose=("$docker_bin" compose --project-directory "$script_dir")
compose_files=()
project_name=
# Any container of the deployment carries the Compose labels. Do not depend on
# `versiond` alone: an interrupted run may have removed it while versiond2,
# PostgreSQL or the proxy still run, and those must not be mistaken for a fresh
# single-versiond host.
# Every container records the file list it was created with. After an overlay
# was added (a third replica, observability) only the containers recreated
# since carry the full list, so take the longest list that contains every
# other one in order; anything else is ambiguous and needs COMPOSE_FILE.
# Every container Compose started from this directory, whatever its name, so a
# replica added later as versiond5 or versiond12 counts as much as versiond.
# The fixed names cover a daemon that does not carry the working_dir label.
deployment_containers=$("$docker_bin" ps -a --format '{{.Names}}' \
    --filter "label=com.docker.compose.project.working_dir=$script_dir") || fail \
    "cannot list the containers of this deployment"
candidates=(versiond devshard-postgres proxy proxy-policy2 proxy-policy api node)
while IFS= read -r candidate; do
    [[ -n $candidate ]] || continue
    [[ " ${candidates[*]} " == *" $candidate "* ]] || candidates+=("$candidate")
done <<<"$deployment_containers"
label_source=
declare -A label_files=()
for candidate in "${candidates[@]}"; do
    container_exists "$candidate" || continue
    [[ -n $label_source ]] || label_source=$candidate
    files=$(container_label "$candidate" com.docker.compose.project.config_files)
    [[ -z $files ]] || label_files[$candidate]=$files
done

# Is comma list $1 an ordered subsequence of comma list $2?
files_subsequence() {
    local -a short long
    local index=0 item
    IFS=, read -r -a short <<<"$1"
    IFS=, read -r -a long <<<"$2"
    for item in "${short[@]}"; do
        while ((index < ${#long[@]})) && [[ ${long[index]} != "$item" ]]; do ((index += 1)); done
        ((index < ${#long[@]})) || return 1
        ((index += 1))
    done
}

complete_label_files() {
    local candidate other complete
    for candidate in "${!label_files[@]}"; do
        complete=true
        for other in "${!label_files[@]}"; do
            files_subsequence "${label_files[$other]}" "${label_files[$candidate]}" || { complete=false; break; }
        done
        if [[ $complete == true ]]; then
            printf '%s\n' "${label_files[$candidate]}"
            return 0
        fi
    done
    return 1
}
if [[ -n ${COMPOSE_FILE:-} ]]; then
    separator=${COMPOSE_PATH_SEPARATOR:-:}
    IFS=$separator read -r -a compose_files <<<"$COMPOSE_FILE"
    files_source=COMPOSE_FILE
elif [[ -n $label_source ]]; then
    working_dir=$(container_label "$label_source" com.docker.compose.project.working_dir)
    [[ -n $working_dir ]] || fail \
        "the running $label_source container was not started by Docker Compose; set COMPOSE_FILE to the files of this deployment"
    [[ $(cd -- "$working_dir" 2>/dev/null && pwd -P) == "$script_dir" ]] || fail \
        "the running deployment lives in $working_dir, not in $script_dir; run the checkout that deployment uses or set COMPOSE_FILE"
    files=$(complete_label_files) || fail \
        "the running containers record different Compose file lists ($(for c in "${!label_files[@]}"; do printf '%s: %s; ' "$c" "${label_files[$c]}"; done)); set COMPOSE_FILE to the complete ordered list"
    IFS=, read -r -a compose_files <<<"$files"
    files_source="running $label_source container"
else
    compose_files=(docker-compose.yml)
    [[ $topology != ha ]] || compose_files+=(docker-compose.versiond.yml)
    files_source='stock files (no running deployment)'
fi
[[ -z $label_source ]] || project_name=$(container_label "$label_source" com.docker.compose.project)
((${#compose_files[@]} > 0)) || fail "no Compose files"
for index in "${!compose_files[@]}"; do
    file=${compose_files[$index]}
    [[ $file == /* ]] || file=$script_dir/$file
    [[ -f $file ]] || fail "Compose file does not exist: $file"
    compose_files[index]=$file
    compose+=(-f "$file")
done
[[ -z $project_name ]] || compose+=(--project-name "$project_name")

echo "Compose files ($files_source):"
printf '  %s\n' "${compose_files[@]}"
rendered=$("${compose[@]}" config --format json) || fail "the Compose model does not render; fix the files above first"
[[ -n $project_name ]] || project_name=$(jq -r '.name // ""' <<<"$rendered")
[[ -n $project_name ]] || fail "cannot determine the Compose project name"

# --- topology ---------------------------------------------------------------

has_service() {
    jq -e --arg s "$1" '.services | has($s)' <<<"$rendered" >/dev/null
}

has_ha_model() {
    jq -e '.networks["versiond-router-back"] != null' <<<"$rendered" >/dev/null
}

# GONKA_HA is the deployment's HA declaration: versiond passes it to its
# children, devshardd refuses to boot with it unless storage is fail-closed
# PostgreSQL, and the routers stamp Devshard-Ha from it. The HA overlay sets it
# on every versiond service. Same boolean grammar as the Go side.
ha_declared() {
    local value
    value=$(jq -r --arg s "$1" '.services[$s].environment.GONKA_HA // ""' <<<"$rendered")
    case ${value,,} in
        1 | t | true | yes | on) return 0 ;;
        '' | 0 | f | false | no | off) return 1 ;;
        *) fail "$1 has GONKA_HA='$value'; use true or false" ;;
    esac
}

# Every local versiond replica: versiond, versiond2, versiond3, ... in that
# order. The first one is the legacy owner of pinned SQLite versions.
mapfile -t versiond_services < <(
    jq -r '.services | keys[] | select(test("^versiond[0-9]*$"))' <<<"$rendered" | sort -V)
((${#versiond_services[@]} > 0)) || fail "the Compose model has no versiond service"

if [[ $topology == auto ]]; then
    if ha_declared "${versiond_services[0]}"; then
        topology=ha
    else
        topology=single
    fi
fi
if [[ $topology == ha ]]; then
    for service in "${versiond_services[@]}"; do
        ha_declared "$service" || fail \
            "$service does not declare GONKA_HA=true; every replica of an HA deployment must"
    done
    has_ha_model || fail \
        "HA topology needs docker-compose.versiond.yml in the Compose file list"
else
    for candidate in "${candidates[@]}"; do
        [[ $candidate == devshard-postgres || $candidate =~ ^versiond[0-9]+$ ]] || continue
        ! container_exists "$candidate" || fail \
            "container $candidate exists but the Compose model is a single-versiond one; set COMPOSE_FILE to the HA file list instead of updating a partial model"
    done
fi
if ! has_service proxy-policy || ! has_service proxy-policy2; then
    fail "the Compose model predates this release; refresh the checkout"
fi
echo "Topology: $topology (${versiond_services[*]})"

# --- preflight --------------------------------------------------------------

service_env() {
    jq -r --arg s "$1" --arg k "$2" '.services[$s].environment[$k] // ""' <<<"$rendered"
}

postgres_mode=none
if [[ $topology == ha ]]; then
    first=${versiond_services[0]}
    for service in "${versiond_services[@]}"; do
        for key in PGHOST PGPORT PGDATABASE PGUSER PGPASSWORD PGSSLMODE PGSSLROOTCERT \
            PGTARGETSESSIONATTRS DEVSHARD_STORAGE_MODE; do
            a=$(service_env "$first" "$key")
            b=$(service_env "$service" "$key")
            if [[ $a != "$b" ]]; then
                [[ $key != PGPASSWORD ]] || fail \
                    "$first and $service disagree on PGPASSWORD; every replica must share one PostgreSQL"
                fail "$first and $service disagree on $key ('$a' vs '$b'); every replica must share one PostgreSQL"
            fi
        done
        # These libpq settings can send the supervisor's lookups or a child's
        # writes to a database other than the one named by PGHOST/PGDATABASE.
        for key in DATABASE_URL PGSERVICE PGSERVICEFILE PGOPTIONS; do
            [[ -z $(service_env "$service" "$key") ]] || fail \
                "$service sets $key; HA replicas must use only PGHOST, PGPORT, PGDATABASE, PGUSER and PGPASSWORD so every process reaches the same database"
        done
    done
    [[ $(service_env "$first" DEVSHARD_STORAGE_MODE) == postgres ]] || fail \
        "HA versiond must run with DEVSHARD_STORAGE_MODE=postgres"
    [[ -n $(service_env "$first" PGHOST) ]] || fail "HA versiond has no PGHOST"
    if [[ $(service_env "$first" PGHOST) == devshard-postgres ]]; then
        has_service devshard-postgres || fail \
            "PGHOST=devshard-postgres but the service is not in the Compose model"
        postgres_mode=local
    else
        postgres_mode=external
        echo "PostgreSQL: external host $(service_env "$first" PGHOST); the bundled devshard-postgres is not touched"
    fi
fi

postgres_helper_image=$(jq -r '.services["devshard-postgres"].image // ""' <<<"$rendered")
[[ -n $postgres_helper_image ]] || postgres_helper_image=postgres:16-alpine

# Proves, before anything is replaced, that the credentials in the model open
# the database and that it is a writable primary. This is what catches a
# changed DEVSHARD_POSTGRES_PASSWORD (the existing cluster keeps the old role
# password), a read-only replica, or an unreachable managed host, while the
# previous release is still fully running. psql runs from the PostgreSQL image
# on the deployment network with the same PG* settings as versiond.
postgres_probe() {
    local sql=$1 first=${versiond_services[0]} network key value lender
    local -a args=()
    for key in PGHOST PGPORT PGDATABASE PGUSER PGPASSWORD PGSSLMODE PGSSLROOTCERT \
        PGSSLCERT PGSSLKEY PGTARGETSESSIONATTRS; do
        value=$(service_env "$first" "$key")
        [[ -z $value ]] || args+=(-e "$key=$value")
    done
    network=$(jq -r '.networks.default.name // ""' <<<"$rendered")
    if [[ -z $network ]] || ! "$docker_bin" network inspect "$network" >/dev/null 2>&1; then
        network=host
    fi
    # A running versiond lends its volumes read-only, so a CA or client
    # certificate named by PGSSL* resolves to the same path in the helper.
    lender=$(running_versiond_container || true)
    [[ -z $lender ]] || args+=(--volumes-from "$lender:ro")
    timeout "${UPDATE_POSTGRES_PROBE_SECONDS:-60}" \
        "$docker_bin" run --rm --network "$network" "${args[@]}" "$postgres_helper_image" \
        psql -q -w -v ON_ERROR_STOP=1 -Atc "$sql"
}

running_versiond_container() {
    local service id
    for service in "${versiond_services[@]}"; do
        id=$("${compose[@]}" ps --quiet "$service") || fail "cannot list $service"
        [[ -n $id ]] || continue
        printf '%s\n' "${id%%$'\n'*}"
        return 0
    done
    return 1
}

# The database lineage the running replicas use, read through the first one
# that answers. A pre-v5 versiond has no such endpoint; that is the first
# cutover and there is nothing to compare yet.
# Reads a versiond's storage proof through its loopback-only endpoint. Prints
# the JSON; returns 1 for a pre-v5 image (404); any other failure stops the
# run, because an unavailable proof on a v5 replica is not "unsupported".
versiond_storage_proof() {
    local id=$1 output
    if output=$("$docker_bin" exec "$id" /bin/busybox wget -qO- -S -T 5 \
        http://127.0.0.1:8080/internal/storage-identity 2>&1); then
        printf '%s\n' "$output"
        return 0
    fi
    case $output in
        *"HTTP/"*" 404 "*) return 1 ;;
    esac
    fail "cannot read the storage lineage through container $id: ${output//$'\n'/ }"
}

if [[ $topology == ha && ${UPDATE_SKIP_POSTGRES_PROBE:-false} != true ]]; then
    probe_needed=true
    if [[ $postgres_mode == local ]]; then
        postgres_any=$("${compose[@]}" ps --all --quiet devshard-postgres) || fail \
            "cannot list devshard-postgres"
        postgres_up=$("${compose[@]}" ps --quiet devshard-postgres) || fail \
            "cannot list devshard-postgres"
        if [[ -z $postgres_any ]]; then
            probe_needed=false   # fresh install: nothing to open yet
        elif [[ -z $postgres_up ]]; then
            fail "devshard-postgres exists but is not running; start it (docker compose start devshard-postgres) so the update can verify the database before changing anything"
        fi
    fi
    if [[ $probe_needed == true ]]; then
        echo "PostgreSQL: checking that the configured credentials open a writable primary"
        # A temporary table exercises the write path (read-only primary,
        # revoked DML, full disk); the transaction is rolled back.
        recovery=$(postgres_probe 'BEGIN; CREATE TEMP TABLE gonka_update_probe (x int); INSERT INTO gonka_update_probe VALUES (1); SELECT pg_is_in_recovery(); ROLLBACK;') || fail \
            "cannot open PostgreSQL for writing with the PG* settings of ${versiond_services[0]} (host $(service_env "${versiond_services[0]}" PGHOST)); fix config.env or the database before updating. Set UPDATE_SKIP_POSTGRES_PROBE=true only if the probe cannot run from this host"
        [[ $recovery == f ]] || fail \
            "PostgreSQL at $(service_env "${versiond_services[0]}" PGHOST) is in recovery (a read-only replica); HA versiond needs the writable primary"

        # Lineage continuity: every running local replica must report the same
        # database, and that database must be the one the model names. The
        # identity alone cannot tell a physical clone apart, so the replica also
        # writes a nonce through its own pool and the model's database must
        # show it; a clone with the same identity would not.
        running_identity=
        proof_source=
        proof_container=
        proof_json=
        for service in "${versiond_services[@]}"; do
            id=$("${compose[@]}" ps --quiet "$service") || fail "cannot list $service"
            [[ -n $id ]] || continue
            id=${id%%$'\n'*}
            proof=$(versiond_storage_proof "$id") || continue
            identity=$(jq -er '.identity | strings | select(length > 0)' <<<"$proof") || fail \
                "$service returned a storage proof without an identity"
            if [[ -z $running_identity ]]; then
                running_identity=$identity
                proof_source=$service
                proof_container=$id
                proof_json=$proof
            elif [[ $identity != "$running_identity" ]]; then
                fail "$proof_source and $service run on different PostgreSQL lineages ($running_identity vs $identity); the pool is already split, repair it before updating"
            fi
        done
        if [[ -n $running_identity && ${UPDATE_ACCEPT_DATABASE_CHANGE:-false} != true ]]; then
            target_identity=$(postgres_probe 'SELECT identity::text FROM devshard_storage_identity WHERE singleton' 2>/dev/null || true)
            target_description="no devshard schema"
            [[ -z $target_identity ]] || target_description="lineage $target_identity"
            [[ $target_identity == "$running_identity" ]] || fail \
                "the running replicas use PostgreSQL lineage $running_identity but the model names a database with $target_description; a rolling replacement would split the pool between two histories. Fix PGHOST, or set UPDATE_ACCEPT_DATABASE_CHANGE=true only for an intended move to a restored copy"
            snapshot=$(jq -r '.snapshot // ""' <<<"$proof_json")
            generation=$(jq -r '.targets[0].generation // ""' <<<"$proof_json")
            if [[ -n $snapshot && -n $generation ]]; then
                nonce=$(cat /proc/sys/kernel/random/uuid)
                request=$(jq -cn --arg n "$nonce" --arg s "$snapshot" --arg g "$generation" \
                    '{operation:"write", nonce:$n, snapshot:$s, generation:$g}')
                "$docker_bin" exec "$proof_container" /bin/busybox wget -qO- -T 5 \
                    --header 'Content-Type: application/json' --post-data "$request" \
                    http://127.0.0.1:8080/internal/storage-challenge >/dev/null 2>&1 || fail \
                    "$proof_source could not write a storage challenge through its PostgreSQL pool; inspect 'docker compose logs $proof_source'"
                observed=$(postgres_probe 'SELECT challenge::text FROM devshard_storage_identity WHERE singleton' 2>/dev/null || true)
                [[ $observed == "$nonce" ]] || fail \
                    "the database the model names did not receive the challenge $proof_source just wrote; it is a copy of the running database, not the same one. Fix PGHOST, or set UPDATE_ACCEPT_DATABASE_CHANGE=true only for an intended move"
                echo "PostgreSQL: database lineage $running_identity unchanged; $proof_source wrote a challenge the model's database shows"
            else
                echo "PostgreSQL: database lineage $running_identity unchanged"
            fi
        fi
    fi
fi

if [[ $postgres_mode == local ]]; then
    # A v4 installation keeps its cluster on the image's anonymous volume. The
    # v5 entrypoint copies it into the bind directory on first start; check the
    # copy fits before stopping anything. Fresh installs have no container yet.
    postgres_container=$("${compose[@]}" ps --all --quiet devshard-postgres 2>/dev/null || true)
    postgres_target=$(jq -r '
        [.services["devshard-postgres"].volumes[]?
         | select(.target == "/var/lib/postgresql/gonka" and .type == "bind")]
        | .[0].source // ""' <<<"$rendered")
    [[ -n $postgres_target ]] || fail \
        "devshard-postgres must bind-mount its data directory at /var/lib/postgresql/gonka"
    if [[ -n $postgres_container ]]; then
        [[ -x $migration_preflight_bin ]] || fail \
            "missing $migration_preflight_bin"
        echo "PostgreSQL: checking the migration copy fits in $postgres_target"
        DOCKER_BIN=$docker_bin POSTGRES_MIGRATION_HELPER_IMAGE=$postgres_helper_image \
            "$migration_preflight_bin" \
            --source-container "$postgres_container" --target-dir "$postgres_target"
    fi
fi

echo "Images after the update:"
for service in "${versiond_services[@]}" devshard-postgres proxy-policy proxy; do
    has_service "$service" || continue
    printf '  %-18s %s\n' "$service" "$(jq -r --arg s "$service" '.services[$s].image' <<<"$rendered")"
done

if [[ $check_only == true ]]; then
    echo "Preflight passed; nothing was changed"
    exit 0
fi

# --- update -----------------------------------------------------------------

image_variable() {
    case $1 in
        versiond*) printf 'VERSIOND_IMAGE\n' ;;
        devshard-postgres) printf 'DEVSHARD_POSTGRES_IMAGE\n' ;;
        proxy) printf 'PROXY_ROUTER_IMAGE\n' ;;
        proxy-policy*) printf 'PROXY_POLICY_IMAGE\n' ;;
    esac
}

container_health() {
    local id
    id=$("${compose[@]}" ps --all --quiet "$1" 2>/dev/null | head -n 1) || return 1
    [[ -n $id ]] || return 1
    "$docker_bin" inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}{{.State.Status}}{{end}}' "$id"
}

current_image() {
    local id
    id=$("${compose[@]}" ps --all --quiet "$1" 2>/dev/null | head -n 1) || return 1
    [[ -n $id ]] || return 1
    "$docker_bin" inspect --format '{{.Image}}' "$id"
}

desired_image_id() {
    local reference
    reference=$(jq -r --arg s "$1" '.services[$s].image // ""' <<<"$rendered")
    [[ -n $reference ]] || return 1
    "$docker_bin" image inspect --format '{{.Id}}' "$reference" 2>/dev/null
}

# Replace one service and wait for its healthcheck. The image it ran before
# is kept under the gonka-previous/<service> tag in Docker's own store, so it
# survives a killed run: a rerun that finds an unhealthy candidate still puts
# the real previous image back, and an operator can do the same by hand with
# <IMAGE_VAR>=gonka-previous/<service>. If the replacement never becomes
# healthy, the previous image is put back with the same command and the run
# stops; the host keeps serving.
up() {
    local service=$1 previous_tag current health variable
    previous_tag=gonka-previous/$service
    # The tag always names the last image that was serving: a candidate that
    # never became healthy must not overwrite it, and a configuration-only
    # change still refreshes it from the healthy container it replaces.
    current=$(current_image "$service" 2>/dev/null || true)
    if [[ -n $current ]]; then
        health=$(container_health "$service")
        if [[ $health == healthy ]]; then
            run "$docker_bin" tag "$current" "$previous_tag"
        elif ! "$docker_bin" image inspect "$previous_tag" >/dev/null 2>&1; then
            run "$docker_bin" tag "$current" "$previous_tag"
        fi
    fi
    if run "${compose[@]}" up -d --no-deps --wait --wait-timeout "$wait_timeout" "$service"; then
        return 0
    fi
    variable=$(image_variable "$service")
    if [[ $dry_run == false && -n $variable ]] && \
        "$docker_bin" image inspect "$previous_tag" >/dev/null 2>&1; then
        echo "update-devshard: $service did not become healthy; putting back its previous image ($previous_tag)" >&2
        if run env "$variable=$previous_tag" "${compose[@]}" up -d --no-deps --wait \
            --wait-timeout "$wait_timeout" "$service"; then
            fail "$service was put back on its previous image and the update stopped here; inspect 'docker compose logs $service', fix the cause and rerun"
        fi
        fail "$service could not be put back either: its service definition changed beyond the image (the first nginx-to-HAProxy proxy cutover is the known case). Restore the previous release's Compose files (git checkout <previous release> -- deploy/join) and run 'docker compose up -d $service'"
    fi
    fail "$service did not become healthy; the update stopped here ('docker compose logs $service')"
}

replicas() {
    jq -r --arg s "$1" '.services[$s].deploy.replicas // 1' <<<"$rendered"
}

# Replicas whose desired count is 0 are decommissioned and left alone.
active_versiond=()
for service in "${versiond_services[@]}"; do
    [[ $(replicas "$service") == 0 ]] || active_versiond+=("$service")
done
((${#active_versiond[@]} > 0)) || fail "every versiond service has 0 replicas"
# Versions in VERSIOND_NON_HA_VERSIONS keep SQLite state on the legacy owner
# only; decommissioning it would take those versions off the network.
legacy_owner=${VERSIOND_LEGACY_HOST:-versiond}
if [[ $topology == ha ]] && has_service "$legacy_owner" && [[ $(replicas "$legacy_owner") == 0 ]]; then
    pinned=$(service_env "$legacy_owner" VERSIOND_NON_HA_VERSIONS)
    [[ -z ${pinned//[[:space:],;]/} ]] || fail \
        "$legacy_owner is VERSIOND_LEGACY_HOST and still owns the pinned versions '$pinned'; it cannot be set to 0 replicas while VERSIOND_NON_HA_VERSIONS is non-empty"
fi

pull_services=()
for service in "${active_versiond[@]}" proxy proxy-policy proxy-policy2 devshard-postgres; do
    [[ $service != devshard-postgres || $postgres_mode == local ]] || continue
    # Only images that are not on this host yet: a rerun after a failure works
    # without the registry.
    "$docker_bin" image inspect "$(jq -r --arg s "$service" '.services[$s].image' <<<"$rendered")" >/dev/null 2>&1 || \
        pull_services+=("$service")
done
((${#pull_services[@]} == 0)) || run "${compose[@]}" pull "${pull_services[@]}"

if [[ $postgres_mode == local ]]; then
    echo "Step: shared PostgreSQL"
    up devshard-postgres
fi

if [[ $topology == ha ]]; then
    echo "Step: versiond-router fleet"
    [[ -x $fleet_bin ]] || fail "missing $fleet_bin"
    run env GONKA_CONFIG_ENV="$config_env" "$fleet_bin" prepare-networks
    run env GONKA_CONFIG_ENV="$config_env" "$fleet_bin" apply
fi

echo "Step: private policy workers one at a time, then the public proxy"
up proxy-policy2
up proxy-policy
up proxy
if [[ $topology == ha ]]; then
    # The proxy healthcheck covers its policy workers only. Before the legacy
    # router is removed and versiond is replaced, the new public proxy must
    # admit every router slot and every live route end to end.
    run env GONKA_CONFIG_ENV="$config_env" "$fleet_bin" verify-admission
fi

if container_exists versiond-router; then
    # The pre-v5 overlay ran one nginx versiond-router service. The fleet slots
    # carry their own names, so a container called versiond-router in this
    # project is that legacy singleton.
    if [[ $(container_label versiond-router com.docker.compose.project) == "$project_name" ]]; then
        echo "Step: removing the legacy versiond-router singleton"
        run "$docker_bin" rm -f versiond-router
    else
        echo "Leaving container versiond-router alone: it belongs to another Compose project"
    fi
fi

echo "Step: versiond replicas (${active_versiond[*]})"
# Last replica first, the legacy owner last: while it is being replaced, the
# other replicas already run the new release behind the routers.
for ((i = ${#active_versiond[@]} - 1; i >= 0; i--)); do
    up "${active_versiond[i]}"
done
# A replica whose desired count is 0 is decommissioned: stop and remove it so
# `restart: always` cannot bring it back into the pool.
for service in "${versiond_services[@]}"; do
    [[ $(replicas "$service") == 0 ]] || continue
    [[ -n $("${compose[@]}" ps --all --quiet "$service" 2>/dev/null || true) ]] || continue
    echo "Step: decommissioning $service (replicas: 0)"
    run "${compose[@]}" stop "$service"
    run "${compose[@]}" rm -f "$service"
done

echo "Update finished"
run "${compose[@]}" ps
if [[ $topology == ha ]]; then
    run env GONKA_CONFIG_ENV="$config_env" "$fleet_bin" status
fi
