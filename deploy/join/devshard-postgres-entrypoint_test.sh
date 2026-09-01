#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
entrypoint="$script_dir/devshard-postgres-entrypoint.sh"
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
mkdir -p "$tmpdir/bin"
cat >"$tmpdir/bin/postgres" <<'EOF'
#!/bin/sh
printf '%s\n' 'postgres (PostgreSQL) 16.15'
EOF
cat >"$tmpdir/bin/ldd" <<'EOF'
#!/bin/sh
printf '%s\n' 'musl libc (x86_64)' 'Version 1.2.6'
exit 1
EOF
cat >"$tmpdir/bin/pg_controldata" <<'EOF'
#!/bin/sh
identifier=1000000000000000000
if [ -s "$1/.test-system-id" ]; then
    identifier=$(cat "$1/.test-system-id")
fi
printf 'Database system identifier:            %s\n' "$identifier"
EOF
cat >"$tmpdir/bin/readlink" <<'EOF'
#!/bin/sh
case "$1" in
    /proc/*/exe)
        if [ -f "$PGDATA/.test-final-process" ]; then
            printf '%s\n' /usr/local/bin/postgres
        else
            printf '%s\n' /bin/sh
        fi
        ;;
    *) /usr/bin/readlink "$@" ;;
esac
EOF
cat >"$tmpdir/bin/psql" <<'EOF'
#!/bin/sh
[ -s "$PGDATA/PG_VERSION" ] && [ ! -e "$PGDATA/.test-probe-fail" ]
EOF
chmod +x "$tmpdir/bin/postgres" "$tmpdir/bin/ldd" \
    "$tmpdir/bin/pg_controldata" "$tmpdir/bin/readlink" "$tmpdir/bin/psql"
test_path="$tmpdir/bin:$PATH"

fail() {
    echo "devshard-postgres-entrypoint_test: $*" >&2
    exit 1
}

new_case() {
    case_dir="$tmpdir/$1"
    legacy="$case_dir/legacy"
    persistent="$case_dir/persistent"
    existing="$case_dir/existing"
    versiond_data="$case_dir/versiond-data"
    versiond2_data="$case_dir/versiond2-data"
    mkdir -p "$legacy" "$persistent" "$existing" \
        "$versiond_data" "$versiond2_data"
	mkdir -p "$legacy/global" "$legacy/pg_wal"
	printf 'control\n' >"$legacy/global/pg_control"
	printf 'wal\n' >"$legacy/pg_wal/000000010000000000000001"
}

source_fingerprint() {
	(cd "$legacy" && find global/pg_control pg_wal -type f -print | LC_ALL=C sort |
		while IFS= read -r file; do sha256sum "$file"; done | sha256sum | awk '{print $1}')
}

run_entrypoint() {
    env \
        PATH="${entrypoint_path:-$test_path}" \
        GONKA_POSTGRES_LEGACY_DATA="$legacy" \
        GONKA_POSTGRES_PERSISTENT_ROOT="$persistent" \
        GONKA_POSTGRES_EXISTING_VERSIOND="$existing" \
        GONKA_POSTGRES_VERSIOND_DATA="$versiond_data" \
        GONKA_POSTGRES_VERSIOND2_DATA="$versiond2_data" \
        GONKA_POSTGRES_OFFICIAL_ENTRYPOINT="${official_entrypoint:-/bin/true}" \
        PGDATA="$persistent/data" \
        DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT="${allow_empty:-false}" \
        GONKA_POSTGRES_STARTUP_TIMEOUT_SECONDS="${startup_timeout:-1800}" \
        GONKA_POSTGRES_TERMINATION_GRACE_SECONDS="${termination_grace:-10}" \
        GONKA_POSTGRES_WATCHDOG_INTERVAL_SECONDS="${watchdog_interval:-5}" \
        GONKA_POSTGRES_WATCHDOG_FAILURES="${watchdog_failures:-12}" \
        "$entrypoint" postgres
}

new_case migrate
printf '16\n' > "$legacy/PG_VERSION"
printf 'preserved\n' > "$legacy/session-row"
mkdir -p "$existing/v4"
run_entrypoint
[[ $(<"$persistent/data/session-row") == preserved ]] || fail \
    "legacy data was not copied"
[[ $(<"$legacy/session-row") == preserved ]] || fail \
    "legacy source was modified"
[[ -f "$persistent/.migrated-from-v4" ]] || fail \
    "migration marker was not written"
[[ $(<"$persistent/.migrated-from-v4") == 1000000000000000000 ]] || fail \
    "migration marker does not record the PostgreSQL source lineage"
[[ $(<"$persistent/.gonka-cluster-lineage") == 1000000000000000000 ]] || fail \
    "migration did not record the common PostgreSQL lineage marker"
[[ ! -e "$persistent/.migrating" ]] || fail \
    "staging directory remained after migration"
[[ ! -e "$persistent/.gonka-copy-complete" ]] || fail \
    "migration completion marker leaked into live storage"

new_case reject-insufficient-space
printf '16\n' > "$legacy/PG_VERSION"
printf 'cannot-fit\n' > "$legacy/session-row"
mkdir -p "$case_dir/bin"
cat >"$case_dir/bin/df" <<'EOF'
#!/bin/sh
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n'
printf '/dev/fake 100 99 1 99%% /persistent\n'
EOF
chmod +x "$case_dir/bin/df"
entrypoint_path="$case_dir/bin:$test_path"
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "migration started without enough free space"
fi
unset entrypoint_path
grep -q 'not enough free space' "$case_dir/stderr" || fail \
    "insufficient-space failure was not diagnosed"
[[ ! -e "$persistent/.migrating" ]] || fail \
    "staging directory was created after the space check failed"

new_case migrate-crash-state
printf '16\n' > "$legacy/PG_VERSION"
printf '1\n' > "$legacy/postmaster.pid"
printf 'wal-recoverable\n' > "$legacy/session-row"
run_entrypoint
[[ $(<"$persistent/data/session-row") == wal-recoverable ]] || fail \
    "crash-state cluster was not copied"
[[ ! -e "$persistent/data/postmaster.pid" ]] || fail \
    "stale postmaster.pid was published"
[[ -e "$legacy/postmaster.pid" ]] || fail \
    "rollback source was modified while removing postmaster.pid"

new_case target-wins
printf '16\n' > "$legacy/PG_VERSION"
printf 'legacy\n' > "$legacy/source"
mkdir -p "$persistent/data"
printf '16\n' > "$persistent/data/PG_VERSION"
printf 'current\n' > "$persistent/data/source"
printf '1000000000000000000\n' > "$persistent/.migrated-from-v4"
source_fingerprint >"$persistent/.gonka-v4-source-wal.sha256"
touch "$persistent/.gonka-copy-complete"
run_entrypoint
[[ $(<"$persistent/data/source") == current ]] || fail \
    "an existing persistent cluster was overwritten"
[[ ! -e "$persistent/.gonka-copy-complete" ]] || fail \
    "stale migration completion marker survived an existing target"

new_case previous-revision-markers
mkdir -p "$persistent/data"
printf '16\n' >"$persistent/data/PG_VERSION"
printf '16\n' >"$persistent/.migrated-from-v4"
: >"$persistent/.gonka-copy-complete"
run_entrypoint
[[ $(<"$persistent/.migrated-from-v4") == 1000000000000000000 ]] || fail \
    "previous-revision major marker was not upgraded"
[[ $(<"$persistent/.gonka-cluster-lineage") == 1000000000000000000 ]] || fail \
    "previous-revision target did not receive a common lineage marker"

new_case resume-publish
printf '16\n' > "$legacy/PG_VERSION"
mkdir -p "$persistent/.migrating"
printf '16\n' > "$persistent/.migrating/PG_VERSION"
printf 'resumed\n' > "$persistent/.migrating/session-row"
mkdir -p "$persistent/.migrating/global" "$persistent/.migrating/pg_wal"
cp "$legacy/global/pg_control" "$persistent/.migrating/global/pg_control"
cp "$legacy/pg_wal/000000010000000000000001" "$persistent/.migrating/pg_wal/"
printf '1000000000000000000\n' > "$persistent/.gonka-copy-complete"
run_entrypoint
[[ $(<"$persistent/data/session-row") == resumed ]] || fail \
    "complete staging data was not published"

new_case reject-foreign-target
printf '16\n' > "$legacy/PG_VERSION"
printf '1000000000000000000\n' > "$legacy/.test-system-id"
mkdir -p "$persistent/data"
printf '16\n' > "$persistent/data/PG_VERSION"
printf '2000000000000000000\n' > "$persistent/data/.test-system-id"
printf '2000000000000000000\n' > "$persistent/.migrated-from-v4"
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "persistent PostgreSQL from another source was accepted"
fi
grep -q 'does not originate from the attached legacy PGDATA' \
    "$case_dir/stderr" || fail \
    "foreign persistent target failure did not explain the lineage mismatch"

new_case recopy-partial-staging
printf '16\n' > "$legacy/PG_VERSION"
printf 'complete\n' > "$legacy/session-row"
mkdir -p "$persistent/.migrating"
printf 'partial\n' > "$persistent/.migrating/session-row"
run_entrypoint
[[ $(<"$persistent/data/session-row") == complete ]] || fail \
    "partial staging data was not replaced from the intact source"

new_case reject-uncommitted-staging
mkdir -p "$persistent/.migrating"
printf '16\n' > "$persistent/.migrating/PG_VERSION"
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "uncommitted staging data was published without its source"
fi
grep -q 'incomplete migration exists' "$case_dir/stderr" || fail \
    "uncommitted-staging failure was not diagnosed"

new_case reject-detached
mkdir -p "$existing/v4"
printf 'install metadata\n' > "$existing/v4/install.json"
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "existing installation was allowed to initialize an empty database"
fi
grep -q 'first-time HA enablement or a detached drained database' \
    "$case_dir/stderr" || fail \
    "detached-volume failure did not explain the safety guard"

new_case reject-postgres-bound
mkdir -p "$existing/v4" "$versiond_data/v5"
printf 'install metadata\n' > "$existing/v4/install.json"
touch "$versiond_data/v5/.pg-bound"
allow_empty=true
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "PostgreSQL-bound data was allowed to initialize an empty database"
fi
unset allow_empty
grep -q 'PostgreSQL-bound devshard data exists' "$case_dir/stderr" || fail \
    "PostgreSQL-bound failure did not identify the data-loss state"
grep -q 'do not use DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT' \
    "$case_dir/stderr" || fail \
    "PostgreSQL-bound failure suggested the destructive override"

new_case reject-postgres-bound-second-root
mkdir -p "$existing/v4" "$versiond2_data/v5"
printf 'install metadata\n' > "$existing/v4/install.json"
touch "$versiond2_data/v5/.pg-bound"
allow_empty=true
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "PostgreSQL binding in the second data root was ignored"
fi
unset allow_empty
grep -q "$versiond2_data/v5/.pg-bound" "$case_dir/stderr" || fail \
    "PostgreSQL-bound failure did not identify the second data root"

new_case reject-missing-evidence-mount
rmdir "$versiond2_data"
allow_empty=true
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "empty initialization was allowed without both evidence mounts"
fi
unset allow_empty
grep -q 'required devshard data directories are not mounted' \
    "$case_dir/stderr" || fail \
    "missing-evidence-mount failure was not diagnosed"

new_case explicit-empty
mkdir -p "$existing/v4"
printf 'install metadata\n' > "$existing/v4/install.json"
allow_empty=true
run_entrypoint
unset allow_empty

new_case initialized-empty-lineage
cat >"$case_dir/official-entrypoint" <<'EOF'
#!/bin/sh
mkdir -p "$PGDATA"
printf '16\n' >"$PGDATA/PG_VERSION"
mkdir -p "$PGDATA/global" "$PGDATA/pg_wal"
printf 'control\n' >"$PGDATA/global/pg_control"
touch "$PGDATA/.test-final-process"
sleep 2
EOF
chmod +x "$case_dir/official-entrypoint"
official_entrypoint=$case_dir/official-entrypoint
allow_empty=true
run_entrypoint
unset allow_empty official_entrypoint
[[ $(<"$persistent/.gonka-cluster-lineage") == 1000000000000000000 ]] || fail \
    "empty initialization did not receive a durable lineage marker"

new_case reject-interrupted-empty-init
cat >"$case_dir/official-entrypoint" <<'EOF'
#!/bin/sh
mkdir -p "$PGDATA/global" "$PGDATA/pg_wal"
printf '16\n' >"$PGDATA/PG_VERSION"
printf 'partial\n' >"$PGDATA/global/pg_control"
exit 1
EOF
chmod +x "$case_dir/official-entrypoint"
official_entrypoint=$case_dir/official-entrypoint
allow_empty=true
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "failed empty initialization returned success"
fi
unset allow_empty official_entrypoint
if run_entrypoint >"$case_dir/retry.stdout" 2>"$case_dir/retry.stderr"; then
    fail "partially initialized cluster was trusted on retry"
fi
grep -q 'initialization may have been interrupted' "$case_dir/retry.stderr" || fail \
    "interrupted initialization was not diagnosed"

new_case fresh
run_entrypoint

new_case reject-partial-target
mkdir -p "$persistent/data"
printf 'partial\n' > "$persistent/data/base"
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "partial persistent PGDATA was accepted"
fi
grep -q 'non-empty but has no PG_VERSION' "$case_dir/stderr" || fail \
    "partial-target failure was not diagnosed"

new_case reject-wrong-major
printf '15\n' > "$legacy/PG_VERSION"
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "PostgreSQL 15 cluster was accepted by the PostgreSQL 16 image"
fi
grep -q 'uses major version 15; expected 16' "$case_dir/stderr" || fail \
    "wrong-major failure was not diagnosed"

new_case reject-wrong-target-major
mkdir -p "$persistent/data"
printf '15\n' > "$persistent/data/PG_VERSION"
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "PostgreSQL 15 target was accepted by the PostgreSQL 16 image"
fi
grep -q 'uses major version 15; expected 16' "$case_dir/stderr" || fail \
    "wrong-target-major failure was not diagnosed"

new_case reject-glibc-runtime
mkdir -p "$persistent/data" "$case_dir/bin"
printf '16\n' > "$persistent/data/PG_VERSION"
printf 'preserved\n' > "$persistent/data/session-row"
cat >"$case_dir/bin/ldd" <<'EOF'
#!/bin/sh
printf '%s\n' 'ldd (Debian GLIBC 2.36) 2.36'
EOF
chmod +x "$case_dir/bin/ldd"
entrypoint_path="$case_dir/bin:$test_path"
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "a glibc image was allowed to open the musl PostgreSQL cluster"
fi
unset entrypoint_path
grep -q 'not compatible with the existing devshard PGDATA' \
    "$case_dir/stderr" || fail \
    "unsupported-libc failure was not diagnosed"
[[ $(<"$persistent/data/session-row") == preserved ]] || fail \
    "unsupported runtime modified PGDATA before failing"

new_case follow-image-major
mkdir -p "$persistent/data" "$case_dir/bin"
printf '17\n' > "$persistent/data/PG_VERSION"
printf '1000000000000000000\n' >"$persistent/.gonka-cluster-lineage"
cat >"$case_dir/bin/postgres" <<'EOF'
#!/bin/sh
printf '%s\n' 'postgres (PostgreSQL) 17.5'
EOF
chmod +x "$case_dir/bin/postgres"
entrypoint_path="$case_dir/bin:$test_path"
run_entrypoint
unset entrypoint_path

new_case reject-incomplete-wal-fingerprint
printf '16\n' >"$legacy/PG_VERSION"
mkdir -p "$case_dir/bin"
cat >"$case_dir/bin/sha256sum" <<'EOF'
#!/bin/sh
case ${1:-} in
    pg_wal/*) exit 1 ;;
    *) exec /usr/bin/sha256sum "$@" ;;
esac
EOF
chmod +x "$case_dir/bin/sha256sum"
entrypoint_path="$case_dir/bin:$test_path"
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "migration accepted an incomplete WAL fingerprint"
fi
unset entrypoint_path
grep -q 'cannot fingerprint PostgreSQL WAL state' "$case_dir/stderr" || fail \
    "WAL fingerprint read failure was not diagnosed"

new_case terminate-stuck-startup
cat >"$case_dir/official-entrypoint" <<'EOF'
#!/bin/sh
trap '' TERM
exec sleep 30
EOF
chmod +x "$case_dir/official-entrypoint"
official_entrypoint=$case_dir/official-entrypoint
startup_timeout=1
termination_grace=1
SECONDS=0
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "PostgreSQL startup hang returned success"
fi
elapsed=$SECONDS
unset official_entrypoint startup_timeout termination_grace
(( elapsed < 8 )) || fail "PostgreSQL startup timeout was not bounded"
grep -q 'did not complete startup within 1s' "$case_dir/stderr" || fail \
    "PostgreSQL startup timeout was not diagnosed"

new_case preserve-graceful-shutdown
mkdir -p "$persistent/data/global" "$persistent/data/pg_wal"
printf '16\n' >"$persistent/data/PG_VERSION"
printf 'control\n' >"$persistent/data/global/pg_control"
printf '1000000000000000000\n' >"$persistent/.gonka-cluster-lineage"
cat >"$case_dir/official-entrypoint" <<'EOF'
#!/bin/sh
touch "$PGDATA/.test-final-process"
trap '
    touch "$PGDATA/.test-probe-fail"
    sleep 3
    touch "$PGDATA/.test-term-complete"
    exit 0
' TERM
while :; do sleep 1; done
EOF
chmod +x "$case_dir/official-entrypoint"
official_entrypoint=$case_dir/official-entrypoint
termination_grace=1
watchdog_interval=1
watchdog_failures=1
env \
    PATH="$test_path" \
    GONKA_POSTGRES_LEGACY_DATA="$legacy" \
    GONKA_POSTGRES_PERSISTENT_ROOT="$persistent" \
    GONKA_POSTGRES_EXISTING_VERSIOND="$existing" \
    GONKA_POSTGRES_VERSIOND_DATA="$versiond_data" \
    GONKA_POSTGRES_VERSIOND2_DATA="$versiond2_data" \
    GONKA_POSTGRES_OFFICIAL_ENTRYPOINT="$official_entrypoint" \
    PGDATA="$persistent/data" \
    GONKA_POSTGRES_TERMINATION_GRACE_SECONDS="$termination_grace" \
    GONKA_POSTGRES_WATCHDOG_INTERVAL_SECONDS="$watchdog_interval" \
    GONKA_POSTGRES_WATCHDOG_FAILURES="$watchdog_failures" \
    "$entrypoint" postgres >"$case_dir/stdout" 2>"$case_dir/stderr" &
supervisor=$!
for _ in {1..50}; do
    [[ -e $persistent/data/.test-final-process ]] && break
    sleep 0.1
done
[[ -e $persistent/data/.test-final-process ]] || fail \
    "PostgreSQL test process did not reach its final process"
sleep 1
kill -TERM "$supervisor"
wait "$supervisor" || fail "graceful PostgreSQL shutdown returned failure"
unset official_entrypoint termination_grace watchdog_interval watchdog_failures
[[ -e $persistent/data/.test-term-complete ]] || fail \
    "watchdog interrupted the PostgreSQL graceful shutdown window"

echo "devshard-postgres-entrypoint_test: ok"
