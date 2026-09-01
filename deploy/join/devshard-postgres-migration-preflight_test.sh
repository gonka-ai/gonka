#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
preflight="$script_dir/devshard-postgres-migration-preflight.sh"
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
    echo "devshard-postgres-migration-preflight_test: $*" >&2
    exit 1
}

cat >"$tmpdir/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail

printf '%q ' "$@" >>"$DOCKER_LOG"
printf '\n' >>"$DOCKER_LOG"

case ${1:-} in
    inspect)
        case "$*" in
            *'{{json .Config.Env}}'*) printf '%s\n' "${POSTGRES_RUNTIME_ENV:-[\"PGDATA=/var/lib/postgresql/data\"]}" ;;
            *'{{.State.Running}}'*) printf '%s\n' "${POSTGRES_RUNTIME_RUNNING:-true}" ;;
            *'.Destination "/var/lib/postgresql/data"'*) printf '%s\n' postgres-v4-volume ;;
        esac
        exit 0
        ;;
    exec)
        printf '%s\n' "${DEVSHARD_SCHEMA_STATE:-t}"
        exit 0
        ;;
    volume)
        [[ ${2:-} == inspect ]] || exit 1
        exit 0
        ;;
    run)
        printf '%s\n' "$DOCKER_PROBE"
        exit 0
        ;;
    *) exit 1 ;;
esac
EOF
chmod +x "$tmpdir/docker"

cat >"$tmpdir/df" <<'EOF'
#!/usr/bin/env bash
printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n'
printf '/dev/fake 999999 1 %s 1%% /target\n' "$FREE_KIB"
EOF
chmod +x "$tmpdir/df"

run_preflight() {
    DOCKER_BIN="$tmpdir/docker" \
        DOCKER_LOG="$tmpdir/docker.log" \
        DOCKER_PROBE="$DOCKER_PROBE" \
        DEVSHARD_SCHEMA_STATE="${DEVSHARD_SCHEMA_STATE:-t}" \
        POSTGRES_RUNTIME_ENV="${POSTGRES_RUNTIME_ENV:-[\"PGDATA=/var/lib/postgresql/data\"]}" \
        POSTGRES_RUNTIME_RUNNING="${POSTGRES_RUNTIME_RUNNING:-true}" \
        FREE_KIB="$FREE_KIB" \
        PATH="$tmpdir:$PATH" \
        "$preflight" "$@"
}

: >"$tmpdir/docker.log"
target_mount="type=bind\\,src=$tmpdir/target\\,dst=/target\\,readonly"
fingerprint=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
DOCKER_PROBE="source 1000 0 1000000000000000000 $fingerprint"
FREE_KIB=1100
export DOCKER_PROBE FREE_KIB
run_preflight --source-container postgres-v4 --target-dir "$tmpdir/target" \
    >"$tmpdir/pass.stdout"
grep -q 'required free: 1100 KiB' "$tmpdir/pass.stdout" || fail \
    "successful preflight did not report its calculation"
grep -q -- '--volumes-from postgres-v4:ro' "$tmpdir/docker.log" || fail \
    "container source was not mounted read-only"
grep -Fq -- "$target_mount" \
    "$tmpdir/docker.log" || fail \
    "target directory was not mounted for the container-source probe"
grep -q 'exec postgres-v4 /bin/sh -ec' "$tmpdir/docker.log" || fail \
    "live container source was not checked for a devshard schema"

: >"$tmpdir/docker.log"
DOCKER_PROBE="target-ready none 1000000000000000000 none"
# This is serialized Docker inspect output, not shell syntax.
# shellcheck disable=SC2089
POSTGRES_RUNTIME_ENV='["PGDATA=/var/lib/postgresql/gonka/data"]'
printf '1000000000000000000\n' >"$tmpdir/target/.gonka-cluster-lineage"
# shellcheck disable=SC2090
export DOCKER_PROBE POSTGRES_RUNTIME_ENV
run_preflight --source-container postgres-v5 --target-dir "$tmpdir/target" \
    >"$tmpdir/v5-day2.stdout"
unset POSTGRES_RUNTIME_ENV
grep -q 'no migration copy is required' "$tmpdir/v5-day2.stdout" || fail \
    "existing v5 PGDATA with an empty legacy volume failed day-2 preflight"
if grep -q 'exec postgres-v5 /bin/sh -ec' "$tmpdir/docker.log"; then
    fail "day-2 v5 container was queried as a live legacy schema source"
fi
DOCKER_PROBE="source 1000 0 1000000000000000000 $fingerprint"
export DOCKER_PROBE

: >"$tmpdir/docker.log"
POSTGRES_RUNTIME_RUNNING=false
export POSTGRES_RUNTIME_RUNNING
run_preflight --source-container postgres-v4 --target-dir "$tmpdir/target" \
    >/dev/null
unset POSTGRES_RUNTIME_RUNNING
if grep -q 'exec postgres-v4 /bin/sh -ec' "$tmpdir/docker.log"; then
    fail "stopped source container was treated as a live schema endpoint"
fi

DEVSHARD_SCHEMA_STATE=f
export DEVSHARD_SCHEMA_STATE
if run_preflight --source-container postgres-v4 --target-dir "$tmpdir/target" \
    >"$tmpdir/fresh-source.stdout" 2>"$tmpdir/fresh-source.stderr"; then
    fail "fresh PostgreSQL source without a devshard schema passed preflight"
fi
unset DEVSHARD_SCHEMA_STATE
grep -q 'fresh anonymous volume may have replaced the original one' \
    "$tmpdir/fresh-source.stderr" || fail \
    "fresh anonymous source failure did not explain dangling-volume recovery"

if run_preflight --source-container postgres-v4 \
    --target-dir "$tmpdir/target,readonly" \
    >"$tmpdir/comma.stdout" 2>"$tmpdir/comma.stderr"; then
    fail "target path with mount-option separator passed preflight"
fi
grep -q 'must not contain a comma' "$tmpdir/comma.stderr" || fail \
    "unsafe target path failure was not explained"

FREE_KIB=1099
export FREE_KIB
if run_preflight --source-container postgres-v4 --target-dir "$tmpdir/target" \
    >"$tmpdir/fail.stdout" 2>"$tmpdir/fail.stderr"; then
    fail "insufficient free space passed preflight"
fi
grep -q 'not enough free space' "$tmpdir/fail.stderr" || fail \
    "insufficient-space error was not explained"

: >"$tmpdir/docker.log"
DOCKER_PROBE="source 2000 0 1000000000000000000 $fingerprint"
FREE_KIB=2200
export DOCKER_PROBE FREE_KIB
run_preflight --source-volume postgres-v4-volume \
    --target-dir "$tmpdir/target" >/dev/null
grep -q 'volume inspect postgres-v4-volume' "$tmpdir/docker.log" || fail \
    "volume existence was not checked"
grep -q 'src=postgres-v4-volume' "$tmpdir/docker.log" || fail \
    "selected volume was not mounted"
grep -q 'readonly' "$tmpdir/docker.log" || fail \
    "volume source was not mounted read-only"
grep -Fq -- "$target_mount" \
    "$tmpdir/docker.log" || fail \
    "target directory was not mounted for the volume-source probe"

DOCKER_PROBE="source 1000 200 1000000000000000000 $fingerprint"
FREE_KIB=900
export DOCKER_PROBE FREE_KIB
run_preflight --source-volume postgres-v4-volume \
    --target-dir "$tmpdir/target" >"$tmpdir/reclaim.stdout"
grep -q 'filesystem free: 900 KiB; reclaimable staging: 200 KiB; effective available: 1100 KiB' \
    "$tmpdir/reclaim.stdout" || fail \
    "partial staging was not counted as reclaimable space"

FREE_KIB=899
export FREE_KIB
if run_preflight --source-volume postgres-v4-volume \
    --target-dir "$tmpdir/target" \
    >"$tmpdir/reclaim-fail.stdout" 2>"$tmpdir/reclaim-fail.stderr"; then
    fail "insufficient effective space passed after staging accounting"
fi

printf '1000000000000000000\n' >"$tmpdir/target/.gonka-cluster-lineage"
printf '%s\n' "$fingerprint" >"$tmpdir/target/.gonka-v4-source-wal.sha256"
DOCKER_PROBE="target-ready 1000000000000000000 1000000000000000000 $fingerprint"
FREE_KIB=0
export DOCKER_PROBE FREE_KIB
run_preflight --source-volume postgres-v4-volume --target-dir "$tmpdir/target" \
    >"$tmpdir/target.stdout"
grep -q 'no migration copy is required' "$tmpdir/target.stdout" || fail \
    "existing persistent PGDATA was not recognized"

DOCKER_PROBE='target-ready 1000000000000000000 1000000000000000000 bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb'
export DOCKER_PROBE
if run_preflight --source-volume postgres-v4-volume --target-dir "$tmpdir/target" \
    >"$tmpdir/stale.stdout" 2>"$tmpdir/stale.stderr"; then
    fail "target older than its rollback volume passed preflight"
fi
grep -q 'source changed after migration' "$tmpdir/stale.stderr" || fail \
    "stale target failure did not explain the data-loss risk"

DOCKER_PROBE="source 1000 0 1000000000000000000 $fingerprint"
export DOCKER_PROBE
if run_preflight --source-volume unproved-volume --target-dir "$tmpdir/target" \
    >"$tmpdir/no-schema.stdout" 2>"$tmpdir/no-schema.stderr"; then
    fail "offline volume without devshard schema evidence passed preflight"
fi
grep -q 'no prior live schema proof\|does not match its durable live schema proof' "$tmpdir/no-schema.stderr" || fail \
    "unsafe offline volume failure did not explain the schema guard"

DOCKER_PROBE="target-ready 1000000000000000000 1000000000000000000 $fingerprint"
export DOCKER_PROBE
run_preflight --source-volume postgres-v4-volume --target-dir "$tmpdir/target" \
    >"$tmpdir/published-before-marker.stdout"
grep -q 'no migration copy is required' \
    "$tmpdir/published-before-marker.stdout" || fail \
    "published target was not recovered through its still-available source"

printf '1000000000000000000\n' >"$tmpdir/target/.gonka-copy-complete"
DOCKER_PROBE="staging-ready 1000000000000000000 1000000000000000000 $fingerprint"
export DOCKER_PROBE
run_preflight --source-volume postgres-v4-volume --target-dir "$tmpdir/target" \
    >"$tmpdir/staging.stdout"
grep -q 'staging is complete' "$tmpdir/staging.stdout" || fail \
    "committed migration staging was not recognized"

DOCKER_PROBE="target-ready 1000000000000000000 2000000000000000000 $fingerprint"
export DOCKER_PROBE
if run_preflight --source-volume postgres-v4-volume \
    --target-dir "$tmpdir/target" \
    >"$tmpdir/foreign-target.stdout" 2>"$tmpdir/foreign-target.stderr"; then
    fail "persistent target from another PostgreSQL source passed preflight"
fi
grep -q 'does not originate from the selected v4 source' \
    "$tmpdir/foreign-target.stderr" || fail \
    "foreign persistent target failure did not explain the lineage mismatch"

DOCKER_PROBE='target-ready none 1000000000000000000 none'
export DOCKER_PROBE
run_preflight --target-dir "$tmpdir/target" >"$tmpdir/target-only.stdout"
grep -q 'no migration copy is required' "$tmpdir/target-only.stdout" || fail \
    "durably bound persistent target was not accepted without a legacy container"

DOCKER_PROBE='source-missing'
export DOCKER_PROBE
if run_preflight --source-volume empty-volume --target-dir "$tmpdir/target" \
    >"$tmpdir/missing.stdout" 2>"$tmpdir/missing.stderr"; then
    fail "a source without PG_VERSION passed preflight"
fi
grep -q 'has no PostgreSQL PG_VERSION' "$tmpdir/missing.stderr" || fail \
    "missing-cluster error was not explained"

echo "devshard-postgres-migration-preflight_test: ok"
