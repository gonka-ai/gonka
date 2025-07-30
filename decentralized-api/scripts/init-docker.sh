#!/bin/sh
set -e

fail() {
  echo "$1" >&2
  if [ -n "${DEBUG-}" ]; then
    tail -f /dev/null
  else
    exit 1
  fi
}

if [ -z "${KEY_NAME-}" ]; then
  echo "Error: KEY_NAME is required."
  exit 1
fi

################################################################################################
############      TEST ONLY - For production, provide ACCOUNT_PUBKEY explicitly     ############

KEYRING_BACKEND="test"
KEYRING_DIR="/root/.inference"

get_key_address() {
  local key_name="$1"
  inferenced keys show "$key_name" --address --keyring-backend "$KEYRING_BACKEND" --keyring-dir "$KEYRING_DIR" 2>/dev/null || fail "Could not get address for key '$key_name'."
}

get_key_pubkey() {
  local key_name="$1"
  inferenced keys show "$key_name" --pubkey --keyring-backend "$KEYRING_BACKEND" --keyring-dir "$KEYRING_DIR" | jq -r '.key' || fail "Could not get pubkey for key '$key_name'."
}

get_validator_key() {
  local node_url="$1"
  for _ in $(seq 1 25); do
    key=$(curl -s "$node_url/status" | jq -r '.result.validator_info.pub_key.value // empty' 2>/dev/null)
    if [ -n "$key" ]; then
      echo "$key"
      return 0
    fi
    sleep 1
  done
  fail "Timed out waiting for validator key from node status."
}


create_and_register_keys() {
  # Test join node:
  # 1. Create cold wallet inside container
  # 2. Register participant with cold wallet
  # 3. Create warm wallet inside container
  # 4. Grant ML ops permissions to warm wallet
  # All these steps are done externally in real environment.
  echo "Creating operator and AI operational keys..."
  local operator_key_name="${KEY_NAME}-operator"

  inferenced keys add "$operator_key_name" --keyring-backend "$KEYRING_BACKEND" --keyring-dir "$KEYRING_DIR" || fail "Failed to create operator key."
  inferenced keys add "$KEY_NAME" --keyring-backend "$KEYRING_BACKEND" --keyring-dir "$KEYRING_DIR" || fail "Failed to create AI operational key."

  export ACCOUNT_PUBKEY=$(get_key_pubkey "$operator_key_name")
  echo "Generated ACCOUNT_PUBKEY: $ACCOUNT_PUBKEY"

  if [ -n "${DAPI_CHAIN_NODE__SEED_API_URL-}" ]; then
    echo "Registering participant..."

    # Get all required data for registration
    local operator_address=$(get_key_address "$operator_key_name")
    local validator_key=$(get_validator_key "${DAPI_CHAIN_NODE__URL}")

    inferenced register-new-participant \
        "$operator_address" \
        "${DAPI_API__PUBLIC_URL}" \
        "$ACCOUNT_PUBKEY" \
        "$validator_key" \
        --node-address "$DAPI_CHAIN_NODE__SEED_API_URL" || fail "Participant registration failed"
  else
    echo "Skipping participant registration: DAPI_CHAIN_NODE__SEED_API_URL not set."
  fi

  echo "Granting ML operations permissions..."
  local ai_op_address=$(get_key_address "$KEY_NAME")
  inferenced tx inference grant-ml-ops-permissions \
    "$operator_key_name" \
    "$ai_op_address" \
    --node "$DAPI_CHAIN_NODE__URL" \
    --from "$operator_key_name" \
    --keyring-backend "$KEYRING_BACKEND" \
    --keyring-dir "$KEYRING_DIR" \
    --gas 2000000 \
    --yes \
    || fail "Warning: Permission granting failed"
}

handle_genesis_key() {
  sleep 10
  echo "Genesis node detected. Extracting pubkey from existing key: $KEY_NAME"
  export ACCOUNT_PUBKEY=$(get_key_pubkey "$KEY_NAME")
  echo "Extracted ACCOUNT_PUBKEY: $ACCOUNT_PUBKEY"
}

if [ "${CREATE_KEY:-false}" = "true" ]; then
  create_and_register_keys
fi

if [ "${DAPI_CHAIN_NODE__IS_GENESIS-}" = "true" ]; then
  handle_genesis_key
fi

##### END TEST ONLY #####
################################################################################################

if [ -z "${ACCOUNT_PUBKEY-}" ]; then
  echo "Error: ACCOUNT_PUBKEY is required."
  exit 1
fi

if [ -z "$DAPI_API__POC_CALLBACK_URL" ]; then
  echo "Error: DAPI_API__POC_CALLBACK_URL is required."
  exit 1
fi

if [ -z "$DAPI_API__PUBLIC_URL" ]; then
  echo "Error: DAPI_API__PUBLIC_URL is required."
  exit 1
fi

yaml_file="/root/api-config.yaml"

if [ -n "$NODE_HOST" ]; then
  echo "Setting node address to http://$NODE_HOST:26657 in $yaml_file"
  sed -i "s/url: .*:26657/url: http:\/\/$NODE_HOST:26657/" "$yaml_file"
fi

echo "Setting keyring_backend to test in $yaml_file"
sed -i "s/keyring_backend: .*/keyring_backend: test/" "$yaml_file"

echo "Initial config (before env var merge)"
cat "$yaml_file"

echo "init for cosmovisor"
mkdir -p /root/.dapi
mkdir -p /root/.dapi/data

cosmovisor init /usr/bin/decentralized-api || fail "Failed to initialize cosmovisor"

if [ -n "${DEBUG-}" ]; then
  echo "running cosmovisor in debug mode"
  cosmovisor run || fail "Failed to start decentralized-api"
else
  echo "Running decentralized-api with cosmovisor"
  exec cosmovisor run
  echo "Decentralized API started successfully?"
fi