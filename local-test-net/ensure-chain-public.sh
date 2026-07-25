#!/usr/bin/env bash
# Create chain-public if missing. Compose files mark it external so Testermint /
# CoreDNS can own IPAM (DHCP pool excludes static test-dns=172.25.0.10).
# Keep in sync with testermint ensureChainPublicNetwork().
set -euo pipefail

if docker network inspect chain-public >/dev/null 2>&1; then
  exit 0
fi

echo "Creating chain-public network (IPAM reserves 172.25.0.0–127 for static addrs)"
docker network create \
  --driver bridge \
  --subnet 172.25.0.0/16 \
  --gateway 172.25.0.1 \
  --ip-range 172.25.128.0/17 \
  chain-public
