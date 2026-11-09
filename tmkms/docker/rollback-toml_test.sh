#!/bin/sh
# Tests for rollback-toml.sh. Needs GNU sed/coreutils (as in the tmkms image).
# Run locally via Docker (from the repo root):
#   docker run --rm -v "$PWD/tmkms:/t:ro" debian:bookworm-slim sh /t/docker/rollback-toml_test.sh
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)
MIGRATE="$HERE/migrate-toml.sh"
ROLLBACK="$HERE/rollback-toml.sh"
LEGACY="$HERE/testdata/tmkms-legacy-0.12.toml"
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

setup() {
  rm -rf "${WORK:?}"/*
  mkdir -p "$WORK/state"
  sed "s#/root/.tmkms/state/#$WORK/state/#" "$LEGACY" > "$WORK/tmkms.toml"
  cp "$WORK/tmkms.toml" "$WORK/original.toml"
  echo '{"height":"123","round":"0","step":3}' > "$WORK/state/priv_validator_state.json"
  chmod 600 "$WORK/tmkms.toml"
}

# 1. migrate → rollback restores the legacy config byte-for-byte.
setup
sh "$MIGRATE" "$WORK/tmkms.toml" >/dev/null
grep -q '^sign_extensions = true$' "$WORK/tmkms.toml" || fail "precondition: migration did not add sign_extensions"
echo '{"height":"456","round":"0","step":3}' > "$WORK/state/priv_validator_state.json"   # tmkms signed newer heights
sh "$ROLLBACK" "$WORK/tmkms.toml" >/dev/null
cmp -s "$WORK/tmkms.toml" "$WORK/original.toml" || fail "rollback did not restore the legacy config"
grep -q '"height":"456"' "$WORK/state/priv_validator_state.json" || fail "rollback touched the double-sign state file"
[ "$(find "$WORK" -maxdepth 1 -name 'tmkms.toml.rollback-bak.*' | wc -l)" = 1 ] || fail "rollback backup missing"
[ "$(stat -c %a "$WORK/tmkms.toml")" = 600 ] || fail "permissions not preserved"
echo "ok 1 migrate then rollback restores legacy config, state untouched"

# 2. Idempotent.
out=$(sh "$ROLLBACK" "$WORK/tmkms.toml")
echo "$out" | grep -q 'already loadable' || fail "second rollback not a no-op: $out"
[ "$(find "$WORK" -maxdepth 1 -name 'tmkms.toml.rollback-bak.*' | wc -l)" = 1 ] || fail "second rollback wrote another backup"
echo "ok 2 idempotent"

# 3. The new template (fresh install) becomes 0.12-loadable too.
cp "$HERE/../template/tmkms.toml" "$WORK/new.toml"
sh "$ROLLBACK" "$WORK/new.toml" >/dev/null
grep -q '^sign_extensions' "$WORK/new.toml" && fail "sign_extensions left in template rollback"
grep -q '^protocol_version = "v0.34"$' "$WORK/new.toml" || fail "protocol_version not v0.34"
echo "ok 3 fresh-install template rolled back"

# 4. A v0.38 protocol_version (unparseable by 0.12) is reset.
setup
sed -i 's/^protocol_version = .*/protocol_version = "v0.38"/' "$WORK/tmkms.toml"
sh "$ROLLBACK" "$WORK/tmkms.toml" >/dev/null
grep -q '^protocol_version = "v0.34"$' "$WORK/tmkms.toml" || fail "v0.38 not reset"
echo "ok 4 v0.38 reset to v0.34"

# 5. Missing file.
if sh "$ROLLBACK" "$WORK/missing.toml" 2>/dev/null; then fail "missing file accepted"; fi
echo "ok 5 errors"

echo "PASS rollback-toml_test.sh"
