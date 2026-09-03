#!/usr/bin/env bash

# Failure-oriented acceptance tests for the PGDATA provenance guard.
#
#   --repro  succeeds only while every selected defect is reproduced
#   --gate   succeeds only when none of the selected defects is reproduced
#
# Keep this test out of the required CI lane while --gate is red. Once the
# production guard is fixed, --gate is the command that belongs in CI; --repro
# then becomes a convenient check that the old failure can no longer be seen.

set -Eeuo pipefail

scenario_ids=(PG-PROV-01 PG-PROV-02)
if [[ ${1:-} == --list ]]; then
    printf '%s\n' "${scenario_ids[@]}"
    exit 0
fi
mode=${1:---gate}
case $mode in
    --gate | --repro) shift || true ;;
    *)
        echo "Usage: ${0##*/} [--gate|--repro] [SCENARIO ...]" >&2
        echo "       ${0##*/} --list" >&2
        exit 2
        ;;
esac
selected=("$@")
if ((${#selected[@]} == 0)); then
    selected=("${scenario_ids[@]}")
fi
for requested in "${selected[@]}"; do
    [[ " ${scenario_ids[*]} " == *" $requested "* ]] || {
        echo "Unknown scenario: $requested" >&2
        exit 2
    }
done

is_selected() {
    local requested=$1 selected_id
    for selected_id in "${selected[@]}"; do
        [[ $selected_id != "$requested" ]] || return 0
    done
    return 1
}

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
entrypoint=$script_dir/devshard-postgres-entrypoint.sh
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
identifier=7000000000000000001
checkpoint=0/100
[ ! -f "$1/.fake-system-identifier" ] || identifier=$(cat "$1/.fake-system-identifier")
[ ! -f "$1/.fake-checkpoint" ] || checkpoint=$(cat "$1/.fake-checkpoint")
printf 'pg_control version number:            1300\n'
printf 'Database system identifier:           %s\n' "$identifier"
printf 'Latest checkpoint location:           %s\n' "$checkpoint"
EOF
chmod +x "$tmpdir/bin/postgres" "$tmpdir/bin/ldd" \
    "$tmpdir/bin/pg_controldata"
test_path=$tmpdir/bin:$PATH

case_root=
legacy=
persistent=
existing=
versiond_data=
versiond2_data=
initdb_dir=

new_case() {
    case_root=$tmpdir/$1
    legacy=$case_root/legacy
    persistent=$case_root/persistent
    existing=$case_root/existing
    versiond_data=$case_root/versiond-data
    versiond2_data=$case_root/versiond2-data
    initdb_dir=$case_root/initdb
    mkdir -p "$legacy" "$persistent" "$existing" "$versiond_data" \
        "$versiond2_data" "$initdb_dir"
}

run_entrypoint() {
    env \
        PATH="$test_path" \
        GONKA_POSTGRES_LEGACY_DATA="$legacy" \
        GONKA_POSTGRES_PERSISTENT_ROOT="$persistent" \
        GONKA_POSTGRES_EXISTING_VERSIOND="$existing" \
        GONKA_POSTGRES_VERSIOND_DATA="$versiond_data" \
        GONKA_POSTGRES_VERSIOND2_DATA="$versiond2_data" \
        GONKA_POSTGRES_OFFICIAL_ENTRYPOINT=/bin/true \
        GONKA_POSTGRES_INITDB_DIR="$initdb_dir" \
        PGDATA="$persistent/data" \
        DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=false \
        "$entrypoint" postgres
}

declare -a reproduced=()
declare -a fixed=()

# PG-PROV-01: a physical clone keeps the same system identifier. If the v4
# volume accepts writes after the copy (for example, during a rollback), the
# wrapper must not silently choose the older persistent target on the next v5
# attempt. A safe implementation may reject the ambiguity or publish the newer
# source; accepting the stale target is the defect.
if is_selected PG-PROV-01; then
    new_case same-system-id-diverged
    mkdir -p "$persistent/data"
    printf '16\n' >"$legacy/PG_VERSION"
    printf '16\n' >"$persistent/data/PG_VERSION"
    printf '0/300\n' >"$legacy/.fake-checkpoint"
    printf '0/200\n' >"$persistent/data/.fake-checkpoint"
    printf 'newer-on-v4\n' >"$legacy/acceptance-row"
    printf 'stale-v5-copy\n' >"$persistent/data/acceptance-row"
    status=0
    run_entrypoint >"$case_root/stdout" 2>"$case_root/stderr" || status=$?
    if ((status == 0)) && [[ $(<"$persistent/data/acceptance-row") == stale-v5-copy ]]; then
        reproduced+=(PG-PROV-01)
    else
        fixed+=(PG-PROV-01)
    fi
fi

# PG-PROV-02: a completion marker outside PGDATA is not evidence for the
# cluster currently mounted as PGDATA. Replacing the target while leaving the
# sibling marker behind must be rejected.
if is_selected PG-PROV-02; then
    new_case stale-sibling-marker
    mkdir -p "$persistent/data"
    printf '16\n' >"$persistent/data/PG_VERSION"
    printf '8000000000000000002\n' >"$persistent/data/.fake-system-identifier"
    printf 'foreign-cluster\n' >"$persistent/data/acceptance-row"
    printf '16\n' >"$persistent/.migrated-from-v4"
    status=0
    run_entrypoint >"$case_root/stdout" 2>"$case_root/stderr" || status=$?
    if ((status == 0)); then
        reproduced+=(PG-PROV-02)
    else
        fixed+=(PG-PROV-02)
    fi
fi

if [[ $mode == --repro ]]; then
    if ((${#reproduced[@]} == ${#selected[@]})); then
        printf 'REPRODUCED %s\n' "${reproduced[*]}"
        exit 0
    fi
    printf 'NOT REPRODUCED %s; still reproduced: %s\n' \
        "${fixed[*]:-none}" "${reproduced[*]:-none}" >&2
    exit 1
fi

if ((${#reproduced[@]} > 0)); then
    printf 'ACCEPTANCE RED: %s\n' "${reproduced[*]}" >&2
    printf 'Fixed scenarios: %s\n' "${fixed[*]:-none}" >&2
    exit 1
fi

printf 'ACCEPTANCE GREEN: %s\n' "${selected[*]}"
