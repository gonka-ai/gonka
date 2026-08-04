#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

compose=(
    docker compose
    --project-directory "$script_dir"
    -f "$script_dir/docker-compose.yml"
    -f "$script_dir/docker-compose.versiond.yml"
)

render_defaults() {
    unset VERSIOND_NON_HA_VERSIONS
    unset VERSIOND_VERSIONS
    unset VERSIOND_ROUTER_ALLOW_COARSE_READINESS
    export DEVSHARD_POSTGRES_PASSWORD=test
    "${compose[@]}" config --format json
}

render_cleared() {
    export DEVSHARD_POSTGRES_PASSWORD=test
    export VERSIOND_NON_HA_VERSIONS=
    export VERSIOND_VERSIONS=
    export VERSIOND_ROUTER_ALLOW_COARSE_READINESS=true
    "${compose[@]}" config --format json
}

if ! render_defaults >"$tmpdir/defaults.json" 2>"$tmpdir/defaults.stderr"; then
    cat "$tmpdir/defaults.stderr" >&2
    exit 1
fi
if ! render_cleared >"$tmpdir/cleared.json" 2>"$tmpdir/cleared.stderr"; then
    cat "$tmpdir/cleared.stderr" >&2
    exit 1
fi

python3 - "$tmpdir/defaults.json" "$tmpdir/cleared.json" <<'PY'
import json
import sys


def router_environment(path):
    with open(path, encoding="utf-8") as config_file:
        config = json.load(config_file)
    return config["services"]["versiond-router"]["environment"]


defaults = router_environment(sys.argv[1])
cleared = router_environment(sys.argv[2])


def require(environment, key, expected, case):
    actual = environment.get(key)
    if actual != expected:
        raise SystemExit(
            f"{case}: {key}={actual!r}, want {expected!r}; environment={environment!r}"
        )


require(defaults, "VERSIOND_NON_HA_VERSIONS", "v1 v2 v3", "unset")
require(defaults, "VERSIOND_VERSIONS", "v4 v5 v6 v7 v8", "unset")
require(defaults, "VERSIOND_ROUTER_ALLOW_COARSE_READINESS", "false", "unset")

require(cleared, "VERSIOND_NON_HA_VERSIONS", "", "explicit empty")
require(cleared, "VERSIOND_VERSIONS", "", "explicit empty")
require(cleared, "VERSIOND_ROUTER_ALLOW_COARSE_READINESS", "true", "coarse")
PY

echo "versiond-compose-config_test: ok"
