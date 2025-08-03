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

if [ "${CREATE_KEY:-false}" = "true" ]; then
  echo "Creating dual keys: Account Key (cold) and ML Operational Key (hot)"

  if command -v inferenced >/dev/null 2>&1; then
    APP_NAME="inferenced"
  else
    APP_NAME="decentralized-api"
  fi

  # Create Account Key (cold wallet) - for admin operations
  COLD_KEY_NAME="${KEY_NAME}-COLD"
  echo "Creating Account Key (cold): $COLD_KEY_NAME"
  $APP_NAME keys add "$COLD_KEY_NAME" \
    --keyring-backend test \
    --keyring-dir /root/.inference

  # Create ML Operational Key (hot wallet) - for automated ML operations
  echo "Creating ML Operational Key (hot): $KEY_NAME"
  $APP_NAME keys add "$KEY_NAME" \
    --keyring-backend test \
    --keyring-dir /root/.inference

  # Export Account Key public key for config
  ACCOUNT_PUBKEY=$($APP_NAME keys show "$COLD_KEY_NAME" --pubkey --keyring-backend test --keyring-dir /root/.inference | jq -r '.key')
  export ACCOUNT_PUBKEY
  echo "Generated ACCOUNT_PUBKEY (cold): $ACCOUNT_PUBKEY"

  # Get addresses
  COLD_KEY_ADDRESS=$($APP_NAME keys show "$COLD_KEY_NAME" --address --keyring-backend test --keyring-dir /root/.inference)
  HOT_KEY_ADDRESS=$($APP_NAME keys show "$KEY_NAME" --address --keyring-backend test --keyring-dir /root/.inference)
  echo "Account Key (cold) address: $COLD_KEY_ADDRESS"
  echo "ML Operational Key (hot) address: $HOT_KEY_ADDRESS"

  # Wait for chain to be ready
  echo "Waiting for chain node to be ready..."
  CHAIN_URL="${NODE_HOST:-localhost}:26657"
  while ! curl -sf "http://$CHAIN_URL/status" >/dev/null 2>&1; do
    echo "Waiting for chain node at $CHAIN_URL..."
    sleep 5
  done
  echo "Chain node is ready"

  # Register participant with Account Key (cold wallet)
  echo "Registering participant with Account Key..."
  
  # Fetch consensus key from node status using correct JSON path
  echo "Fetching consensus key from node status..."
  STATUS_RESPONSE=$($APP_NAME status --node "http://$CHAIN_URL" 2>/dev/null)
  echo "Status response: $STATUS_RESPONSE"
  
  CONSENSUS_KEY=$(echo "$STATUS_RESPONSE" | jq -r '.result.validator_info.pub_key.value // .ValidatorInfo.PubKey.value // empty')
  if [ -z "$CONSENSUS_KEY" ] || [ "$CONSENSUS_KEY" = "null" ]; then
    echo "Warning: Could not fetch consensus key from node status, trying alternative approach..."
    # Try alternative JSON path for different node versions
    CONSENSUS_KEY=$(echo "$STATUS_RESPONSE" | jq -r '.validator_info.pub_key.value // empty')
    
    if [ -z "$CONSENSUS_KEY" ] || [ "$CONSENSUS_KEY" = "null" ]; then
      echo "Error: Could not fetch consensus key from node status with any known JSON path"
      echo "Status response was: $STATUS_RESPONSE"
      exit 1
    fi
  fi
  echo "Successfully fetched consensus key: $CONSENSUS_KEY"
  
  SEED_NODE_URL="${SEED_NODE_ADDRESS:-http://195.242.13.239:8000}"
  NODE_PUBLIC_URL="${DAPI_API__PUBLIC_URL}"
  
  echo "Registering participant:"
  echo "  Node URL: $NODE_PUBLIC_URL"
  echo "  Account Address: $COLD_KEY_ADDRESS"
  echo "  Consensus Key: $CONSENSUS_KEY"
  echo "  Seed Node: $SEED_NODE_URL"
  
  $APP_NAME register-new-participant \
    "$NODE_PUBLIC_URL" \
    "$ACCOUNT_PUBKEY" \
    "$CONSENSUS_KEY" \
    --node-address "$SEED_NODE_URL" || {
    echo "Warning: Participant registration failed, continuing anyway..."
  }

  # Wait for participant to be available and funded
  echo "Waiting for participant account to be funded..."
  sleep 30

  # Grant ML Operations permissions from Account Key to ML Operational Key
  echo "Granting ML Operations permissions from Account Key to ML Operational Key..."
  $APP_NAME tx inference grant-ml-ops-permissions \
    "$COLD_KEY_NAME" \
    "$HOT_KEY_ADDRESS" \
    --from "$COLD_KEY_NAME" \
    --keyring-backend test \
    --keyring-dir /root/.inference \
    --node "http://$CHAIN_URL" \
    --yes || {
    echo "Warning: Permission granting failed, continuing anyway..."
  }

  echo "Dual key setup completed successfully!"
  echo "  Account Key (cold): $COLD_KEY_NAME ($COLD_KEY_ADDRESS)"
  echo "  ML Operational Key (hot): $KEY_NAME ($HOT_KEY_ADDRESS)"
fi

# If ACCOUNT_PUBKEY is not provided but CREATE_KEY=false, try to extract from existing key
if [ -z "${ACCOUNT_PUBKEY-}" ]; then
  echo "WARNING: ACCOUNT_PUBKEY not provided, attempting to extract from existing key: $KEY_NAME"
  echo "WARNING: For production, explicitly provide ACCOUNT_PUBKEY or set CREATE_KEY=true"
  sleep 20

  export KEYRING_BACKEND="test"
  # Check if the key exists
  if inferenced keys show "$KEY_NAME" --keyring-backend $KEYRING_BACKEND --keyring-dir /root/.inference >/dev/null 2>&1; then
    ACCOUNT_PUBKEY=$(inferenced keys show "$KEY_NAME" --pubkey --keyring-backend $KEYRING_BACKEND --keyring-dir /root/.inference | jq -r '.key')
    export ACCOUNT_PUBKEY
    echo "Extracted ACCOUNT_PUBKEY from existing key: $ACCOUNT_PUBKEY"
  else
    echo "Error: Key '$KEY_NAME' not found and ACCOUNT_PUBKEY not provided"
    echo "Either set CREATE_KEY=true to create a new key, or provide ACCOUNT_PUBKEY, or ensure key '$KEY_NAME' exists"
    exit 1
  fi
fi

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