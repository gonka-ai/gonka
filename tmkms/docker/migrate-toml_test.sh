#!/bin/sh
# Tests for migrate-toml.sh. Needs GNU sed/coreutils (as in the tmkms image).
# Run locally via Docker (from the repo root):
#   docker run --rm -v "$PWD/tmkms:/t:ro" debian:bookworm-slim sh /t/docker/migrate-toml_test.sh
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)
MIGRATE="$HERE/migrate-toml.sh"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }
count() { grep -c "$1" "$2" || true; }

setup() {
  rm -rf "${WORK:?}"/*
  mkdir -p "$WORK/state"
  sed "s#/root/.tmkms/state/#$WORK/state/#" "$HERE/testdata/tmkms-legacy-0.12.toml" > "$WORK/tmkms.toml"
  echo '{"height":"123","round":"0","step":3}' > "$WORK/state/priv_validator_state.json"
  chmod 600 "$WORK/tmkms.toml"
}

# 1. Legacy 0.12 config gets both settings, backups are written.
setup
sh "$MIGRATE" "$WORK/tmkms.toml" >/dev/null
[ "$(count '^sign_extensions = true$' "$WORK/tmkms.toml")" = 1 ] || fail "sign_extensions not added once"
[ "$(count '^protocol_version = "v0.34"$' "$WORK/tmkms.toml")" = 1 ] || fail "protocol_version not v0.34 (tmkms 0.14 accepts only v0.34)"
awk '/^\[\[chain\]\]/{getline; print; exit}' "$WORK/tmkms.toml" | grep -q '^sign_extensions = true$' \
  || fail "sign_extensions must sit directly under [[chain]]"
backup=$(find "$WORK" -maxdepth 1 -name 'tmkms.toml.bak.*' | head -n 1)
[ -n "$backup" ] || fail "toml backup missing"
grep -q '^sign_extensions' "$backup" && fail "backup is not the original"
[ -f "$WORK/state/priv_validator_state.json.pre-vote-extensions" ] || fail "state backup missing"
[ "$(stat -c %a "$WORK/tmkms.toml")" = 600 ] || fail "permissions not preserved"
# Everything except the two settings is unchanged.
strip() { grep -v '^sign_extensions\|^protocol_version' "$1" | md5sum; }
[ "$(strip "$WORK/tmkms.toml")" = "$(strip "$backup")" ] || fail "unrelated lines changed"
echo "ok 1 legacy config migrated"

# 2. Idempotent: second run changes nothing, no new backup.
before=$(md5sum < "$WORK/tmkms.toml")
sleep 1
out=$(sh "$MIGRATE" "$WORK/tmkms.toml")
echo "$out" | grep -q 'already up to date' || fail "second run not a no-op: $out"
[ "$(md5sum < "$WORK/tmkms.toml")" = "$before" ] || fail "second run modified file"
[ "$(find "$WORK" -maxdepth 1 -name 'tmkms.toml.bak.*' | wc -l)" = 1 ] || fail "second run wrote another backup"
echo "ok 2 idempotent"

# 3. New template is already up to date.
cp "$HERE/../template/tmkms.toml" "$WORK/new.toml"
sh "$MIGRATE" "$WORK/new.toml" | grep -q 'already up to date' || fail "current template should not need migration"
echo "ok 3 current template untouched"

# 4. Existing sign_extensions = false is overwritten by the env value.
setup
sed -i '/^\[\[chain\]\]/a sign_extensions = false' "$WORK/tmkms.toml"
sh "$MIGRATE" "$WORK/tmkms.toml" >/dev/null
[ "$(count '^sign_extensions' "$WORK/tmkms.toml")" = 1 ] || fail "duplicate sign_extensions"
grep -q '^sign_extensions = true$' "$WORK/tmkms.toml" || fail "false not flipped to true"
echo "ok 4 existing false updated"

# 5. Env overrides and validation.
setup
TMKMS_SIGN_EXTENSIONS=false TMKMS_PROTOCOL_VERSION=v0.38 sh "$MIGRATE" "$WORK/tmkms.toml" >/dev/null
grep -q '^sign_extensions = false$' "$WORK/tmkms.toml" || fail "env false ignored"
grep -q '^protocol_version = "v0.38"$' "$WORK/tmkms.toml" || fail "env v0.38 ignored"

# 5b. A config already switched to v0.38 (unparseable by tmkms 0.14) goes back to v0.34.
setup
sed -i 's/^protocol_version = .*/protocol_version = "v0.38"/' "$WORK/tmkms.toml"
sh "$MIGRATE" "$WORK/tmkms.toml" >/dev/null
grep -q '^protocol_version = "v0.34"$' "$WORK/tmkms.toml" || fail "v0.38 not reset to v0.34"
if TMKMS_SIGN_EXTENSIONS=yes sh "$MIGRATE" "$WORK/tmkms.toml" 2>/dev/null; then fail "invalid bool accepted"; fi
if TMKMS_PROTOCOL_VERSION=v1.0 sh "$MIGRATE" "$WORK/tmkms.toml" 2>/dev/null; then fail "invalid version accepted"; fi
echo "ok 5 env overrides and validation"

# 6. Errors: missing file, no [[chain]].
if sh "$MIGRATE" "$WORK/missing.toml" 2>/dev/null; then fail "missing file accepted"; fi
printf '[[validator]]\naddr = "x"\n' > "$WORK/bad.toml"
if sh "$MIGRATE" "$WORK/bad.toml" 2>/dev/null; then fail "toml without [[chain]] accepted"; fi
echo "ok 6 errors"

echo "PASS migrate-toml_test.sh"
