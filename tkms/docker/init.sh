#!/bin/sh
set -eu
TOML_FILE="/root/.tmkms/tmkms.toml"

if [ ! -w "$TOML_FILE" ]; then
  echo "Error: Cannot write to $TOML_FILE"
  exit 1
fi

if [ -z "${VALIDATOR_LISTEN_ADDRESS:-}" ]; then
  echo "Error: VALIDATOR_LISTEN_ADDRESS is not set"
  exit 1
fi

escaped_addr=$(printf '%s' "$VALIDATOR_LISTEN_ADDRESS" | sed 's/[\/&]/\\&/g')
sed -i "s/^addr *= *\".*\"/addr = \"$escaped_addr\"/" "$TOML_FILE"

echo "Set addr to \"$VALIDATOR_LISTEN_ADDRESS\" in $TOML_FILE"

if [ ! -f "/root/.tmkms/secrets/priv_validator_key.softsign" ]; then
  tmkms softsign keygen /root/.tmkms/secrets/priv_validator_key.softsign
fi

tmkms start -c /root/.tmkms/tmkms.toml