#!/bin/sh
# Make a migrated tmkms.toml loadable by the legacy tmkms 0.12 image again.
#
# migrate-toml.sh adds `sign_extensions = true` under [[chain]]. tmkms 0.12 has
# no such field and refuses to start ("parse error: unknown field
# `sign_extensions`"), so rolling back by only reverting the image tag takes the
# validator offline. Run this BEFORE starting the 0.12 image.
#
# Only valid before the chain enables vote extensions: from that height on a
# signer without extension signing makes the node panic on its first precommit.
#
# The double-sign state file is never touched: always keep the CURRENT
# priv_validator_state.json when rolling back (restoring an older one could
# let tmkms sign a height/round it already signed).
#
# Idempotent. Writes a timestamped backup before the first change.
#
# Usage: rollback-toml.sh <path/to/tmkms.toml>
# Without the new image (plain host shell, same effect):
#   cp .tmkms/tmkms.toml .tmkms/tmkms.toml.rollback-bak && \
#   sed -i '/^sign_extensions *=/d; s/^protocol_version *=.*/protocol_version = "v0.34"/' .tmkms/tmkms.toml
set -eu

TOML_FILE="${1:?usage: rollback-toml.sh <tmkms.toml>}"

if [ ! -f "$TOML_FILE" ]; then
  echo "Error: $TOML_FILE not found" >&2
  exit 1
fi

tmp="${TOML_FILE}.rollback.$$"
trap 'rm -f "$tmp"' EXIT
cp "$TOML_FILE" "$tmp"

# tmkms 0.12 knows neither field value below; v0.34 is the only protocol it accepts.
sed -i '/^sign_extensions *=/d' "$tmp"
if grep -q '^protocol_version *=' "$tmp"; then
  sed -i 's/^protocol_version *=.*/protocol_version = "v0.34"/' "$tmp"
fi

if cmp -s "$TOML_FILE" "$tmp"; then
  echo "tmkms.toml already loadable by tmkms 0.12 (no sign_extensions, protocol_version v0.34)"
  exit 0
fi

stamp=$(date -u +%Y%m%dT%H%M%SZ)
cp -p "$TOML_FILE" "${TOML_FILE}.rollback-bak.${stamp}"
echo "Backed up $TOML_FILE to ${TOML_FILE}.rollback-bak.${stamp}"

chmod --reference="$TOML_FILE" "$tmp" 2>/dev/null || true
mv "$tmp" "$TOML_FILE"
trap - EXIT
echo "Rolled back $TOML_FILE for tmkms 0.12: removed sign_extensions, protocol_version = v0.34"
echo "Keep the current state/priv_validator_state.json; do not restore an older copy."
