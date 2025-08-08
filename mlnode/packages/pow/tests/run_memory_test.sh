#!/bin/bash

# Memory Test Runner
# This script runs the memory profiling test against a running PoW server

echo "Running Memory Test..."
echo "====================="

# Set up PYTHONPATH to include required packages
export PYTHONPATH="/mnt/ramdisk/tamaz/mlnode/packages/pow/src:/mnt/ramdisk/tamaz/mlnode/packages/common/src:$PYTHONPATH"

# Set default environment variables if not set
export GPU_DEVICE_ID=${GPU_DEVICE_ID:-0}
export SERVER_URL=${SERVER_URL:-http://localhost:8080}

echo "GPU Device ID: $GPU_DEVICE_ID"
echo "Server URL: $SERVER_URL"
echo "PYTHONPATH: $PYTHONPATH"
echo ""

# Check if server is responding before running tests
echo "Checking server availability..."
for i in {1..10}; do
    if curl -s -f "$SERVER_URL/api/v1/pow/status" > /dev/null 2>&1; then
        echo "Server is responding!"
        break
    fi
    if [ $i -eq 10 ]; then
        echo "Error: Server is not responding at $SERVER_URL"
        echo "Make sure the server is running before executing this test."
        exit 1
    fi
    echo "Attempt $i/10: Server not ready yet, waiting..."
    sleep 2
done

echo ""

# Run the memory test
echo "Running server memory test..."
python3 server_memory_test.py

echo ""
echo "Memory Test Complete!" 