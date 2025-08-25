#!/bin/bash

# PoW Server Starter
# This script starts the PoW server and keeps it running

echo "Starting PoW Server..."
echo "====================="

# Set up PYTHONPATH to include required packages
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
export PYTHONPATH="$PROJECT_ROOT/packages/pow/src:$PROJECT_ROOT/packages/common/src:$PYTHONPATH"

# Set default environment variables if not set
export GPU_DEVICE_ID=${GPU_DEVICE_ID:-0}
export SERVER_URL=${SERVER_URL:-http://localhost:8080}

echo "GPU Device ID: $GPU_DEVICE_ID"
echo "Server URL: $SERVER_URL"
echo "PYTHONPATH: $PYTHONPATH"
echo ""

# Create temporary log file
SERVER_LOG=$(mktemp /tmp/server_log.XXXXXX)

# Function to cleanup processes on exit
cleanup() {
    echo ""
    echo "Cleaning up..."
    if [ ! -z "$SERVER_PID" ]; then
        echo "Stopping server (PID: $SERVER_PID)..."
        kill $SERVER_PID 2>/dev/null
        wait $SERVER_PID 2>/dev/null
    fi
    # Clean up log file
    rm -f "$SERVER_LOG"
    echo "Cleanup complete."
}

# Set trap to cleanup on script exit
trap cleanup EXIT

# Start the server in the background
echo "Starting PoW server..."
python3 -m uvicorn pow.service.app:app --host 0.0.0.0 --port 8080 --log-level info > "$SERVER_LOG" 2>&1 &
SERVER_PID=$!

echo "Server started with PID: $SERVER_PID"

# Wait for server to be ready
echo "Waiting for server to be ready..."
sleep 10

# Check if server process is still running
if ! kill -0 $SERVER_PID 2>/dev/null; then
    echo "Error: Server process died"
    echo "Server logs:"
    echo "============"
    cat "$SERVER_LOG"
    exit 1
fi

# Check if server is responding
echo "Checking server health..."
for i in {1..30}; do
    if curl -s -f "$SERVER_URL/api/v1/pow/status" > /dev/null 2>&1; then
        echo "Server is responding!"
        break
    fi
    if [ $i -eq 30 ]; then
        echo "Error: Server is not responding after 30 attempts"
        echo "Server logs:"
        echo "============"
        cat "$SERVER_LOG"
        exit 1
    fi
    echo "Attempt $i/30: Server not ready yet, waiting..."
    sleep 2
done

echo ""
echo "Server is ready and running!"
echo "Press Ctrl+C to stop the server..."

# Keep the script running and show server logs
tail -f "$SERVER_LOG" 