#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
preflight="$script_dir/postgres-deployment-preflight.sh"
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

fail() {
    echo "postgres-deployment-preflight_test: $*" >&2
    exit 1
}

write_config() {
    local extra=${1:-}
    cat >"$tmpdir/config.json" <<EOF
{"services":{"versiond":{"environment":{"DEVSHARD_STORAGE_MODE":"postgres","PGHOST":"pg","PGPORT":"5432","PGDATABASE":"devshardd","PGUSER":"user"$extra}},"versiond2":{"environment":{"DEVSHARD_STORAGE_MODE":"postgres","PGHOST":"pg","PGPORT":"5432","PGDATABASE":"devshardd","PGUSER":"user"$extra}}}}
EOF
}

cat >"$tmpdir/docker" <<'EOF'
#!/usr/bin/env bash
set -Eeuo pipefail
printf '%q ' "$@" >>"$DOCKER_LOG"
printf '\n' >>"$DOCKER_LOG"
if [[ $1 == compose && ${*: -3} == "config --format json" ]]; then
    cat "$CONFIG_JSON"
elif [[ $1 == compose && ${*: -3:1} == ps && ${*: -2:1} == -q ]]; then
    if [[ ${NO_LIVE:-false} != true ]]; then
        [[ ${*: -1} == versiond ]] && printf 'container-1\n' || printf 'container-2\n'
    fi
elif [[ $1 == inspect ]]; then
    printf '%s\n' \
        DEVSHARD_STORAGE_MODE=postgres PGHOST=pg PGPORT=5432 \
        PGDATABASE=devshardd PGUSER=user "${RUNTIME_EXTRA:-}"
elif [[ $1 == exec ]]; then
    identity=$IDENTITY_ONE
    [[ $2 == container-1 ]] || identity=$IDENTITY_TWO
    printf '{"identity":"%s"}\n' "$identity"
else
    exit 1
fi
EOF
chmod +x "$tmpdir/docker"

run_preflight() {
    : >"$tmpdir/docker.log"
    DOCKER_BIN="$tmpdir/docker" DOCKER_LOG="$tmpdir/docker.log" \
        CONFIG_JSON="$tmpdir/config.json" IDENTITY_ONE="${IDENTITY_ONE:-db-1}" \
        IDENTITY_TWO="${IDENTITY_TWO:-db-1}" RUNTIME_EXTRA="${RUNTIME_EXTRA:-}" \
        "$preflight" "$@" -- -f base.yml -f ha.yml
}

write_config
run_preflight --expected-identity db-1 >"$tmpdir/ok"
grep -qx db-1 "$tmpdir/ok" || fail "matching identity was not returned"
! grep -Eq '(^| )((up|start|stop|rm|down|create|update))( |$)' "$tmpdir/docker.log" ||
    fail "preflight attempted a deployment mutation"

write_config ',"PGOPTIONS":"-c search_path=other"'
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "rendered PGOPTIONS bypass was accepted"
fi
grep -q PGOPTIONS "$tmpdir/err" || fail "PGOPTIONS failure was not diagnosed"

write_config
RUNTIME_EXTRA='PGOPTIONS=-c search_path=other'
export RUNTIME_EXTRA
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "running PGOPTIONS bypass was accepted"
fi
unset RUNTIME_EXTRA
grep -q PGOPTIONS "$tmpdir/err" || fail "runtime PGOPTIONS failure was not diagnosed"

IDENTITY_TWO=db-2
export IDENTITY_TWO
if run_preflight >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "different live database identities were accepted"
fi
unset IDENTITY_TWO
grep -q 'different PostgreSQL databases' "$tmpdir/err" ||
    fail "identity mismatch was not diagnosed"

NO_LIVE=true
export NO_LIVE
run_preflight >"$tmpdir/no-live"
grep -q 'no live replicas to compare' "$tmpdir/no-live" ||
    fail "read-only rendered-contract check rejected an absent deployment"
if run_preflight --require-live >"$tmpdir/out" 2>"$tmpdir/err"; then
    fail "live deployment gate accepted an absent deployment"
fi
unset NO_LIVE
grep -q 'no live versiond replicas' "$tmpdir/err" ||
    fail "missing live replicas were not diagnosed"

echo "postgres-deployment-preflight_test: ok"
