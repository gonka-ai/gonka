#!/usr/bin/env bash
# Tear down local-test-net compose projects. Run from local-test-net/ or via stop-rebuild.sh.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

docker compose -p genesis down -v || true
docker compose -p join1 down -v || true
docker compose -p join2 down -v || true
docker compose -p join3 down -v || true
docker compose -p join4 down -v || true

# Long-lived shared CoreDNS (compose project testdns) — not owned by genesis/join.
docker compose -p testdns \
  -f "${SCRIPT_DIR}/docker-compose.dns.yml" \
  --project-directory "${REPO_ROOT}" \
  down -v || true

docker network rm chain-public 2>/dev/null || true
