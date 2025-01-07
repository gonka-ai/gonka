#!/bin/sh
set -e
# Check if mandatory argument is provided
if [ -z "$KEY_NAME" ]; then
  echo "Error: KEY_NAME is required."
  exit 1
fi

if [ -z "$POC_CALLBACK_URL" ]; then
  echo "Error: POC_CALLBACK_URL is required."
  exit 1
fi

if [ -z "$PUBLIC_URL" ]; then
  echo "Error: PUBLIC_URL is required."
  exit 1
fi

yaml_file="/root/api-config.yaml"

if [ -n "$NODE_HOST" ]; then
  echo "Setting node address to http://$NODE_HOST:26657 in $yaml_file"
  sed -i "s/url: .*:26657/url: http:\/\/$NODE_HOST:26657/" "$yaml_file"
fi

echo "Setting keyring_backend to test in $yaml_file"
sed -i "s/keyring_backend: .*/keyring_backend: test/" "$yaml_file"

if [ "$IS_GENESIS" = "true" ]; then
  echo "Setting IS_GENESIS to $IS_GENESIS in $yaml_file"
  sed -i "s/is_genesis: .*/is_genesis: \"$IS_GENESIS\"/" "$yaml_file"
fi

echo "Setting public_url to $PUBLIC_URL in $yaml_file"
sed -i "s/public_url: .*/public_url: \"$PUBLIC_URL\"/" "$yaml_file"

if [ "$IS_GENESIS" = "true" ]; then
  echo "Setting IS_GENESIS to $IS_GENESIS in $yaml_file"
  sed -i "s/is_genesis: .*/is_genesis: \"$IS_GENESIS\"/" "$yaml_file"
fi

echo "The final api config:"
cat "$yaml_file"

exec decentralized-api
