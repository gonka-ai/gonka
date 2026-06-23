#!/usr/bin/env bash
# Sync Gonka BLS epoch group keys to the Ethereum BridgeContract.
#
# Steady state (NORMAL_OPERATION): finds the highest Gonka epoch whose BLS
# validation_signature is ready (sequential from bridge+1), then calls
# bridge-enable-normal-op.js with --target-epoch. Does not submit while BLS
# for the next epoch is still missing (e.g. dashboard latest during PoC).
#
# Usage:
#   source /srv/dai/bridge-epoch-sync.env   # or copy bridge-epoch-sync.env.example
#   ./bridge-epoch-sync.sh --once            # single attempt (cron)
#   ./bridge-epoch-sync.sh                   # loop (systemd)
#
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

ONCE=false
while [[ $# -gt 0 ]]; do
    case "$1" in
        --once) ONCE=true; shift ;;
        -h|--help)
            sed -n '2,12p' "$0"
            exit 0
            ;;
        *) echo "Unknown argument: $1" >&2; exit 1 ;;
    esac
done

ENV_FILE="${BRIDGE_EPOCH_SYNC_ENV:-/srv/dai/bridge-epoch-sync.env}"
if [[ -f "$ENV_FILE" ]]; then
    # shellcheck disable=SC1090
    source "$ENV_FILE"
elif [[ -f "$SCRIPT_DIR/bridge-epoch-sync.env" ]]; then
    # shellcheck disable=SC1091
    source "$SCRIPT_DIR/bridge-epoch-sync.env"
fi

resolve_gonka_root() {
    local candidate
    for candidate in \
        "$SCRIPT_DIR/gonka" \
        "$SCRIPT_DIR" \
        "$(cd "$SCRIPT_DIR/../gonka" 2>/dev/null && pwd || true)"; do
        if [[ -n "$candidate" && -f "$candidate/proposals/ethereum-bridge-contact/package.json" ]]; then
            echo "$candidate"
            return 0
        fi
    done
    if [[ -f "$SCRIPT_DIR/../../../proposals/ethereum-bridge-contact/package.json" ]]; then
        cd "$SCRIPT_DIR/../../.." && pwd
        return 0
    fi
    return 1
}

GONKA_ROOT="$(resolve_gonka_root || true)"

if [[ -n "${BRIDGE_CONTRACT_DIR:-}" ]]; then
    :
elif [[ -n "${BRIDGE_DIR:-}" ]]; then
    BRIDGE_CONTRACT_DIR="$BRIDGE_DIR"
elif [[ -n "$GONKA_ROOT" ]]; then
    BRIDGE_CONTRACT_DIR="$GONKA_ROOT/proposals/ethereum-bridge-contact"
else
    BRIDGE_CONTRACT_DIR="$SCRIPT_DIR/proposals/ethereum-bridge-contact"
fi

if [[ -n "${ENABLE_SCRIPT:-}" ]]; then
    :
elif [[ -n "$GONKA_ROOT" && -f "$GONKA_ROOT/test-net-cloud/nebius/bridge/bridge-enable-normal-op.js" ]]; then
    ENABLE_SCRIPT="$GONKA_ROOT/test-net-cloud/nebius/bridge/bridge-enable-normal-op.js"
elif [[ -f "$SCRIPT_DIR/bridge-enable-normal-op.js" ]]; then
    ENABLE_SCRIPT="$SCRIPT_DIR/bridge-enable-normal-op.js"
else
    ENABLE_SCRIPT="$SCRIPT_DIR/test-net-cloud/nebius/bridge/bridge-enable-normal-op.js"
fi

GONKA_API="${GONKA_API:-http://localhost:8000}"
SEPOLIA_RPC_URL="${SEPOLIA_RPC_URL:-https://ethereum-sepolia-rpc.publicnode.com}"
POLL_SECONDS="${POLL_SECONDS:-30}"
LOG_FILE="${LOG_FILE:-/var/log/bridge-epoch-sync.log}"
VERBOSE="${VERBOSE:-0}"
HEARTBEAT_EVERY="${HEARTBEAT_EVERY:-1}"

require_var() {
    if [[ -z "${!1:-}" ]]; then
        echo "Missing required variable: $1 (set in $ENV_FILE)" >&2
        exit 1
    fi
}

require_var PRIVATE_KEY
require_var BRIDGE_ADDRESS

if [[ ! -d "$BRIDGE_CONTRACT_DIR" ]]; then
    echo "ERR  BRIDGE_CONTRACT_DIR not found: $BRIDGE_CONTRACT_DIR" >&2
    echo "     Add to $ENV_FILE: BRIDGE_DIR=/srv/dai/gonka/proposals/ethereum-bridge-contact" >&2
    exit 1
fi

if [[ ! -f "$ENABLE_SCRIPT" ]]; then
    echo "ERR  bridge-enable-normal-op.js not found: $ENABLE_SCRIPT" >&2
    echo "     Add to $ENV_FILE: ENABLE_SCRIPT=/srv/dai/gonka/test-net-cloud/nebius/bridge/bridge-enable-normal-op.js" >&2
    exit 1
fi

POLL_COUNT=0
LAST_GONKA_EPOCH=""
LAST_GONKA_LATEST=""
LAST_BRIDGE_EPOCH=""
LAST_STATUS=""

log_line() {
    local line
    line="$(date -Is) $*"
    echo "$line"
    if [[ -n "$LOG_FILE" ]]; then
        local log_dir
        log_dir="$(dirname "$LOG_FILE")"
        if [[ -w "$log_dir" || ! -e "$LOG_FILE" ]]; then
            echo "$line" >>"$LOG_FILE" 2>/dev/null || true
        fi
    fi
}

log_info()  { log_line "INFO  $*"; }
log_ok()    { log_line "OK    $*"; }
log_warn()  { log_line "WARN  $*"; }
log_wait()  { log_line "WAIT  $*"; }
log_err()   { log_line "ERR   $*"; }

log_detail() {
    if [[ "$VERBOSE" == "1" ]]; then
        while IFS= read -r line; do
            log_line "DETAIL $line"
        done
    fi
}

get_gonka_effective_epoch() {
    curl -sf --max-time 15 \
        "${GONKA_API%/}/chain-api/productscience/inference/inference/get_current_epoch" \
        | jq -r '.epoch // empty'
}

# Latest epoch matches the validator dashboard (epoch_info). During PoC it is
# effective+1 until SetNewValidatorsStage flips the effective pointer.
get_gonka_latest_epoch() {
    curl -sf --max-time 15 \
        "${GONKA_API%/}/chain-api/productscience/inference/inference/epoch_info" \
        | jq -r '.latest_epoch.index // empty'
}

bls_epoch_has_validation() {
    local epoch="$1"
    local sig
    sig="$(curl -sf --max-time 15 \
        "${GONKA_API%/}/chain-api/productscience/inference/bls/epoch_data/${epoch}" \
        | jq -r '.epoch_data.validation_signature // empty')" || true
    [[ -n "$sig" && "$sig" != "null" ]]
}

# Highest epoch we can submit on Ethereum: bridge+1 .. latest, stopping at the
# first epoch without validation_signature (epochs must be submitted in order).
get_gonka_bridge_target_epoch() {
    local bridge_epoch="$1"
    local latest_epoch="$2"
    local target="${bridge_epoch:-0}"
    local epoch

    if [[ -z "$bridge_epoch" || "$bridge_epoch" == "?" ]]; then
        echo "$latest_epoch"
        return 0
    fi

    epoch=$((bridge_epoch + 1))
    while (( epoch <= latest_epoch )); do
        if bls_epoch_has_validation "$epoch"; then
            target="$epoch"
            epoch=$((epoch + 1))
        else
            break
        fi
    done
    echo "$target"
}

get_bridge_epoch() {
  ensure_node_deps >/dev/null 2>&1 || true
  (
    cd "$BRIDGE_CONTRACT_DIR"
    export NODE_PATH=./node_modules
    export SEPOLIA_RPC_URL
    export BRIDGE_ADDRESS
    node <<'NODE'
const { ethers } = require("ethers");
(async () => {
  const provider = new ethers.JsonRpcProvider(process.env.SEPOLIA_RPC_URL);
  const abi = ["function getLatestEpochInfo() view returns (uint64 epochId, uint64 timestamp, bytes groupKey)"];
  const bridge = new ethers.Contract(process.env.BRIDGE_ADDRESS, abi, provider);
  const info = await bridge.getLatestEpochInfo();
  process.stdout.write(info.epochId.toString());
})().catch(() => process.exit(1));
NODE
  ) 2>/dev/null || echo ""
}

ensure_node_deps() {
    if [[ ! -d "$BRIDGE_CONTRACT_DIR/node_modules/ethers" ]]; then
        log_info "Installing npm deps in $BRIDGE_CONTRACT_DIR"
        (cd "$BRIDGE_CONTRACT_DIR" && npm install --silent)
    fi
}

run_sync() {
    local target_epoch="$1"
    ensure_node_deps
    (
        cd "$BRIDGE_CONTRACT_DIR"
        export NODE_PATH=./node_modules
        export PRIVATE_KEY
        export SEPOLIA_RPC_URL
        export BRIDGE_ADDRESS
        node "$ENABLE_SCRIPT" \
            --bridge "$BRIDGE_ADDRESS" \
            --api "$GONKA_API" \
            --rpc "$SEPOLIA_RPC_URL" \
            --target-epoch "$target_epoch"
    )
}

# Run sync, tee output to a temp file for parsing, and log key lines live.
run_sync_capture() {
    local target_epoch="$1"
    local output_file rc line
    output_file="$(mktemp)"

    set +e
    run_sync "$target_epoch" 2>&1 | tee "$output_file" | while IFS= read -r line; do
        case "$line" in
            Submitting\ epoch*|Tx:*|Confirmed\ in\ block*|SUCCESS:*|Fatal\ error*)
                log_info "sync $line"
                ;;
        esac
    done
    rc="${PIPESTATUS[0]}"
    set -e

    cat "$output_file"
    rm -f "$output_file"
    return "$rc"
}

extract_tx_hashes() {
    grep -oE '0x[0-9a-fA-F]{64}' || true
}

should_heartbeat() {
    local status="$1"
    if [[ "$status" != "caught_up" ]]; then
        return 0
    fi
    if [[ "$HEARTBEAT_EVERY" -le 1 ]]; then
        return 0
    fi
    (( POLL_COUNT % HEARTBEAT_EVERY == 0 ))
}

sync_once() {
    local gonka_effective gonka_latest gonka_target bridge_epoch behind output rc status tx_hashes
  POLL_COUNT=$((POLL_COUNT + 1))

    gonka_effective="$(get_gonka_effective_epoch)" || true
    gonka_latest="$(get_gonka_latest_epoch)" || true
    if [[ -z "$gonka_latest" || "$gonka_latest" == "null" ]]; then
        log_warn "poll=$POLL_COUNT could not read latest Gonka epoch from ${GONKA_API%/}/chain-api/.../epoch_info"
        return 1
    fi

    bridge_epoch="$(get_bridge_epoch)" || true
    gonka_target="$(get_gonka_bridge_target_epoch "$bridge_epoch" "$gonka_latest")"

    if [[ -z "$bridge_epoch" ]]; then
        bridge_epoch="?"
        behind="?"
    else
        behind=$((gonka_target - bridge_epoch))
        if (( behind < 0 )); then
            behind=0
        fi
    fi

    if [[ -n "$gonka_effective" && "$gonka_effective" != "$gonka_latest" ]]; then
        log_info "poll=$POLL_COUNT gonka_ready=$gonka_target gonka_effective=$gonka_effective gonka_latest=$gonka_latest bridge_epoch=$bridge_epoch behind=$behind bridge=$BRIDGE_ADDRESS"
    else
        log_info "poll=$POLL_COUNT gonka_ready=$gonka_target bridge_epoch=$bridge_epoch behind=$behind bridge=$BRIDGE_ADDRESS"
    fi

    if [[ "$behind" == "0" ]]; then
        status="caught_up"
        if [[ "$gonka_target" != "$LAST_GONKA_EPOCH" || "$bridge_epoch" != "$LAST_BRIDGE_EPOCH" || "$gonka_latest" != "$LAST_GONKA_LATEST" || "$status" != "$LAST_STATUS" ]]; then
            if [[ "$gonka_latest" != "$gonka_target" ]]; then
                log_info "poll=$POLL_COUNT caught up; PoC in progress (gonka_latest=$gonka_latest BLS pending, bridge=$bridge_epoch)"
            else
                log_info "poll=$POLL_COUNT caught up (gonka_ready=$gonka_target bridge=$bridge_epoch)"
            fi
        elif should_heartbeat "$status"; then
            if [[ "$gonka_latest" != "$gonka_target" ]]; then
                log_info "poll=$POLL_COUNT heartbeat caught_up gonka_ready=$gonka_target gonka_latest=$gonka_latest bridge=$bridge_epoch (BLS pending)"
            else
                log_info "poll=$POLL_COUNT heartbeat caught_up gonka_ready=$gonka_target bridge=$bridge_epoch"
            fi
        fi
        LAST_GONKA_EPOCH="$gonka_target"
        LAST_GONKA_LATEST="$gonka_latest"
        LAST_BRIDGE_EPOCH="$bridge_epoch"
        LAST_STATUS="$status"
        return 0
    fi

    if [[ "$behind" != "?" ]]; then
        log_info "poll=$POLL_COUNT catching up: submitting up to $behind epoch(s) on Ethereum (may take several minutes)..."
    fi

    set +e
    output="$(run_sync_capture "$gonka_target")"
    rc=$?
    set -e

    if [[ "$VERBOSE" == "1" ]]; then
        echo "$output" | log_detail
    fi

    status="unknown"
    if [[ $rc -eq 0 ]]; then
        if echo "$output" | grep -q "SUCCESS: Bridge epochs synced"; then
            status="synced"
            tx_hashes="$(echo "$output" | grep -E '^Tx:' | extract_tx_hashes | tr '\n' ' ')"
            log_ok "poll=$POLL_COUNT submitted epoch(s) through gonka_ready=$gonka_target bridge_epoch_was=$bridge_epoch tx=${tx_hashes:-n/a}"
            echo "$output" | grep -E '^(Submitting epoch|Tx:|Confirmed|SUCCESS)' | while IFS= read -r line; do
                log_info "$line"
            done
            LAST_GONKA_EPOCH="$gonka_target"
            LAST_BRIDGE_EPOCH="$gonka_target"
            LAST_STATUS="$status"
            return 0
        fi
        if echo "$output" | grep -q "SUCCESS: Bridge is now in NORMAL_OPERATION"; then
            status="bootstrapped"
            tx_hashes="$(echo "$output" | grep -E '^Tx:' | extract_tx_hashes | tr '\n' ' ')"
            log_ok "poll=$POLL_COUNT bridge entered NORMAL_OPERATION gonka_ready=$gonka_target tx=${tx_hashes:-n/a}"
            echo "$output" | grep -E '^(Tx:|Confirmed|SUCCESS|Resetting)' | while IFS= read -r line; do
                log_info "$line"
            done
            LAST_STATUS="$status"
            return 0
        fi
        if echo "$output" | grep -q "already in NORMAL_OPERATION"; then
            status="caught_up"
            if [[ "$gonka_target" != "$LAST_GONKA_EPOCH" || "$bridge_epoch" != "$LAST_BRIDGE_EPOCH" || "$status" != "$LAST_STATUS" ]]; then
                if [[ -n "$gonka_effective" && "$gonka_effective" != "$gonka_latest" ]]; then
                    log_info "poll=$POLL_COUNT caught up (gonka_ready=$gonka_target gonka_effective=$gonka_effective gonka_latest=$gonka_latest bridge=$bridge_epoch)"
                else
                    log_info "poll=$POLL_COUNT caught up (gonka_ready=$gonka_target bridge=$bridge_epoch)"
                fi
            elif should_heartbeat "$status"; then
                if [[ -n "$gonka_effective" && "$gonka_effective" != "$gonka_latest" ]]; then
                    log_info "poll=$POLL_COUNT heartbeat caught_up gonka_ready=$gonka_target gonka_effective=$gonka_effective gonka_latest=$gonka_latest bridge=$bridge_epoch"
                else
                    log_info "poll=$POLL_COUNT heartbeat caught_up gonka_ready=$gonka_target bridge=$bridge_epoch"
                fi
            fi
            LAST_GONKA_EPOCH="$gonka_target"
            LAST_BRIDGE_EPOCH="$bridge_epoch"
            LAST_STATUS="$status"
            return 0
        fi
    fi

    if echo "$output" | grep -qiE 'validation_signature is missing|missing validation_signature'; then
        status="bls_not_ready"
        log_wait "poll=$POLL_COUNT gonka_ready=$gonka_target bridge_epoch=$bridge_epoch — BLS validation_signature not ready yet"
        LAST_STATUS="$status"
        return 1
    fi

    if echo "$output" | grep -qiE 'Fatal error|Signer is not the BridgeContract owner|No contract found'; then
        status="fatal"
        log_err "poll=$POLL_COUNT gonka_ready=$gonka_target rc=$rc"
        echo "$output" | while IFS= read -r line; do
            log_err "$line"
        done
        LAST_STATUS="$status"
        return "$rc"
    fi

    status="error"
    log_err "poll=$POLL_COUNT gonka_ready=$gonka_target bridge_epoch=$bridge_epoch rc=$rc unexpected response"
    echo "$output" | tail -20 | while IFS= read -r line; do
        log_err "$line"
    done
    LAST_STATUS="$status"
    return "${rc:-1}"
}

if [[ "$ONCE" == true ]]; then
    sync_once
    exit $?
fi

log_info "Starting bridge epoch sync loop poll=${POLL_SECONDS}s api=$GONKA_API rpc=$SEPOLIA_RPC_URL"
log_info "Paths gonka_root=${GONKA_ROOT:-n/a} contract_dir=$BRIDGE_CONTRACT_DIR enable_script=$ENABLE_SCRIPT"
log_info "Config bridge=$BRIDGE_ADDRESS log_file=$LOG_FILE verbose=$VERBOSE heartbeat_every=$HEARTBEAT_EVERY"

while true; do
    sync_once || true
    log_info "poll=$POLL_COUNT done; sleeping ${POLL_SECONDS}s"
    sleep "$POLL_SECONDS"
done
