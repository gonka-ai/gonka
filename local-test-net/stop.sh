#!/usr/bin/env bash
set -euo pipefail

compose_down() {
  local project="$1"
  shift

  docker compose -p "${project}" "$@" down -v
}

compose_down genesis \
  -f docker-compose-base.yml \
  -f docker-compose.genesis.yml \
  -f docker-compose.dns.yml \
  -f docker-compose.dns-overrides.yml \
  -f docker-compose.postgres.yml

for project in join1 join2 join3 join4; do
  compose_down "${project}" \
    -f docker-compose-base.yml \
    -f docker-compose.join.yml \
    -f docker-compose.dns.yml \
    -f docker-compose.dns-overrides.yml
done
