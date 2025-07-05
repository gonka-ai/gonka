#!/bin/sh
set -e

# Create log directories
mkdir -p /var/log/geth /var/log/prysm

echo "Initializing Ethereum Bridge Service Version 0.1.0"

# Generate JWT secret if it doesn't exist
if [ ! -f $JWT_SECRET_PATH ]; then
    openssl rand -hex 32 > $JWT_SECRET_PATH
    echo "Generated new JWT secret"
fi

# Handle persistent Geth data
if [ -d "$PERSISTENT_DB_DIR" ] && [ -n "$(ls -A $PERSISTENT_DB_DIR)" ]; then
    echo "Copying Geth data from persistent storage..."
    # Copy contents directly to the mounted directory
    cp -r $PERSISTENT_DB_DIR/geth/. $GETH_DATA_DIR/
    echo "Copied Geth data to $GETH_DATA_DIR/"
fi

# Create log processing scripts
mkdir -p /tmp/log_formatters

# Create Geth log formatter
cat > /tmp/log_formatters/geth_formatter.sh << 'EOL'
#!/bin/sh
while IFS= read -r line; do
  echo "GETH: $line"
done
EOL
chmod +x /tmp/log_formatters/geth_formatter.sh

# Create Prysm log formatter
cat > /tmp/log_formatters/prysm_formatter.sh << 'EOL'
#!/bin/sh
while IFS= read -r line; do
  # Extract level and reformat to uppercase at the beginning
  if echo "$line" | grep -q 'level='; then
    level=$(echo "$line" | sed -E 's/.*level=([^ ]+).*/\1/')
    level_upper=$(echo "$level" | tr '[:lower:]' '[:upper:]')
    
    # Extract time and format it in brackets
    timestamp=$(echo "$line" | sed -E 's/.*time="([^"]+)".*/\1/')
    # Extract date components manually to avoid dependencies on specific date command options
    month=$(echo "$timestamp" | cut -d'-' -f2)
    day=$(echo "$timestamp" | cut -d'-' -f3 | cut -d' ' -f1)
    time=$(echo "$timestamp" | cut -d' ' -f2 | cut -d'.' -f1)
    
    # Extract message and the rest of the parameters
    msg=$(echo "$line" | sed -E 's/.*msg="([^"]+)".*/\1/')
    params=$(echo "$line" | sed -E 's/.*msg="[^"]+"(.*)/\1/')
    
    # Reconstructed line in Geth format
    echo "PRSM: $level_upper [$month-$day|$time.000] $msg$params"
  else
    echo "PRSM: $line"
  fi
done
EOL
chmod +x /tmp/log_formatters/prysm_formatter.sh

# Start Geth in the background and redirect output to log file
echo "Starting Geth..."
geth --datadir $GETH_DATA_DIR \
     --http \
     --http.addr 0.0.0.0 \
     --http.api "eth,net,engine" \
     --bridge.endpoints $BRIDGE_API_URL \
     --bridge.contract $BRIDGE_CONTRACT \
     --authrpc.jwtsecret $JWT_SECRET_PATH 2>&1 | /tmp/log_formatters/geth_formatter.sh > /var/log/geth/geth.log &

GETH_PID=$!
echo "Geth started with PID: $GETH_PID"

# Start Prysm beacon chain in the background and redirect output to log file
echo "Starting Prysm beacon chain..."
FORCE_CLEAR=""
if [ "$DEBUG" != "true" ]; then
    FORCE_CLEAR="--force-clear-db"
    echo "Force clear DB enabled (set DEBUG=true to disable)"
fi

beacon-chain \
    --accept-terms-of-use \
    $FORCE_CLEAR \
    --checkpoint-sync-url=$BEACON_STATE_URL \
    --execution-endpoint=http://127.0.0.1:8551 \
    --datadir $PRYSM_DATA_DIR \
    --jwt-secret $JWT_SECRET_PATH 2>&1 | /tmp/log_formatters/prysm_formatter.sh > /var/log/prysm/beacon.log &

PRYSM_PID=$!
echo "Prysm beacon chain started with PID: $PRYSM_PID"

# Function to check if processes are still running
check_processes() {
    if ! kill -0 $GETH_PID 2>/dev/null; then
        echo "Geth process died. Exiting..."
        exit 1
    fi
    if ! kill -0 $PRYSM_PID 2>/dev/null; then
        echo "Prysm process died. Exiting..."
        exit 1
    fi
}

# Function to display logs
tail_logs() {
    # Combine logs without filename headers
    (
      echo "=== Starting combined log output ==="
      tail -f /var/log/geth/geth.log /var/log/prysm/beacon.log | while IFS= read -r line; do
        # Skip lines that look like file headers from tail
        if ! echo "$line" | grep -q "^==> .* <==$"; then
          echo "$line"
        fi
      done
    ) &
    TAIL_PID=$!
}

# Start showing logs
tail_logs

# Trap to handle termination
trap "kill $GETH_PID $PRYSM_PID $TAIL_PID 2>/dev/null" SIGTERM SIGINT

# Main loop to keep container running and check process health
while true; do
    check_processes
    sleep 5
done 