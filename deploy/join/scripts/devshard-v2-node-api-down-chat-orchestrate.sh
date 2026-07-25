#!/usr/bin/env bash
# Thin wrapper: stop node+api fleet-wide, chat (expect failure), finalize after restart.
#
#   APPROVE=1 ./scripts/devshard-v2-node-api-down-chat-orchestrate.sh run
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
JOIN_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

export HALT_SERVICES=node,api
export EXPECT_CHAT_FAIL=1
export HOSTS_FILTER="${HOSTS_FILTER:-testnet-4}"
export LOG_FILE="${LOG_FILE:-/tmp/devshard-v2-node-api-down-chat.log}"
export HAPPY_STATE="${HAPPY_STATE:-/tmp/devshard-v2-node-api-down-chat.happy.state}"
export STATE_FILE="${STATE_FILE:-/tmp/devshard-v2-node-api-down-chat.state}"
export CHAT_LOG="${CHAT_LOG:-/tmp/devshard-v2-node-api-down-chat.sse}"
export CHAT_META="${CHAT_META:-/tmp/devshard-v2-node-api-down-chat.meta}"
export CHAT_PID_FILE="${CHAT_PID_FILE:-/tmp/devshard-v2-node-api-down-chat.pid}"
export CHAT_DONE="${CHAT_DONE:-/tmp/devshard-v2-node-api-down-chat.done}"
export PAYLOAD_FILE="${PAYLOAD_FILE:-/tmp/devshard-v2-node-api-down-chat.payload.json}"
export CHAT_RUNNER_LOG="${CHAT_RUNNER_LOG:-/tmp/devshard-v2-node-api-down-chat-runner.log}"

echo "[node-api-down-wrapper] ensuring node+api up on ${HOSTS_FILTER} before experiment"
"$JOIN_DIR/scripts/prepare-compose-restart.sh" coordinated-start "$HALT_SERVICES" "$HOSTS_FILTER" | tee -a "$LOG_FILE"

exec "$SCRIPT_DIR/devshard-v2-node-down-chat-orchestrate.sh" "${@:-run}"
