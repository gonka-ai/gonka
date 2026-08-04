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
    inspect) exit 0 ;;
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
        FREE_KIB="$FREE_KIB" \
        PATH="$tmpdir:$PATH" \
        "$preflight" "$@"
}

: >"$tmpdir/docker.log"
DOCKER_PROBE='source 1000'
FREE_KIB=1100
export DOCKER_PROBE FREE_KIB
run_preflight --source-container postgres-v4 --target-dir "$tmpdir/target" \
    >"$tmpdir/pass.stdout"
grep -q 'required free: 1100 KiB' "$tmpdir/pass.stdout" || fail \
    "successful preflight did not report its calculation"
grep -q -- '--volumes-from postgres-v4:ro' "$tmpdir/docker.log" || fail \
    "container source was not mounted read-only"

FREE_KIB=1099
export FREE_KIB
if run_preflight --source-container postgres-v4 --target-dir "$tmpdir/target" \
    >"$tmpdir/fail.stdout" 2>"$tmpdir/fail.stderr"; then
    fail "insufficient free space passed preflight"
fi
grep -q 'not enough free space' "$tmpdir/fail.stderr" || fail \
    "insufficient-space error was not explained"

: >"$tmpdir/docker.log"
DOCKER_PROBE='source 2000'
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

DOCKER_PROBE='target-ready'
FREE_KIB=0
export DOCKER_PROBE FREE_KIB
run_preflight --source-container postgres-v5 --target-dir "$tmpdir/target" \
    >"$tmpdir/target.stdout"
grep -q 'no migration copy is required' "$tmpdir/target.stdout" || fail \
    "existing persistent PGDATA was not recognized"

DOCKER_PROBE='staging-ready'
export DOCKER_PROBE
run_preflight --source-container postgres-v5 --target-dir "$tmpdir/target" \
    >"$tmpdir/staging.stdout"
grep -q 'staging is complete' "$tmpdir/staging.stdout" || fail \
    "committed migration staging was not recognized"

DOCKER_PROBE='source-missing'
export DOCKER_PROBE
if run_preflight --source-volume empty-volume --target-dir "$tmpdir/target" \
    >"$tmpdir/missing.stdout" 2>"$tmpdir/missing.stderr"; then
    fail "a source without PG_VERSION passed preflight"
fi
grep -q 'has no PostgreSQL PG_VERSION' "$tmpdir/missing.stderr" || fail \
    "missing-cluster error was not explained"

echo "devshard-postgres-migration-preflight_test: ok"
