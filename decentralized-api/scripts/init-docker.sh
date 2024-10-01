#!/bin/sh
set -e
# Check if mandatory argument is provided
if [ -z "$KEY_NAME" ]; then
  echo "Error: KEY_NAME is required."
  exit 1
fi

yaml_file="/root/api-config.yaml"
sed -i "s/url: .*:26657/url: http:\/\/$KEY_NAME-node:26657/" "$yaml_file"
sed -i "s/account_name: .*/account_name: \"$KEY_NAME\"/" "$yaml_file"
sed -i "s/keyring_backend: .*/keyring_backend: test/" "$yaml_file"

exec decentralized-api
# just a simple command to keep the container running
#tail -f /dev/null