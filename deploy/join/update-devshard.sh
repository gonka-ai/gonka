#!/usr/bin/env bash

# Update the devshard services of one Gonka join deployment to the release in
# this checkout.
#
# The host updater sequences PostgreSQL, the router fleet, public ingress, and
# versiond replicas. VERSIOND_LEGACY_HOST is replaced last. Public ingress is
# replaced as a group because its proxy and policy workers depend on each other.
# Failed replacements restore the saved Docker specification; an interrupted
# replacement is recovered on the next normal run. PostgreSQL restores only its
# image, retaining the migration target and its data. See the release guide for
# maintenance requirements and rollback limits.
# The entire run holds the same deployment lock as versiond-router-fleet.sh.

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
storage_check_only=false
storage_reference_env=
storage_containers=()
topology_option=false

fail() {
    echo "update-devshard: $*" >&2
    exit 1
}

usage() {
    cat >&2 <<'EOF'
Usage: update-devshard.sh [--check] [--dry-run] [--topology auto|single|ha]
       update-devshard.sh --check-storage --reference-env FILE [--container NAME ...]

Run from deploy/join after `git fetch` and checking out the release.
config.env is read from this directory (or GONKA_CONFIG_ENV).

  --check      run the preflight without replacing services (writes DB probes)
  --dry-run    run the preflight, then print the replacement commands
  --topology   override detection (auto: HA when the versiond service
               declares GONKA_HA=true, which the HA overlay sets)

  --check-storage  verify running HA devshard processes against an independently
                   configured PostgreSQL; no config.env or Compose required
  --reference-env  shell file with the known working pool's PGHOST, PGDATABASE,
                   PGUSER and connection settings (psql runs in Docker)
  --container      versiond container to check; repeat for several (default: versiond)

Storage checking writes a control value through each process's database pool.
Run checks one host at a time, before admitting new/replaced replicas to traffic.
The client uses DEVSHARD_POSTGRES_IMAGE (default postgres:16-alpine) and host
networking. Explicit TLS settings are unsupported; PGSSLMODE=disable is allowed.

Compose files come from COMPOSE_FILE when set, otherwise from the labels of
the running versiond container, otherwise from the stock files.
EOF
}

while (($# > 0)); do
    case $1 in
        --check) check_only=true; shift ;;
        --dry-run) dry_run=true; shift ;;
        --check-storage) storage_check_only=true; shift ;;
        --reference-env)
            (($# >= 2)) || fail "--reference-env requires a file"
            storage_reference_env=$2; shift 2 ;;
        --container)
            (($# >= 2)) || fail "--container requires a name"
            storage_containers+=("$2"); shift 2 ;;
        --topology)
            (($# >= 2)) || fail "--topology requires a value"
            topology=$2
            topology_option=true
            shift 2
            ;;
        -h | --help) usage; exit 0 ;;
        *) usage; fail "unknown argument: $1" ;;
    esac
done
case $topology in auto | single | ha) ;; *) fail "--topology must be auto, single, or ha" ;; esac

if [[ $storage_check_only == true ]]; then
    [[ $check_only == false && $dry_run == false && $topology_option == false ]] || \
        fail "--check-storage cannot be combined with --check, --dry-run or --topology"
    [[ -n $storage_reference_env ]] || fail "--check-storage requires --reference-env"
    # shellcheck source=deploy/join/versiond-storage-check.sh
    source "$script_dir/versiond-storage-check.sh"
    check_versiond_storage "$docker_bin" "$script_dir" "$config_env" \
        "$storage_reference_env" "${storage_containers[@]}"
    exit 0
fi
[[ -z $storage_reference_env && ${#storage_containers[@]} == 0 ]] || \
    fail "--reference-env and --container require --check-storage"

# --- configuration ----------------------------------------------------------

[[ -f $config_env ]] || fail "configuration file not found: $config_env (copy config.env.template)"
set -a
# shellcheck disable=SC1090
source "$config_env"
set +a
config_dir=$(cd -- "$(dirname -- "$config_env")" && pwd -P)

for tool in "$docker_bin" jq timeout flock sha256sum python3; do
    command -v "$tool" >/dev/null 2>&1 || fail "$tool is required"
done
for setting in UPDATE_WAIT_TIMEOUT_SECONDS UPDATE_POSTGRES_PROBE_SECONDS; do
    value=${!setting:-}
    [[ -n $value ]] || continue
    [[ $value =~ ^[1-9][0-9]{0,5}$ && $value -le 86400 ]] || \
        fail "$setting must be an integer from 1 to 86400 seconds"
done
wait_timeout=${UPDATE_WAIT_TIMEOUT_SECONDS:-2100}
"$docker_bin" info >/dev/null 2>&1 || fail "cannot reach the Docker daemon with $docker_bin"
compose_version=$("$docker_bin" compose version --short 2>/dev/null) || \
    fail "Docker Compose v2 is required"
compose_core=${compose_version#v}
compose_core=${compose_core%%[-+]*}
[[ $(printf '%s\n%s\n' "$min_compose_version" "$compose_core" | sort -V | head -n1) == "$min_compose_version" ]] || \
    fail "Docker Compose $min_compose_version or newer is required; found $compose_version"

# One deployment, one operator at a time. The fleet script takes the same lock
# and inherits it from this process, so its steps below run under this hold.
# --check writes a storage challenge too, so it takes the lock as well; two
# concurrent checks would otherwise overwrite each other's nonce.
# shellcheck source=deploy/join/deployment-lock.sh
source "$script_dir/deployment-lock.sh"

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
    local format="{{index .Config.Labels \"$2\"}}" recovery_label
    case $2 in
        com.docker.compose.project.config_files | com.docker.compose.project.working_dir)
            recovery_label=ai.gonka.updater.compose.${2##*.}
            format="{{if index .Config.Labels \"$recovery_label\"}}{{index .Config.Labels \"$recovery_label\"}}{{else}}$format{{end}}"
            ;;
    esac
    "$docker_bin" inspect --format "$format" "$1" 2>/dev/null
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
# Recovery containers retain canonical paths separately: Compose's built-in
# labels point to the temporary frozen model, which has already been deleted.
recovery_containers=$("$docker_bin" ps -a --format '{{.Names}}' \
    --filter "label=ai.gonka.updater.compose.working_dir=$script_dir") || fail \
    "cannot list recovered containers of this deployment"
deployment_containers+=$'\n'$recovery_containers
candidates=(versiond devshard-postgres proxy proxy-policy2 proxy-policy api node)
while IFS= read -r candidate; do
    [[ -n $candidate ]] || continue
    [[ " ${candidates[*]} " == *" $candidate "* ]] || candidates+=("$candidate")
done <<<"$deployment_containers"
label_source=
declare -A label_files=()
for candidate in "${candidates[@]}"; do
    container_exists "$candidate" || continue
    # Router fleet slots share this directory but are their own Compose
    # projects; their file list must not compete with the main model's.
    [[ $(container_label "$candidate" ai.gonka.component) != versiond-router ]] || continue
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
        "the host updater requires the network node's HA Compose model; update remote versiond containers on their own hosts with Docker Compose, then run --check-storage before pool admission"
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
gonka_acquire_deployment_lock "$config_dir" "$project_name" || exit 1
# shellcheck source=deploy/join/updater-rollback.sh
source "$script_dir/updater-rollback.sh"
initialize_rollback

# --- preflight --------------------------------------------------------------

service_env() {
    jq -r --arg s "$1" --arg k "$2" '.services[$s].environment[$k] // ""' <<<"$rendered"
}

replicas() {
    jq -r --arg s "$1" '.services[$s].deploy.replicas // 1' <<<"$rendered"
}

active_versiond=()
for service in "${versiond_services[@]}"; do
    count=$(replicas "$service")
    [[ $count == 0 || $count == 1 ]] || fail "$service must have 0 or 1 replicas; add separate versiond services for more hosts"
    [[ $count == 0 ]] || active_versiond+=("$service")
done
((${#active_versiond[@]} > 0)) || fail "every versiond service has 0 replicas"
legacy_owner=${VERSIOND_LEGACY_HOST:-versiond}
if [[ $topology == ha ]] && has_service "$legacy_owner" && [[ $(replicas "$legacy_owner") == 0 ]]; then
    pinned=$(service_env "$legacy_owner" VERSIOND_NON_HA_VERSIONS)
    [[ -z ${pinned//[[:space:],;]/} ]] || fail \
        "$legacy_owner is VERSIOND_LEGACY_HOST and still owns the pinned versions '$pinned'; it cannot be set to 0 replicas while VERSIOND_NON_HA_VERSIONS is non-empty"
fi

# The migration override is an offline operation, never a way to skip a
# failing proof on a live writer. This guard also applies when probes are skipped.
if [[ ${UPDATE_ACCEPT_DATABASE_CHANGE:-false} == true ]]; then
    for service in "${versiond_services[@]}"; do
        id=$("${compose[@]}" ps --quiet "$service") || fail "cannot list $service"
        [[ -z $id ]] || fail "UPDATE_ACCEPT_DATABASE_CHANGE requires all versiond writers stopped; $service is running"
    done
    [[ -z ${VERSIOND_POOL_ENDPOINTS_FILE:-} && -z ${VERSIOND_HOSTS:-} ]] || fail \
        "UPDATE_ACCEPT_DATABASE_CHANGE cannot verify remote writers are stopped; migrate a multi-host database using the offline recovery procedure"
fi

postgres_mode=none
if [[ $topology == ha ]]; then
    first=${versiond_services[0]}
    for service in "${versiond_services[@]}"; do
        # TLS is outside this host updater's supported deployment scope.
        # Reject explicit TLS configuration rather than claiming to validate
        # candidate certificates through the live container's old mounts.
        unsupported_tls=$(jq -r --arg s "$service" '
            [.services[$s].environment | to_entries[] |
             select(.key | startswith("PGSSL")) | select(.value != null and .value != "") |
             select(.key != "PGSSLMODE" or .value != "disable") | .key] | join(", ")' <<<"$rendered")
        [[ -z $unsupported_tls ]] || fail \
            "$service sets unsupported TLS settings: $unsupported_tls; this host updater supports PostgreSQL without explicit TLS configuration (PGSSLMODE=disable is allowed)"
        for key in PGHOST PGPORT PGDATABASE PGUSER PGPASSWORD PGSSLMODE \
            PGTARGETSESSIONATTRS DEVSHARD_STORAGE_MODE; do
            a=$(service_env "$first" "$key")
            b=$(service_env "$service" "$key")
            if [[ $a != "$b" ]]; then
                [[ $key != PGPASSWORD ]] || fail \
                    "$first and $service disagree on PGPASSWORD; every replica must share one PostgreSQL"
                fail "$first and $service disagree on $key ('$a' vs '$b'); every replica must share one PostgreSQL"
            fi
        done
        # Reject implicit libpq destinations and settings unsupported by the
        # child pgx connection parser; use the explicit shared destination.
        for key in DATABASE_URL PGSERVICE PGSERVICEFILE PGOPTIONS PGHOSTADDR; do
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
    local sql=$1 first=${versiond_services[0]} network key value
    local -a args=()
    for key in PGHOST PGPORT PGDATABASE PGUSER PGPASSWORD PGSSLMODE PGTARGETSESSIONATTRS; do
        value=$(service_env "$first" "$key")
        [[ -z $value ]] || args+=(-e "$key=$value")
    done
    network=$(jq -r '.networks.default.name // ""' <<<"$rendered")
    if [[ -z $network ]] || ! "$docker_bin" network inspect "$network" >/dev/null 2>&1; then
        network=host
    fi
    timeout "${UPDATE_POSTGRES_PROBE_SECONDS:-60}" \
        "$docker_bin" run --rm --network "$network" "${args[@]}" "$postgres_helper_image" \
        psql -q -w -v ON_ERROR_STOP=1 -Atc "$sql"
}

# Reads a versiond's storage proof through its loopback-only endpoint. Prints
# the JSON; returns 1 for a pre-v5 image (404); any other failure stops the
# run, because an unavailable proof on a v5 replica is not "unsupported".
versiond_storage_proof() {
    local id=$1 body errors
    errors=$(mktemp)
    if body=$("$docker_bin" exec "$id" /bin/busybox wget -qO- -T 5 \
        http://127.0.0.1:8080/internal/storage-identity 2>"$errors"); then
        rm -f "$errors"
        printf '%s\n' "$body"
        return 0
    fi
    body=$(tr '\n' ' ' <"$errors"); rm -f "$errors"
    case $body in
        *"HTTP/"*" 404 "*) return 1 ;;
    esac
    echo "update-devshard: cannot read the storage lineage through container $id: $body" >&2
    return 3
}

validate_storage_proof() {
    jq -e '
        (.identity | type == "string" and length > 0) and
        (.snapshot | type == "string" and length > 0) and
        (.targets | type == "array" and length > 0) and
        (.children == (.targets | length)) and
        all(.targets[];
            (.generation | type == "string" and length > 0) and
            (.version | type == "string" and length > 0) and
            (.pool_max_connections | type == "number" and . > 0 and . == floor)) and
        (([.targets[].generation] | unique | length) == (.targets | length))
    ' >/dev/null
}

# v4 has no application-pool proof. Only the bundled cluster's checked
# volume migration establishes provenance automatically for that first cut.
check_legacy_database() {
    local service=$1 id=$2 previous_host previous_database previous_port candidate_port environment
    [[ $postgres_mode == local ]] || fail \
        "$service has no storage proof API; an online update cannot prove continuity with external PostgreSQL. Stop every writer and use UPDATE_ACCEPT_DATABASE_CHANGE=true only after verifying the intended database"
    environment=$("$docker_bin" inspect --format '{{json .Config.Env}}' "$id") || fail "cannot read the existing environment of $service"
    previous_host=$(jq -r '[.[] | select(startswith("PGHOST=")) | ltrimstr("PGHOST=")][0] // ""' <<<"$environment") || \
        fail "cannot read the existing PGHOST of $service"
    previous_database=$(jq -r '[.[] | select(startswith("PGDATABASE=")) | ltrimstr("PGDATABASE=")][0] // ""' <<<"$environment") || \
        fail "cannot read the existing PGDATABASE of $service"
    previous_port=$(jq -r '[.[] | select(startswith("PGPORT=")) | ltrimstr("PGPORT=")][0] // ""' <<<"$environment") || \
        fail "cannot read the existing PGPORT of $service"
    candidate_port=$(service_env "$service" PGPORT)
    [[ $previous_host == devshard-postgres && $previous_database == "$(service_env "$service" PGDATABASE)" && \
       ${previous_port:-5432} == "${candidate_port:-5432}" ]] || \
        fail "$service has no storage proof and its existing database differs from the bundled migration target; stop writers and verify the database before an offline migration"
}

check_connection_budget() {
    local versions capacity maximum reserved available required pool observed_pool endpoint_file reserve service id endpoints additional
    local members=${#active_versiond[@]} largest_pool=0
    local -a local_members=("${active_versiond[@]}")
    # A decommissioned replica still consumes connections until the final
    # stop step. Count it together with every candidate that will be started.
    for service in "${versiond_services[@]}"; do
        [[ " ${active_versiond[*]} " != *" $service "* ]] || continue
        id=$("${compose[@]}" ps --quiet "$service") || fail "cannot list $service for PostgreSQL capacity"
        [[ -z $id ]] || { ((members += 1)); local_members+=("$service"); }
    done
    versions=$(printf '%s\n' "${VERSIOND_VERSIONS:-v4 v5}"; \
        for proof in "${proof_documents[@]}"; do jq -r '.targets[].version' <<<"$proof"; done)
    versions=$(jq -nr --arg names "$versions" --arg legacy "${VERSIOND_NON_HA_VERSIONS:-v1 v2 v3}" '
        ($legacy | [splits("[ ,;\\s]+") | select(length > 0)]) as $legacy |
        $names | [splits("[ ,;\\s]+") | select(length > 0)] | unique - $legacy | length')
    ((versions > 0 && versions <= 10000)) || fail "cannot determine the HA version count for PostgreSQL capacity"
    for service in "${active_versiond[@]}"; do
        pool=$(service_env "$service" PG_POOL_MAX_CONNS)
        pool=${pool:-4}
        [[ $pool =~ ^[1-9][0-9]{0,5}$ ]] || fail "$service has an invalid PG_POOL_MAX_CONNS"
        ((pool <= largest_pool)) || largest_pool=$pool
    done
    for proof in "${proof_documents[@]}"; do
        observed_pool=$(jq '[.targets[].pool_max_connections] | max' <<<"$proof")
        ((observed_pool <= 999999)) || fail "running PostgreSQL pool capacity is invalid"
        ((observed_pool <= largest_pool)) || largest_pool=$observed_pool
    done
    endpoints='[]'
    if [[ -n ${VERSIOND_POOL_ENDPOINTS_FILE:-} ]]; then
        endpoint_file=$VERSIOND_POOL_ENDPOINTS_FILE
        [[ $endpoint_file == /* ]] || endpoint_file=$config_dir/$endpoint_file
        endpoints=$(jq -ce 'select(type == "array" and length > 0)' "$endpoint_file") || \
            fail "cannot read versiond pool membership for PostgreSQL capacity"
    elif [[ -n ${VERSIOND_HOSTS:-} ]]; then
        endpoints=$(jq -n --arg hosts "$VERSIOND_HOSTS" '$hosts |
            [splits("[ ,;\\s]+") | select(length > 0) | split(":") |
                if length > 2 then error("invalid versiond address") else {host: .[0], port: .[1]} end]') || \
            fail "cannot read VERSIOND_HOSTS for PostgreSQL capacity"
    fi
    # Membership can omit local writers (including ones about to be stopped).
    # Count the union, identifying local entries by service/container address,
    # never by the router's arbitrary id. Unrecognized aliases/IPs count extra.
    additional=$(jq -er --argjson endpoints "$endpoints" --arg locals "${local_members[*]}" \
        --arg port "${VERSIOND_PORT:-8080}" '
        . as $model |
        [$locals | split(" ")[] | . as $s |
            ($s, $model.services[$s].container_name // empty) | ascii_downcase] as $local_hosts |
        $endpoints | map(
            if (.host | type) != "string" then error("invalid endpoint host") else . end |
            .host |= ascii_downcase | .port = ((.port // $port) | tonumber) |
            if (.host | test("^[a-z0-9][a-z0-9.-]*$")) and
                (.port >= 1 and .port <= 65535 and .port == (.port | floor))
            then {host, port} else error("invalid endpoint address") end) |
        unique_by([.host, .port]) |
        map(select(.port != 8080 or (.host as $h | $local_hosts | index($h) | not))) | length
    ' <<<"$rendered") || fail "cannot determine versiond pool membership for PostgreSQL capacity"
    members=$((members + additional))
    ((members <= 10000)) || fail "versiond pool membership exceeds the supported capacity calculation"
    reserve=${UPDATE_POSTGRES_CONNECTION_RESERVE:-0}
    [[ $reserve =~ ^[0-9]{1,6}$ ]] || fail "UPDATE_POSTGRES_CONNECTION_RESERVE must be an integer from 0 to 999999"
    required=$((members * (2 * versions * (largest_pool + 2) + 5) + 10#$reserve))
    capacity=$(postgres_probe "SELECT current_setting('max_connections')::integer || '|' || (current_setting('superuser_reserved_connections')::integer + COALESCE(current_setting('reserved_connections', true), '0')::integer)") || \
        fail "cannot read PostgreSQL connection capacity"
    IFS='|' read -r maximum reserved <<<"$capacity"
    [[ $maximum =~ ^[1-9][0-9]{0,8}$ && $reserved =~ ^[0-9]{1,9}$ ]] || fail "invalid PostgreSQL connection capacity response"
    available=$((maximum - reserved))
    ((available >= required)) || fail \
        "PostgreSQL connection capacity is too small: need $required, available $available ($maximum total, $reserved server-reserved); includes $members replicas, $versions HA versions, pool size $largest_pool and $reserve operator-reserved connections"
    echo "PostgreSQL: connection budget $required fits $available available connections"
}

proof_documents=()
if [[ $topology == ha && ${UPDATE_SKIP_POSTGRES_PROBE:-false} != true ]]; then
    probe_needed=true
    if [[ $postgres_mode == local ]]; then
        postgres_any=$("${compose[@]}" ps --all --quiet devshard-postgres) || fail \
            "cannot list devshard-postgres"
        postgres_up=$("${compose[@]}" ps --quiet devshard-postgres) || fail \
            "cannot list devshard-postgres"
        if [[ -z $postgres_any ]]; then
            for service in "${versiond_services[@]}"; do
                id=$("${compose[@]}" ps --quiet "$service") || fail "cannot list $service"
                [[ -z $id ]] || fail "$service is running but bundled PostgreSQL is absent; this is not a fresh installation"
            done
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
        proof_containers=()
        proof_documents=()
        for service in "${versiond_services[@]}"; do
            id=$("${compose[@]}" ps --quiet "$service") || fail "cannot list $service"
            [[ -n $id ]] || continue
            id=${id%%$'\n'*}
            proof_status=0
            proof=$(versiond_storage_proof "$id") || proof_status=$?
            if ((proof_status == 1)); then
                check_legacy_database "$service" "$id"
                continue
            fi
            ((proof_status == 0)) || fail \
                "$service did not answer its storage proof; a v5 replica whose proof is unavailable is not \"unsupported\""
            validate_storage_proof <<<"$proof" || fail "$service returned an incomplete or invalid storage proof"
            identity=$(jq -r .identity <<<"$proof")
            proof_containers+=("$service=$id")
            proof_documents+=("$proof")
            if [[ -z $running_identity ]]; then
                running_identity=$identity
                proof_source=$service
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
            # Every running replica writes through every generation it runs;
            # the model's database must show each nonce. A replica or a child
            # generation on a clone with the same identity would not.
            challenged=0
            for index in "${!proof_containers[@]}"; do
                service=${proof_containers[index]%%=*}
                id=${proof_containers[index]#*=}
                snapshot=$(jq -r .snapshot <<<"${proof_documents[index]}")
                while IFS= read -r generation; do
                    nonce=$(cat /proc/sys/kernel/random/uuid)
                    request=$(jq -cn --arg n "$nonce" --arg s "$snapshot" --arg g "$generation" \
                        '{operation:"write", nonce:$n, snapshot:$s, generation:$g}')
                    response=$("$docker_bin" exec "$id" /bin/busybox wget -qO- -T 5 \
                        --header 'Content-Type: application/json' --post-data "$request" \
                        http://127.0.0.1:8080/internal/storage-challenge 2>/dev/null) || fail \
                        "$service (generation $generation) could not write a storage challenge through its PostgreSQL pool; inspect 'docker compose logs $service'"
                    jq -e --arg id "$running_identity" --arg s "$snapshot" --arg g "$generation" \
                        '.identity == $id and .snapshot == $s and .generation == $g and .found == true' \
                        <<<"$response" >/dev/null || fail "$service returned an invalid storage challenge response"
                    observed=$(postgres_probe 'SELECT challenge::text FROM devshard_storage_identity WHERE singleton' 2>/dev/null || true)
                    [[ $observed == "$nonce" ]] || fail \
                        "the database the model names did not receive the challenge $service (generation $generation) just wrote; that generation runs on a copy of the running database, not on it. Fix PGHOST, or set UPDATE_ACCEPT_DATABASE_CHANGE=true only for an intended move"
                    ((challenged += 1))
                done < <(jq -r '.targets[]?.generation // empty' <<<"${proof_documents[index]}")
            done
            echo "PostgreSQL: database lineage $running_identity unchanged; $challenged generation(s) wrote a challenge the model's database shows"
            for index in "${!proof_containers[@]}"; do
                service=${proof_containers[index]%%=*}
                id=${proof_containers[index]#*=}
                proof=$(versiond_storage_proof "$id") || fail "$service lost its storage proof after verification"
                validate_storage_proof <<<"$proof" || fail "$service returned an invalid final storage proof"
                [[ $(jq -r .snapshot <<<"$proof") == $(jq -r .snapshot <<<"${proof_documents[index]}") && \
                   $(jq -r .identity <<<"$proof") == "$running_identity" ]] || fail "$service changed processes or database during verification; retry"
            done
        fi
        check_connection_budget
    fi
fi

if [[ $postgres_mode == local ]]; then
    # A v4 installation keeps its cluster on the image's anonymous volume. The
    # v5 entrypoint copies it into the bind directory on first start; check the
    # copy fits before stopping anything. Fresh installs have no container yet.
    postgres_container=$("${compose[@]}" ps --all --quiet devshard-postgres) || fail \
        "cannot list devshard-postgres"
    postgres_target=$(jq -r '
        [.services["devshard-postgres"].volumes[]?
         | select(.target == "/var/lib/postgresql/gonka" and .type == "bind")]
        | .[0].source // ""' <<<"$rendered")
    [[ -n $postgres_target ]] || fail \
        "devshard-postgres must bind-mount its data directory at /var/lib/postgresql/gonka"
    postgres_storage=$(python3 "$state_helper" postgres-storage "$postgres_container" <<<"$rendered") || \
        fail "PostgreSQL storage validation failed before replacement"
    if [[ -n $postgres_container ]]; then
        [[ -x $migration_preflight_bin ]] || fail \
            "missing $migration_preflight_bin"
        echo "PostgreSQL: checking the migration copy fits in $postgres_target"
        DOCKER_BIN=$docker_bin POSTGRES_MIGRATION_HELPER_IMAGE=$postgres_helper_image \
            "$migration_preflight_bin" \
            --source-container "$postgres_container" --target-dir "$postgres_target"
        if [[ $postgres_storage == migrate ]]; then
            # A completed old copy cannot stand in for the live v4 source.
            # An interrupted copy is resumed using its saved journal instead.
            source_image=$("$docker_bin" inspect --format '{{.Image}}' "$postgres_container") || fail "cannot inspect PostgreSQL image"
            "$docker_bin" run --rm --network none --mount "type=bind,src=$postgres_target,dst=/target,readonly" \
                --entrypoint sh "$source_image" -ec 'test ! -e /target/data/PG_VERSION && test ! -e /target/.gonka-copy-complete' || \
                fail "the v4 migration target already contains a copy; verify it during maintenance before replacing the running database"
        fi
    fi
fi

echo "Images after the update:"
for service in "${versiond_services[@]}" devshard-postgres proxy-policy proxy; do
    has_service "$service" || continue
    printf '  %-18s %s\n' "$service" "$(jq -r --arg s "$service" '.services[$s].image' <<<"$rendered")"
done

if [[ $check_only == true ]]; then
    echo "Preflight passed; no services were replaced"
    exit 0
fi

# --- update -----------------------------------------------------------------

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

# PostgreSQL keeps its current data/configuration even on rollback. Retain its
# prior image only when a healthy service actually changes.
remember_previous() {
    local service=$1 previous_tag current health existing
    [[ $dry_run == false ]] || return 0
    previous_tag=gonka-previous/${GONKA_DEPLOYMENT_KEY:0:16}/$service
    current=
    existing=$("${compose[@]}" ps --all --quiet "$service") || fail "cannot list $service for rollback"
    if [[ -n $existing ]]; then
        current=$(current_image "$service") || fail "cannot read the current image of $service"
    fi
    [[ -n $current ]] || return 0
    health=$(container_health "$service") || fail "cannot read the health of $service"
    if [[ $health == healthy ]] && service_changed "$service" "$existing"; then
        run "$docker_bin" tag "$current" "$previous_tag"
    fi
}

required_versiond_routes=()
required_legacy_routes=()

up() {
    local service=$1 member_id
    local -a candidate_routes=("${required_versiond_routes[@]}")
    if [[ $service == "$legacy_owner" ]]; then
        candidate_routes+=("${required_legacy_routes[@]}")
    fi
    if [[ $dry_run == true ]]; then
        run "${compose[@]}" up -d --no-deps --wait --wait-timeout "$wait_timeout" "$service"
        return
    fi
    if [[ $service != devshard-postgres ]]; then
        if [[ $topology == ha ]]; then
            member_id=$("${compose[@]}" ps --all --quiet "$service") || fail "cannot inspect $service before replacement"
            if [[ -n $member_id && $(container_health "$service") == healthy ]] && ! service_changed "$service" "$member_id"; then
                return 0
            fi
            if [[ -n $member_id ]]; then
                run env GONKA_CONFIG_ENV="$config_env" "$fleet_bin" verify-member-reserve "$member_id" "${required_versiond_routes[@]}" || \
                    fail "$service is still needed for a required route; leaving it running"
            fi
        fi
        begin_replacement "$service"
        run "${compose[@]}" up -d --no-deps --wait --wait-timeout "$wait_timeout" "$service" || \
            fail "$service did not become healthy; restoring its previous specification"
        if [[ $topology == ha ]]; then
            member_id=$("${compose[@]}" ps --quiet "$service") || fail "cannot inspect candidate $service"
            run env GONKA_CONFIG_ENV="$config_env" "$fleet_bin" verify-member "$member_id" "${candidate_routes[@]}" || \
                fail "$service has not been admitted for every required version; restoring it"
        fi
        commit_replacement
        return 0
    fi
    local id nonce="" staging
    id=$("${compose[@]}" ps --all --quiet "$service") || fail "cannot list PostgreSQL"
    if [[ -n $id && $(container_health "$service") == healthy ]] && ! service_changed "$service" "$id"; then
        return 0
    fi
    remember_previous "$service"
    if [[ -n $id && ${UPDATE_ACCEPT_DATABASE_CHANGE:-false} != true ]]; then
        nonce=$(python3 "$state_helper" postgres-seal "$id" <<<"$rendered") || \
            fail "cannot record the running PostgreSQL history before replacement"
    fi
    mkdir -p "$state_dir"
    chmod 700 "$state_dir"
    staging=$(mktemp -d "$state_dir/pending.XXXXXX")
    (umask 077; python3 "$state_helper" postgres-prepare "$staging/postgres.json" "$project_name" "$id" "$nonce" "$script_dir" "$(IFS=,; printf '%s' "${compose_files[*]}")" <<<"$rendered") || \
        fail "cannot save PostgreSQL recovery state"
    python3 "$state_helper" publish "$staging" "$state_dir/pending" || fail "cannot publish PostgreSQL recovery state"
    run "${compose[@]}" up -d --no-deps --wait --wait-timeout "$wait_timeout" "$service" || \
        fail "PostgreSQL replacement failed; recovering the saved migration target"
    python3 "$state_helper" postgres-verify "$state_dir/pending/postgres.json" || \
        fail "PostgreSQL history changed after replacement; recovery remains pending"
    commit_replacement
}

pull_services=()
missing_images=()
for service in "${active_versiond[@]}" proxy proxy-policy proxy-policy2 devshard-postgres; do
    [[ $service != devshard-postgres || $postgres_mode == local ]] || continue
    pull_services+=("$service")
    "$docker_bin" image inspect "$(jq -r --arg s "$service" '.services[$s].image' <<<"$rendered")" >/dev/null 2>&1 || \
        missing_images+=("$service")
done
# Refresh every image when the registry answers; when it does not, a rerun
# after a failure still works as long as every image is already on the host.
if ! run "${compose[@]}" pull "${pull_services[@]}"; then
    ((${#missing_images[@]} == 0)) || fail \
        "cannot pull the images of ${missing_images[*]} and they are not on this host"
    echo "update-devshard: the registry is unreachable; continuing with the cached images" >&2
fi
router_image=$(VERSIOND_ROUTER_SLOT=0 VERSIOND_ROUTER_METRICS_NETWORK=unused "$docker_bin" compose \
    --project-directory "$script_dir" -f "$script_dir/versiond-router-slot/docker-compose.yml" \
    config --format json 2>/dev/null | jq -r '.services.router.image // ""') || router_image=
if [[ $topology == ha && -n $router_image ]]; then
    if ! run "$docker_bin" pull "$router_image"; then
        "$docker_bin" image inspect "$router_image" >/dev/null 2>&1 || fail \
            "cannot pull the router image $router_image and it is not on this host"
        echo "update-devshard: continuing with the cached router image" >&2
    fi
fi

if [[ $postgres_mode == local ]]; then
    echo "Step: shared PostgreSQL"
    up devshard-postgres
fi

if [[ $topology == ha ]]; then
    echo "Step: versiond-router fleet"
    [[ -x $fleet_bin ]] || fail "missing $fleet_bin"
    run env GONKA_CONFIG_ENV="$config_env" "$fleet_bin" prepare-networks
    # First cutover: replicas that predate the router back network are not on
    # it, and the new routers would see no upstream at all. Attach every
    # running replica under the pool aliases the model gives versiond; the
    # replacement container joins through the model itself.
    back_network=$(jq -r '.networks["versiond-router-back"].name // ""' <<<"$rendered")
    [[ -n $back_network ]] || fail "the model names no versiond-router-back network"
    mapfile -t pool_aliases < <(jq -r '.services.versiond.networks["versiond-router-back"].aliases[]?' <<<"$rendered")
    ((${#pool_aliases[@]} > 0)) || fail "the model gives versiond no alias on $back_network"
    alias_arguments=()
    for pool_alias in "${pool_aliases[@]}"; do alias_arguments+=(--alias "$pool_alias"); done
    for service in "${versiond_services[@]}"; do
        id=$("${compose[@]}" ps --quiet "$service") || fail "cannot list $service"
        [[ -n $id ]] || continue
        id=${id%%$'\n'*}
        attached=$("$docker_bin" inspect --format '{{json .NetworkSettings.Networks}}' "$id") || fail \
            "cannot read the networks of $service"
        jq -e --arg network "$back_network" 'has($network)' <<<"$attached" >/dev/null && continue
        run "$docker_bin" network connect "${alias_arguments[@]}" "$back_network" "$id"
    done
    # The image was refreshed above; the fleet may not depend on the registry.
    run env GONKA_CONFIG_ENV="$config_env" VERSIOND_ROUTER_PULL_POLICY=missing "$fleet_bin" apply
fi

echo "Step: public proxy listener, then the private policy workers one at a time"
# The policy workers resolve proxy-policy-ingress, an alias only the new
# public proxy publishes, and give up after 30 seconds. Start the new proxy
# first without waiting for its health (it needs a policy worker for that),
# keep the previous proxy image under its tag, then bring the workers up and
# finally wait for the proxy itself.
begin_replacement proxy proxy-policy2 proxy-policy
run "${compose[@]}" up -d --no-deps proxy || fail "public proxy creation failed"
for service in proxy-policy2 proxy-policy proxy; do
    run "${compose[@]}" up -d --no-deps --wait --wait-timeout "$wait_timeout" "$service" || \
        fail "$service did not become healthy during public cutover"
done
if [[ $topology == ha ]]; then
    # The proxy healthcheck covers its policy workers only. Before the legacy
    # router is removed and versiond is replaced, the new public proxy must
    # admit every router slot and every live route end to end.
    run env GONKA_CONFIG_ENV="$config_env" "$fleet_bin" verify-admission || fail "public route admission failed"
fi
commit_replacement

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

required_versiond_routes=()
required_legacy_routes=()
if [[ $topology == ha ]]; then
    live_routes=$(GONKA_CONFIG_ENV="$config_env" "$fleet_bin" versiond-routes) || fail "cannot preserve the served version set"
    proof_routes=$(for proof in "${proof_documents[@]}"; do jq -r '.targets[].version' <<<"$proof"; done)
    routes=$(jq -nr --arg live "$live_routes $proof_routes" '
        $live | [splits("[ ,;\\s]+") | select(length > 0)] | unique | .[]') || fail "cannot preserve the required version set"
    while IFS= read -r route; do
        [[ -n $route ]] || continue
        [[ $route =~ ^[a-zA-Z0-9][a-zA-Z0-9._+~-]{0,63}$ ]] || fail "invalid required route $route"
        if jq -en --arg route "$route" --arg legacy "${VERSIOND_NON_HA_VERSIONS-v1 v2 v3}" '$legacy | [splits("[ ,;\\s]+") | select(length > 0)] | index($route) != null' >/dev/null; then
            required_legacy_routes+=("$route")
        else
            required_versiond_routes+=("$route")
        fi
    done <<<"$routes"
fi

echo "Step: versiond replicas (${active_versiond[*]})"
# Last replica first, the legacy owner last: while it is being replaced, the
# other replicas already run the new release behind the routers.
for ((i = ${#active_versiond[@]} - 1; i >= 0; i--)); do
    [[ ${active_versiond[i]} != "$legacy_owner" ]] || continue
    up "${active_versiond[i]}"
done
if [[ " ${active_versiond[*]} " == *" $legacy_owner "* ]]; then
    up "$legacy_owner"
fi
# A replica whose desired count is 0 is decommissioned: stop and remove it so
# `restart: always` cannot bring it back into the pool.
for service in "${versiond_services[@]}"; do
    [[ $(replicas "$service") == 0 ]] || continue
    existing=$("${compose[@]}" ps --all --quiet "$service") || fail "cannot list $service"
    [[ -n $existing ]] || continue
    echo "Step: decommissioning $service (replicas: 0)"
    if [[ $topology == ha ]]; then
        run env GONKA_CONFIG_ENV="$config_env" "$fleet_bin" verify-member-reserve "$existing" "${required_versiond_routes[@]}" || \
            fail "cannot decommission $service: no ready reserve for its routes"
    fi
    run "${compose[@]}" stop "$service"
    run "${compose[@]}" rm -f "$service"
done

echo "Update finished"
run "${compose[@]}" ps
if [[ $topology == ha ]]; then
    run env GONKA_CONFIG_ENV="$config_env" "$fleet_bin" status
fi
