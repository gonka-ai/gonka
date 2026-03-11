#!/usr/bin/env bash
# Binary Singularity — LITE deploy & run
# Usage: ./run.sh [--raw-input /path/to/file] [--iterations N] [--hub-key KEY]
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

RAW_INPUT=""
ITERATIONS="${ITERATIONS:-12}"
HUB_KEY="${HUB_KEY:-}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --raw-input) RAW_INPUT="$2"; shift 2 ;;
    --iterations) ITERATIONS="$2"; shift 2 ;;
    --hub-key) HUB_KEY="$2"; shift 2 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

[[ -f .env ]] || cp .env.example .env

echo "▶ Building LITE stack..."
docker compose build --parallel

echo "▶ Starting services..."
docker compose up -d embedder mock-node

echo "▶ Waiting for health checks..."
timeout 60 bash -c 'until docker compose ps --format json | grep -q "healthy"; do sleep 2; done'

mkdir -p results

EXTRA_ARGS=""
[[ -n "$RAW_INPUT" ]] && EXTRA_ARGS="--raw-input $RAW_INPUT --chunk-lines 50"

echo "▶ Running experiment (iterations=$ITERATIONS)..."
docker compose run --rm \
  -e ITERATIONS="$ITERATIONS" \
  -e HUB_KEY="$HUB_KEY" \
  $( [[ -n "$RAW_INPUT" ]] && echo "-v ${RAW_INPUT}:${RAW_INPUT}:ro" ) \
  runner \
  ./runner \
    --matrix /scenarios/scenario_matrix.json \
    --iterations "$ITERATIONS" \
    --models mock \
    --embed http://embedder:8686 \
    --dapi http://mock-node:8082 \
    --store /data/slots \
    --output /results \
    --hub-url https://gonka.gg/api/public/stats/historical \
    --hub-key "$HUB_KEY" \
    $EXTRA_ARGS

echo "▶ Results written to: $SCRIPT_DIR/results/"
echo "▶ Key artifacts:"
ls "$SCRIPT_DIR/results/" 2>/dev/null || echo "  (check docker volume)"

docker compose stop
echo "✓ LITE experiment complete."
