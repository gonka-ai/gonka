#!/bin/sh
set -eu

# Copy file from TEMPLATE_DIR (used at docker build stage for initializing tmkms)
#   to TARGET_DIR (where we mount the volume)
TEMPLATE_DIR="/app/tmkms_init_data"
TARGET_DIR="/root/.tmkms"
TOML_FILE="$TARGET_DIR/tmkms.toml"

if [ ! -d "$TARGET_DIR/secrets" ]; then
  mkdir -p "$TARGET_DIR/secrets"
fi
if [ ! -d "$TARGET_DIR/state" ]; then
  mkdir -p "$TARGET_DIR/state"
fi

if [ ! -f "$TOML_FILE" ]; then
  echo "Initializing $TARGET_DIR from template $TEMPLATE_DIR..."
  if [ -n "$(ls -A $TEMPLATE_DIR)" ]; then # Check if TEMPLATE_DIR is not empty
    cp -R "$TEMPLATE_DIR/." "$TARGET_DIR/"
    echo "Initialization complete."
  else
    echo "Warning: Template directory $TEMPLATE_DIR is empty. Skipping copy."
  fi
else
  echo "$TARGET_DIR already initialized."
fi

if [ ! -w "$TOML_FILE" ]; then
  echo "Error: Cannot write to $TOML_FILE. Check volume permissions and ensure it's not empty if this is the first run."
  exit 1
fi

if [ -z "${VALIDATOR_LISTEN_ADDRESS:-}" ]; then
  echo "Error: VALIDATOR_LISTEN_ADDRESS is not set"
  exit 1
fi

escaped_addr=$(printf '%s' "$VALIDATOR_LISTEN_ADDRESS" | sed 's/[\/&]/\\&/g')
sed -i "s/^addr *= *\".*\"/addr = \"$escaped_addr\"/" "$TOML_FILE"

echo "Set addr to \"$VALIDATOR_LISTEN_ADDRESS\" in $TOML_FILE"
echo "Contents of $TOML_FILE:"
cat "$TOML_FILE"

if [ "${WITHKEYGEN:-false}" = "true" ] && [ ! -f "$TARGET_DIR/secrets/priv_validator_key.softsign" ]; then
  echo "Generating new key in $TARGET_DIR/secrets/priv_validator_key.softsign as WITHKEYGEN is true and key is missing."
  tmkms softsign keygen "$TARGET_DIR/secrets/priv_validator_key.softsign"
elif [ "${WITHKEYGEN:-false}" = "true" ] && [ -f "$TARGET_DIR/secrets/priv_validator_key.softsign" ]; then
  echo "Key $TARGET_DIR/secrets/priv_validator_key.softsign already exists. WITHKEYGEN is true, but skipping generation."
fi

pubkey=$(tmkms-pubkey --json)
echo "Pubkey:\n$pubkey"

# For the import_keys case, the priv_validator_key.softsign should have been copied from TEMPLATE_DIR.
# If it's not there, and we are not in keygen mode, it's an issue.
if [ "${WITHKEYGEN:-false}" = "false" ] && [ ! -f "$TARGET_DIR/secrets/priv_validator_key.softsign" ]; then
  echo "Error: Key $TARGET_DIR/secrets/priv_validator_key.softsign not found, and not in key generation mode. Ensure keys were imported correctly into the image's template directory."
  exit 1
fi

tmkms start -c "$TOML_FILE"
