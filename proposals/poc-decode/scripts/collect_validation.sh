#!/usr/bin/env bash
# Collect artifacts for validation.ipynb
# Usage:
#   bash scripts/collect_validation.sh --honest
#   bash scripts/collect_validation.sh --fraud
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
POC_DECODE_DIR="$(dirname "$SCRIPT_DIR")"

NUM_HASHES=1
NUM_NONCES=4
MAX_TOKENS=256
OUTPUT_DIR="$POC_DECODE_DIR/data/validation"

HONEST_INFERENCE_URL="http://0.0.0.0:8000"
HONEST_VALIDATION_URL="http://146.115.17.158:53301"

FRAUD_INFERENCE_URL="http://146.115.17.158:15902"
FRAUD_VALIDATION_URL="http://146.115.17.158:53301"

if [[ $# -ne 1 ]] || [[ "$1" != "--honest" && "$1" != "--fraud" ]]; then
    echo "Usage: $0 --honest | --fraud" >&2
    exit 1
fi

case "$1" in
    --honest)
        INFERENCE_URL="$HONEST_INFERENCE_URL"
        VALIDATION_URL="$HONEST_VALIDATION_URL"
        SCENARIO="Honest"
        ;;
    --fraud)
        INFERENCE_URL="$FRAUD_INFERENCE_URL"
        VALIDATION_URL="$FRAUD_VALIDATION_URL"
        SCENARIO="Fraud"
        ;;
esac

mkdir -p "$OUTPUT_DIR"
cd "$POC_DECODE_DIR"

echo "=== $SCENARIO scenario ==="
echo "  inference:  $INFERENCE_URL"
echo "  validation: $VALIDATION_URL"
echo "  hashes: $NUM_HASHES  nonces: $NUM_NONCES  max_tokens: $MAX_TOKENS"
echo

python scripts/poc_validation.py \
    --inference  "$INFERENCE_URL" \
    --validation "$VALIDATION_URL" \
    --num-hashes "$NUM_HASHES" \
    --num-nonces "$NUM_NONCES" \
    --max-tokens "$MAX_TOKENS" \
    --results-dir "$OUTPUT_DIR"

echo
echo "Done. Artifacts saved to $OUTPUT_DIR"
