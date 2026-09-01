#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

export DEVSHARD_POSTGRES_PASSWORD=test
export VERSIOND_ROUTER_SLOT=0
export VERSIOND_ROUTER_METRICS_NETWORK=join_default

docker compose --project-directory "$script_dir" \
    -f "$script_dir/docker-compose.yml" \
    -f "$script_dir/docker-compose.versiond.yml" \
    config --format json >"$tmpdir/main.json"
VERSIOND_REPLICAS=1 VERSIOND2_REPLICAS=0 \
    docker compose --project-directory "$script_dir" \
    -f "$script_dir/docker-compose.yml" \
    -f "$script_dir/docker-compose.versiond.yml" \
    config --format json >"$tmpdir/main-evacuated.json"
docker compose --project-directory "$script_dir" \
    --project-name gonka-versiond-router-test \
    -f "$script_dir/versiond-router-slot/docker-compose.yml" \
    config --format json >"$tmpdir/slot.json"
VERSIOND_ROUTER_IMAGE=example.invalid/versiond-router:test \
VERSIOND_POOL_HOST=custom-versiond-pool \
VERSIOND_ROUTING_CATALOG_URL=http://catalog.example.test/versions \
    docker compose --project-directory "$script_dir" \
    --project-name gonka-versiond-router-test \
    -f "$script_dir/versiond-router-slot/docker-compose.yml" \
    config --format json >"$tmpdir/slot-overrides.json"

jq -e '
  (.services | has("versiond-router") | not) and
  (.services.proxy.environment.VERSIOND_ROUTER_POOL_HOST == "versiond-router-fleet") and
  (.services.proxy.environment.VERSIOND_ROUTER_FLEET_CAPACITY == "16") and
  (.services.proxy.environment.PROXY_ROUTER_ACTIVATION_MIN_READY == "2") and
  (.services.proxy.environment.VERSIOND_ROUTING_CATALOG_URL ==
    "http://api:9100/versions") and
  (.services.proxy.networks | has("versiond-router-front")) and
  (.services.proxy.networks | has("versiond-router-back")) and
	(.services.versiond.networks["versiond-router-back"].aliases | index("versiond-pool")) and
	(.services.versiond.deploy.replicas == 1) and
	(.services.versiond2.deploy.replicas == 1) and
	(.services["devshard-postgres"].healthcheck.test[1] | contains("psql -h 127.0.0.1")) and
	(.services["devshard-postgres"].healthcheck.test[1] | contains("SELECT 1")) and
	(.services["devshard-postgres"].healthcheck.test[1] | contains("kill -TERM 1") | not) and
	(.services["devshard-postgres"].healthcheck.test[1] | contains("pg_isready") | not) and
	(.networks["versiond-router-front"].external == true) and
  (.networks["versiond-router-back"].external == true)
' "$tmpdir/main.json" >/dev/null

jq -e '
  (.services.versiond.deploy.replicas == 1) and
  (.services.versiond2.deploy.replicas == 0)
' "$tmpdir/main-evacuated.json" >/dev/null

jq -e '
  (.services.router.labels["ai.gonka.component"] == "versiond-router") and
  (.services.router.environment.VERSIOND_POOL_HOST == "versiond-pool") and
  (.services.router.networks.front.aliases | index("versiond-router-fleet")) and
  (.services.router.environment.VERSIOND_ROUTING_CATALOG_URL ==
    "http://versiond-routing-oracle:9100/versions") and
  (.services.router.networks.metrics.aliases | index("versiond-router-metrics")) and
  (.services.router.stop_signal == "SIGUSR1") and
  (.services.router.volumes | length == 1) and
  (.networks.front.external == true) and
  (.networks.back.external == true) and
  (.networks.metrics.external == true)
' "$tmpdir/slot.json" >/dev/null

jq -e '
  (.services.router.image == "example.invalid/versiond-router:test") and
  (.services.router.environment.VERSIOND_POOL_HOST == "custom-versiond-pool") and
  (.services.router.environment.VERSIOND_ROUTING_CATALOG_URL ==
    "http://catalog.example.test/versions")
' "$tmpdir/slot-overrides.json" >/dev/null

router_scrape=$(awk '
  /job_name: versiond-router/ { capture = 1 }
  capture { print }
  capture && /refresh_interval:/ { exit }
' "$script_dir/observability/prometheus.yml")
grep -q -- '- versiond-router-metrics' <<<"$router_scrape"
grep -q 'port: 8405' <<<"$router_scrape"

echo "versiond-compose-config_test: ok"
