#!/bin/bash

# PoW Init Generate Request Script
# This script sends an init generate request to the PoW server

echo "PoW Init Generate Request"
echo "========================="

# Set default values if environment variables are not set
export SERVER_URL=${SERVER_URL:-http://localhost:8080}
export BATCH_RECEIVER_URL=${BATCH_RECEIVER_URL:-http://localhost:5000}
export NODE_ID=${NODE_ID:-0}
export NODE_COUNT=${NODE_COUNT:-1}
export BLOCK_HASH=${BLOCK_HASH:-$(date +%s | sha256sum | cut -d' ' -f1)}
export BLOCK_HEIGHT=${BLOCK_HEIGHT:-1}
export PUBLIC_KEY=${PUBLIC_KEY:-"public_key_$(date +%s)"}
export BATCH_SIZE=${BATCH_SIZE:-1}
export R_TARGET=${R_TARGET:-10.0}
export FRAUD_THRESHOLD=${FRAUD_THRESHOLD:-0.01}

echo "Configuration:"
echo "  Server URL: $SERVER_URL"
echo "  Batch Receiver URL: $BATCH_RECEIVER_URL"
echo "  Node ID: $NODE_ID"
echo "  Node Count: $NODE_COUNT"
echo "  Block Hash: $BLOCK_HASH"
echo "  Block Height: $BLOCK_HEIGHT"
echo "  Public Key: $PUBLIC_KEY"
echo "  Batch Size: $BATCH_SIZE"
echo "  R Target: $R_TARGET"
echo "  Fraud Threshold: $FRAUD_THRESHOLD"
echo ""

# Create JSON payload
JSON_PAYLOAD=$(cat <<EOF
{
  "node_id": $NODE_ID,
  "node_count": $NODE_COUNT,
  "block_hash": "$BLOCK_HASH",
  "block_height": $BLOCK_HEIGHT,
  "public_key": "$PUBLIC_KEY",
  "batch_size": $BATCH_SIZE,
  "r_target": $R_TARGET,
  "fraud_threshold": $FRAUD_THRESHOLD,
  "params": {
    "dim": 1024,
    "n_layers": 32,
    "n_heads": 32,
    "n_kv_heads": 32,
    "vocab_size": 8196,
    "ffn_dim_multiplier": 10.0,
    "multiple_of": 2048,
    "norm_eps": 1e-05,
    "rope_theta": 10000.0,
    "use_scaled_rope": false,
    "seq_len": 128
  },
  "url": "$BATCH_RECEIVER_URL"
}
EOF
)

echo "Sending init generate request..."
echo "JSON Payload:"
echo "$JSON_PAYLOAD" | jq '.' 2>/dev/null || echo "$JSON_PAYLOAD"
echo ""

# Send the request
RESPONSE=$(curl -s -w "\nHTTP_STATUS:%{http_code}" \
  -X POST \
  -H "Content-Type: application/json" \
  -d "$JSON_PAYLOAD" \
  "$SERVER_URL/api/v1/pow/init/generate")

# Extract HTTP status and response body
HTTP_STATUS=$(echo "$RESPONSE" | grep "HTTP_STATUS:" | cut -d: -f2)
RESPONSE_BODY=$(echo "$RESPONSE" | sed '/HTTP_STATUS:/d')

echo "Response:"
echo "HTTP Status: $HTTP_STATUS"
echo "Response Body:"
echo "$RESPONSE_BODY" | jq '.' 2>/dev/null || echo "$RESPONSE_BODY"

if [ "$HTTP_STATUS" = "200" ]; then
    echo ""
    echo "✅ Init generate request successful!"
    echo ""
    echo "You can check the server status with:"
    echo "  curl -s $SERVER_URL/api/v1/pow/status | jq"
else
    echo ""
    echo "❌ Init generate request failed!"
    exit 1
fi 