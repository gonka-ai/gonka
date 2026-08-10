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
slot_compose=(
    docker compose
    --project-directory "$script_dir"
    --project-name gonka-versiond-router-test
    -f "$script_dir/versiond-router-slot/docker-compose.yml"
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
VERSIOND_ROUTER_SLOT=test VERSIOND_NON_HA_VERSIONS='v1 v2 v3' \
    VERSIOND_VERSIONS='v4 v5 v6 v7 v8' \
    VERSIOND_ROUTER_ALLOW_COARSE_READINESS=false \
    "${slot_compose[@]}" config --format json >"$tmpdir/slot-defaults.json"
VERSIOND_ROUTER_SLOT=test VERSIOND_NON_HA_VERSIONS='' VERSIOND_VERSIONS='' \
    VERSIOND_ROUTER_ALLOW_COARSE_READINESS=true \
    "${slot_compose[@]}" config --format json >"$tmpdir/slot-cleared.json"

python3 - "$tmpdir/defaults.json" "$tmpdir/cleared.json" \
    "$tmpdir/slot-defaults.json" "$tmpdir/slot-cleared.json" <<'PY'
import json
import sys


def load(path):
    with open(path, encoding="utf-8") as config_file:
        return json.load(config_file)


defaults_config = load(sys.argv[1])
cleared_config = load(sys.argv[2])
slot_defaults_config = load(sys.argv[3])["services"]["router"]
slot_defaults = slot_defaults_config["environment"]
slot_cleared = load(sys.argv[4])["services"]["router"]["environment"]

if slot_defaults_config["labels"].get("ai.gonka.fleet") != "gonka-versiond-router":
    raise SystemExit("router slot has no stable fleet ownership label")

if "versiond-router" in defaults_config["services"]:
    raise SystemExit("the main Compose project still owns a versiond-router replica")

proxy = defaults_config["services"]["proxy"]
policy = defaults_config["services"]["proxy-policy"]
if "versiond-router-front" not in proxy["networks"]:
    raise SystemExit("public HAProxy is not attached to the router front network")
if policy["environment"].get("VERSIOND_SERVICE_NAME") != "proxy":
    raise SystemExit("nginx policy workers do not use the public HAProxy distributor")
if policy["environment"].get("VERSIOND_SERVICE_IS_ABSOLUTE") != "true":
    raise SystemExit("the internal HAProxy service name can still receive KEY_NAME prefixing")

defaults = proxy["environment"]
cleared = cleared_config["services"]["proxy"]["environment"]


def require(environment, key, expected, case):
    actual = environment.get(key)
    if actual != expected:
        raise SystemExit(
            f"{case}: {key}={actual!r}, want {expected!r}; environment={environment!r}"
        )


require(defaults, "VERSIOND_NON_HA_VERSIONS", "v1 v2 v3", "unset")
require(defaults, "VERSIOND_VERSIONS", "v4 v5 v6 v7 v8", "unset")
require(cleared, "VERSIOND_NON_HA_VERSIONS", "", "explicit empty")
require(cleared, "VERSIOND_VERSIONS", "", "explicit empty")

require(slot_defaults, "VERSIOND_NON_HA_VERSIONS", "v1 v2 v3", "slot unset")
require(slot_defaults, "VERSIOND_VERSIONS", "v4 v5 v6 v7 v8", "slot unset")
require(slot_defaults, "VERSIOND_ROUTER_ALLOW_COARSE_READINESS", "false", "slot unset")
require(slot_cleared, "VERSIOND_NON_HA_VERSIONS", "", "slot explicit empty")
require(slot_cleared, "VERSIOND_VERSIONS", "", "slot explicit empty")
require(slot_cleared, "VERSIOND_ROUTER_ALLOW_COARSE_READINESS", "true", "slot coarse")
PY

echo "versiond-compose-config_test: ok"
