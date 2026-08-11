#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT
export VERSIOND_ROUTER_METRICS_NETWORK=join_default

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
observability_compose=(
    docker compose
    --project-directory "$script_dir"
    -f "$script_dir/docker-compose.yml"
    -f "$script_dir/docker-compose.observability.yml"
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
JAEGER_ENABLED=true JAEGER_BASIC_AUTH_USER=test \
JAEGER_BASIC_AUTH_PASSWORD=test-secret GRAFANA_ENABLED=true \
GRAFANA_ADMIN_PASSWORD=test-secret \
    "${observability_compose[@]}" config --format json \
    >"$tmpdir/observability.json"
JAEGER_ENABLED=true JAEGER_BASIC_AUTH_USER=test \
JAEGER_BASIC_AUTH_PASSWORD=test-secret GRAFANA_ENABLED=true \
GRAFANA_ADMIN_PASSWORD=test-secret PROXY_V4_IMAGE=test/proxy:v4 \
    "${observability_compose[@]}" \
        -f "$script_dir/docker-compose.proxy-v4-compat.yml" \
        config --format json >"$tmpdir/observability-rollback.json"

cat >"$tmpdir/managed-postgres.yml" <<'EOF'
services:
  versiond:
    environment:
      PGHOST: managed-postgres.internal
  versiond2:
    environment:
      PGHOST: managed-postgres.internal
EOF
env DEVSHARD_POSTGRES_PASSWORD=test \
    "${compose[@]}" -f "$tmpdir/managed-postgres.yml" config --format json \
    >"$tmpdir/managed-postgres.json"

python3 - "$tmpdir/defaults.json" "$tmpdir/cleared.json" \
    "$tmpdir/slot-defaults.json" "$tmpdir/slot-cleared.json" \
    "$tmpdir/observability.json" "$tmpdir/managed-postgres.json" \
    "$tmpdir/observability-rollback.json" <<'PY'
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
observability = load(sys.argv[5])["services"]
managed_postgres = load(sys.argv[6])["services"]
observability_rollback = load(sys.argv[7])["services"]["proxy"]

if slot_defaults_config["labels"].get("ai.gonka.fleet") != "gonka-versiond-router":
    raise SystemExit("router slot has no stable fleet ownership label")
if "versiond-router-fleet" not in slot_defaults_config["networks"]["front"].get(
    "aliases", []
):
    raise SystemExit("router slot does not publish the dedicated fleet DNS alias")
if "versiond-router" in slot_defaults_config["networks"]["front"].get("aliases", []):
    raise SystemExit("router slot still shares the migration singleton DNS alias")
if "versiond-router-test-front" not in slot_defaults_config["networks"]["front"].get(
    "aliases", []
):
    raise SystemExit("router slot has no unique data-plane bind alias")
if "versiond-router-metrics" not in slot_defaults_config["networks"]["metrics"].get(
    "aliases", []
):
    raise SystemExit("router slot has no shared Prometheus discovery alias")
if "versiond-router-test-metrics" not in slot_defaults_config["networks"][
    "metrics"
].get("aliases", []):
    raise SystemExit("router slot has no unique metrics bind alias")

if "versiond-router" in defaults_config["services"]:
    raise SystemExit("the main Compose project still owns a versiond-router replica")

proxy = defaults_config["services"]["proxy"]
policy = defaults_config["services"]["proxy-policy"]
api = defaults_config["services"]["api"]
if "versiond-router-front" not in proxy["networks"]:
    raise SystemExit("public HAProxy is not attached to the router front network")
if "proxy-router-metrics" not in proxy["networks"]["default"].get("aliases", []):
    raise SystemExit("public HAProxy has no internal Prometheus alias")
if policy["environment"].get("VERSIOND_SERVICE_NAME") != "proxy-policy-ingress":
    raise SystemExit("nginx policy workers do not use the public HAProxy distributor")
if policy["environment"].get("VERSIOND_SERVICE_IS_ABSOLUTE") != "true":
    raise SystemExit("the internal HAProxy service name can still receive KEY_NAME prefixing")
if policy["environment"].get("PROXY_PROTOCOL_PEER") != "proxy-policy-ingress":
    raise SystemExit("nginx policy workers do not derive trust from the private ingress peer")
if "proxy-policy-front" not in policy["networks"] or "proxy-policy-front" not in proxy["networks"]:
    raise SystemExit("public HAProxy and policy workers do not share the isolated front network")
for network in ("versiond-router-front", "versiond-router-back"):
    if not defaults_config["networks"][network].get("external"):
        raise SystemExit(f"router fleet network {network} is still owned by main Compose")
if "versiond-router-back" not in api["networks"]:
    raise SystemExit("dapi governance catalog is not reachable from the inner router fleet")
if "versiond-routing-oracle" not in api["networks"]["versiond-router-back"].get(
    "aliases", []
):
    raise SystemExit("dapi has no stable governance-catalog alias on the router back network")

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
require(
    defaults,
    "VERSIOND_ROUTING_CATALOG_URL",
    "http://api:9100/versions",
    "parent governance catalog",
)
require(
    defaults,
    "PROXY_ROUTER_METRICS_BIND_HOST",
    "proxy-router-metrics",
    "parent metrics bind",
)
require(defaults, "PROXY_ROUTER_VERSION_CAPACITY", "32", "parent dynamic capacity")
require(defaults, "HAPROXY_DNS_RESOLVER", "127.0.0.11:53", "parent DNS resolver")
require(defaults, "VERSIOND_ROUTING_CATALOG_POLL_SECONDS", "5", "parent catalog poll")
require(
    defaults,
    "VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS",
    "3",
    "parent catalog timeout",
)
require(
    defaults,
    "VERSIOND_ROUTER_POOL_HOST",
    "versiond-router-fleet",
    "steady-state fleet DNS",
)
require(cleared, "VERSIOND_NON_HA_VERSIONS", "", "explicit empty")
require(cleared, "VERSIOND_VERSIONS", "", "explicit empty")

require(slot_defaults, "VERSIOND_NON_HA_VERSIONS", "v1 v2 v3", "slot unset")
require(slot_defaults, "VERSIOND_VERSIONS", "v4 v5 v6 v7 v8", "slot unset")
require(slot_defaults, "VERSIOND_ROUTER_ALLOW_COARSE_READINESS", "false", "slot unset")
require(
    slot_defaults,
    "VERSIOND_ROUTING_CATALOG_URL",
    "http://versiond-routing-oracle:9100/versions",
    "slot governance catalog",
)
require(slot_defaults, "VERSIOND_ROUTER_VERSION_CAPACITY", "32", "slot dynamic capacity")
require(slot_defaults, "HAPROXY_DNS_RESOLVER", "127.0.0.11:53", "slot DNS resolver")
require(
    slot_defaults,
    "VERSIOND_ROUTER_FRONT_BIND_HOST",
    "versiond-router-test-front",
    "slot data-plane bind",
)
require(
    slot_defaults,
    "VERSIOND_ROUTER_METRICS_NETWORK_NAME",
    "join_default",
    "slot metrics network",
)
require(
    slot_defaults,
    "VERSIOND_ROUTER_METRICS_BIND_HOST",
    "versiond-router-test-metrics",
    "slot metrics bind",
)
require(slot_defaults, "VERSIOND_ROUTING_CATALOG_POLL_SECONDS", "5", "slot catalog poll")
require(
    slot_defaults,
    "VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS",
    "3",
    "slot catalog timeout",
)
require(slot_cleared, "VERSIOND_NON_HA_VERSIONS", "", "slot explicit empty")
require(slot_cleared, "VERSIOND_VERSIONS", "", "slot explicit empty")
require(slot_cleared, "VERSIOND_ROUTER_ALLOW_COARSE_READINESS", "true", "slot coarse")

for service in ("proxy", "proxy-policy"):
    environment = observability[service]["environment"]
    require(environment, "JAEGER_ENABLED", "true", f"{service} observability")
    require(environment, "GRAFANA_ENABLED", "true", f"{service} observability")
    require(
        environment,
        "JAEGER_BASIC_AUTH_PASSWORD",
        "test-secret",
        f"{service} observability",
    )

require(
    observability_rollback["environment"],
    "JAEGER_ENABLED",
    "true",
    "v4 proxy rollback observability",
)
require(
    observability_rollback["environment"],
    "GRAFANA_ENABLED",
    "true",
    "v4 proxy rollback observability",
)

for service in ("versiond", "versiond2"):
    require(
        managed_postgres[service]["environment"],
        "PGHOST",
        "managed-postgres.internal",
        f"{service} managed PostgreSQL override",
    )
PY

grep -q '^  - job_name: proxy-router$' "$script_dir/observability/prometheus.yml" || {
    echo "versiond-compose-config_test: Prometheus does not scrape proxy-router" >&2
    exit 1
}
grep -q '^  - job_name: versiond-router$' "$script_dir/observability/prometheus.yml" || {
    echo "versiond-compose-config_test: Prometheus does not discover router slots" >&2
    exit 1
}

echo "versiond-compose-config_test: ok"
