#!/bin/bash
# Quick test starter - run this after your Docker image is downloaded

set -e

IMAGE="ghcr.io/ekaterynakuznetsova/vllm:v0.9.1-deterministic"
MODEL="meta-llama/Llama-3.2-1B-Instruct"

echo "🚀 Starting vLLM Test"
echo "===================="
echo ""

# Check if image exists
if ! docker images | grep -q "ekaterynakuznetsova/vllm"; then
    echo "⚠️  Image not found locally. Checking if download is complete..."
    echo "   Looking for: $IMAGE"
    echo ""
    docker images | grep vllm || echo "   No vLLM images found"
    echo ""
    read -p "Press Enter when image download is complete, or Ctrl+C to exit..."
fi

echo "✅ Image found!"
echo ""

# Start the container
echo "🔧 Starting vLLM container..."

# Detect if we're on Mac (no GPU) or Linux (with GPU)
if [[ "$OSTYPE" == "darwin"* ]]; then
    echo "   (Running on Mac - CPU mode)"
    docker run -d --rm \
      -p 8000:8000 \
      -v ~/.cache/huggingface:/root/.cache/huggingface \
      --name vllm-test \
      $IMAGE \
      --model $MODEL \
      --host 0.0.0.0 \
      --port 8000
else
    echo "   (Running with GPU support)"
    docker run -d --rm \
      --gpus all \
      -p 8000:8000 \
      -v ~/.cache/huggingface:/root/.cache/huggingface \
      --name vllm-test \
      $IMAGE \
      --model $MODEL \
      --host 0.0.0.0 \
      --port 8000
fi

echo "✅ Container started!"
echo ""
echo "⏳ Waiting for server to be ready (this may take 1-2 minutes for model loading)..."
echo "   You can watch progress with: docker logs -f vllm-test"
echo ""

# Wait for server to be ready
MAX_WAIT=180
ELAPSED=0
while [ $ELAPSED -lt $MAX_WAIT ]; do
    if curl -s http://localhost:8000/v1/models > /dev/null 2>&1; then
        echo ""
        echo "✅ Server is ready!"
        break
    fi
    printf "."
    sleep 5
    ELAPSED=$((ELAPSED + 5))
done

if [ $ELAPSED -ge $MAX_WAIT ]; then
    echo ""
    echo "❌ Server didn't start within $MAX_WAIT seconds"
    echo "Check logs: docker logs vllm-test"
    exit 1
fi

echo ""
echo "🧪 Running tests..."
echo "===================="
echo ""

# Run the Python test
python3 "$(dirname "$0")/test_deterministic_sampling.py"

TEST_RESULT=$?

echo ""
echo "===================="
if [ $TEST_RESULT -eq 0 ]; then
    echo "✅ ALL TESTS PASSED!"
else
    echo "❌ Some tests failed"
fi
echo "===================="
echo ""

# Ask if user wants to keep server running
read -p "Keep vLLM server running for manual testing? [Y/n] " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Nn]$ ]]; then
    echo "✅ Server is running at http://localhost:8000"
    echo ""
    echo "Test with:"
    echo "  python3 $(dirname "$0")/test_deterministic_sampling.py"
    echo ""
    echo "Stop with:"
    echo "  docker stop vllm-test"
else
    echo "🛑 Stopping server..."
    docker stop vllm-test
    echo "✅ Done!"
fi
