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
marker. Source and target are mounted read-only and are not modified.
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
source_id=none
if [ -s "$1/PG_VERSION" ]; then
    source_id=$(cluster_id "$1")
fi
if [ -s "$2/data/PG_VERSION" ]; then
    target_id=$(cluster_id "$2/data")
    marker_id=$(cat "$2/.migrated-from-v4" 2>/dev/null || printf 'none')
    printf 'target-ready %s %s %s\n' "$source_id" "$target_id" "$marker_id"
elif [ -s "$2/.migrating/PG_VERSION" ] &&
    [ -f "$2/.gonka-copy-complete" ]; then
    staging_id=$(cluster_id "$2/.migrating")
    marker_id=$(cat "$2/.gonka-copy-complete" 2>/dev/null || printf 'none')
    printf 'staging-ready %s %s %s\n' "$source_id" "$staging_id" "$marker_id"
elif [ -s "$1/PG_VERSION" ]; then
    source_kib=$(du -sk "$1" | cut -f1)
    reclaimable_kib=0
    if [ -d "$2/.migrating" ]; then
        reclaimable_kib=$(du -sk "$2/.migrating" | cut -f1)
    fi
    printf 'source %s %s %s\n' "$source_kib" "$reclaimable_kib" "$source_id"
else
    printf 'source-missing\n'
fi
PROBE
)

probe_args=(
    run --rm
    --network none
    --read-only
    --security-opt no-new-privileges
    # Inspect an existing Compose :Z bind without relabeling it away from the
    # running PostgreSQL container.
    --security-opt label=disable
    --mount "type=bind,src=$target_dir,dst=/target,readonly"
)

if [[ -n $source_container ]]; then
    "$docker_bin" inspect "$source_container" >/dev/null ||
        fail "source container does not exist: $source_container"
    probe=$("$docker_bin" "${probe_args[@]}" \
        --volumes-from "$source_container:ro" \
        --entrypoint /bin/sh "$helper_image" \
        -ec "$probe_script" sh /var/lib/postgresql/data /target)
elif [[ -n $source_volume ]]; then
    "$docker_bin" volume inspect "$source_volume" >/dev/null ||
        fail "source volume does not exist: $source_volume"
    probe=$("$docker_bin" "${probe_args[@]}" \
        --mount "type=volume,src=$source_volume,dst=/source,readonly" \
        --entrypoint /bin/sh "$helper_image" \
        -ec "$probe_script" sh /source /target)
else
    probe=$("$docker_bin" "${probe_args[@]}" \
        --entrypoint /bin/sh "$helper_image" \
        -ec "$probe_script" sh /missing-source /target)
fi

read -r probe_state first second third extra <<<"$probe"
[[ -z ${extra:-} ]] || fail "unexpected source probe output: $probe"
if [[ -n $source_container && $probe_state != source-missing ]]; then
    devshard_schema=$(
        # The quoted program expands inside the PostgreSQL container.
        # shellcheck disable=SC2016
        "$docker_bin" exec "$source_container" /bin/sh -ec '
            user=${POSTGRES_USER:-postgres}
            database=${POSTGRES_DB:-$user}
            PGPASSWORD=${POSTGRES_PASSWORD:-} psql \
                -h 127.0.0.1 -U "$user" -d "$database" -AtX \
                -v ON_ERROR_STOP=1 -c \
                "SELECT to_regclass('"'"'public.devshard_session_index'"'"') IS NOT NULL"
        '
    ) || fail "cannot verify the devshard schema in source container $source_container"
    [[ $devshard_schema == t ]] || fail \
        "source container $source_container has PostgreSQL data but no devshard schema; a fresh anonymous volume may have replaced the original one (locate and recover the dangling v4 volume)"
fi
case $probe_state in
    target-ready)
        source_identifier=$first
        target_identifier=$second
        marker_identifier=$third
        [[ $target_identifier =~ ^[0-9]+$ ]] || fail \
            "persistent PostgreSQL target has an invalid system identifier"
        if [[ $source_identifier == none ]]; then
            [[ $marker_identifier == "$target_identifier" ]] || fail \
                "persistent PostgreSQL target is not bound to its recorded migration lineage"
        else
            [[ $source_identifier == "$target_identifier" ]] || fail \
                "persistent PostgreSQL target does not originate from the selected v4 source"
            [[ $marker_identifier == none || \
                $marker_identifier == "$target_identifier" ]] || fail \
                "persistent PostgreSQL target conflicts with its recorded migration lineage"
        fi
        echo "PostgreSQL persistent PGDATA already exists; no migration copy is required"
        exit 0
        ;;
    staging-ready)
        source_identifier=$first
        staging_identifier=$second
        marker_identifier=$third
        [[ $source_identifier =~ ^[0-9]+$ ]] || fail \
            "completed PostgreSQL staging requires its selected v4 source"
        [[ $staging_identifier == "$source_identifier" && \
            $marker_identifier == "$source_identifier" ]] || fail \
            "PostgreSQL migration staging does not match the selected v4 source"
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
