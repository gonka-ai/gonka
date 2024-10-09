#!/bin/sh
set -e
# Check if mandatory argument is provided
if [ -z "$KEY_NAME" ]; then
  echo "Error: KEY_NAME is required."
  exit 1
fi

yaml_file="/root/api-config.yaml"

if [ -n "$NODE_HOST" ]; then
  echo "Setting node address to http://$NODE_HOST:26657 in $yaml_file"
  sed -i "s/url: .*:26657/url: http:\/\/$NODE_HOST:26657/" "$yaml_file"
fi

sed -i "s/account_name: .*/account_name: \"$KEY_NAME\"/" "$yaml_file"
sed -i "s/keyring_backend: .*/keyring_backend: test/" "$yaml_file"

exec decentralized-api
