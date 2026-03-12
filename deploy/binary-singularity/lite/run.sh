#!/usr/bin/env bash
# Binary Singularity — LITE deploy & run
#
# All settings can be set via environment variables (read from .env if present)
# or overridden via CLI flags. Binary input is fully controlled from env:
#   BS_RAW_INPUT=/path/to/file ./run.sh
#   ./run.sh --raw-input /path/to/file --iterations 36 --hub-key gnk_live_...
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$SCRIPT_DIR"

# Load .env if present
[[ -f .env ]] && set -o allexport && source .env && set +o allexport

# Defaults (can be overridden by .env or CLI)
RAW_INPUT="${BS_RAW_INPUT:-}"
CHUNK_LINES="${BS_CHUNK_LINES:-50}"
ITERATIONS="${ITERATIONS:-12}"
HUB_KEY="${HUB_KEY:-}"
MIN_SIM_BPS="${BS_MIN_SIM_BPS:-7500}"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --raw-input) RAW_INPUT="$2"; shift 2 ;;
    --chunk-lines) CHUNK_LINES="$2"; shift 2 ;;
    --iterations) ITERATIONS="$2"; shift 2 ;;
    --hub-key) HUB_KEY="$2"; shift 2 ;;
    --min-sim-bps) MIN_SIM_BPS="$2"; shift 2 ;;
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
if [[ -n "$RAW_INPUT" ]]; then
  [[ -f "$RAW_INPUT" ]] || { echo "ERROR: BS_RAW_INPUT file not found: $RAW_INPUT"; exit 1; }
  EXTRA_ARGS="--raw-input $RAW_INPUT --chunk-lines $CHUNK_LINES"
  echo "▶ Raw binary input: $RAW_INPUT ($CHUNK_LINES lines/chunk)"
fi

echo "▶ Running experiment (iterations=$ITERATIONS, min_sim_bps=$MIN_SIM_BPS)..."
docker compose run --rm \
  -e ITERATIONS="$ITERATIONS" \
  -e HUB_KEY="$HUB_KEY" \
  -e BS_MIN_SIM_BPS="$MIN_SIM_BPS" \
  $( [[ -n "$RAW_INPUT" ]] && echo "-v ${RAW_INPUT}:${RAW_INPUT}:ro" ) \
  runner \
  ./runner \
    --matrix /scenarios/scenario_matrix.json \
    --iterations "$ITERATIONS" \
    --models "${MODELS:-mock}" \
    --embed http://embedder:8686 \
    --dapi http://mock-node:8082 \
    --store /data/slots \
    --output /results \
    --hub-url "${HUB_URL:-https://gonka.gg/api/public/stats/historical}" \
    --hub-key "$HUB_KEY" \
    $EXTRA_ARGS

echo "▶ Results written to: $SCRIPT_DIR/results/"
echo "▶ Key artifacts:"
ls "$SCRIPT_DIR/results/" 2>/dev/null || echo "  (check docker volume)"

docker compose stop
echo "✓ LITE experiment complete."
