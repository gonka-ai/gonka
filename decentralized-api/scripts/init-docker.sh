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

##### TEST ONLY - For production, provide ACCOUNT_PUBKEY explicitly #####
##### Register participant with cold wallet #####
if [ "${CREATE_KEY:-false}" = "true" ]; then
  echo "Creating 2 keys for role-based key management..."
  
  # Create Operator Key (Account Key) - Cold Wallet for central control
  OPERATOR_KEY_NAME="${KEY_NAME}-operator"
  echo "Creating operator key (cold wallet): $OPERATOR_KEY_NAME"
  inferenced keys add "$OPERATOR_KEY_NAME" \
    --keyring-backend test \
    --keyring-dir /root/.inference
  
  ACCOUNT_PUBKEY=$(inferenced keys show "$OPERATOR_KEY_NAME" --pubkey --keyring-backend test --keyring-dir /root/.inference | jq -r '.key')
  export ACCOUNT_PUBKEY
  echo "Generated ACCOUNT_PUBKEY: $ACCOUNT_PUBKEY"
  
  # Create AI Operational Key - Hot Wallet for automated AI workload transactions  
  echo "Creating AI operational key (hot wallet): $KEY_NAME"
  inferenced keys add "$KEY_NAME" \
    --keyring-backend test \
    --keyring-dir /root/.inference
  
  echo "Generated KEY_NAME: $KEY_NAME"
  
  # Phase 2: Register participant with cold wallet (if DAPI_CHAIN_NODE__SEED_API_URL provided)
  if [ -n "${DAPI_CHAIN_NODE__SEED_API_URL-}" ]; then
    echo "Registering participant using cold wallet: $OPERATOR_KEY_NAME"
    sleep 4
    
    # Get validator consensus key from chain node RPC status endpoint (works with TMKMS)
    CHAIN_NODE_URL="${DAPI_CHAIN_NODE__URL}"
    echo "Fetching validator consensus key from chain node: $CHAIN_NODE_URL"
    VALIDATOR_CONSENSUS_KEY=$(curl -s "$CHAIN_NODE_URL/status" | jq -r '.result.validator_info.pub_key.value // empty' 2>/dev/null || echo "")
    
    if [ -z "$VALIDATOR_CONSENSUS_KEY" ]; then
      fail "Could not retrieve validator consensus key automatically"
    else
      OPERATOR_ADDRESS=$(inferenced keys show "$OPERATOR_KEY_NAME" --address --keyring-backend test --keyring-dir /root/.inference)
      
      echo "Operator Address: $OPERATOR_ADDRESS"
      echo "Node URL: ${DAPI_API__PUBLIC_URL}"
      echo "Operator Public Key: $ACCOUNT_PUBKEY"
      echo "Validator Consensus Key: $VALIDATOR_CONSENSUS_KEY"
      echo "Seed API URL: $DAPI_CHAIN_NODE__SEED_API_URL"
      
      inferenced register-new-participant \
        "$OPERATOR_ADDRESS" \
        "${DAPI_API__PUBLIC_URL}" \
        "$ACCOUNT_PUBKEY" \
        "$VALIDATOR_CONSENSUS_KEY" \
        --node-address "$DAPI_CHAIN_NODE__SEED_API_URL" || fail "Participant registration failed. This may be normal if seed node is not yet accessible."
      sleep 4
    fi
  else
    echo "DAPI_CHAIN_NODE__SEED_API_URL not provided, skipping participant registration"
    echo "To register participant manually later, use:"
    echo "inferenced register-new-participant [operator-address] [node-url] [operator-public-key] [validator-consensus-key] --node-address [seed-node-url]"
  fi
  
  # Phase 3: Grant ML operations permissions from operator key to AI operational key
  echo "Granting ML operations permissions from operator key to AI operational key..."
  AI_OPERATIONAL_ADDRESS=$(inferenced keys show "$KEY_NAME" --address --keyring-backend test --keyring-dir /root/.inference)
  
  echo "AI Operational Address: $AI_OPERATIONAL_ADDRESS"
  echo "Granting permissions from $OPERATOR_KEY_NAME to $AI_OPERATIONAL_ADDRESS"
  echo "DAPI_CHAIN_NODE__URL: $DAPI_CHAIN_NODE__URL"
  
  inferenced tx inference grant-ml-ops-permissions \
    "$OPERATOR_KEY_NAME" \
    "$AI_OPERATIONAL_ADDRESS" \
    --node "$DAPI_CHAIN_NODE__URL" \
    --from "$OPERATOR_KEY_NAME" \
    --keyring-backend test \
    --keyring-dir /root/.inference \
    --gas auto \
    --gas-adjustment 1.5 \
    --yes || {
    echo "Warning: Permission granting failed. This may be normal if node is not yet connected to the network."
    echo "Permissions can be granted manually later using:"
    echo "inferenced tx inference grant-ml-ops-permissions $OPERATOR_KEY_NAME $AI_OPERATIONAL_ADDRESS --from $OPERATOR_KEY_NAME"
  }
fi
##### END TEST ONLY #####

##### TEST ONLY - ACCOUNT_PUBKEY fallback extraction #####
# Genesis => single key
if [ "${DAPI_CHAIN_NODE__IS_GENESIS-}" = "true" ]; then
  sleep 10
  # Check if the key exists before trying to show it
  if inferenced keys show "$KEY_NAME" --keyring-backend test --keyring-dir /root/.inference >/dev/null 2>&1; then
    ACCOUNT_PUBKEY=$(inferenced keys show "$KEY_NAME" --pubkey --keyring-backend test --keyring-dir /root/.inference | jq -r '.key')
    export ACCOUNT_PUBKEY
    echo "Extracted ACCOUNT_PUBKEY from existing key: $KEY_NAME"
  else
    echo "Warning: Key '$KEY_NAME' not found in keyring. Available keys:"
    inferenced keys list --keyring-backend test --keyring-dir /root/.inference || true
    echo "Please ensure the genesis key exists or provide ACCOUNT_PUBKEY explicitly."
  fi
fi
##### END TEST ONLY #####

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