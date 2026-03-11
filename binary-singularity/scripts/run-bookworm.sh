#!/usr/bin/env bash
set -euo pipefail

# Binary Singularity — Bookworm Experiment Master Script
#
# This script orchestrates the full experiment on a bookworm machine:
#   1. Build all containers
#   2. Start the stack (embedder + mock-node + API + runtime)
#   3. Wait for health checks
#   4. Run the 4×16×4 scenario matrix
#   5. Collect metrics and generate report
#   6. Stop the stack
#
# Prerequisites:
#   - Docker with compose plugin (or docker-compose)
#   - At least 4 GB RAM
#   - No GPU required
#
# Usage:
#   cd binary-singularity/
#   ./scripts/run-bookworm.sh

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT_DIR="$(dirname "$SCRIPT_DIR")"
RESULTS_DIR="${ROOT_DIR}/results/$(date +%Y%m%d_%H%M%S)"

cd "$ROOT_DIR"

echo "╔══════════════════════════════════════════════════╗"
echo "║  BINARY SINGULARITY — Bookworm Experiment        ║"
echo "╚══════════════════════════════════════════════════╝"
echo ""
echo "Root:    $ROOT_DIR"
echo "Results: $RESULTS_DIR"
echo ""

mkdir -p "$RESULTS_DIR"

# ── Step 1: Build ──
echo "▶ [1/6] Building containers..."
if command -v docker-compose &> /dev/null; then
    COMPOSE="docker-compose"
elif docker compose version &> /dev/null 2>&1; then
    COMPOSE="docker compose"
else
    echo "ERROR: Neither docker-compose nor 'docker compose' found."
    echo "Install: sudo apt install docker-compose-plugin"
    exit 1
fi

$COMPOSE build 2>&1 | tail -5
echo "  ✓ Build complete"

# ── Step 2: Start stack ──
echo "▶ [2/6] Starting stack..."
$COMPOSE up -d embedder mock-node
echo "  ✓ Embedder + Mock-node started"

# ── Step 3: Wait for health ──
echo "▶ [3/6] Waiting for services to be healthy..."

wait_for_health() {
    local name="$1"
    local url="$2"
    local max_wait="${3:-60}"
    local waited=0

    while [ $waited -lt $max_wait ]; do
        if curl -sf "$url" > /dev/null 2>&1; then
            echo "  ✓ $name is healthy"
            return 0
        fi
        sleep 2
        waited=$((waited + 2))
    done
    echo "  ✗ $name failed health check after ${max_wait}s"
    return 1
}

wait_for_health "Embedder" "http://localhost:8686/health" 120
wait_for_health "Mock-node" "http://localhost:8080/health" 30

# ── Step 4: Run scenario matrix ──
echo "▶ [4/6] Running 4×16×4 scenario matrix..."
echo "  Matrix: 4 participants × 16 scenarios × 4 modes = 256 runs"
echo ""

# Build and run scenario-runner locally (no Docker needed if Go is available)
if command -v go &> /dev/null; then
    echo "  Using local Go build..."
    cd "$ROOT_DIR"
    go build -o "$ROOT_DIR/bin/scenario-runner" ./scenarios/runner/
    
    mkdir -p "$ROOT_DIR/.bs-slots"
    
    "$ROOT_DIR/bin/scenario-runner" \
        --matrix "$ROOT_DIR/scenarios/matrix.json" \
        --output "$RESULTS_DIR" \
        --embedder "http://localhost:8686" \
        --dapi "http://localhost:8080" \
        --store "$ROOT_DIR/.bs-slots" \
        -v 2>&1 | tee "$RESULTS_DIR/runner.log"
else
    echo "  Using Docker for scenario runner..."
    $COMPOSE run --rm \
        -v "$RESULTS_DIR:/results" \
        scenario-runner \
        --matrix /scenarios/matrix.json \
        --output /results
fi

echo ""
echo "  ✓ Scenario matrix complete"

# ── Step 5: Collect additional metrics ──
echo "▶ [5/6] Collecting additional metrics..."

# Copy slot store
if [ -d "$ROOT_DIR/.bs-slots" ]; then
    cp -r "$ROOT_DIR/.bs-slots" "$RESULTS_DIR/slots/"
fi

# Embedder stats
curl -sf http://localhost:8686/health > "$RESULTS_DIR/embedder_health.json" 2>/dev/null || true

# Generate markdown summary
cat > "$RESULTS_DIR/SUMMARY.md" << 'HEREDOC'
# Binary Singularity — Experiment Results

## Experiment Parameters

| Parameter | Value |
|-----------|-------|
| Stack | Bookworm (CPU-only, no GPU) |
| Participants | 4 (2 farmers + 2 hosts) |
| Scenarios | 16 (4 phases × 4 per phase) |
| Modes | 4 (baseline, cache, slots, full) |
| Total runs | 256 |
| Embedder | all-MiniLM-L6-v2 (384-dim, CPU) |
| Inference | Mock node (deterministic) |

## Files

- `experiment_report.json` — Full results with all 256 runs
- `epoch_metrics.json` — Per-epoch L6/L8/L9/PQM metrics
- `slot_stats.json` — Final PatternSlot store statistics
- `runner.log` — Execution log
- `slots/` — Binary slot store (pattern_slots.bin)

## How to Reproduce

```bash
cd binary-singularity/
./scripts/run-bookworm.sh
```

## Architecture Reference

See `docs/GPU_savings_over_distance.md` §7–8 for full architecture description.
HEREDOC

echo "  ✓ Summary generated"

# ── Step 6: Stop stack ──
echo "▶ [6/6] Stopping stack..."
$COMPOSE down 2>/dev/null || true
echo "  ✓ Stack stopped"

echo ""
echo "╔══════════════════════════════════════════════════╗"
echo "║  EXPERIMENT COMPLETE                              ║"
echo "║  Results: $RESULTS_DIR  ║"
echo "╚══════════════════════════════════════════════════╝"
echo ""
echo "Key files:"
echo "  $RESULTS_DIR/experiment_report.json"
echo "  $RESULTS_DIR/epoch_metrics.json"
echo "  $RESULTS_DIR/slot_stats.json"
echo ""
echo "Next steps:"
echo "  1. Review results: cat $RESULTS_DIR/experiment_report.json | python3 -m json.tool"
echo "  2. Compare modes: jq '.metrics.by_mode' $RESULTS_DIR/experiment_report.json"
echo "  3. Check PQM: jq '.metrics.overall' $RESULTS_DIR/experiment_report.json"
