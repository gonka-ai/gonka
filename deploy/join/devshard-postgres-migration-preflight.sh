#!/usr/bin/env bash

set -Eeuo pipefail

docker_bin=${DOCKER_BIN:-docker}
helper_image=${POSTGRES_MIGRATION_HELPER_IMAGE:-${DEVSHARD_POSTGRES_IMAGE:-postgres:16-alpine}}
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
    (--source-container ID | --source-volume NAME) --target-dir DIR

Checks that the target filesystem has enough free space for an atomic copy of
the v4 PostgreSQL cluster. Source and target are mounted read-only and are not
modified.
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
if [[ -z $source_container && -z $source_volume ]]; then
    fail "one source option is required"
fi
command -v "$docker_bin" >/dev/null 2>&1 || fail "$docker_bin is required"

mkdir -p "$target_dir"
target_dir=$(cd -- "$target_dir" && pwd -P)

probe_script=$(cat <<'PROBE'
if [ -s "$2/data/PG_VERSION" ]; then
    printf 'target-ready\n'
elif [ -s "$2/.migrating/PG_VERSION" ] &&
    [ -f "$2/.gonka-copy-complete" ]; then
    printf 'staging-ready\n'
elif [ -s "$1/PG_VERSION" ]; then
    source_kib=$(du -sk "$1" | cut -f1)
    reclaimable_kib=0
    if [ -d "$2/.migrating" ]; then
        reclaimable_kib=$(du -sk "$2/.migrating" | cut -f1)
    fi
    printf 'source %s %s\n' "$source_kib" "$reclaimable_kib"
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
else
    "$docker_bin" volume inspect "$source_volume" >/dev/null ||
        fail "source volume does not exist: $source_volume"
    probe=$("$docker_bin" "${probe_args[@]}" \
        --mount "type=volume,src=$source_volume,dst=/source,readonly" \
        --entrypoint /bin/sh "$helper_image" \
        -ec "$probe_script" sh /source /target)
fi

read -r probe_state source_kib reclaimable_kib extra <<<"$probe"
[[ -z ${extra:-} ]] || fail "unexpected source probe output: $probe"
case $probe_state in
    target-ready)
        echo "PostgreSQL persistent PGDATA already exists; no migration copy is required"
        exit 0
        ;;
    staging-ready)
        echo "PostgreSQL migration staging is complete; no new copy is required"
        exit 0
        ;;
    source-missing)
        fail "the selected v4 source has no PostgreSQL PG_VERSION"
        ;;
    source) ;;
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
