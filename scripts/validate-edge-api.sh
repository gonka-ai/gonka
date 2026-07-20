#!/usr/bin/env bash
# edge-api validation: unit tests, compose render, optional live smoke.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"

echo "==> edge-api unit tests (queryapi + contract + proof roundtrip)"
(cd "${REPO_ROOT}/edge-api" && go test ./queryapi/... -count=1)

echo "==> common observability + chain (edge-api transport deps)"
(cd "${REPO_ROOT}/common" && go test ./observability/... ./chain/... -count=1)

echo "==> docker compose render (local-test-net base + genesis)"
KEY_NAME=genesis EDGE_API_BUILD_CONTEXT=. docker compose --project-directory "${REPO_ROOT}" \
  -f local-test-net/docker-compose-base.yml \
  -f local-test-net/docker-compose.genesis.yml \
  config --quiet

echo "==> docker compose render (local-test-net multi edge-api + router)"
KEY_NAME=genesis EDGE_API_BUILD_CONTEXT=. docker compose --project-directory "${REPO_ROOT}" \
  -f local-test-net/docker-compose-base.yml \
  -f local-test-net/docker-compose.genesis.yml \
  -f local-test-net/docker-compose.edge-api.yml \
  -f local-test-net/docker-compose.edge-api-router-proxy.yml \
  config --quiet

echo "==> docker compose render (deploy/join + multi edge-api)"
docker compose -f "${REPO_ROOT}/deploy/join/docker-compose.yml" config --quiet
docker compose \
  -f "${REPO_ROOT}/deploy/join/docker-compose.yml" \
  -f "${REPO_ROOT}/deploy/join/docker-compose.edge-api-multi.yml" \
  config --quiet

echo "==> docker compose render (deploy/join + multi versiond)"
# Overlay requires DEVSHARD_POSTGRES_PASSWORD (no default); dummy is enough to validate render.
DEVSHARD_POSTGRES_PASSWORD=validate-only docker compose \
  -f "${REPO_ROOT}/deploy/join/docker-compose.yml" \
  -f "${REPO_ROOT}/deploy/join/docker-compose.versiond.yml" \
  config --quiet

if [[ -n "${PROXY_URL:-}" ]]; then
  echo "==> live proxy smoke (${PROXY_URL})"
  curl -fsS "${PROXY_URL}/v1/status" | grep -q '"ok"'
  curl -fsS "${PROXY_URL}/v1/models" | grep -q '"object"'
  epoch_code="$(curl -s -o /dev/null -w '%{http_code}' "${PROXY_URL}/v1/epochs/latest/participants")"
  echo "epoch-participants HTTP ${epoch_code}"
  if [[ "${epoch_code}" == "000" ]]; then
    echo "FATAL: /v1/epochs/latest/participants unreachable via proxy" >&2
    exit 1
  fi
  # Chat must still reach dapi (expect 4xx without body, not connection refused).
  code="$(curl -s -o /dev/null -w '%{http_code}' -X POST "${PROXY_URL}/v1/chat/completions" \
    -H 'Content-Type: application/json' -d '{}')"
  echo "chat/completions HTTP ${code}"
  if [[ "${code}" == "000" ]]; then
    echo "FATAL: /v1/chat/completions unreachable via proxy" >&2
    exit 1
  fi
else
  echo "==> skip live proxy smoke (set PROXY_URL=http://localhost to enable)"
fi

if [[ -n "${EDGE_API_URL:-}" && -n "${DAPI_URL:-}" ]]; then
  echo "==> live dapi vs edge-api compatibility (-tags compat)"
  (cd "${REPO_ROOT}/edge-api/queryapi/tests/compatibility" && go test -tags compat -count=1 -run TestCompatibility \
    -endpoint1="${EDGE_API_URL}" \
    -endpoint2="${DAPI_URL}")
else
  echo "==> skip live dapi vs edge-api compat (set EDGE_API_URL and DAPI_URL to enable)"
fi

echo "edge-api validation passed."
