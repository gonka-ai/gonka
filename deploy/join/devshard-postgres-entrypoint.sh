#!/bin/sh

set -eu

legacy_data=${GONKA_POSTGRES_LEGACY_DATA:-/var/lib/postgresql/data}
persistent_root=${GONKA_POSTGRES_PERSISTENT_ROOT:-/var/lib/postgresql/gonka}
existing_versiond=${GONKA_POSTGRES_EXISTING_VERSIOND:-/var/lib/postgresql/gonka-existing-versiond}
versiond_data=${GONKA_POSTGRES_VERSIOND_DATA:-/var/lib/postgresql/gonka-versiond-data}
versiond2_data=${GONKA_POSTGRES_VERSIOND2_DATA:-/var/lib/postgresql/gonka-versiond2-data}
official_entrypoint=${GONKA_POSTGRES_OFFICIAL_ENTRYPOINT:-/usr/local/bin/docker-entrypoint.sh}
initdb_dir=${GONKA_POSTGRES_INITDB_DIR:-/docker-entrypoint-initdb.d}
target_data=${PGDATA:-$persistent_root/data}
staging_data=$persistent_root/.migrating
staging_complete=$persistent_root/.gonka-copy-complete
# Lives inside PGDATA: the initdb hook that writes it runs as the postgres
# user, which owns PGDATA but not the bind-mount root around it.
init_complete=$target_data/.gonka-init-complete
# Inside PGDATA like the init marker: a marker next to a replaced PGDATA is
# not evidence about the cluster that is mounted now.
migrated_marker=$target_data/.migrated-from-v4
expected_major=

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

# The identifier initdb stamps on a cluster; a copy keeps it, another cluster
# never shares it.
# "X/Y" checkpoint LSN as one decimal number for comparison.
cluster_checkpoint_lsn() {
    ccl_output=$(pg_controldata "$1") ||
        die "cannot read pg_controldata for $1"
    ccl_lsn=$(printf '%s\n' "$ccl_output" |
        sed -n 's/^Latest checkpoint location:[[:space:]]*//p' | head -n 1)
    case "$ccl_lsn" in
        */*) ;;
        *) die "pg_controldata for $1 reports no checkpoint location" ;;
    esac
    printf '%d\n' "$(( (0x${ccl_lsn%%/*} << 32) + 0x${ccl_lsn##*/} ))"
}

cluster_system_identifier() {
    csi_output=$(pg_controldata "$1") ||
        die "cannot read pg_controldata for $1"
    csi_id=$(printf '%s\n' "$csi_output" |
        sed -n 's/^Database system identifier:[[:space:]]*//p' | head -n 1)
    [ -n "$csi_id" ] || die "pg_controldata for $1 reports no system identifier"
    printf '%s\n' "$csi_id"
}

validate_cluster() {
    cluster_version=$(cat "$1/PG_VERSION") ||
        die "cannot read $1/PG_VERSION"
    [ "$cluster_version" = "$expected_major" ] ||
        die "PostgreSQL cluster in $1 uses major version ${cluster_version:-unknown}; expected $expected_major"
}

# Every mounted replica data directory: the two shipped ones plus any
# gonka-versiond<N>-data mount an extra replica overlay adds.
versiond_data_roots() {
    printf '%s\n' "$versiond_data" "$versiond2_data"
    for extra in "$(dirname "$versiond_data")"/gonka-versiond*-data; do
        [ -d "$extra" ] || continue
        case "$extra" in
            "$versiond_data" | "$versiond2_data") ;;
            *) printf '%s\n' "$extra" ;;
        esac
    done
}

postgres_binding_marker() {
    for storage_root in $(versiond_data_roots); do
        [ -d "$storage_root" ] || return 3
        marker=$(find "$storage_root" -type f -name .pg-bound -print -quit) ||
            return 2
        if [ -n "$marker" ]; then
            printf '%s\n' "$marker"
            return 0
        fi
    done
    return 1
}

detect_postgres_major() {
    postgres_version=$(postgres --version 2>/dev/null) ||
        die "cannot determine PostgreSQL server version"
    case "$postgres_version" in
        'postgres (PostgreSQL) '*)
            postgres_version=${postgres_version#'postgres (PostgreSQL) '}
            expected_major=${postgres_version%%.*}
            ;;
        *) die "cannot parse PostgreSQL server version: $postgres_version" ;;
    esac
    case "$expected_major" in
        '' | *[!0-9]*)
            die "invalid PostgreSQL server major version: $expected_major"
            ;;
    esac
}

validate_runtime_family() {
    libc_version=$(ldd --version 2>&1 || :)
    case "$libc_version" in
        musl\ libc*) ;;
        *)
            die "the selected PostgreSQL image is not compatible with the existing devshard PGDATA; use the release image or migrate the cluster before changing image variants"
            ;;
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
    rm -f "$staging_complete" ||
        die "cannot remove PostgreSQL migration completion marker"
    sync
}

case "$target_data" in
    "$persistent_root"/*) ;;
    *) die "PGDATA must be below $persistent_root" ;;
esac
[ "$legacy_data" != "$target_data" ] || die "legacy and persistent PGDATA are identical"
[ -x "$official_entrypoint" ] || die "official PostgreSQL entrypoint is not executable"
detect_postgres_major
validate_runtime_family
mkdir -p "$persistent_root"

if cluster_exists "$target_data"; then
    validate_cluster "$target_data"
    if cluster_exists "$legacy_data"; then
        # Both a persistent cluster and the v4 volume are attached. The
        # persistent one wins only if it is the copy of that volume; a foreign
        # PG16 cluster in DEVSHARD_POSTGRES_DATA_DIR must not silently replace
        # the devshard history.
        target_identifier=$(cluster_system_identifier "$target_data") ||
            die "cannot verify the persistent PostgreSQL cluster identity"
        legacy_identifier=$(cluster_system_identifier "$legacy_data") ||
            die "cannot verify the attached v4 PostgreSQL cluster identity"
        [ "$target_identifier" = "$legacy_identifier" ] ||
            die "persistent PGDATA $target_data is a different cluster (system identifier $target_identifier) than the attached v4 volume ($legacy_identifier); refusing to start on the wrong history. Point DEVSHARD_POSTGRES_DATA_DIR at the migrated copy or detach the wrong volume"
        # Same cluster, but the v4 volume may have accepted writes after the
        # copy (a rollback that ran on it). The copy must not silently win
        # over the newer history.
        target_lsn=$(cluster_checkpoint_lsn "$target_data") ||
            die "cannot read the persistent PostgreSQL checkpoint"
        legacy_lsn=$(cluster_checkpoint_lsn "$legacy_data") ||
            die "cannot read the attached v4 PostgreSQL checkpoint"
        [ "$legacy_lsn" -le "$target_lsn" ] ||
            die "the attached v4 volume advanced past the persistent copy in $target_data (checkpoint $legacy_lsn > $target_lsn); it received writes after the copy. Move the persistent copy aside so the volume is migrated again, or detach the volume if the copy is the history you want"
    elif [ ! -f "$migrated_marker" ] && [ ! -f "$init_complete" ]; then
        die "persistent PGDATA $target_data has no completion marker: its initialization or migration did not finish; remove it to initialize again, or restore it from a backup"
    fi
    rm -f "$staging_complete" ||
        die "cannot remove stale PostgreSQL migration completion marker"
elif [ -e "$target_data" ] && directory_has_entries "$target_data"; then
    die "persistent PGDATA is non-empty but has no PG_VERSION: $target_data"
elif cluster_exists "$staging_data" && [ -f "$staging_complete" ]; then
    validate_cluster "$staging_data"
    log "finishing an interrupted atomic migration"
    printf '%s\n' "$(cat "$staging_data/PG_VERSION")" >"$staging_data/.migrated-from-v4" ||
        die "cannot record the v4 migration marker"
    publish_staging
elif cluster_exists "$legacy_data"; then
    validate_cluster "$legacy_data"

    if [ -e "$staging_data" ]; then
        rm -rf "$staging_data" || die "cannot reset stale migration staging data"
    fi
    rm -f "$staging_complete" ||
        die "cannot reset stale migration completion marker"
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
    printf '%s\n' "$(cat "$staging_data/PG_VERSION")" >"$staging_data/.migrated-from-v4" ||
        die "cannot record the v4 migration marker"
    publish_staging
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

    binding_marker=
    if binding_marker=$(postgres_binding_marker); then
        die "PostgreSQL-bound devshard data exists at $binding_marker but no PostgreSQL cluster is attached; restore the exact legacy volume and do not use DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT"
    else
        marker_status=$?
        case "$marker_status" in
            1) ;;
            2) die "cannot inspect devshard data for PostgreSQL binding markers" ;;
            3) die "required devshard data directories are not mounted; refusing empty PostgreSQL initialization" ;;
            *) die "unexpected devshard binding-marker inspection status: $marker_status" ;;
        esac
    fi

    if directory_has_entries "$existing_versiond" && [ "$allow_empty" != true ]; then
        die "existing versiond artifacts found but no PostgreSQL cluster or .pg-bound marker is attached; this is either first-time HA enablement or a detached drained database (restore the v4 volume, or set DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true once only for confirmed first-time HA enablement)"
    fi
    # initdb, the role and the database are created by the official entrypoint
    # before it runs the init scripts; the last script records completion so
    # a cluster left half-initialised by a crash is refused on the next start.
    [ -d "$initdb_dir" ] || mkdir -p "$initdb_dir" ||
        die "cannot create $initdb_dir for the initialization completion hook"
    printf '%s\n' '#!/bin/sh' 'set -eu' ": >\"$init_complete\"" 'sync' \
        >"$initdb_dir/zz-gonka-init-complete.sh" ||
        die "cannot install the initialization completion hook"
    chmod 755 "$initdb_dir/zz-gonka-init-complete.sh"
    log "no existing PostgreSQL cluster found; initializing persistent PGDATA"
fi

exec "$official_entrypoint" "$@"
