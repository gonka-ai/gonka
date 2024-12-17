#!/bin/sh
set -e
# Check if mandatory argument is provided
if [ -z "$KEY_NAME" ]; then
  echo "Error: KEY_NAME is required."
  exit 1
fi

if [ -z "$PUBLIC_IP" ]; then
  echo "Error: PUBLIC_IP is required."
  exit 1
fi

yaml_file="/root/api-config.yaml"

if [ -n "$NODE_HOST" ]; then
  echo "Setting node address to http://$NODE_HOST:26657 in $yaml_file"
  sed -i "s/url: .*:26657/url: http:\/\/$NODE_HOST:26657/" "$yaml_file"
fi

echo "Setting account_name address to $KEY_NAME in $yaml_file"
sed -i "s/account_name: .*/account_name: \"$KEY_NAME\"/" "$yaml_file"

echo "Setting keyring_backend to test in $yaml_file"
sed -i "s/keyring_backend: .*/keyring_backend: test/" "$yaml_file"

echo "Setting host to $PUBLIC_IP in $yaml_file"
sed -i "s/host: .*/host: \"$PUBLIC_IP\"/" "$yaml_file"

echo "The final api config:"
cat "$yaml_file"

exec decentralized-api
