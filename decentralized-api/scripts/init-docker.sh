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
  echo "Creating account key: $KEY_NAME"

  if command -v inferenced >/dev/null 2>&1; then
    APP_NAME="inferenced"
  else
    APP_NAME="decentralized-api"
  fi

  $APP_NAME keys add "$KEY_NAME" \
    --keyring-backend test \
    --keyring-dir /root/.inference

  ACCOUNT_PUBKEY=$($APP_NAME keys show "$KEY_NAME" --pubkey --keyring-backend test --keyring-dir /root/.inference | jq -r '.key')
  export ACCOUNT_PUBKEY
  echo "Generated ACCOUNT_PUBKEY: $ACCOUNT_PUBKEY"
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

echo "init for nats"
mkdir -p /root/.dapi/.nats

cosmovisor init /usr/bin/decentralized-api || fail "Failed to initialize cosmovisor"

if [ -n "${DEBUG-}" ]; then
  echo "running cosmovisor in debug mode"
  cosmovisor run || fail "Failed to start decentralized-api"
else
  echo "Running decentralized-api with cosmovisor"
  exec cosmovisor run
  echo "Decentralized API started successfully?"
fi