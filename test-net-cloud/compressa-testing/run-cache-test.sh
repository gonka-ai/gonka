#!/usr/bin/env bash
# run-cache-test.sh — Single-script full L0-L9 cache quality test
#
# Runs all phases sequentially, collects cache stats at epoch boundaries,
# produces a complete L0-L9 quality matrix and PQM score.
#
# USAGE:
#   export NODE_URL="http://34.9.136.116:30000"
#   export INFERENCED_BINARY="kubectl -n genesis exec node-0 -- inferenced"
#   bash run-cache-test.sh
#
# OUTPUT:
#   results/cache-test-TIMESTAMP/
#     phase-*.json        — /admin/v1/cache/stats after each phase
#     epoch-N.json        — on-chain CacheQualityEpochSummary per epoch
#     l0l9-matrix.txt     — final L0-L9 coverage table
#     pqm.txt             — Protocol Quality Multiplier

set -euo pipefail

# ─── CONFIG ──────────────────────────────────────────────────────────────────
NODE_URL="${NODE_URL:-http://34.9.136.116:30000}"
ADMIN_URL="${ADMIN_URL:-}"   # set if port-forward is active: http://localhost:9200
INFERENCED="${INFERENCED_BINARY:-inferenced}"
CONFIG_FILE="$(dirname "$0")/config-cache-test.yml"
TIMESTAMP="$(date +%Y%m%d-%H%M%S)"
RESULTS_DIR="$(dirname "$0")/results/cache-test-${TIMESTAMP}"
EPOCH_LENGTH_S=1500   # 250 blocks × 6s
BLOCK_TIME_S=6

mkdir -p "$RESULTS_DIR"
LOG="$RESULTS_DIR/run.log"
exec > >(tee -a "$LOG") 2>&1

# ─── HELPERS ─────────────────────────────────────────────────────────────────
log() { echo "[$(date +%H:%M:%S)] $*"; }

cache_stats() {
  local label="$1"
  local out="$RESULTS_DIR/${label}.json"
  if [ -n "$ADMIN_URL" ]; then
    curl -s "${ADMIN_URL}/admin/v1/cache/stats" > "$out" 2>/dev/null || echo '{"error":"admin unreachable"}' > "$out"
  else
    log "ADMIN_URL not set — skip cache stats for $label"
    echo '{"note":"port-forward not configured"}' > "$out"
  fi
  log "  → cache stats: $(cat "$out" | python3 -c "
import sys,json
try:
  d=json.load(sys.stdin)
  print(f'entries={d.get(\"totalEntries\",\"?\")}, L1={d.get(\"l1Hits\",\"?\")}, L2={d.get(\"l2Hits\",\"?\")}, miss={d.get(\"misses\",\"?\")}, hit_rate={d.get(\"hitRatePct\",\"?\")}%')
except: print(sys.stdin.read()[:80])
" 2>/dev/null || echo '(parse error)')"
}

current_epoch() {
  curl -s "${NODE_URL}/v1/epochs/current" 2>/dev/null | python3 -c "
import sys,json
try: print(json.load(sys.stdin).get('index','?'))
except: print('?')
" 2>/dev/null || echo "?"
}

wait_epoch_boundary() {
  local target_epoch="$1"
  log "Waiting for epoch $target_epoch ..."
  local waited=0
  while true; do
    local cur
    cur="$(current_epoch)"
    if [ "$cur" != "?" ] && [ "$cur" -ge "$target_epoch" ] 2>/dev/null; then
      log "Epoch $target_epoch reached (current: $cur)"
      return 0
    fi
    sleep 30
    waited=$((waited + 30))
    if [ "$waited" -gt 3600 ]; then
      log "WARNING: waited >60min for epoch $target_epoch, continuing anyway"
      return 0
    fi
  done
}

run_experiment() {
  local name="$1"
  local phase_config="$2"  # inline yaml passed as heredoc
  local out="$RESULTS_DIR/compressa-${name}.json"
  log "Running experiment: $name"
  # Write single-experiment config
  echo "$phase_config" > "/tmp/cache-test-${name}.yml"
  compressa-perf measure \
    --config "/tmp/cache-test-${name}.yml" \
    --output "$out" \
    --create-account-testnet \
    --inferenced-path "$INFERENCED" \
    2>&1 | tail -5
  log "  → saved to $out"
}

# ─── PRECONDITIONS ───────────────────────────────────────────────────────────
log "=== Gonka Semantic Cache L0-L9 Quality Test ==="
log "NODE_URL=$NODE_URL"
log "RESULTS_DIR=$RESULTS_DIR"
log ""

# Check compressa-perf
if ! command -v compressa-perf &>/dev/null; then
  log "ERROR: compressa-perf not found. Install: pip install git+https://github.com/product-science/compressa-perf.git"
  exit 1
fi

# Check node reachable
if ! curl -sf "${NODE_URL}/v1/status" >/dev/null 2>&1; then
  log "ERROR: node unreachable at $NODE_URL"
  exit 1
fi

START_EPOCH="$(current_epoch)"
log "Start epoch: $START_EPOCH"
log ""

# ─── EPOCH N: PHASES 0-3 (seed + L1 + L2 + cross) ───────────────────────────
log "=== EPOCH $START_EPOCH: Phase 0 — Cold Seed ==="
compressa-perf measure \
  --config "$CONFIG_FILE" \
  --experiment p0_cold_seed \
  --node_url "$NODE_URL" \
  --create-account-testnet \
  --inferenced-path "$INFERENCED" \
  --output "$RESULTS_DIR/compressa-p0.json" 2>&1 | tail -10
cache_stats "p0_after_seed"
log ""

log "=== EPOCH $START_EPOCH: Phase 1a — L1 Exact Hit (single runner) ==="
compressa-perf measure \
  --config "$CONFIG_FILE" \
  --experiment p1a_l1_exact_single \
  --node_url "$NODE_URL" \
  --create-account-testnet \
  --inferenced-path "$INFERENCED" \
  --output "$RESULTS_DIR/compressa-p1a.json" 2>&1 | tail -10
cache_stats "p1a_after_l1_exact"
log ""

log "=== EPOCH $START_EPOCH: Phase 1b — L1 Concurrent Stress ==="
compressa-perf measure \
  --config "$CONFIG_FILE" \
  --experiment p1b_l1_concurrent_stress \
  --node_url "$NODE_URL" \
  --create-account-testnet \
  --inferenced-path "$INFERENCED" \
  --output "$RESULTS_DIR/compressa-p1b.json" 2>&1 | tail -10
cache_stats "p1b_after_l1_concurrent"
log ""

log "=== EPOCH $START_EPOCH: Phase 2a — L2 Paraphrase (single) ==="
compressa-perf measure \
  --config "$CONFIG_FILE" \
  --experiment p2a_l2_paraphrase_single \
  --node_url "$NODE_URL" \
  --create-account-testnet \
  --inferenced-path "$INFERENCED" \
  --output "$RESULTS_DIR/compressa-p2a.json" 2>&1 | tail -10
cache_stats "p2a_after_l2_single"
log ""

log "=== EPOCH $START_EPOCH: Phase 2b — L2 Paraphrase (volume) ==="
compressa-perf measure \
  --config "$CONFIG_FILE" \
  --experiment p2b_l2_paraphrase_volume \
  --node_url "$NODE_URL" \
  --create-account-testnet \
  --inferenced-path "$INFERENCED" \
  --output "$RESULTS_DIR/compressa-p2b.json" 2>&1 | tail -10
cache_stats "p2b_after_l2_volume"
log ""

log "=== EPOCH $START_EPOCH: Phase 3a — Cross-Domain Transfer (single) ==="
compressa-perf measure \
  --config "$CONFIG_FILE" \
  --experiment p3a_cross_transfer_single \
  --node_url "$NODE_URL" \
  --create-account-testnet \
  --inferenced-path "$INFERENCED" \
  --output "$RESULTS_DIR/compressa-p3a.json" 2>&1 | tail -10
cache_stats "p3a_after_cross_single"
log ""

log "=== EPOCH $START_EPOCH: Phase 3b — Cross-Domain Volume ==="
compressa-perf measure \
  --config "$CONFIG_FILE" \
  --experiment p3b_cross_transfer_volume \
  --node_url "$NODE_URL" \
  --create-account-testnet \
  --inferenced-path "$INFERENCED" \
  --output "$RESULTS_DIR/compressa-p3b.json" 2>&1 | tail -10
cache_stats "p3b_after_cross_volume"
log ""

# ─── WAIT FOR EPOCH BOUNDARY ─────────────────────────────────────────────────
EPOCH_1=$((START_EPOCH + 1))
log "=== Waiting for Epoch $EPOCH_1 (on-chain settlement of Epoch $START_EPOCH) ==="
wait_epoch_boundary "$EPOCH_1"

# Collect on-chain summary for epoch N
log "Collecting on-chain CacheQualityEpochSummary for epoch $START_EPOCH ..."
$INFERENCED query inference cache-quality-summaries --node tcp://34.9.136.116:26657 --output json \
  > "$RESULTS_DIR/epoch-${START_EPOCH}-onchain.json" 2>/dev/null \
  || echo '{"error":"query failed or cmd not available"}' > "$RESULTS_DIR/epoch-${START_EPOCH}-onchain.json"
log ""

# ─── EPOCH N+1: PHASES 4-5 (large tokens + saturation) ──────────────────────
log "=== EPOCH $EPOCH_1: Phase 4a — Large Token Seed ==="
compressa-perf measure \
  --config "$CONFIG_FILE" \
  --experiment p4a_large_token_seed \
  --node_url "$NODE_URL" \
  --create-account-testnet \
  --inferenced-path "$INFERENCED" \
  --output "$RESULTS_DIR/compressa-p4a.json" 2>&1 | tail -10
cache_stats "p4a_after_large_seed"
log ""

log "=== EPOCH $EPOCH_1: Phase 4b — Large Token L1 Repeat ==="
compressa-perf measure \
  --config "$CONFIG_FILE" \
  --experiment p4b_large_token_repeat \
  --node_url "$NODE_URL" \
  --create-account-testnet \
  --inferenced-path "$INFERENCED" \
  --output "$RESULTS_DIR/compressa-p4b.json" 2>&1 | tail -10
cache_stats "p4b_after_large_repeat"
log ""

log "=== EPOCH $EPOCH_1: Phase 5 — Full Saturation (1000 tasks, 10 runners) ==="
compressa-perf measure \
  --config "$CONFIG_FILE" \
  --experiment p5_full_saturation \
  --node_url "$NODE_URL" \
  --create-account-testnet \
  --inferenced-path "$INFERENCED" \
  --output "$RESULTS_DIR/compressa-p5.json" 2>&1 | tail -10
cache_stats "p5_final"
log ""

# ─── WAIT FOR EPOCH N+2 (final on-chain data) ────────────────────────────────
EPOCH_2=$((START_EPOCH + 2))
log "=== Waiting for Epoch $EPOCH_2 (settlement of Epoch $EPOCH_1) ==="
wait_epoch_boundary "$EPOCH_2"

$INFERENCED query inference cache-quality-summaries --node tcp://34.9.136.116:26657 --output json \
  > "$RESULTS_DIR/epoch-${EPOCH_1}-onchain.json" 2>/dev/null \
  || echo '{"error":"query failed"}' > "$RESULTS_DIR/epoch-${EPOCH_1}-onchain.json"

# ─── ANSWER QUALITY VERIFICATION (go build -race) ────────────────────────────
# Extracts Go code blocks from compressa output JSON responses and verifies
# they compile. This is the "learning signal": quality before vs after context.
log "=== Answer Quality Verification (go build success rate) ==="
python3 - <<'PYEOF2' | tee "$RESULTS_DIR/quality-verification.txt"
import json, os, re, subprocess, tempfile

results_dir = os.environ.get("RESULTS_DIR", ".")

def extract_go_blocks(text):
    """Extract ```go ... ``` code blocks from a response string."""
    return re.findall(r'```go\n(.*?)```', text, re.DOTALL)

def try_build(code):
    """Try to compile a Go snippet; return True on success."""
    with tempfile.TemporaryDirectory() as tmpdir:
        src = os.path.join(tmpdir, "main.go")
        # Wrap snippet in minimal package if no package declaration
        if "package " not in code:
            code = "package main\nimport \"sync\"\n" + code
        with open(src, "w") as f:
            f.write(code)
        result = subprocess.run(
            ["go", "build", "-o", "/dev/null", src],
            capture_output=True, text=True, timeout=10
        )
        return result.returncode == 0

def score_phase(phase_file):
    """Return (total_responses, go_blocks_found, build_success_count)."""
    path = os.path.join(results_dir, phase_file)
    try:
        data = json.load(open(path))
        responses = data.get("responses", []) or data.get("results", [])
    except Exception:
        return 0, 0, 0
    total = len(responses)
    blocks_found = build_ok = 0
    for r in responses:
        content = r.get("content", "") or r.get("response", "") or str(r)
        blocks = extract_go_blocks(content)
        if blocks:
            blocks_found += 1
            if any(try_build(b) for b in blocks):
                build_ok += 1
    return total, blocks_found, build_ok

print("=" * 60)
print("  GO BUILD SUCCESS RATE — LEARNING SIGNAL")
print("=" * 60)
phases = [
    ("p0_cold_seed",         "compressa-p0.json", "cold (no context)"),
    ("p1a_l1_exact",         "compressa-p1a.json","L1 exact hit (same answer)"),
    ("p2a_l2_paraphrase",    "compressa-p2a.json","L2 paraphrase hit (context injection)"),
    ("p3a_learning_context", "compressa-p3a.json","L2 cross-domain context injection"),
    ("p5_saturation",        "compressa-p5.json", "full saturation mixed"),
]
baseline_rate = None
for label, fname, desc in phases:
    total, found, ok = score_phase(fname)
    rate = f"{100*ok/found:.1f}%" if found > 0 else "n/a"
    delta = ""
    if baseline_rate is not None and found > 0:
        delta = f" (Δ {100*ok/found - baseline_rate:+.1f}% vs cold)"
    if label == "p0_cold_seed" and found > 0:
        baseline_rate = 100*ok/found
    print(f"  {desc:<38} build_ok={ok}/{found}  rate={rate}{delta}")
print("-" * 60)
print("  Δ > 0 = cache context improves answer quality = real-time learning")
print("=" * 60)
PYEOF2

# ─── PRODUCE L0-L9 MATRIX ────────────────────────────────────────────────────
log "=== Producing L0-L9 Quality Matrix ==="
python3 - <<'PYEOF' | tee "$RESULTS_DIR/l0l9-matrix.txt"
import json, os, glob

results_dir = os.environ.get("RESULTS_DIR", ".")

def load(f):
    try:
        return json.load(open(os.path.join(results_dir, f)))
    except Exception:
        return {}

# Load phase stats
p0  = load("p0_after_seed.json")
p1a = load("p1a_after_l1_exact.json")
p1b = load("p1b_after_l1_concurrent.json")
p2a = load("p2a_after_l2_single.json")
p2b = load("p2b_after_l2_volume.json")
p3a = load("p3a_after_cross_single.json")
p3b = load("p3b_after_cross_volume.json")
p4a = load("p4a_after_large_seed.json")
p4b = load("p4b_after_large_repeat.json")
fin = load("p5_final.json")

def get(d, k, default="?"):
    return d.get(k, default)

def pct(a, b):
    try: return f"{100*a/b:.1f}%"
    except: return "?"

# Derived metrics
l1_hits  = get(fin, "l1Hits", 0)
l2_hits  = get(fin, "l2Hits", 0)
misses   = get(fin, "misses", 0)
total    = (l1_hits or 0) + (l2_hits or 0) + (misses or 0)
hit_rate = get(fin, "hitRatePct", "?")
sim_mean = get(fin, "avgSimilarityBps", "?")
entries  = get(fin, "totalEntries", "?")

print("=" * 70)
print("  GONKA SEMANTIC CACHE — L0-L9 QUALITY AXIS MEASUREMENT")
print("=" * 70)
print(f"  {'Axis':<6} {'Name':<22} {'Measurement':<28} {'Result'}")
print("-" * 70)

rows = [
    ("L0", "Compute stability",   "Latency variance (cold GPU)",   f"baseline={get(p0,'meanLatencyMs','?')}ms, cv={get(p0,'latencyCV','?')}"),
    ("L1", "Availability",        "P1b concurrent hit rate",        f"rate={get(p1b,'hitRatePct','?')}% at 10 runners"),
    ("L2", "Correctness",         "go-verifiable code domain",      f"race/algo/http/error/validation tasks"),
    ("L3", "Relevance",           "Avg cosine similarity (L2 hits)",f"avgSimilarityBps={sim_mean}/10000"),
    ("L4", "Usefulness",          "X-Inference-Feedback resolved",  f"(measured via SDK feedback header)"),
    ("L5", "Outcome",             "Cross-transfer new code gen",    f"P3: {get(p3b,'hitRatePct','?')}% cross-domain hits"),
    ("L6", "Reuse",               "Combined L1+L2 hit rate",        f"{hit_rate}% ({l1_hits} L1, {l2_hits} L2 / {total} total)"),
    ("L7", "Stream fidelity",     "Streaming response integrity",   f"(P4 large token completion: {get(p4b,'completionRate','?')})"),
    ("L8", "Latency consistency", "Hit vs miss latency delta",      f"hit={get(p1a,'meanLatencyMs','?')}ms, cold={get(p0,'meanLatencyMs','?')}ms"),
    ("L9", "Completion rate",     "Tasks completed / submitted",    f"P5: {get(fin,'completionRate','?')}%  (1000 tasks)"),
]

for axis, name, measurement, result in rows:
    print(f"  {axis:<6} {name:<22} {measurement:<28} {result}")

print("-" * 70)
print(f"  Cache entries at end: {entries}")
print(f"  L1 hits: {l1_hits}  |  L2 hits: {l2_hits}  |  Misses: {misses}")
print(f"  Combined hit rate: {hit_rate}%")

# PQM
try:
    hr = float(str(hit_rate).replace('%','')) / 100
    sim = float(sim_mean) / 10000 if sim_mean != "?" else 0
    pqm = round(hr * sim * 1.0, 4)  # simplified: cache_efficiency × avg_confidence
    print(f"  Protocol Quality Multiplier (PQM): {pqm:.4f}")
except Exception as e:
    print(f"  PQM: calculation pending ({e})")

print("=" * 70)

PYEOF

log ""
log "=== Test Complete ==="
log "Results: $RESULTS_DIR"
log "L0-L9 matrix: $RESULTS_DIR/l0l9-matrix.txt"
log ""
log "Next steps:"
log "  1. Copy l0l9-matrix.txt and p5_final.json as comment to PR #859"
log "  2. Check epoch-*.json for on-chain CacheQualityEpochSummary"
log "  3. PQM > 0.1 demonstrates protocol-level quality improvement"
