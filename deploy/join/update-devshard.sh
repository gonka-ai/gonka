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
#   5. versiond2, then versiond   (each behind active router checks, --wait)
#
# The single-versiond topology performs steps 3 and 5 only.

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
  --topology   override detection (auto: HA when devshard-postgres or
               versiond2 exists, or docker-compose.versiond.yml is in use)

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

# --- what to run and how ----------------------------------------------------

run() {
    printf '+'
    printf ' %q' "$@"
    printf '\n'
    if [[ $dry_run == false ]]; then
        "$@"
    fi
}

container_exists() {
    "$docker_bin" inspect --type container "$1" >/dev/null 2>&1
}

container_label() {
    "$docker_bin" inspect --format "{{index .Config.Labels \"$2\"}}" "$1" 2>/dev/null
}

# --- Compose files and project ---------------------------------------------

compose=("$docker_bin" compose --project-directory "$script_dir")
compose_files=()
project_name=
if [[ -n ${COMPOSE_FILE:-} ]]; then
    separator=${COMPOSE_PATH_SEPARATOR:-:}
    IFS=$separator read -r -a compose_files <<<"$COMPOSE_FILE"
    files_source=COMPOSE_FILE
elif container_exists versiond; then
    working_dir=$(container_label versiond com.docker.compose.project.working_dir)
    [[ -n $working_dir ]] || fail \
        "the running versiond container was not started by Docker Compose; set COMPOSE_FILE to the files of this deployment"
    [[ $(cd -- "$working_dir" 2>/dev/null && pwd -P) == "$script_dir" ]] || fail \
        "the running deployment lives in $working_dir, not in $script_dir; run the checkout that deployment uses or set COMPOSE_FILE"
    IFS=, read -r -a compose_files <<<"$(container_label versiond com.docker.compose.project.config_files)"
    project_name=$(container_label versiond com.docker.compose.project)
    files_source='running versiond container'
else
    compose_files=(docker-compose.yml)
    [[ $topology != ha ]] || compose_files+=(docker-compose.versiond.yml)
    files_source='stock files (no running versiond)'
fi
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

# --- topology ---------------------------------------------------------------

has_service() {
    jq -e --arg s "$1" '.services | has($s)' <<<"$rendered" >/dev/null
}

if [[ $topology == auto ]]; then
    if container_exists devshard-postgres || container_exists versiond2 || has_service versiond2; then
        topology=ha
    else
        topology=single
    fi
fi
if [[ $topology == ha ]]; then
    has_service versiond2 || fail \
        "HA topology needs docker-compose.versiond.yml in the Compose file list"
    if ! has_service proxy-policy || ! has_service proxy-policy2; then
        fail "the Compose model predates this release; refresh the checkout"
    fi
fi
echo "Topology: $topology"

# --- preflight --------------------------------------------------------------

service_env() {
    jq -r --arg s "$1" --arg k "$2" '.services[$s].environment[$k] // ""' <<<"$rendered"
}

postgres_mode=none
if [[ $topology == ha ]]; then
    for key in PGHOST PGPORT PGDATABASE PGUSER DEVSHARD_STORAGE_MODE; do
        a=$(service_env versiond "$key")
        b=$(service_env versiond2 "$key")
        [[ $a == "$b" ]] || fail \
            "versiond and versiond2 disagree on $key ('$a' vs '$b'); both replicas must share one PostgreSQL"
    done
    [[ $(service_env versiond DEVSHARD_STORAGE_MODE) == postgres ]] || fail \
        "HA versiond must run with DEVSHARD_STORAGE_MODE=postgres"
    [[ -n $(service_env versiond PGHOST) ]] || fail "HA versiond has no PGHOST"
    if [[ $(service_env versiond PGHOST) == devshard-postgres ]]; then
        has_service devshard-postgres || fail \
            "PGHOST=devshard-postgres but the service is not in the Compose model"
        postgres_mode=local
    else
        postgres_mode=external
        echo "PostgreSQL: external host $(service_env versiond PGHOST); the bundled devshard-postgres is not touched"
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
        DOCKER_BIN=$docker_bin "$migration_preflight_bin" \
            --source-container "$postgres_container" --target-dir "$postgres_target"
    fi
fi

echo "Images after the update:"
for service in versiond versiond2 devshard-postgres proxy-policy proxy; do
    has_service "$service" || continue
    printf '  %-18s %s\n' "$service" "$(jq -r --arg s "$service" '.services[$s].image' <<<"$rendered")"
done

if [[ $check_only == true ]]; then
    echo "Preflight passed; nothing was changed"
    exit 0
fi

# --- update -----------------------------------------------------------------

up() {
    run "${compose[@]}" up -d --no-deps --wait --wait-timeout "$wait_timeout" "$@"
}

replicas() {
    jq -r --arg s "$1" '.services[$s].deploy.replicas // 1' <<<"$rendered"
}

pull_services=(versiond proxy proxy-policy proxy-policy2)
if [[ $topology == ha ]]; then
    [[ $(replicas versiond2) == 0 ]] || pull_services+=(versiond2)
    [[ $postgres_mode != local ]] || pull_services+=(devshard-postgres)
fi
run "${compose[@]}" pull "${pull_services[@]}"

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

echo "Step: private policy workers, then the public proxy"
up proxy-policy2 proxy-policy
up proxy

if container_exists versiond-router; then
    # The pre-v5 overlay ran one nginx versiond-router service. The fleet slots
    # carry their own names, so a container called versiond-router in this
    # project is that legacy singleton.
    if [[ -n $project_name && $(container_label versiond-router com.docker.compose.project) == "$project_name" ]] || \
        [[ -z $project_name ]]; then
        echo "Step: removing the legacy versiond-router singleton"
        run "$docker_bin" rm -f versiond-router
    else
        echo "Leaving container versiond-router alone: it belongs to another Compose project"
    fi
fi

echo "Step: versiond"
if [[ $topology == ha && $(replicas versiond2) != 0 ]]; then
    up versiond2
fi
up versiond

echo "Update finished"
run "${compose[@]}" ps
if [[ $topology == ha ]]; then
    run env GONKA_CONFIG_ENV="$config_env" "$fleet_bin" status
fi
