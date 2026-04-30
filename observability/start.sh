#!/bin/sh
set -e

CHAIN_NODE="${CHAIN_NODE:-node}"

echo "Starting observability stack (prometheus + grafana)..."
echo "  Target node: ${CHAIN_NODE}"

CHAIN_NODE="${CHAIN_NODE}" docker compose up -d

echo ""
echo "Grafana:    http://localhost:3000  (admin/admin)"
echo "Prometheus: http://localhost:9099"
