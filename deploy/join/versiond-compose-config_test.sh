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
docker compose --project-directory "$script_dir" \
    --project-name gonka-versiond-router-test \
    -f "$script_dir/versiond-router-slot/docker-compose.yml" \
    config --format json >"$tmpdir/slot.json"

jq -e '
  (.services | has("versiond-router") | not) and
  (.services.proxy.environment.VERSIOND_ROUTER_POOL_HOST == "versiond-router-fleet") and
  (.services.proxy.networks | has("versiond-router-front")) and
  (.services.proxy.networks | has("versiond-router-back")) and
  (.services.versiond.networks["versiond-router-back"].aliases | index("versiond-pool")) and
  (.networks["versiond-router-front"].external == true) and
  (.networks["versiond-router-back"].external == true)
' "$tmpdir/main.json" >/dev/null

jq -e '
  (.services.router.labels["ai.gonka.component"] == "versiond-router") and
  (.services.router.networks.front.aliases | index("versiond-router-fleet")) and
  (.services.router.environment.VERSIOND_ROUTING_CATALOG_URL ==
    "http://versiond-routing-oracle:9100/versions") and
  (.services.router.stop_signal == "SIGUSR1") and
  (.services.router.volumes | length == 1) and
  (.networks.front.external == true) and
  (.networks.back.external == true) and
  (.networks.metrics.external == true)
' "$tmpdir/slot.json" >/dev/null

echo "versiond-compose-config_test: ok"
