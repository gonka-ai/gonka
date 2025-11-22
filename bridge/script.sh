#!/bin/sh
set -e
# Ensure we can detect failures within pipelines when supported
set -o pipefail 2>/dev/null || true

# Allow configuring internal ports per-container (no host exposure)
# Defaults match client defaults to preserve existing behavior
GETH_HTTP_PORT=${GETH_HTTP_PORT:-8545}
GETH_AUTHRPC_PORT=${GETH_AUTHRPC_PORT:-8551}
GETH_P2P_PORT=${GETH_P2P_PORT:-30303}
GETH_DISCOVERY_PORT=${GETH_DISCOVERY_PORT:-$GETH_P2P_PORT}
PRYSM_P2P_TCP_PORT=${PRYSM_P2P_TCP_PORT:-13000}
PRYSM_P2P_UDP_PORT=${PRYSM_P2P_UDP_PORT:-12000}

require_env_vars() {
    missing=false
    for var_name in "$@"; do
        eval "value=\${$var_name:-}"
        if [ -z "$value" ]; then
            echo "Error: required environment variable $var_name is not set" >&2
            missing=true
        fi
    done
    if [ "$missing" = "true" ]; then
        echo "Exiting because required environment variables are missing." >&2
        exit 1
    fi
}

require_env_vars \
    JWT_SECRET_PATH \
    GETH_DATA_DIR \
    PRYSM_DATA_DIR \
    BRIDGE_POSTBLOCK \
    BRIDGE_GETADDRESSES \
    BEACON_STATE_URL

# Create log directories
mkdir -p /var/log/geth /var/log/prysm
# Define Prysm log file paths (raw and formatted)
PRYSM_RAW_LOG=/var/log/prysm/beacon.raw.log
PRYSM_FORMATTED_LOG=/var/log/prysm/beacon.log

echo "Initializing Ethereum Bridge Service Version 0.1.0"

# Generate JWT secret if it doesn't exist
if [ ! -f "$JWT_SECRET_PATH" ]; then
    openssl rand -hex 32 > "$JWT_SECRET_PATH"
    echo "Generated new JWT secret"
fi

# Handle persistent Geth data
if [ -n "$PERSISTENT_DB_DIR" ] && [ -d "$PERSISTENT_DB_DIR" ] && [ -n "$(ls -A "$PERSISTENT_DB_DIR")" ]; then
    echo "Copying Geth data from persistent storage..."
    # Copy contents directly to the mounted directory
    cp -r "$PERSISTENT_DB_DIR/geth/." "$GETH_DATA_DIR/"
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



# Function to start Geth
is_pid_alive() {
    if [ -n "$1" ] && kill -0 "$1" 2>/dev/null; then
        return 0
    fi
    return 1
}
stop_process() {
    local name=$1
    local pid=$2
    if [ -z "$pid" ]; then
        return 0
    fi
    if ! is_pid_alive "$pid"; then
        return 0
    fi
    echo "Stopping $name (PID: $pid)"
    kill "$pid" 2>/dev/null || true
    for i in 1 2 3 4 5 6 7 8 9 10; do
        if ! is_pid_alive "$pid"; then
            break
        fi
        sleep 1
    done
    if is_pid_alive "$pid"; then
        echo "$name did not exit in time, sending SIGKILL"
        kill -9 "$pid" 2>/dev/null || true
        sleep 1
    fi
}

describe_exit() {
    # Print a human-readable reason for a process exit code
    local exit_code=$1
    if [ "$exit_code" -eq 0 ]; then
        echo "exited cleanly (code 0)"
        return 0
    fi
    if [ "$exit_code" -ge 128 ] 2>/dev/null; then
        local signal=$((exit_code - 128))
        case "$signal" in
            9)  echo "terminated by SIGKILL (9) — likely OOMKilled" ;;
            11) echo "terminated by SIGSEGV (11) — segmentation fault" ;;
            6)  echo "terminated by SIGABRT (6) — abort" ;;
            15) echo "terminated by SIGTERM (15)" ;;
            *)  echo "terminated by signal $signal (exit $exit_code)" ;;
        esac
    else
        echo "exited with status $exit_code"
    fi
}


# Wait up to N seconds for a PID to stay alive (handles immediate crash-on-start)
wait_until_alive() {
    local pid=$1
    local timeout=${2:-5}
    local elapsed=0
    while [ $elapsed -lt $timeout ]; do
        if ! is_pid_alive "$pid"; then
            return 1
        fi
        sleep 1
        elapsed=$((elapsed+1))
    done
    return 0
}

start_geth() {
    echo "Starting Geth..."
    geth --datadir $GETH_DATA_DIR \
         --http \
         --http.addr 0.0.0.0 \
         --http.port $GETH_HTTP_PORT \
         --http.api "eth,net,engine" \
          --ipcdisable \
         --bridge.postblock $BRIDGE_POSTBLOCK \
         --bridge.getaddresses $BRIDGE_GETADDRESSES \
         --authrpc.addr 127.0.0.1 \
         --authrpc.port $GETH_AUTHRPC_PORT \
         --authrpc.jwtsecret $JWT_SECRET_PATH \
         --port $GETH_P2P_PORT \
         --discovery.port $GETH_DISCOVERY_PORT 2>&1 | /tmp/log_formatters/geth_formatter.sh > /var/log/geth/geth.log &
    
    GETH_PID=$!
    # Ensure it didn't crash immediately
    if wait_until_alive "$GETH_PID" 3; then
        echo "Geth started with PID: $GETH_PID"
    else
        echo "Geth failed to stay alive after start (PID: $GETH_PID)"
        # Attempt stale lock recovery if present
        LOCK_FILE="$GETH_DATA_DIR/chaindata/LOCK"
        if [ -f "$LOCK_FILE" ]; then
            echo "Detected stale DB lock at $LOCK_FILE; removing and retrying once"
            rm -f "$LOCK_FILE" || true
            sleep 1
            # Retry once
            geth --datadir $GETH_DATA_DIR \
                 --http \
                 --http.addr 0.0.0.0 \
                 --http.port $GETH_HTTP_PORT \
                 --http.api "eth,net,engine" \
                 --ipcdisable \
                 --bridge.postblock $BRIDGE_POSTBLOCK \
                 --bridge.getaddresses $BRIDGE_GETADDRESSES \
                 --authrpc.addr 127.0.0.1 \
                 --authrpc.port $GETH_AUTHRPC_PORT \
                 --authrpc.jwtsecret $JWT_SECRET_PATH \
                 --port $GETH_P2P_PORT \
                 --discovery.port $GETH_DISCOVERY_PORT 2>&1 | /tmp/log_formatters/geth_formatter.sh > /var/log/geth/geth.log &
            GETH_PID=$!
            if wait_until_alive "$GETH_PID" 3; then
                echo "Geth started after stale lock recovery (PID: $GETH_PID)"
                return 0
            fi
        fi
        return 1
    fi
}

# Function to start Prysm
start_prysm() {
    echo "Starting Prysm beacon chain..."
    FORCE_CLEAR=""
    if [ "$DEBUG" != "true" ]; then
        FORCE_CLEAR="--force-clear-db"
        echo "Force clear DB enabled (set DEBUG=true to disable)"
    fi

    # Truncate raw log on each (re)start to make crash context clear
    : > "$PRYSM_RAW_LOG"

    # Start Prysm and redirect its stdout/stderr directly to the raw log
    if command -v stdbuf >/dev/null 2>&1; then
        stdbuf -oL -eL \
        beacon-chain \
            --accept-terms-of-use \
            $FORCE_CLEAR \
            --checkpoint-sync-url=$BEACON_STATE_URL \
            --execution-endpoint=http://127.0.0.1:$GETH_AUTHRPC_PORT \
            --datadir $PRYSM_DATA_DIR \
            --p2p-tcp-port=$PRYSM_P2P_TCP_PORT \
            --p2p-udp-port=$PRYSM_P2P_UDP_PORT \
            --jwt-secret $JWT_SECRET_PATH >> "$PRYSM_RAW_LOG" 2>&1 &
    else
        beacon-chain \
            --accept-terms-of-use \
            $FORCE_CLEAR \
            --checkpoint-sync-url=$BEACON_STATE_URL \
            --execution-endpoint=http://127.0.0.1:$GETH_AUTHRPC_PORT \
            --datadir $PRYSM_DATA_DIR \
            --p2p-tcp-port=$PRYSM_P2P_TCP_PORT \
            --p2p-udp-port=$PRYSM_P2P_UDP_PORT \
            --jwt-secret $JWT_SECRET_PATH >> "$PRYSM_RAW_LOG" 2>&1 &
    fi

    PRYSM_PID=$!
    PRYSM_EXIT_STATUS=""  # reset any previous exit code

    # (Re)start log formatter to transform raw Prysm logs into the formatted file
    if [ -n "$PRYSM_FORMATTER_PID" ]; then
        stop_process "Prysm log formatter" "$PRYSM_FORMATTER_PID"
    fi
    tail -n +1 -F "$PRYSM_RAW_LOG" | /tmp/log_formatters/prysm_formatter.sh > "$PRYSM_FORMATTED_LOG" &
    PRYSM_FORMATTER_PID=$!

    if wait_until_alive "$PRYSM_PID" 3; then
        echo "Prysm beacon chain started with PID: $PRYSM_PID"
    else
        echo "Prysm failed to stay alive after start (PID: $PRYSM_PID)"
        # Capture immediate failure reason if available
        if wait "$PRYSM_PID" 2>/dev/null; then
            PRYSM_EXIT_STATUS=$?
        fi
        return 1
    fi
}

# Function to restart both processes
restart_processes() {
    echo "Restarting both processes..."
    
    # Kill existing processes if they exist
    stop_process "Geth" "$GETH_PID"
    stop_process "Prysm" "$PRYSM_PID"
    stop_process "Prysm log formatter" "$PRYSM_FORMATTER_PID"
    
    # Start processes in correct order (Geth first, then Prysm)
    if start_geth; then
        sleep 3  # Give Geth time to start
        if start_prysm; then
            echo "Both processes restarted successfully"
        else
            echo "Prysm restart failed; will retry in monitor loop"
        fi
    else
        echo "Geth restart failed; will retry in monitor loop"
    fi
}

# Function to check if processes are still running and restart if needed
check_and_restart_processes() {
    local restart_needed=false
    local geth_died=false
    local prysm_died=false
    
    # Check Geth
    if [ -n "$GETH_PID" ] && ! kill -0 $GETH_PID 2>/dev/null; then
        echo "Geth process (PID: $GETH_PID) died"
        geth_died=true
        restart_needed=true
    fi
    
    # Check Prysm
    if [ -n "$PRYSM_PID" ] && ! kill -0 $PRYSM_PID 2>/dev/null; then
        # Retrieve and report the exit status, once
        if [ -z "$PRYSM_EXIT_STATUS" ]; then
            if wait $PRYSM_PID 2>/dev/null; then
                PRYSM_EXIT_STATUS=$?
            else
                PRYSM_EXIT_STATUS=$?
            fi
        fi
        echo "Prysm process (PID: $PRYSM_PID) $(describe_exit ${PRYSM_EXIT_STATUS:-"unknown"})"
        # Show recent raw logs to aid debugging
        if [ -f "$PRYSM_RAW_LOG" ]; then
            echo "--- Last 100 lines of Prysm raw log ---"
            tail -n 100 "$PRYSM_RAW_LOG" | sed 's/^/PRSM: /'
            echo "--- End Prysm raw log excerpt ---"
        fi
        prysm_died=true
        restart_needed=true
        # Stop formatter if still running; it will be restarted with Prysm
        stop_process "Prysm log formatter" "$PRYSM_FORMATTER_PID"
    fi
    
    # Restart if either process died
    if [ "$restart_needed" = "true" ]; then
        echo "Restarting processes due to crash..."
        restart_processes
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

# Start both processes initially (Geth first, then Prysm)
start_geth
sleep 3  # Give Geth time to start
start_prysm


# Start showing logs
tail_logs

# Trap to handle termination
trap "echo 'Received termination signal, shutting down...'; kill $GETH_PID $PRYSM_PID $TAIL_PID 2>/dev/null; exit 0" SIGTERM SIGINT
trap "echo 'Received termination signal, shutting down...'; kill $GETH_PID $PRYSM_PID $PRYSM_FORMATTER_PID $TAIL_PID 2>/dev/null; exit 0" SIGTERM SIGINT

# Main loop to keep container running and check process health
echo "Bridge service started. Monitoring processes..."
while true; do
    check_and_restart_processes
    sleep 5
done 