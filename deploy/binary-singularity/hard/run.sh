#!/usr/bin/env bash
# Binary Singularity — HARD deploy (K3s mesh via k3d)
# Reproduces Exp 3 / Exp 4 topology: 1 server + 3 agents
#
# Usage:
#   ./run.sh                         # standard run
#   ./run.sh --raw-input /path/file  # Exp 4: raw binary ingestion
#   ./run.sh --hub-key KEY           # with hub approval check
#
# Requirements: Docker, k3d >= 5.x, kubectl, go >= 1.22
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$(dirname "$(dirname "$SCRIPT_DIR")")")"
BS_DIR="$ROOT_DIR/binary-singularity"

# Load .env if present
[[ -f .env ]] && set -o allexport && source .env && set +o allexport

RAW_INPUT="${BS_RAW_INPUT:-}"
CHUNK_LINES="${BS_CHUNK_LINES:-50}"
ITERATIONS="${ITERATIONS:-12}"
HUB_KEY="${HUB_KEY:-}"
MODELS="${MODELS:-mock}"
MIN_SIM_BPS="${BS_MIN_SIM_BPS:-7500}"
CLUSTER_NAME="bs-mesh"

while [[ $# -gt 0 ]]; do
  case "$1" in
    --raw-input) RAW_INPUT="$2"; shift 2 ;;
    --chunk-lines) CHUNK_LINES="$2"; shift 2 ;;
    --iterations) ITERATIONS="$2"; shift 2 ;;
    --hub-key) HUB_KEY="$2"; shift 2 ;;
    --models) MODELS="$2"; shift 2 ;;
    --min-sim-bps) MIN_SIM_BPS="$2"; shift 2 ;;
    --destroy) k3d cluster delete "$CLUSTER_NAME" 2>/dev/null; exit 0 ;;
    *) echo "Unknown arg: $1"; exit 1 ;;
  esac
done

log() { echo "▶ $*"; }

# ── 1. Build images ───────────────────────────────────────────────────────────
log "Building container images..."
docker build -t bs-embedder:lite -f "$BS_DIR/Dockerfile.embedder" "$BS_DIR"
docker build -t bs-mocknode:lite "$BS_DIR/mocknode"
docker build -t bs-runner:lite "$BS_DIR/scenarios/runner"

# ── 2. Create / reuse k3d cluster ────────────────────────────────────────────
if k3d cluster list | grep -q "$CLUSTER_NAME"; then
  log "Reusing existing cluster: $CLUSTER_NAME"
else
  log "Creating k3d cluster: $CLUSTER_NAME (1 server + 3 agents)..."
  k3d cluster create --config "$SCRIPT_DIR/k3d-cluster.yaml"
fi

# ── 3. Import images into cluster ────────────────────────────────────────────
log "Importing images into k3d cluster..."
k3d image import bs-embedder:lite bs-mocknode:lite -c "$CLUSTER_NAME"

# ── 4. Deploy manifests ───────────────────────────────────────────────────────
log "Applying Kubernetes manifests..."
kubectl apply -f "$SCRIPT_DIR/bs-mesh.yaml"
kubectl -n binary-singularity rollout status deployment/embedder --timeout=120s
kubectl -n binary-singularity rollout status deployment/mock-node --timeout=60s

# ── 5. Get NodePort endpoints ─────────────────────────────────────────────────
NODE_IP=$(k3d node list -o json | python3 -c "
import json,sys
nodes=json.load(sys.stdin)
for n in nodes:
    if 'server' in n.get('name','') or 'agent' in n.get('name',''):
        print('localhost'); break
" 2>/dev/null || echo "localhost")

EMBED_URL="http://${NODE_IP}:30686"
DAPI_URL="http://${NODE_IP}:30082"

log "Cluster endpoints: embedder=$EMBED_URL  mock-node=$DAPI_URL"

# ── 6. Build runner locally ───────────────────────────────────────────────────
log "Building scenario runner..."
cd "$BS_DIR/scenarios/runner"
go build -o runner . 2>&1
cd "$SCRIPT_DIR"

RUNNER="$BS_DIR/scenarios/runner/runner"
STORE_DIR="${TMPDIR:-/tmp}/bs-slots-hard"
OUTPUT_DIR="$SCRIPT_DIR/results/$(date +%Y%m%d_%H%M%S)"
mkdir -p "$STORE_DIR" "$OUTPUT_DIR"

# ── 7. Run experiment ─────────────────────────────────────────────────────────
EXTRA_ARGS=""
if [[ -n "$RAW_INPUT" ]]; then
  [[ -f "$RAW_INPUT" ]] || { echo "ERROR: BS_RAW_INPUT file not found: $RAW_INPUT"; exit 1; }
  EXTRA_ARGS="--raw-input $RAW_INPUT --chunk-lines $CHUNK_LINES"
  log "Raw binary input: $RAW_INPUT ($CHUNK_LINES lines/chunk)"
fi

log "Running experiment: iterations=$ITERATIONS models=$MODELS min_sim_bps=$MIN_SIM_BPS"
"$RUNNER" \
  --matrix "$BS_DIR/scenarios/scenario_matrix.json" \
  --iterations "$ITERATIONS" \
  --models "$MODELS" \
  --embed "$EMBED_URL" \
  --dapi "$DAPI_URL" \
  --store "$STORE_DIR" \
  --output "$OUTPUT_DIR" \
  --hub-url "${HUB_URL:-https://gonka.gg/api/public/stats/historical}" \
  --hub-key "$HUB_KEY" \
  $EXTRA_ARGS

log "Results: $OUTPUT_DIR"
log "Key files:"
ls "$OUTPUT_DIR"/*.json 2>/dev/null | while read f; do echo "  - $f"; done

log "✓ HARD experiment complete."
echo ""
echo "To destroy cluster:  k3d cluster delete $CLUSTER_NAME"
echo "To keep for reuse:   leave cluster running, rerun ./run.sh"
