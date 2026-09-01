#!/bin/sh

set -eu

legacy_data=${GONKA_POSTGRES_LEGACY_DATA:-/var/lib/postgresql/data}
persistent_root=${GONKA_POSTGRES_PERSISTENT_ROOT:-/var/lib/postgresql/gonka}
existing_versiond=${GONKA_POSTGRES_EXISTING_VERSIOND:-/var/lib/postgresql/gonka-existing-versiond}
versiond_data=${GONKA_POSTGRES_VERSIOND_DATA:-/var/lib/postgresql/gonka-versiond-data}
versiond2_data=${GONKA_POSTGRES_VERSIOND2_DATA:-/var/lib/postgresql/gonka-versiond2-data}
official_entrypoint=${GONKA_POSTGRES_OFFICIAL_ENTRYPOINT:-/usr/local/bin/docker-entrypoint.sh}
target_data=${PGDATA:-$persistent_root/data}
staging_data=$persistent_root/.migrating
staging_complete=$persistent_root/.gonka-copy-complete
lineage_marker=$persistent_root/.migrated-from-v4
cluster_marker=$persistent_root/.gonka-cluster-lineage
source_fingerprint_marker=$persistent_root/.gonka-v4-source-wal.sha256
migration_commit_name=.gonka-migration-commit
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

validate_cluster() {
    cluster_version=$(cat "$1/PG_VERSION") ||
        die "cannot read $1/PG_VERSION"
    [ "$cluster_version" = "$expected_major" ] ||
        die "PostgreSQL cluster in $1 uses major version ${cluster_version:-unknown}; expected $expected_major"
}

cluster_system_identifier() {
    identifier=$(pg_controldata "$1" 2>/dev/null | awk -F: '
        $1 ~ /^[[:space:]]*Database system identifier[[:space:]]*$/ {
            sub(/^[[:space:]]+/, "", $2)
            sub(/[[:space:]]+$/, "", $2)
            print $2
            exit
        }
    ') || die "cannot inspect PostgreSQL lineage in $1"
    case "$identifier" in
        '' | *[!0-9]*) die "invalid PostgreSQL system identifier in $1" ;;
    esac
    printf '%s\n' "$identifier"
}

cluster_wal_fingerprint() {
    cluster=$1
	[ -f "$cluster/global/pg_control" ] && [ -d "$cluster/pg_wal" ] ||
		die "PostgreSQL cluster in $cluster has no control/WAL state to fingerprint"
    # The single-quoted program is evaluated by the child shell.
    # shellcheck disable=SC2016
    fingerprint=$(
        timeout -k 5 \
            "${GONKA_POSTGRES_FINGERPRINT_TIMEOUT_SECONDS:-300}s" \
            /bin/sh -ec '
                cluster=$1
                unsorted=$(mktemp)
                files=$(mktemp)
                hashes=$(mktemp)
                trap '\''rm -f "$unsorted" "$files" "$hashes"'\'' EXIT
                cd "$cluster"
                find global/pg_control pg_wal -type f -print >"$unsorted"
                LC_ALL=C sort "$unsorted" >"$files"
                while IFS= read -r file; do
                    sha256sum "$file" >>"$hashes"
                done <"$files"
                sha256sum "$hashes" | awk '\''{ print $1 }'\''
            ' sh "$cluster"
    ) || die "cannot fingerprint PostgreSQL WAL state in $cluster"
    case "$fingerprint" in
        '' | *[!0-9a-f]*) die "invalid PostgreSQL WAL fingerprint in $cluster" ;;
    esac
    [ "${#fingerprint}" -eq 64 ] ||
        die "invalid PostgreSQL WAL fingerprint length in $cluster"
    printf '%s\n' "$fingerprint"
}

validate_migration_lineage() {
    cluster=$1
    cluster_identifier=$(cluster_system_identifier "$cluster")
    legacy_marker_is_old=false

    if [ -s "$cluster_marker" ]; then
        recorded_identifier=$(awk 'NR == 1 { print $1 }' "$cluster_marker") ||
            die "cannot read PostgreSQL cluster lineage marker"
        [ "$recorded_identifier" = "$cluster_identifier" ] ||
            die "persistent PostgreSQL cluster does not match its durable lineage marker"
    fi
    if [ -s "$lineage_marker" ]; then
        recorded_identifier=$(cat "$lineage_marker") ||
            die "cannot read PostgreSQL migration lineage marker"
        if [ "$recorded_identifier" != "$cluster_identifier" ] &&
            [ "$recorded_identifier" != "$expected_major" ]; then
            die "persistent PostgreSQL cluster does not match its migration lineage marker"
        fi
        [ "$recorded_identifier" != "$expected_major" ] ||
            legacy_marker_is_old=true
    fi
    if cluster_exists "$legacy_data"; then
        source_identifier=$(cluster_system_identifier "$legacy_data")
        [ "$source_identifier" = "$cluster_identifier" ] ||
            die "persistent PostgreSQL cluster does not originate from the attached legacy PGDATA"
        current_fingerprint=$(cluster_wal_fingerprint "$legacy_data")
        if [ ! -s "$source_fingerprint_marker" ]; then
            [ "$legacy_marker_is_old" = true ] ||
                die "persistent PostgreSQL target has an attached legacy source but no durable source snapshot; refusing to guess which copy is newer"
            case "${DEVSHARD_POSTGRES_ACCEPT_LEGACY_TARGET:-false}" in
                1 | true | yes) ;;
                *) die "historical PostgreSQL marker 16 cannot prove whether the retained source or target is newer; select the target explicitly with DEVSHARD_POSTGRES_ACCEPT_LEGACY_TARGET=true, or restore the authoritative source" ;;
            esac
            printf '%s\n' "$current_fingerprint" \
                >"$source_fingerprint_marker" ||
                die "cannot upgrade PostgreSQL source snapshot marker"
        fi
        recorded_fingerprint=$(awk 'NR == 1 { print $1 }' \
            "$source_fingerprint_marker") ||
            die "cannot read PostgreSQL source snapshot marker"
        [ "$recorded_fingerprint" = "$current_fingerprint" ] ||
            die "attached legacy PostgreSQL source changed after migration; refusing to start a potentially stale target"
    fi
    # Upgrade both the short-lived marker format from the previous updater
    # revision (which contained only "16") and targets published before the
    # common marker existed.
    printf '%s\n' "$cluster_identifier" >"$lineage_marker" ||
        die "cannot record PostgreSQL migration lineage"
    printf '%s\n' "$cluster_identifier" >"$cluster_marker" ||
        die "cannot record PostgreSQL cluster lineage"
    sync
}

write_atomic_marker() {
    path=$1
    value=$2
    temporary=$path.tmp.$$
    printf '%s\n' "$value" >"$temporary" ||
        die "cannot write PostgreSQL marker $path"
    chmod 600 "$temporary" || die "cannot secure PostgreSQL marker $path"
    mv -f "$temporary" "$path" || die "cannot publish PostgreSQL marker $path"
}

recover_published_migration() {
    commit_marker=$target_data/$migration_commit_name
    [ -s "$commit_marker" ] || return 0
    read -r committed_identifier committed_fingerprint extra_field \
        <"$commit_marker" || die "cannot read atomic PostgreSQL migration commit"
    [ -z "${extra_field:-}" ] ||
        die "atomic PostgreSQL migration commit has unexpected fields"
    case "$committed_identifier" in
        '' | *[!0-9]*) die "atomic PostgreSQL migration commit has invalid lineage" ;;
    esac
    case "$committed_fingerprint" in
        *[!0-9a-f]* | '')
            die "atomic PostgreSQL migration commit has invalid source snapshot"
            ;;
    esac
    [ "${#committed_fingerprint}" -eq 64 ] ||
        die "atomic PostgreSQL migration commit has invalid source snapshot"
    [ "$(cluster_system_identifier "$target_data")" = \
        "$committed_identifier" ] ||
        die "atomic PostgreSQL migration commit does not match PGDATA"
    if cluster_exists "$legacy_data"; then
        [ "$(cluster_system_identifier "$legacy_data")" = \
            "$committed_identifier" ] ||
            die "atomic PostgreSQL migration commit does not match its source"
        [ "$(cluster_wal_fingerprint "$legacy_data")" = \
            "$committed_fingerprint" ] ||
            die "legacy PostgreSQL source changed after atomic publication"
    fi
    write_atomic_marker "$lineage_marker" "$committed_identifier"
    write_atomic_marker "$cluster_marker" "$committed_identifier"
    write_atomic_marker "$source_fingerprint_marker" "$committed_fingerprint"
    rm -f "$staging_complete" ||
        die "cannot remove recovered PostgreSQL migration completion marker"
    sync
}

postgres_binding_marker() {
    for storage_root in "$versiond_data" "$versiond2_data"; do
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
    source_identifier=$1
    source_fingerprint=$2
    if [ -e "$target_data" ]; then
        if directory_has_entries "$target_data"; then
            die "refusing to replace non-empty incomplete target $target_data"
        fi
        rmdir "$target_data" || die "cannot remove empty target $target_data"
    fi
    mv "$staging_data" "$target_data" ||
        die "cannot publish migrated PostgreSQL cluster"
    recover_published_migration
    [ "$(cat "$lineage_marker")" = "$source_identifier" ] ||
        die "published PostgreSQL migration lineage changed unexpectedly"
    [ "$(cat "$source_fingerprint_marker")" = "$source_fingerprint" ] ||
        die "published PostgreSQL source snapshot changed unexpectedly"
}

forward_signal() {
    signal=$1
    [ -z "${postgres_child:-}" ] ||
        kill -"$signal" "$postgres_child" 2>/dev/null || true
}

handle_postgres_signal() {
    signal=$1
    shutdown_requested=true
    if [ -n "${watchdog_child:-}" ]; then
        kill "$watchdog_child" 2>/dev/null || true
    fi
    forward_signal "$signal"
}

handle_postgres_reload() {
    forward_signal HUP
}

postgres_final_process_is_running() {
    executable=$(readlink "/proc/$postgres_child/exe" 2>/dev/null || :)
    [ "${executable##*/}" = postgres ]
}

postgres_child_is_running() {
    kill -0 "$postgres_child" 2>/dev/null || return 1
    process_state=$(awk '{ print $3 }' "/proc/$postgres_child/stat" 2>/dev/null || :)
    [ "$process_state" != Z ]
}

postgres_sql_probe() {
    user=${POSTGRES_USER:-postgres}
    database=${POSTGRES_DB:-$user}
    timeout -s KILL 5 env PGCONNECT_TIMEOUT=3 \
        PGOPTIONS='-c statement_timeout=3000' \
        PGPASSWORD="${POSTGRES_PASSWORD:-}" psql \
        -h 127.0.0.1 -U "$user" -d "$database" -AtX \
        -v ON_ERROR_STOP=1 -c 'SELECT 1' >/dev/null 2>&1
}

postgres_watchdog() {
    failures=0
    while postgres_child_is_running; do
        sleep "${GONKA_POSTGRES_WATCHDOG_INTERVAL_SECONDS:-5}"
        if postgres_sql_probe; then
            failures=0
        else
            failures=$((failures + 1))
            if [ "$failures" -ge "${GONKA_POSTGRES_WATCHDOG_FAILURES:-12}" ]; then
                log "PostgreSQL failed $failures bounded SQL probes; terminating the server for restart-policy recovery"
                kill -TERM "$postgres_child" 2>/dev/null || true
                sleep "${GONKA_POSTGRES_TERMINATION_GRACE_SECONDS:-10}"
                kill -KILL "$postgres_child" 2>/dev/null || true
                return
            fi
        fi
    done
}

run_official_supervised() {
    record_lineage=${1:-false}
    shift
    startup_timeout=${GONKA_POSTGRES_STARTUP_TIMEOUT_SECONDS:-1800}
    termination_grace=${GONKA_POSTGRES_TERMINATION_GRACE_SECONDS:-10}
    case "$startup_timeout:$termination_grace" in
        *[!0-9:]* | 0:* | *:0) die \
            "PostgreSQL supervisor timeouts must be positive integer seconds" ;;
    esac
    "$official_entrypoint" "$@" &
    postgres_child=$!
    shutdown_requested=false
    trap 'handle_postgres_signal TERM' TERM
    trap 'handle_postgres_signal INT' INT
    trap 'handle_postgres_reload' HUP

    ready=false
    startup_deadline=$(( $(date +%s) + startup_timeout ))
    while kill -0 "$postgres_child" 2>/dev/null; do
        if postgres_final_process_is_running && postgres_sql_probe; then
            ready=true
            break
        fi
        if [ "$shutdown_requested" = false ] && \
            [ "$(date +%s)" -ge "$startup_deadline" ]; then
            log "PostgreSQL did not complete startup within ${startup_timeout}s; terminating it for restart-policy recovery"
            kill -TERM "$postgres_child" 2>/dev/null || true
            sleep "$termination_grace"
            kill -KILL "$postgres_child" 2>/dev/null || true
            break
        fi
        sleep 1
    done
    if [ "$ready" = true ]; then
        if [ "$record_lineage" = true ]; then
            validate_cluster "$target_data"
            identifier=$(cluster_system_identifier "$target_data")
            printf '%s\n' "$identifier" >"$cluster_marker" ||
                die "cannot record initialized PostgreSQL cluster lineage"
            sync
        fi
        postgres_watchdog &
        watchdog_child=$!
    fi

    status=0
    while :; do
        if wait "$postgres_child"; then candidate_status=0; else candidate_status=$?; fi
        if ! postgres_child_is_running; then
            status=$candidate_status
            break
        fi
    done
    if [ -n "${watchdog_child:-}" ] && [ "$shutdown_requested" = false ]; then
        kill "$watchdog_child" 2>/dev/null || true
    fi
    [ -z "${watchdog_child:-}" ] || wait "$watchdog_child" 2>/dev/null || true
    trap - TERM INT HUP
    return "$status"
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
    recover_published_migration
    if [ ! -s "$cluster_marker" ] && [ ! -s "$lineage_marker" ] &&
        ! cluster_exists "$legacy_data"; then
        die "persistent PGDATA has no completed lineage marker; initialization may have been interrupted"
    fi
    validate_migration_lineage "$target_data"
    rm -f "$staging_complete" ||
        die "cannot remove stale PostgreSQL migration completion marker"
elif [ -e "$target_data" ] && directory_has_entries "$target_data"; then
    die "persistent PGDATA is non-empty but has no PG_VERSION: $target_data"
elif cluster_exists "$staging_data" && [ -f "$staging_complete" ]; then
    validate_cluster "$staging_data"
    source_identifier=$(cluster_system_identifier "$legacy_data")
	if [ -s "$cluster_marker" ]; then
		[ "$(awk 'NR == 1 { print $1 }' "$cluster_marker")" = "$source_identifier" ] ||
			die "PostgreSQL staging conflicts with the common lineage marker"
	fi
	if [ -s "$lineage_marker" ]; then
		recorded_identifier=$(awk 'NR == 1 { print $1 }' "$lineage_marker")
		[ "$recorded_identifier" = "$source_identifier" ] ||
			[ "$recorded_identifier" = "$expected_major" ] ||
			die "PostgreSQL staging conflicts with the historical migration marker"
	fi
    if [ ! -s "$staging_complete" ] && [ -s "$lineage_marker" ] &&
        [ "$(cat "$lineage_marker")" = "$expected_major" ]; then
        # Compatibility with the first updater revision: the copy-complete
        # file was empty and .migrated-from-v4 contained the major version.
        printf '%s\n' "$source_identifier" >"$staging_complete" ||
            die "cannot upgrade PostgreSQL staging lineage marker"
    fi
    [ -s "$staging_complete" ] ||
        die "PostgreSQL migration completion marker has no source lineage"
    [ "$(cat "$staging_complete")" = "$source_identifier" ] ||
        die "PostgreSQL migration staging does not match the attached legacy PGDATA"
    [ "$(cluster_system_identifier "$staging_data")" = "$source_identifier" ] ||
        die "PostgreSQL migration staging has a different system identifier"
    source_fingerprint=$(cluster_wal_fingerprint "$legacy_data")
    staging_fingerprint=$(cluster_wal_fingerprint "$staging_data")
    [ "$source_fingerprint" = "$staging_fingerprint" ] ||
        die "completed PostgreSQL staging changed independently of its source"
    printf '%s\n' "$source_fingerprint" >"$source_fingerprint_marker" ||
        die "cannot record the migrated PostgreSQL source snapshot"
    write_atomic_marker "$staging_data/$migration_commit_name" \
        "$source_identifier $source_fingerprint"
    log "finishing an interrupted atomic migration"
    publish_staging "$source_identifier" "$source_fingerprint"
elif cluster_exists "$legacy_data"; then
    validate_cluster "$legacy_data"
    source_identifier=$(cluster_system_identifier "$legacy_data")

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
    source_fingerprint=$(cluster_wal_fingerprint "$legacy_data")
    staging_fingerprint=$(cluster_wal_fingerprint "$staging_data")
    [ "$source_fingerprint" = "$staging_fingerprint" ] ||
        die "PostgreSQL migration WAL fingerprint differs from its source"
    printf '%s\n' "$source_fingerprint" >"$source_fingerprint_marker" ||
        die "cannot record the migrated PostgreSQL source snapshot"
    sync
    write_atomic_marker "$staging_data/$migration_commit_name" \
        "$source_identifier $source_fingerprint"
    printf '%s\n' "$source_identifier" >"$staging_complete" ||
        die "cannot record PostgreSQL migration source lineage"
    sync
    publish_staging "$source_identifier" "$source_fingerprint"
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
    log "no existing PostgreSQL cluster found; initializing persistent PGDATA"
    run_official_supervised true "$@"
    exit $?
fi

run_official_supervised false "$@"
