#!/bin/sh

set -eu

legacy_data=${GONKA_POSTGRES_LEGACY_DATA:-/var/lib/postgresql/data}
persistent_root=${GONKA_POSTGRES_PERSISTENT_ROOT:-/var/lib/postgresql/gonka}
existing_versiond=${GONKA_POSTGRES_EXISTING_VERSIOND:-/var/lib/postgresql/gonka-existing-versiond}
official_entrypoint=${GONKA_POSTGRES_OFFICIAL_ENTRYPOINT:-/usr/local/bin/docker-entrypoint.sh}
target_data=${PGDATA:-$persistent_root/data}
staging_data=$persistent_root/.migrating
staging_complete=$staging_data/.gonka-copy-complete

log() {
    printf '%s\n' "gonka-postgres-entrypoint: $*" >&2
}

die() {
    log "$*"
    exit 1
}

directory_has_entries() {
    [ -d "$1" ] || return 1
    first_entry=$(find "$1" -mindepth 1 -print -quit) ||
        die "cannot inspect $1"
    [ -n "$first_entry" ]
}

cluster_exists() {
    [ -s "$1/PG_VERSION" ]
}

validate_cluster() {
    cluster_version=$(cat "$1/PG_VERSION") ||
        die "cannot read $1/PG_VERSION"
    case "$cluster_version" in
        '' | *[!0-9]*) die "invalid PostgreSQL cluster version in $1" ;;
    esac
}

ensure_migration_space() {
    source_kib=$(du -sk "$legacy_data" | awk '{ print $1 }') ||
        die "cannot measure PostgreSQL source cluster"
    free_kib=$(df -Pk "$persistent_root" | awk 'NR == 2 { print $4 }') ||
        die "cannot measure free space for $persistent_root"
    case "$source_kib" in
        '' | *[!0-9]* | 0) die "invalid PostgreSQL source size" ;;
    esac
    case "$free_kib" in
        '' | *[!0-9]*) die "invalid PostgreSQL free-space measurement" ;;
    esac
    required_kib=$((source_kib + (source_kib + 9) / 10))
    log "source is $source_kib KiB; migration requires $required_kib KiB; $free_kib KiB is free"
    [ "$free_kib" -ge "$required_kib" ] ||
        die "not enough free space for PostgreSQL migration"
}

publish_staging() {
    if [ -e "$target_data" ]; then
        if directory_has_entries "$target_data"; then
            die "refusing to replace non-empty incomplete target $target_data"
        fi
        rmdir "$target_data" || die "cannot remove empty target $target_data"
    fi
    mv "$staging_data" "$target_data" ||
        die "cannot publish migrated PostgreSQL cluster"
    sync
}

case "$target_data" in
    "$persistent_root"/*) ;;
    *) die "PGDATA must be below $persistent_root" ;;
esac
[ "$legacy_data" != "$target_data" ] || die "legacy and persistent PGDATA are identical"
[ -x "$official_entrypoint" ] || die "official PostgreSQL entrypoint is not executable"
mkdir -p "$persistent_root"

if cluster_exists "$target_data"; then
    validate_cluster "$target_data"
elif [ -e "$target_data" ] && directory_has_entries "$target_data"; then
    die "persistent PGDATA is non-empty but has no PG_VERSION: $target_data"
elif cluster_exists "$staging_data" && [ -f "$staging_complete" ]; then
    validate_cluster "$staging_data"
    log "finishing an interrupted atomic migration"
    publish_staging
elif cluster_exists "$legacy_data"; then
    validate_cluster "$legacy_data"

    if [ -e "$staging_data" ]; then
        rm -rf "$staging_data" || die "cannot reset stale migration staging data"
    fi
    ensure_migration_space
    mkdir "$staging_data"

    log "migrating the preserved v4 PostgreSQL cluster into persistent storage"
    cp -a "$legacy_data/." "$staging_data/" || die "PostgreSQL cluster copy failed"
    if [ -e "$staging_data/postmaster.pid" ]; then
        log "legacy cluster was not shut down cleanly; PostgreSQL will recover it from WAL"
        rm "$staging_data/postmaster.pid" || die "cannot remove stale postmaster.pid"
    fi
    chmod 700 "$staging_data" || die "cannot secure migrated PGDATA"
    validate_cluster "$staging_data"
    [ "$(cat "$legacy_data/PG_VERSION")" = "$(cat "$staging_data/PG_VERSION")" ] ||
        die "migrated PostgreSQL version does not match its source"
    sync
    : >"$staging_complete"
    sync
    publish_staging
    printf '%s\n' "$(cat "$target_data/PG_VERSION")" \
        >"$persistent_root/.migrated-from-v4"
    log "v4 PostgreSQL migration completed"
else
    if [ -e "$staging_data" ] && directory_has_entries "$staging_data"; then
        die "an incomplete migration exists but its v4 source is unavailable"
    fi

    allow_empty=${DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT:-false}
    case "$allow_empty" in
        true | false) ;;
        *) die "DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT must be true or false" ;;
    esac

    if directory_has_entries "$existing_versiond" && [ "$allow_empty" != true ]; then
        die "existing versiond artifacts found but no PostgreSQL cluster is attached; refusing to initialize an empty database (restore the v4 volume or explicitly set DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true for a new HA database)"
    fi
    log "no existing PostgreSQL cluster found; initializing persistent PGDATA"
fi

exec "$official_entrypoint" "$@"
