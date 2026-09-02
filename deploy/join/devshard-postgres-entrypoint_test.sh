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
if [ -e "$1/.controldata-fail" ]; then
    echo 'pg_controldata: could not open file' >&2
    exit 1
fi
identifier=7000000000000000001
[ ! -f "$1/.fake-system-identifier" ] || identifier=$(cat "$1/.fake-system-identifier")
printf 'pg_control version number:            1300\n'
printf 'Database system identifier:           %s\n' "$identifier"
EOF
chmod +x "$tmpdir/bin/postgres" "$tmpdir/bin/ldd" "$tmpdir/bin/pg_controldata"
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
    initdb_dir="$case_dir/initdb"
    mkdir -p "$legacy" "$persistent" "$existing" \
        "$versiond_data" "$versiond2_data" "$initdb_dir"
}

run_entrypoint() {
    env \
        PATH="${entrypoint_path:-$test_path}" \
        GONKA_POSTGRES_LEGACY_DATA="$legacy" \
        GONKA_POSTGRES_PERSISTENT_ROOT="$persistent" \
        GONKA_POSTGRES_EXISTING_VERSIOND="$existing" \
        GONKA_POSTGRES_VERSIOND_DATA="$versiond_data" \
        GONKA_POSTGRES_VERSIOND2_DATA="$versiond2_data" \
        GONKA_POSTGRES_OFFICIAL_ENTRYPOINT=/bin/true \
        GONKA_POSTGRES_INITDB_DIR="$initdb_dir" \
        PGDATA="$persistent/data" \
        DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT="${allow_empty:-false}" \
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
touch "$persistent/.gonka-copy-complete"
run_entrypoint
[[ $(<"$persistent/data/source") == current ]] || fail \
    "an existing persistent cluster was overwritten"
[[ ! -e "$persistent/.gonka-copy-complete" ]] || fail \
    "stale migration completion marker survived an existing target"

new_case resume-publish
mkdir -p "$persistent/.migrating"
printf '16\n' > "$persistent/.migrating/PG_VERSION"
printf 'resumed\n' > "$persistent/.migrating/session-row"
touch "$persistent/.gonka-copy-complete"
run_entrypoint
[[ $(<"$persistent/data/session-row") == resumed ]] || fail \
    "complete staging data was not published"

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

new_case fresh
run_entrypoint
[[ -x "$initdb_dir/zz-gonka-init-complete.sh" ]] || fail \
    "fresh initialization did not install the completion hook"
grep -q "$persistent/data/.gonka-init-complete" "$initdb_dir/zz-gonka-init-complete.sh" || fail \
    "the completion hook does not record completion under the persistent root"

new_case reject-unfinished-target
mkdir -p "$persistent/data"
printf '16\n' > "$persistent/data/PG_VERSION"
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "a persistent cluster without a completion marker was accepted"
fi
grep -q 'has no completion marker' "$case_dir/stderr" || fail \
    "unfinished-target failure was not diagnosed"

new_case accept-completed-target
mkdir -p "$persistent/data"
printf '16\n' > "$persistent/data/PG_VERSION"
: > "$persistent/data/.gonka-init-complete"
run_entrypoint

new_case reject-foreign-target
printf '16\n' > "$legacy/PG_VERSION"
printf '1\n' > "$legacy/.fake-system-identifier"
mkdir -p "$persistent/data"
printf '16\n' > "$persistent/data/PG_VERSION"
printf '2\n' > "$persistent/data/.fake-system-identifier"
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "a foreign persistent cluster next to the v4 volume was accepted"
fi
grep -q 'is a different cluster' "$case_dir/stderr" || fail \
    "foreign-target failure was not diagnosed"

new_case reject-unreadable-controldata
printf '16\n' > "$legacy/PG_VERSION"
mkdir -p "$persistent/data"
printf '16\n' > "$persistent/data/PG_VERSION"
: > "$persistent/data/.controldata-fail"
if run_entrypoint >"$case_dir/stdout" 2>"$case_dir/stderr"; then
    fail "an unreadable pg_controldata was treated as matching"
fi
grep -q 'cannot read pg_controldata' "$case_dir/stderr" || fail \
    "controldata failure was not diagnosed: $(cat "$case_dir/stderr")"

new_case resume-publish-marks-migration
mkdir -p "$persistent/.migrating"
printf '16\n' > "$persistent/.migrating/PG_VERSION"
: > "$persistent/.gonka-copy-complete"
run_entrypoint
[[ -f "$persistent/.migrated-from-v4" ]] || fail \
    "a resumed migration did not record the v4 migration marker"
run_entrypoint || fail "the resumed cluster was refused on the next start"

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
: > "$persistent/data/.gonka-init-complete"
cat >"$case_dir/bin/postgres" <<'EOF'
#!/bin/sh
printf '%s\n' 'postgres (PostgreSQL) 17.5'
EOF
chmod +x "$case_dir/bin/postgres"
entrypoint_path="$case_dir/bin:$test_path"
run_entrypoint
unset entrypoint_path

echo "devshard-postgres-entrypoint_test: ok"
