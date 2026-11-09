#!/bin/sh
# Migrate an existing tmkms.toml (kept on the operator's volume across image
# upgrades) to the settings CometBFT vote extensions need:
#
#   [[chain]]      sign_extensions = true      (tmkms 0.14.x)
#   [[validator]]  protocol_version = "v0.34"  (the only value tmkms 0.14 accepts;
#                                               it is the secret-connection version,
#                                               not the CometBFT version)
#
# tmkms 0.15.0 is NOT usable for vote extensions: it encodes SignedVoteResponse
# with cometbft.types.v1beta1.Vote (no extension fields), so the extension
# signature never reaches CometBFT (iqlusioninc/tmkms#1174).
#
# The template only applies on first start (init.sh copies it when no
# tmkms.toml exists), so without this step operators who upgrade the image keep
# the old config and their node panics once the chain enables vote extensions.
#
# Idempotent. Writes a timestamped backup of the original config, and a one-time
# backup of the double-sign state file, before the first change.
#
# Usage: migrate-toml.sh <path/to/tmkms.toml>
# Env:   TMKMS_SIGN_EXTENSIONS  (true|false, default true)
#        TMKMS_PROTOCOL_VERSION (v0.34|v0.38, default v0.34)
set -eu

TOML_FILE="${1:?usage: migrate-toml.sh <tmkms.toml>}"
SIGN_EXTENSIONS="${TMKMS_SIGN_EXTENSIONS:-true}"
PROTOCOL_VERSION="${TMKMS_PROTOCOL_VERSION:-v0.34}"

case "$SIGN_EXTENSIONS" in
  true|false) ;;
  *) echo "Error: TMKMS_SIGN_EXTENSIONS must be true or false, got '$SIGN_EXTENSIONS'" >&2; exit 1 ;;
esac
case "$PROTOCOL_VERSION" in
  v0.34|v0.38) ;;
  *) echo "Error: TMKMS_PROTOCOL_VERSION must be v0.34 or v0.38, got '$PROTOCOL_VERSION'" >&2; exit 1 ;;
esac

if [ ! -f "$TOML_FILE" ]; then
  echo "Error: $TOML_FILE not found" >&2
  exit 1
fi
if ! grep -q '^\[\[chain\]\]' "$TOML_FILE"; then
  echo "Error: no [[chain]] section in $TOML_FILE" >&2
  exit 1
fi

tmp="${TOML_FILE}.migrate.$$"
trap 'rm -f "$tmp"' EXIT
cp "$TOML_FILE" "$tmp"

# protocol_version lives only in [[validator]]; replace an existing value.
if grep -q '^protocol_version *=' "$tmp"; then
  sed -i "s/^protocol_version *=.*/protocol_version = \"$PROTOCOL_VERSION\"/" "$tmp"
fi

# sign_extensions: update in place, or add it right after each [[chain]] header
# (keys directly below a table header belong to that table).
if grep -q '^sign_extensions *=' "$tmp"; then
  sed -i "s/^sign_extensions *=.*/sign_extensions = $SIGN_EXTENSIONS/" "$tmp"
else
  sed -i "/^\[\[chain\]\]/a sign_extensions = $SIGN_EXTENSIONS" "$tmp"
fi

if cmp -s "$TOML_FILE" "$tmp"; then
  echo "tmkms.toml already up to date (sign_extensions = $SIGN_EXTENSIONS, protocol_version = $PROTOCOL_VERSION)"
  exit 0
fi

stamp=$(date -u +%Y%m%dT%H%M%SZ)
cp -p "$TOML_FILE" "${TOML_FILE}.bak.${stamp}"
echo "Backed up $TOML_FILE to ${TOML_FILE}.bak.${stamp}"

# One-time copy of the double-sign protection state before the first migration.
state_file=$(sed -n 's/^state_file *= *"\(.*\)"/\1/p' "$tmp" | head -n 1)
if [ -n "$state_file" ] && [ -f "$state_file" ] && [ ! -f "${state_file}.pre-vote-extensions" ]; then
  cp -p "$state_file" "${state_file}.pre-vote-extensions"
  echo "Backed up $state_file to ${state_file}.pre-vote-extensions"
fi

# Keep the original file's permissions; replace atomically.
chmod --reference="$TOML_FILE" "$tmp" 2>/dev/null || true
mv "$tmp" "$TOML_FILE"
trap - EXIT
echo "Migrated $TOML_FILE: sign_extensions = $SIGN_EXTENSIONS, protocol_version = $PROTOCOL_VERSION"
