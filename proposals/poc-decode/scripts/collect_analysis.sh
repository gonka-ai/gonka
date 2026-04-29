#!/usr/bin/env bash
# Collect hidden-state datasets for analysis.ipynb
# Usage:
#   URL=http://host:port bash scripts/collect_analysis.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POC_DECODE_DIR="$(dirname "$SCRIPT_DIR")"

URL="${URL:-http://localhost:8000}"

MAX_TOKENS=32

OUTPUT_DIR="$POC_DECODE_DIR/data/dist_analysis"
mkdir -p "$OUTPUT_DIR"

cd "$POC_DECODE_DIR"

echo "=== Collecting hidden states for analysis.ipynb ==="
echo "  server:     $URL"
echo "  max_tokens: $MAX_TOKENS"
echo "  output:     $OUTPUT_DIR"
echo

# 100 different random hashes, 1 fixed nonce — used for hash-variance analysis
echo "random_diff_hashes_one_nonce — 100 hashes × 1 nonce..."
python scripts/collect_hidden_states.py \
    --url        "$URL" \
    --num-hashes 100 \
    --nonces      42 \
    --max-tokens "$MAX_TOKENS" \
    --output     "$OUTPUT_DIR/random_diff_hashes_one_nonce.npz"

# 1 random hash, 100 different nonces — used for nonce-variance analysis
echo "random_one_hash_diff_nonces — 1 hash × 100 nonces..."
python scripts/collect_hidden_states.py \
    --url        "$URL" \
    --num-hashes 1 \
    --nonces     0:100 \
    --max-tokens "$MAX_TOKENS" \
    --output     "$OUTPUT_DIR/random_one_hash_diff_nonces.npz"


echo
echo "Done. Files written to $OUTPUT_DIR:"
ls -lh "$OUTPUT_DIR"
