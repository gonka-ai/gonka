#!/bin/bash
set -euo pipefail

# Deploy community_sale contract and fund it with synthetic testnet USDT.
# Invoked automatically by launch.py --mode genesis after RPC is ready.

export BASE_DIR="${TESTNET_BASE_DIR:-/srv/dai}"
export KEY_DIR="$BASE_DIR/.inference"
export CHAIN_ID="${CHAIN_ID:-gonka-testnet}"
export KEY_NAME="${KEY_NAME:-gonka-account-key}"
export NODE_OPTS="--node http://localhost:8000/chain-rpc/"

BOUNTY_POOL_IBC_DENOM="${BOUNTY_POOL_IBC_DENOM:-ibc/115F68FBA220A028C6F6ED08EA0C1A9C8C52798B14FB66E6C89D5D8C06A524D4}"
BOUNTY_POOL_CHAIN_ID="${BOUNTY_POOL_CHAIN_ID:-kava_2222-10}"
BOUNTY_POOL_AMOUNT="${BOUNTY_POOL_AMOUNT:-1500000000000}"
BOUNTY_POOL_GOV_AUTHORITY="${BOUNTY_POOL_GOV_AUTHORITY:-gonka10d07y265gmmuvt4z0w9aw880jnsr700j2h5m33}"
BOUNTY_POOL_COMMUNITY_SALE_LABEL="${BOUNTY_POOL_COMMUNITY_SALE_LABEL:-community-sale-testnet-v1}"
PASSWORD="${KEYRING_PASSWORD:-12345678}"
STATE_FILE="$BASE_DIR/bounty-pool-state.json"

if [ -f "$BASE_DIR/inferenced" ]; then
    export APP_NAME="$BASE_DIR/inferenced"
else
    export APP_NAME="inferenced"
fi

GONKA_REPO="${BASE_DIR}/gonka"
WASM_PATH="${GONKA_REPO}/inference-chain/contracts/community-sale/artifacts/community_sale.wasm"
if [ ! -f "$WASM_PATH" ]; then
    echo "Error: community_sale.wasm not found at $WASM_PATH"
    exit 1
fi

get_keyring_backend() {
    if printf "%s\n" "$PASSWORD" | "$APP_NAME" keys show "$KEY_NAME" \
        --keyring-backend file --keyring-dir "$KEY_DIR" >/dev/null 2>&1; then
        export KEYRING_BACKEND="file"
        return 0
    fi
    if printf "%s\n" "$PASSWORD" | "$APP_NAME" keys show "$KEY_NAME" \
        --keyring-backend test --keyring-dir "$KEY_DIR" >/dev/null 2>&1; then
        export KEYRING_BACKEND="test"
        return 0
    fi
    echo "Error: Key '$KEY_NAME' not found in $KEY_DIR"
    return 1
}

run_keys_cmd() {
    printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | "$APP_NAME" keys "$@" \
        --keyring-backend "$KEYRING_BACKEND" --home "$KEY_DIR"
}

get_keyring_backend || exit 1

MY_ADDR=$(run_keys_cmd show "$KEY_NAME" -a 2>/dev/null)
if [ -z "$MY_ADDR" ]; then
    echo "Error: Could not retrieve address for key '$KEY_NAME'"
    exit 1
fi

echo "=================================================="
echo "Community Sale Bounty Pool Setup"
echo "Signer:  $MY_ADDR"
echo "Chain:   $CHAIN_ID"
echo "Denom:   $BOUNTY_POOL_IBC_DENOM"
echo "Amount:  $BOUNTY_POOL_AMOUNT"
echo "=================================================="

echo "Storing community_sale.wasm..."
RAW_STORE_OUT=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | "$APP_NAME" tx wasm store "$WASM_PATH" \
    --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
    --keyring-backend "$KEYRING_BACKEND" --home "$KEY_DIR" $NODE_OPTS 2>&1)

STORE_TX=$(echo "$RAW_STORE_OUT" | sed -n '/{/,$p')
TX_HASH=$(echo "$STORE_TX" | jq -r '.txhash' 2>/dev/null || echo "null")
if [ "$TX_HASH" = "null" ] || [ -z "$TX_HASH" ]; then
    echo "Error: WASM store failed"
    echo "$RAW_STORE_OUT"
    exit 1
fi
echo "Store TX: $TX_HASH"

CODE_ID=""
for _ in $(seq 1 20); do
    TX_QUERY=$("$APP_NAME" query tx "$TX_HASH" $NODE_OPTS --output json 2>/dev/null || echo "")
    JSON_QUERY=$(echo "$TX_QUERY" | sed -n '/{/,$p')
    CODE_ID=$(echo "$JSON_QUERY" | jq -r '.events[] | select(.type=="store_code") | .attributes[] | select(.key=="code_id") | .value' 2>/dev/null | head -n1)
    if [ -n "$CODE_ID" ] && [ "$CODE_ID" != "null" ]; then
        break
    fi
    sleep 2
done

if [ -z "$CODE_ID" ] || [ "$CODE_ID" = "null" ]; then
    echo "Error: Could not extract code_id from $TX_HASH"
    exit 1
fi
echo "Code ID: $CODE_ID"

INIT_MSG=$(jq -n \
    --arg admin "$BOUNTY_POOL_GOV_AUTHORITY" \
    --arg buyer "$MY_ADDR" \
    --arg chain_id "$BOUNTY_POOL_CHAIN_ID" \
    --arg ibc_denom "$BOUNTY_POOL_IBC_DENOM" \
    '{
        admin: $admin,
        buyer: $buyer,
        accepted_chain_id: $chain_id,
        accepted_eth_contract: "0x0000000000000000000000000000000000000000",
        accepted_ibc_denom: $ibc_denom,
        price_usd: "1",
        native_denom: "ngonka",
        allow_all_trade_tokens: true
    }')

echo "Instantiating community_sale..."
RAW_INST_OUT=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | "$APP_NAME" tx wasm instantiate "$CODE_ID" "$INIT_MSG" \
    --label "$BOUNTY_POOL_COMMUNITY_SALE_LABEL" \
    --admin "$BOUNTY_POOL_GOV_AUTHORITY" \
    --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
    --keyring-backend "$KEYRING_BACKEND" --home "$KEY_DIR" $NODE_OPTS 2>&1)

INST_TX=$(echo "$RAW_INST_OUT" | sed -n '/{/,$p')
INST_HASH=$(echo "$INST_TX" | jq -r '.txhash' 2>/dev/null || echo "null")
if [ "$INST_HASH" = "null" ] || [ -z "$INST_HASH" ]; then
    echo "Error: Instantiate failed"
    echo "$RAW_INST_OUT"
    exit 1
fi
echo "Instantiate TX: $INST_HASH"

CONTRACT_ADDR=""
for _ in $(seq 1 20); do
    TX_QUERY=$("$APP_NAME" query tx "$INST_HASH" $NODE_OPTS --output json 2>/dev/null || echo "")
    JSON_QUERY=$(echo "$TX_QUERY" | sed -n '/{/,$p')
    CONTRACT_ADDR=$(echo "$JSON_QUERY" | jq -r '.events[] | select(.type=="instantiate") | .attributes[] | select(.key=="_contract_address") | .value' 2>/dev/null | head -n1)
    if [ -n "$CONTRACT_ADDR" ] && [ "$CONTRACT_ADDR" != "null" ]; then
        break
    fi
    sleep 2
done

if [ -z "$CONTRACT_ADDR" ] || [ "$CONTRACT_ADDR" = "null" ]; then
    echo "Error: Could not extract contract address from $INST_HASH"
    exit 1
fi
echo "Community sale contract: $CONTRACT_ADDR"

echo "Checking signer balance for ${BOUNTY_POOL_IBC_DENOM}..."
BALANCES_OUT=$("$APP_NAME" q bank balances "$MY_ADDR" $NODE_OPTS --output json 2>/dev/null || echo "{}")
AVAILABLE_AMOUNT=$(echo "$BALANCES_OUT" | jq -r --arg denom "$BOUNTY_POOL_IBC_DENOM" '.balances[]? | select(.denom == $denom) | .amount' 2>/dev/null | head -n1)
AVAILABLE_AMOUNT="${AVAILABLE_AMOUNT:-0}"
if [ "$AVAILABLE_AMOUNT" = "null" ] || [ -z "$AVAILABLE_AMOUNT" ]; then
    AVAILABLE_AMOUNT="0"
fi
echo "Signer balance: ${AVAILABLE_AMOUNT}${BOUNTY_POOL_IBC_DENOM}"
if [ "$AVAILABLE_AMOUNT" -lt "$BOUNTY_POOL_AMOUNT" ]; then
    echo "Error: signer does not have enough bounty pool funds"
    echo "Required: ${BOUNTY_POOL_AMOUNT}${BOUNTY_POOL_IBC_DENOM}"
    echo "Available: ${AVAILABLE_AMOUNT}${BOUNTY_POOL_IBC_DENOM}"
    exit 1
fi

echo "Funding contract with ${BOUNTY_POOL_AMOUNT}${BOUNTY_POOL_IBC_DENOM}..."
set +e
RAW_SEND_OUT=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | "$APP_NAME" tx bank send "$MY_ADDR" "$CONTRACT_ADDR" \
    "${BOUNTY_POOL_AMOUNT}${BOUNTY_POOL_IBC_DENOM}" \
    --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
    --keyring-backend "$KEYRING_BACKEND" --home "$KEY_DIR" $NODE_OPTS 2>&1)
SEND_RC=$?
set -e

if [ "$SEND_RC" -ne 0 ]; then
    echo "Error: Bank send command failed"
    echo "$RAW_SEND_OUT"
    exit "$SEND_RC"
fi

SEND_TX=$(echo "$RAW_SEND_OUT" | sed -n '/{/,$p')
SEND_HASH=$(echo "$SEND_TX" | jq -r '.txhash' 2>/dev/null || echo "null")
if [ "$SEND_HASH" = "null" ] || [ -z "$SEND_HASH" ]; then
    echo "Error: Bank send failed"
    echo "$RAW_SEND_OUT"
    exit 1
fi
echo "Fund TX: $SEND_HASH"

jq -n \
    --arg addr "$CONTRACT_ADDR" \
    --arg code_id "$CODE_ID" \
    --arg denom "$BOUNTY_POOL_IBC_DENOM" \
    --arg chain_id "$BOUNTY_POOL_CHAIN_ID" \
    --arg amount "$BOUNTY_POOL_AMOUNT" \
    '{
        community_sale_address: $addr,
        community_sale_code_id: $code_id,
        ibc_denom: $denom,
        chain_id: $chain_id,
        amount: $amount
    }' > "$STATE_FILE"

echo "Wrote $STATE_FILE"
echo "Bounty pool ready:"
echo "  contract: $CONTRACT_ADDR"
echo "  denom: $BOUNTY_POOL_IBC_DENOM"
echo "  amount: $BOUNTY_POOL_AMOUNT"
