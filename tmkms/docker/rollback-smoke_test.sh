#!/bin/sh
# End-to-end rollback check against the real legacy tmkms 0.12 image:
#   1. tmkms 0.12 refuses a migrated config (documents the hazard);
#   2. after rollback-toml.sh the same config parses and tmkms starts.
# No validator node is needed: "starting up" proves the config parsed; the
# following connection errors are expected.
#
# Needs Docker. Run from the repo root:
#   sh tmkms/docker/rollback-smoke_test.sh
# Env: TMKMS_LEGACY_IMAGE (default ghcr.io/product-science/tmkms-softsign-with-keygen:0.2.16)
set -eu

HERE=$(cd "$(dirname "$0")" && pwd)
IMAGE="${TMKMS_LEGACY_IMAGE:-ghcr.io/product-science/tmkms-softsign-with-keygen:0.2.16}"
WORK=$(mktemp -d)
NAME="tmkms-rollback-smoke-$$"
trap 'docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"' EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

# Runs the legacy image on $WORK and prints its logs once it either starts or fails.
run_legacy() {
  docker rm -f "$NAME" >/dev/null 2>&1 || true
  docker run -d --platform linux/amd64 --name "$NAME" -e VALIDATOR_LISTEN_ADDRESS=tcp://127.0.0.1:26658 \
    -v "$WORK:/root/.tmkms" "$IMAGE" >/dev/null
  i=0
  while [ $i -lt 60 ]; do
    logs=$(docker logs "$NAME" 2>&1 || true)
    case "$logs" in
      *"starting up"*|*"fatal error"*) break ;;
    esac
    i=$((i + 1)); sleep 2
  done
  docker logs "$NAME" 2>&1
}

mkdir -p "$WORK/state" "$WORK/secrets"
cp "$HERE/../template/tmkms.toml" "$WORK/tmkms.toml"   # current template = migrated shape

logs=$(run_legacy)
echo "$logs" | grep -q "unknown field \`sign_extensions\`" \
  || fail "expected tmkms 0.12 to reject sign_extensions; got: $(echo "$logs" | tail -3)"
echo "ok 1 tmkms 0.12 rejects the migrated config"

# Run the script the way an operator would: GNU userland (as in the image / Linux hosts).
docker run --rm -v "$HERE:/d:ro" -v "$WORK:/w" debian:bookworm-slim \
  sh /d/rollback-toml.sh /w/tmkms.toml >/dev/null
logs=$(run_legacy)
echo "$logs" | grep -q "tmkms 0.12" || fail "tmkms 0.12 did not start after rollback; got: $(echo "$logs" | tail -3)"
echo "$logs" | grep -q "unknown field" && fail "config still rejected after rollback"
echo "ok 2 tmkms 0.12 starts after rollback-toml.sh"

echo "PASS rollback-smoke_test.sh"
