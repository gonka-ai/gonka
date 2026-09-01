#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
# shellcheck source=deploy/join/devshard-v5-release.env
# shellcheck disable=SC1091 # Runtime path is anchored to this script.
source "$script_dir/devshard-v5-release.env"
docker_bin=${DOCKER_BIN:-docker}
helper_image=${POSTGRES_MIGRATION_HELPER_IMAGE:-$DEVSHARD_V5_POSTGRES_IMAGE}
source_container=
source_volume=
target_dir=

fail() {
    echo "devshard-postgres-migration-preflight: $*" >&2
    exit 1
}

usage() {
    cat >&2 <<'EOF'
Usage:
  devshard-postgres-migration-preflight.sh \
    [(--source-container ID | --source-volume NAME)] --target-dir DIR

Checks that a live v4 source contains the devshard schema and that the target
filesystem has enough free space for an atomic copy. A source-less check is
accepted only for an already published target bound to a durable lineage
marker. The first live schema proof records the exact source volume so a later
stopped-container recovery does not guess from PostgreSQL heap bytes.
EOF
}

while [[ $# -gt 0 ]]; do
    case $1 in
        --source-container)
            [[ $# -ge 2 ]] || fail "--source-container requires a value"
            source_container=$2
            shift 2
            ;;
        --source-volume)
            [[ $# -ge 2 ]] || fail "--source-volume requires a value"
            source_volume=$2
            shift 2
            ;;
        --target-dir)
            [[ $# -ge 2 ]] || fail "--target-dir requires a value"
            target_dir=$2
            shift 2
            ;;
        -h | --help)
            usage
            exit 0
            ;;
        *)
            usage
            fail "unknown argument: $1"
            ;;
    esac
done

[[ -n $target_dir ]] || fail "--target-dir is required"
[[ $target_dir != *,* ]] || fail \
    "target directory must not contain a comma: $target_dir"
if [[ -n $source_container && -n $source_volume ]]; then
    fail "use either --source-container or --source-volume, not both"
fi
command -v "$docker_bin" >/dev/null 2>&1 || fail "$docker_bin is required"
command -v timeout >/dev/null 2>&1 || fail "timeout is required"
command -v jq >/dev/null 2>&1 || fail "jq is required"

mkdir -p "$target_dir"
target_dir=$(cd -- "$target_dir" && pwd -P)

probe_script=$(cat <<'PROBE'
cluster_id() {
    pg_controldata "$1" | awk -F: '
        $1 ~ /^[[:space:]]*Database system identifier[[:space:]]*$/ {
            sub(/^[[:space:]]+/, "", $2)
            sub(/[[:space:]]+$/, "", $2)
            print $2
            exit
        }
    '
}
wal_fingerprint() {
    cluster=$1
    unsorted=$(mktemp)
    files=$(mktemp)
    hashes=$(mktemp)
    cd "$cluster"
    find global/pg_control pg_wal -type f -print >"$unsorted"
    LC_ALL=C sort "$unsorted" >"$files"
    while IFS= read -r file; do
        sha256sum "$file" >>"$hashes"
    done <"$files"
    sha256sum "$hashes" | awk '{ print $1 }'
    rm -f "$unsorted" "$files" "$hashes"
}
source_id=none
source_fingerprint=none
if [ -s "$1/PG_VERSION" ]; then
    source_id=$(cluster_id "$1")
    source_fingerprint=$(wal_fingerprint "$1")
fi
if [ -s "$2/data/PG_VERSION" ]; then
    target_id=$(cluster_id "$2/data")
    printf 'target-ready %s %s %s\n' \
        "$source_id" "$target_id" "$source_fingerprint"
elif [ -s "$2/.migrating/PG_VERSION" ] &&
    [ -f "$2/.gonka-copy-complete" ]; then
    staging_id=$(cluster_id "$2/.migrating")
    staging_fingerprint=$(wal_fingerprint "$2/.migrating")
    printf 'staging-ready %s %s %s %s\n' \
        "$source_id" "$staging_id" "$source_fingerprint" \
        "$staging_fingerprint"
elif [ -s "$1/PG_VERSION" ]; then
    source_kib=$(du -sk "$1" | cut -f1)
    reclaimable_kib=0
    if [ -d "$2/.migrating" ]; then
        reclaimable_kib=$(du -sk "$2/.migrating" | cut -f1)
    fi
    printf 'source %s %s %s %s\n' \
        "$source_kib" "$reclaimable_kib" "$source_id" "$source_fingerprint"
else
    printf 'source-missing\n'
fi
PROBE
)

# Commas below are part of Docker's tmpfs specification.
# shellcheck disable=SC2054
probe_args=(
    run --rm
    --network none
    --read-only
    --tmpfs /tmp:rw,noexec,nosuid,size=16m
    --security-opt no-new-privileges
    # Inspect an existing Compose :Z bind without relabeling it away from the
    # running PostgreSQL container.
    --security-opt label=disable
    --mount "type=bind,src=$target_dir,dst=/target,readonly"
)

if [[ -n $source_container ]]; then
    "$docker_bin" inspect "$source_container" >/dev/null ||
        fail "source container does not exist: $source_container"
    probe=$(timeout -k 5 \
        "${POSTGRES_MIGRATION_PROBE_TIMEOUT_SECONDS:-300}s" \
        "$docker_bin" "${probe_args[@]}" \
        --volumes-from "$source_container:ro" \
        --entrypoint /bin/sh "$helper_image" \
        -ec "$probe_script" sh /var/lib/postgresql/data /target)
elif [[ -n $source_volume ]]; then
    "$docker_bin" volume inspect "$source_volume" >/dev/null ||
        fail "source volume does not exist: $source_volume"
    probe=$(timeout -k 5 \
        "${POSTGRES_MIGRATION_PROBE_TIMEOUT_SECONDS:-300}s" \
        "$docker_bin" "${probe_args[@]}" \
        --mount "type=volume,src=$source_volume,dst=/source,readonly" \
        --entrypoint /bin/sh "$helper_image" \
        -ec "$probe_script" sh /source /target)
else
    probe=$(timeout -k 5 \
        "${POSTGRES_MIGRATION_PROBE_TIMEOUT_SECONDS:-300}s" \
        "$docker_bin" "${probe_args[@]}" \
        --entrypoint /bin/sh "$helper_image" \
        -ec "$probe_script" sh /missing-source /target)
fi

read -r probe_state first second third fourth extra <<<"$probe"
[[ -z ${extra:-} ]] || fail "unexpected source probe output: $probe"
source_storage_key=
source_is_active=false
case $probe_state in
    target-ready | staging-ready) probe_source_id=$first ;;
    source) probe_source_id=$third ;;
    *) probe_source_id=none ;;
esac
if [[ -n $source_container && $probe_state != source-missing ]]; then
    source_storage_key=$("$docker_bin" inspect --format \
        '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}' \
        "$source_container") || fail "cannot inspect source PostgreSQL volume"
    runtime_environment=$("$docker_bin" inspect --format '{{json .Config.Env}}' \
        "$source_container") || fail "cannot inspect source PostgreSQL environment"
    runtime_pgdata=$(jq -r \
        '[.[] | select(startswith("PGDATA=")) | ltrimstr("PGDATA=")] | last // "/var/lib/postgresql/data"' \
        <<<"$runtime_environment") || fail "cannot parse source PostgreSQL environment"
    runtime_running=$("$docker_bin" inspect --format '{{.State.Running}}' \
        "$source_container") || fail "cannot inspect source PostgreSQL state"
    case $runtime_running in true | false) ;; *) fail \
        "source PostgreSQL container returned an invalid running state" ;; esac
    [[ $runtime_pgdata != /var/lib/postgresql/data || \
        $runtime_running != true ]] || source_is_active=true
fi

write_schema_proof() {
    local identifier=$1 storage=$2 temporary
    [[ -n $storage ]] || fail \
        "legacy PostgreSQL source is not a named volume; keep its container running for the migration proof"
    temporary=$(mktemp "$target_dir/.gonka-v4-schema-proof.XXXXXX") || fail \
        "cannot create PostgreSQL schema proof"
    printf '%s %s\n' "$identifier" "$storage" >"$temporary"
    chmod 600 "$temporary"
    mv -f "$temporary" "$target_dir/.gonka-v4-schema-proof"
    sync -d "$target_dir" 2>/dev/null || sync
}

require_schema_proof() {
    local identifier=$1 storage=$2 proof_id proof_storage extra_field
    [[ -s $target_dir/.gonka-v4-schema-proof ]] || fail \
        "offline legacy PostgreSQL source has no prior live schema proof; start the exact v4 container and rerun preflight"
    read -r proof_id proof_storage extra_field <"$target_dir/.gonka-v4-schema-proof"
    [[ -z ${extra_field:-} && $proof_id == "$identifier" && \
        $proof_storage == "$storage" ]] || fail \
        "offline legacy PostgreSQL source does not match its durable live schema proof"
}

if [[ -n $source_container && $probe_state != source-missing && \
    $source_is_active == true ]]; then
    [[ $probe_state != target-ready ]] || fail \
        "both the persistent target and an active legacy PostgreSQL source exist; their write histories may have diverged, so select and restore the authoritative copy explicitly"
    devshard_schema=$(
        # The quoted program expands inside the PostgreSQL container.
        # shellcheck disable=SC2016
        timeout --kill-after=2s 12s "$docker_bin" exec "$source_container" /bin/sh -ec '
            user=${POSTGRES_USER:-postgres}
            database=${POSTGRES_DB:-$user}
            PGCONNECT_TIMEOUT=5 PGOPTIONS="-c statement_timeout=5000" \
            PGPASSWORD=${POSTGRES_PASSWORD:-} psql \
                -h 127.0.0.1 -U "$user" -d "$database" -AtX \
                -v ON_ERROR_STOP=1 -c \
                "SELECT to_regclass('"'"'public.devshard_session_index'"'"') IS NOT NULL"
        '
    ) || fail "cannot verify the devshard schema in source container $source_container"
    [[ $devshard_schema == t ]] || fail \
        "source container $source_container has PostgreSQL data but no devshard schema; a fresh anonymous volume may have replaced the original one (locate and recover the dangling v4 volume)"
    write_schema_proof "$probe_source_id" "$source_storage_key"
elif [[ -n $source_container && $probe_state != source-missing && \
    $probe_source_id != none ]]; then
    require_schema_proof "$probe_source_id" "$source_storage_key"
elif [[ -n $source_volume && $probe_state != source-missing && \
    $probe_source_id != none ]]; then
    require_schema_proof "$probe_source_id" "$source_volume"
fi

marker_value() {
    local path=$1 value
    value=$(awk 'NR == 1 { print $1 }' "$path" 2>/dev/null || :)
    printf '%s\n' "${value:-none}"
}

validate_published_markers() {
    local identifier=$1 common legacy
    common=$(marker_value "$target_dir/.gonka-cluster-lineage")
    legacy=$(marker_value "$target_dir/.migrated-from-v4")
    [[ $common == none || $common == "$identifier" ]] || fail \
        "persistent PostgreSQL common lineage marker conflicts with PGDATA"
    [[ $legacy == none || $legacy == 16 || $legacy == "$identifier" ]] || fail \
        "persistent PostgreSQL migration marker conflicts with PGDATA"
    [[ $common != none || $legacy != none ]] || fail \
        "persistent PostgreSQL target has no completed lineage marker"
}

require_unchanged_source_snapshot() {
    local fingerprint=$1 allow_legacy_upgrade=${2:-false}
    local recorded legacy temporary
    [[ $fingerprint =~ ^[0-9a-f]{64}$ ]] || fail \
        "legacy PostgreSQL source returned an invalid WAL fingerprint"
    recorded=$(marker_value "$target_dir/.gonka-v4-source-wal.sha256")
    if [[ $recorded == none && $allow_legacy_upgrade == true ]]; then
        legacy=$(marker_value "$target_dir/.migrated-from-v4")
        if [[ $legacy == 16 ]]; then
            temporary=$(mktemp \
                "$target_dir/.gonka-v4-source-wal.sha256.XXXXXX") || fail \
                "cannot create PostgreSQL source snapshot marker"
            printf '%s\n' "$fingerprint" >"$temporary"
            chmod 600 "$temporary"
            mv -f "$temporary" \
                "$target_dir/.gonka-v4-source-wal.sha256"
            sync -d "$target_dir" 2>/dev/null || sync
            recorded=$fingerprint
        fi
    fi
    [[ $recorded != none ]] || fail \
        "persistent target and legacy source coexist without a durable source snapshot; refusing to guess which copy is newer"
    [[ $recorded == "$fingerprint" ]] || fail \
        "legacy PostgreSQL source changed after migration; refusing to use the potentially stale persistent target"
}
case $probe_state in
    target-ready)
        source_identifier=$first
        target_identifier=$second
        source_fingerprint=${third:-none}
        [[ $target_identifier =~ ^[0-9]+$ ]] || fail \
            "persistent PostgreSQL target has an invalid system identifier"
        if [[ $source_identifier == none ]]; then
            validate_published_markers "$target_identifier"
        else
            [[ $source_identifier == "$target_identifier" ]] || fail \
                "persistent PostgreSQL target does not originate from the selected v4 source"
            validate_published_markers "$target_identifier"
            require_unchanged_source_snapshot "$source_fingerprint" true
        fi
        echo "PostgreSQL persistent PGDATA already exists; no migration copy is required"
        exit 0
        ;;
    staging-ready)
        source_identifier=$first
        staging_identifier=$second
        source_fingerprint=${third:-none}
        staging_fingerprint=${fourth:-none}
        [[ $source_identifier =~ ^[0-9]+$ ]] || fail \
            "completed PostgreSQL staging requires its selected v4 source"
        [[ $staging_identifier == "$source_identifier" ]] || fail \
            "PostgreSQL migration staging does not match the selected v4 source"
        completion=$(marker_value "$target_dir/.gonka-copy-complete")
        legacy=$(marker_value "$target_dir/.migrated-from-v4")
        [[ $completion == "$source_identifier" || \
            ($completion == none && $legacy == 16) ]] || fail \
            "PostgreSQL migration completion marker conflicts with its source"
        [[ $staging_fingerprint == "$source_fingerprint" ]] || fail \
            "completed PostgreSQL staging changed independently of its source"
        require_unchanged_source_snapshot "$source_fingerprint" true
        echo "PostgreSQL migration staging is complete; no new copy is required"
        exit 0
        ;;
    source-missing)
        fail "the selected v4 source has no PostgreSQL PG_VERSION"
        ;;
    source)
        source_kib=$first
        reclaimable_kib=$second
        source_identifier=$third
        source_fingerprint=${fourth:-none}
        [[ $source_identifier =~ ^[0-9]+$ ]] || fail \
            "invalid PostgreSQL source system identifier"
        ;;
    *) fail "unexpected source probe output: $probe" ;;
esac

[[ $source_kib =~ ^[1-9][0-9]*$ ]] || fail \
    "invalid PostgreSQL source size: ${source_kib:-empty}"
[[ $reclaimable_kib =~ ^[0-9]+$ ]] || fail \
    "invalid reclaimable staging size: ${reclaimable_kib:-empty}"

free_kib=$(df -Pk -- "$target_dir" | awk 'NR == 2 { print $4 }')
[[ $free_kib =~ ^[0-9]+$ ]] || fail \
    "cannot determine free space for $target_dir"

# The migration is a full copy. Keep a 10% reserve for filesystem metadata,
# WAL growth between this preflight and shutdown, and the completion marker.
required_kib=$((source_kib + (source_kib + 9) / 10))
effective_free_kib=$((free_kib + reclaimable_kib))
printf 'PostgreSQL source: %s KiB; required free: %s KiB; filesystem free: %s KiB; reclaimable staging: %s KiB; effective available: %s KiB\n' \
    "$source_kib" "$required_kib" "$free_kib" "$reclaimable_kib" \
    "$effective_free_kib"
if ((effective_free_kib < required_kib)); then
    fail "not enough free space for PostgreSQL migration"
fi
