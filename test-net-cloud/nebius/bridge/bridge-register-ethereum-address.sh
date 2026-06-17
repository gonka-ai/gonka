#!/bin/bash
set -e

# Register one or more bridge contract addresses under chain "ethereum".
# The bridge geth adapter always queries ?chain=ethereum and posts originChain=ethereum,
# even when ETHEREUM_NETWORK=sepolia. WGNK burns on the Sepolia BridgeContract are only
# auto-detected after the contract is registered here.

export BASE_DIR="${TESTNET_BASE_DIR:-/srv/dai}"

if [ -f "$BASE_DIR/inferenced" ]; then
    export APP_NAME="$BASE_DIR/inferenced"
else
    export APP_NAME="inferenced"
fi

export KEY_DIR="$BASE_DIR/.inference"
export CHAIN_ID="${CHAIN_ID:-gonka-testnet}"
export KEY_NAME="${KEY_NAME:-gonka-account-key}"
export NODE_OPTS="--node http://localhost:8000/chain-rpc/"
export BRIDGE_CHAIN_NAME="${BRIDGE_CHAIN_NAME:-ethereum}"

PASSWORD="12345678"
BRIDGE_ADDRESS=""
PROPOSAL_ID_ARG=""

while [[ $# -gt 0 ]]; do
  case $1 in
    --address)
      BRIDGE_ADDRESS="$2"
      shift 2
      ;;
    --chain)
      BRIDGE_CHAIN_NAME="$2"
      shift 2
      ;;
    --password)
      PASSWORD="$2"
      shift 2
      ;;
    --proposal)
      PROPOSAL_ID_ARG="$2"
      shift 2
      ;;
    *)
      echo "Error: Unknown option $1"
      echo "Usage: bridge-register-ethereum-address.sh --address 0x... [--chain ethereum] [--password PASS] [--proposal ID]"
      exit 1
      ;;
  esac
done

if [ -z "$PROPOSAL_ID_ARG" ] && [ -z "$BRIDGE_ADDRESS" ]; then
    echo "Error: --address is required for new proposals."
    exit 1
fi

get_keyring_backend() {
    local pass=$1
    export KEYRING_BACKEND=""
    if printf "%s\n" "$pass" | $APP_NAME keys show "$KEY_NAME" --keyring-backend "file" --keyring-dir "$KEY_DIR" >/dev/null 2>&1; then
        export KEYRING_BACKEND="file"
        return 0
    fi
    if printf "%s\n" "$pass" | $APP_NAME keys show "$KEY_NAME" --keyring-backend "test" --keyring-dir "$KEY_DIR" >/dev/null 2>&1; then
        export KEYRING_BACKEND="test"
        return 0
    fi
    echo "Error: Key '$KEY_NAME' not found in $KEY_DIR"
    return 1
}

run_keys_cmd() {
    printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME keys "$@"
}

echo "Register bridge address on Gonka chain '$BRIDGE_CHAIN_NAME'"
echo "Binary: $APP_NAME"

get_keyring_backend "$PASSWORD" || exit 1

MY_ADDR=$(run_keys_cmd show "$KEY_NAME" -a --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" 2>/dev/null)
echo "Signer: $MY_ADDR"

if [ -z "$PROPOSAL_ID_ARG" ]; then
    echo "Bridge address: $BRIDGE_ADDRESS"
    GOV_ACCOUNT_JSON=$($APP_NAME q auth module-account gov --output json $NODE_OPTS </dev/null)
    AUTHORITY_ADDRESS=$(echo "$GOV_ACCOUNT_JSON" | jq -r '.account.value.address // .account.base_account.address // empty')
    if [ -z "$AUTHORITY_ADDRESS" ]; then
        echo "Error: Could not fetch gov module account address"
        exit 1
    fi

    PROPOSAL_FILE="/tmp/proposal_register_bridge_ethereum.json"
    jq -n \
      --arg auth "$AUTHORITY_ADDRESS" \
      --arg chain "$BRIDGE_CHAIN_NAME" \
      --arg bridge "$BRIDGE_ADDRESS" \
      '{
        messages: [
          {
            "@type": "/inference.inference.MsgRegisterBridgeAddresses",
            authority: $auth,
            chainName: $chain,
            addresses: [$bridge]
          }
        ],
        deposit: "25000000ngonka",
        title: ("Register bridge contract on " + $chain),
        summary: ("Add bridge contract for auto WGNK burn / inbound detection (chain=" + $chain + ")"),
        metadata: "https://github.com/gonka-ai/gonka"
      }' > "$PROPOSAL_FILE"

    echo "Submitting proposal..."
    RAW_SUBMIT_OUT=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME tx gov submit-proposal "$PROPOSAL_FILE" \
      --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
      --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" $NODE_OPTS 2>&1)
    SUBMIT_OUT=$(echo "$RAW_SUBMIT_OUT" | sed -n '/{/,$p')
    TX_HASH=$(echo "$SUBMIT_OUT" | jq -r '.txhash' 2>/dev/null || echo "null")
    if [ "$TX_HASH" == "null" ] || [ -z "$TX_HASH" ]; then
        echo "Error: submit-proposal failed"
        echo "$RAW_SUBMIT_OUT"
        exit 1
    fi
    echo "TX Hash: $TX_HASH"
    sleep 6
    PROPOSAL_ID=$($APP_NAME q gov proposals --output json $NODE_OPTS </dev/null | jq -r '.proposals[-1].id')
else
    PROPOSAL_ID="$PROPOSAL_ID_ARG"
fi

echo "Proposal ID: $PROPOSAL_ID"
STATUS=$($APP_NAME q gov proposal "$PROPOSAL_ID" --output json $NODE_OPTS </dev/null | jq -r '.status')
echo "Status: $STATUS"

echo "Voting YES..."
VOTE_OUT=$(printf "%s\n%s\n" "$PASSWORD" "$PASSWORD" | $APP_NAME tx gov vote "$PROPOSAL_ID" yes \
  --from "$KEY_NAME" --chain-id "$CHAIN_ID" --gas auto --gas-adjustment 1.5 --yes --output json \
  --keyring-backend "$KEYRING_BACKEND" --home "$BASE_DIR/.inference" $NODE_OPTS 2>&1)
echo "$VOTE_OUT"

echo "Done. After proposal passes, verify:"
echo "  curl -sS 'http://localhost:8000/v1/bridge/addresses?chain=${BRIDGE_CHAIN_NAME}' | jq .addresses"
