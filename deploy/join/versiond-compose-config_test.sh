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
        config --format json >"$tmpdir/observability-rollback-base.json"
# enable-router-ha derives operator-owned environment additions from the
# effective model before freezing the immutable v4 rollback generation.
jq --slurpfile current "$tmpdir/observability.json" '
    .services.proxy.environment += (
        $current[0].services.proxy.environment
        | with_entries(select(.key | test("^(JAEGER_|GRAFANA_)")))
    )
    | del(.services.proxy.environment.VERSIOND_SERVICE_NAME)
    | del(.services.proxy.environment.VERSIOND_PORT)
' "$tmpdir/observability-rollback-base.json" \
    >"$tmpdir/observability-rollback.json"

cat >"$tmpdir/managed-postgres.yml" <<'EOF'
services:
  versiond:
    environment:
      PGHOST: managed-postgres.internal
    depends_on:
      custom-secret:
        condition: service_started
  versiond2:
    environment:
      PGHOST: managed-postgres.internal
    depends_on:
      custom-secret:
        condition: service_started
  custom-secret:
    image: alpine:3.21
EOF
env DEVSHARD_POSTGRES_PASSWORD=test \
    "${compose[@]}" -f "$tmpdir/managed-postgres.yml" config --format json \
    >"$tmpdir/managed-postgres.json"
env DEVSHARD_POSTGRES_PASSWORD=test \
    "${compose[@]}" -f "$tmpdir/managed-postgres.yml" \
    -f "$script_dir/docker-compose.versiond-external-postgres.yml" \
    config --format json >"$tmpdir/managed-postgres-external.json"

python3 - "$tmpdir/defaults.json" "$tmpdir/cleared.json" \
    "$tmpdir/slot-defaults.json" "$tmpdir/slot-cleared.json" \
    "$tmpdir/observability.json" "$tmpdir/managed-postgres.json" \
    "$tmpdir/observability-rollback.json" \
    "$tmpdir/managed-postgres-external.json" <<'PY'
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
managed_postgres_external = load(sys.argv[8])["services"]

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
policy2 = defaults_config["services"]["proxy-policy2"]
api = defaults_config["services"]["api"]
if "versiond-router-front" not in proxy["networks"]:
    raise SystemExit("public HAProxy is not attached to the router front network")
if "proxy-router-metrics" not in proxy["networks"]["default"].get("aliases", []):
    raise SystemExit("public HAProxy has no internal Prometheus alias")
for service, worker in (("proxy-policy", policy), ("proxy-policy2", policy2)):
    if worker["environment"].get("VERSIOND_SERVICE_NAME") != "proxy-policy-ingress":
        raise SystemExit(f"{service} does not use the public HAProxy distributor")
    if worker["environment"].get("VERSIOND_SERVICE_IS_ABSOLUTE") != "true":
        raise SystemExit(f"{service} can still receive KEY_NAME prefixing")
    if worker["environment"].get("PROXY_PROTOCOL_PEER") != "proxy-policy-ingress":
        raise SystemExit(f"{service} does not derive trust from the private ingress peer")
if "deploy" in policy and policy["deploy"].get("replicas", 1) != 1:
    raise SystemExit("proxy-policy remains a scaled all-at-once service")
if policy.get("depends_on", {}).get("proxy-policy2", {}).get("condition") != "service_healthy":
    raise SystemExit("ordinary Compose updates do not reconcile the reserve policy slot first")
for service in ("proxy-policy", "proxy-policy2"):
    if proxy.get("depends_on", {}).get(service, {}).get("condition") != "service_healthy":
        raise SystemExit(f"public proxy can start before {service} is healthy")
if "proxy-policy-front" not in policy["networks"] or "proxy-policy-front" not in proxy["networks"]:
    raise SystemExit("public HAProxy and policy workers do not share the isolated front network")
for network in ("versiond-router-front", "versiond-router-back"):
    if not defaults_config["networks"][network].get("external"):
        raise SystemExit(f"router fleet network {network} is still owned by main Compose")
if "versiond-router-back" in api.get("networks", {}):
    raise SystemExit("dapi mutating listener is exposed to the router back network")
if "versiond-router-back" not in proxy["networks"]:
    raise SystemExit("read-only catalog bridge is not attached to the router back network")
if "versiond-routing-oracle" not in proxy["networks"]["versiond-router-back"].get("aliases", []):
    raise SystemExit("read-only catalog bridge has no stable router-back alias")

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
require(defaults, "PROXY_ROUTER_CATALOG_BIND_HOST", "versiond-routing-oracle", "catalog bridge bind")
require(defaults, "PROXY_ROUTER_CATALOG_UPSTREAM_HOST", "api", "catalog bridge upstream")
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
    "VERSIOND_ROUTING_ACTIVATION_MIN_READY",
    "2",
    "parent activation reserve",
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
require(
    slot_defaults,
    "VERSIOND_ROUTING_ACTIVATION_MIN_READY",
    "2",
    "slot activation reserve",
)
require(slot_cleared, "VERSIOND_NON_HA_VERSIONS", "", "slot explicit empty")
require(slot_cleared, "VERSIOND_VERSIONS", "", "slot explicit empty")
require(slot_cleared, "VERSIOND_ROUTER_ALLOW_COARSE_READINESS", "true", "slot coarse")

for service in ("proxy", "proxy-policy", "proxy-policy2"):
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

if observability_rollback["networks"] != {"default": None}:
    raise SystemExit(
        f"v4 rollback retained v5 proxy networks: {observability_rollback['networks']!r}"
    )
if {volume["target"] for volume in observability_rollback["volumes"]} != {
    "/etc/nginx/ssl"
}:
    raise SystemExit("v4 rollback retained the HAProxy state volume")
for field in ("cap_add", "cap_drop", "security_opt"):
    if field in observability_rollback:
        raise SystemExit(f"v4 rollback retained HAProxy-only {field}")
if set(observability_rollback["depends_on"]) != {
    "api",
    "edge-api",
    "explorer",
    "node",
    "proxy-ssl",
    "versiond",
}:
    raise SystemExit("v4 rollback retained the v5 policy-worker dependency graph")
if observability_rollback["healthcheck"] != {
    "test": ["CMD", "curl", "-f", "http://localhost/health"],
    "timeout": "10s",
    "interval": "30s",
    "retries": 3,
    "start_period": "10s",
}:
    raise SystemExit("v4 rollback does not restore the old nginx health contract")
for key in (
    "HAPROXY_DNS_RESOLVER",
    "PROXY_POLICY_POOL_HOST",
    "PROXY_ROUTER_POLICY_BIND_HOST",
    "PROXY_ROUTER_VERSION_CAPACITY",
    "VERSIOND_ROUTER_POOL_HOST",
    "VERSIOND_ROUTING_CATALOG_URL",
    "VERSIOND_SERVICE_NAME",
    "VERSIOND_PORT",
):
    if key in observability_rollback["environment"]:
        raise SystemExit(f"v4 single-router rollback retained v5 environment {key}")
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
    postgres_dependency = managed_postgres_external[service].get(
        "depends_on", {}
    ).get("devshard-postgres")
    if postgres_dependency is not None and postgres_dependency.get(
        "required", True
    ):
        raise SystemExit(f"{service} still requires local PostgreSQL")
    if "custom-secret" not in managed_postgres_external[service].get(
        "depends_on", {}
    ):
        raise SystemExit(f"{service} lost an operator-owned dependency")

if "devshard-postgres" in managed_postgres_external:
    raise SystemExit("external PostgreSQL topology still starts local PostgreSQL")
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
