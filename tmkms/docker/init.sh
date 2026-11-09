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

# KMS secret-connection identity: one per install. Older images baked a single
# shared key into the image template (`tmkms init` at build time); new installs
# get their own. Existing volumes keep theirs (rotation is optional: the node
# does not pin the KMS identity).
KMS_IDENTITY="$TARGET_DIR/secrets/kms-identity.key"
if [ ! -f "$KMS_IDENTITY" ]; then
  echo "Generating KMS identity key $KMS_IDENTITY"
  kms_init_dir=$(mktemp -d)
  tmkms init "$kms_init_dir/init" >/dev/null
  cp "$kms_init_dir/init/secrets/kms-identity.key" "$KMS_IDENTITY"
  chmod 600 "$KMS_IDENTITY"
  rm -rf "$kms_init_dir"
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

# Existing volumes keep the tmkms.toml from the image they were created with;
# bring it up to the vote-extension settings (sign_extensions, protocol_version).
/root/migrate-toml.sh "$TOML_FILE"

echo "tmkms version: $(tmkms version 2>/dev/null || echo unknown)"
echo "Contents of $TOML_FILE:"
cat "$TOML_FILE"

if [ "${WITHKEYGEN:-false}" = "true" ] && [ ! -f "$TARGET_DIR/secrets/priv_validator_key.softsign" ]; then
  echo "Generating new key in $TARGET_DIR/secrets/priv_validator_key.softsign as WITHKEYGEN is true and key is missing."
  tmkms softsign keygen "$TARGET_DIR/secrets/priv_validator_key.softsign"
elif [ "${WITHKEYGEN:-false}" = "true" ] && [ -f "$TARGET_DIR/secrets/priv_validator_key.softsign" ]; then
  echo "Key $TARGET_DIR/secrets/priv_validator_key.softsign already exists. WITHKEYGEN is true, but skipping generation."
fi

pubkey=$(tmkms-pubkey --json)
printf 'Pubkey:\n%s\n' "$pubkey"

# For the import_keys case, the priv_validator_key.softsign should have been copied from TEMPLATE_DIR.
# If it's not there, and we are not in keygen mode, it's an issue.
if [ "${WITHKEYGEN:-false}" = "false" ] && [ ! -f "$TARGET_DIR/secrets/priv_validator_key.softsign" ]; then
  echo "Error: Key $TARGET_DIR/secrets/priv_validator_key.softsign not found, and not in key generation mode. Ensure keys were imported correctly into the image's template directory."
  exit 1
fi

# Drop privileges for the long-running signer (review finding M2). Setup above
# runs as root because existing volumes are root-owned; the volume is handed to
# the unprivileged user before start. TMKMS_RUN_AS=root keeps the old behaviour.
RUN_AS="${TMKMS_RUN_AS:-tmkms}"
if [ "$RUN_AS" != "root" ] && [ "$(id -u)" = "0" ] && id "$RUN_AS" >/dev/null 2>&1; then
  chown -R "$RUN_AS:$RUN_AS" "$TARGET_DIR"
  echo "Starting tmkms as $RUN_AS"
  exec setpriv --reuid="$RUN_AS" --regid="$RUN_AS" --init-groups --no-new-privs \
    tmkms start -c "$TOML_FILE"
fi
echo "Starting tmkms as $(id -un)"
exec tmkms start -c "$TOML_FILE"
