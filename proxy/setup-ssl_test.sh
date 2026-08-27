#!/usr/bin/env bash

set -euo pipefail

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
TEST_ROOT=$(mktemp -d)
SSL_DIR="$TEST_ROOT/ssl"
STATE_DIR="$TEST_ROOT/state"
PID=

cleanup() {
  if [ -n "$PID" ]; then
    kill "$PID" >/dev/null 2>&1 || true
    wait "$PID" >/dev/null 2>&1 || true
  fi
  rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

fail() {
  echo "setup-ssl_test: $*" >&2
  for log in "$TEST_ROOT"/*.log; do
    [ -f "$log" ] && cat "$log" >&2
  done
  exit 1
}

mkdir -p "$TEST_ROOT/bin" "$SSL_DIR" "$STATE_DIR"

cat > "$TEST_ROOT/bin/curl" <<'EOF'
#!/bin/sh
set -eu

case " $* " in
  *"/health "*)
    ;;
  *"/v1/tokens "*)
    printf '%s\n' '{"token":"test-token"}'
    ;;
  *"/v1/certs/auto "*)
    printf '{"certificate":"%s","private_key":"%s","order_id":"%s"}\n' \
      "${TEST_ISSUE_CERT:-INITIAL CERTIFICATE}" \
      "${TEST_ISSUE_KEY:-INITIAL PRIVATE KEY}" \
      "${TEST_ISSUE_ORDER_ID-order-1}"
    ;;
  *"/renew "*)
    printf '%s' "${TEST_RENEW_STATUS:-200}"
    ;;
  *"/bundle "*)
    printf '%s\n' '-----BEGIN CERTIFICATE-----' \
      "${TEST_RENEWED_CERT:-RENEWED CERTIFICATE}" \
      '-----END CERTIFICATE-----'
    ;;
  *)
    echo "unexpected curl request: $*" >&2
    exit 1
    ;;
esac
EOF

REAL_MV=$(command -v mv)
export REAL_MV STATE_DIR
cat > "$TEST_ROOT/bin/mv" <<'EOF'
#!/bin/sh
set -eu

target=
for argument in "$@"; do
  target=$argument
done

pause() {
  : > "$STATE_DIR/mv-reached"
  while [ ! -f "$STATE_DIR/continue" ]; do
    sleep 0.01
  done
}

if [ "${TEST_MV_PAUSE:-}" = before ] \
    && [ "${target##*/}" = "${TEST_MV_TARGET:-cert.pem}" ]; then
  pause
fi
if [ -n "${TEST_MV_FAIL_TARGET:-}" ] \
    && [ "${target##*/}" = "$TEST_MV_FAIL_TARGET" ] \
    && [ ! -f "$STATE_DIR/mv-failed" ]; then
  : > "$STATE_DIR/mv-failed"
  exit 1
fi
"$REAL_MV" "$@"
if [ "${TEST_MV_PAUSE:-}" = after ] \
    && [ "${target##*/}" = "${TEST_MV_TARGET:-cert.pem}" ]; then
  pause
fi
EOF
chmod +x "$TEST_ROOT/bin/curl" "$TEST_ROOT/bin/mv"

wait_for_mv() {
  for _ in $(seq 1 200); do
    [ -f "$STATE_DIR/mv-reached" ] && return 0
    if [ -n "$PID" ] && ! kill -0 "$PID" 2>/dev/null; then
      return 1
    fi
    sleep 0.01
  done
  return 1
}

start_setup() {
  local pause=$1 target=$2 mode=$3 log=$4
  PATH="$TEST_ROOT/bin:$PATH" \
    SSL_DIR="$SSL_DIR" \
    CERT_ISSUER_DOMAIN=example.test \
    PROXY_SSL_WAIT_SECONDS=1 \
    TEST_MV_PAUSE="$pause" \
    TEST_MV_TARGET="$target" \
    TEST_MV_FAIL_TARGET="${TEST_MV_FAIL_TARGET:-}" \
    TEST_ISSUE_CERT="${TEST_ISSUE_CERT:-INITIAL CERTIFICATE}" \
    TEST_ISSUE_KEY="${TEST_ISSUE_KEY:-INITIAL PRIVATE KEY}" \
    TEST_ISSUE_ORDER_ID="${TEST_ISSUE_ORDER_ID-order-1}" \
    TEST_RENEW_STATUS="${TEST_RENEW_STATUS:-200}" \
    TEST_RENEWED_CERT="${TEST_RENEWED_CERT:-RENEWED CERTIFICATE}" \
    bash "$ROOT/setup-ssl.sh" "$mode" > "$log" 2>&1 &
  PID=$!
}

wait_for_setup() {
  local expected_status=$1
  local actual_status

  if wait "$PID"; then
    actual_status=0
  else
    actual_status=$?
  fi
  PID=
  [ "$actual_status" -eq "$expected_status" ] \
    || fail "setup returned $actual_status instead of $expected_status"
}

file_mode() {
  if stat -c '%a' "$1" >/dev/null 2>&1; then
    stat -c '%a' "$1"
  else
    stat -f '%Lp' "$1"
  fi
}

assert_no_staging_files() {
  if find "$SSL_DIR" \( -name '.*.tmp.*' -o -name '.*.backup.*' \) \
      -print -quit | grep -q .; then
    fail "certificate publication left staging files behind"
  fi
}

# A stale half-pair must not combine with a newly issued bundle. Pause before
# the certificate rename and verify that metadata and key are complete first.
printf '%s\n' 'STALE CERTIFICATE' > "$SSL_DIR/cert.pem"
printf '%s\n' 'stale-order' > "$SSL_DIR/order.id"
start_setup before cert.pem issue "$TEST_ROOT/issue.log"
wait_for_mv || fail "initial publication did not reach the certificate rename"
[ "$(cat "$SSL_DIR/private.key")" = 'INITIAL PRIVATE KEY' ] \
  || fail "initial private key was not published as a complete file"
[ "$(cat "$SSL_DIR/order.id")" = 'order-1' ] \
  || fail "order ID was not published before the certificate marker"
[ ! -e "$SSL_DIR/cert.pem" ] \
  || fail "initial publication exposed a certificate before the pair was ready"
[ "$(file_mode "$SSL_DIR/private.key")" = 600 ] \
  || fail "initial private key was exposed with the wrong mode"
[ "$(file_mode "$SSL_DIR/order.id")" = 600 ] \
  || fail "order ID was exposed with the wrong mode"
: > "$STATE_DIR/continue"
wait_for_setup 0
[ "$(cat "$SSL_DIR/cert.pem")" = 'INITIAL CERTIFICATE' ] \
  || fail "initial certificate content changed"
[ "$(cat "$SSL_DIR/private.key")" = 'INITIAL PRIVATE KEY' ] \
  || fail "initial private key was not published completely"
[ "$(cat "$SSL_DIR/order.id")" = 'order-1' ] \
  || fail "initial order ID was not published"
[ "$(file_mode "$SSL_DIR/cert.pem")" = 644 ] \
  || fail "initial certificate mode is not 0644"
[ "$(file_mode "$SSL_DIR/private.key")" = 600 ] \
  || fail "initial private key mode is not 0600"
[ "$(file_mode "$SSL_DIR/order.id")" = 600 ] \
  || fail "initial order ID mode is not 0600"
assert_no_staging_files

# The issuer always supplies an order ID. Rejecting a malformed response before
# changing live files is safer than silently disabling future renewal.
TEST_ISSUE_ORDER_ID=
start_setup none none issue "$TEST_ROOT/missing-order.log"
wait_for_setup 1
[ "$(cat "$SSL_DIR/cert.pem")" = 'INITIAL CERTIFICATE' ] \
  || fail "missing order ID changed the active certificate"
[ "$(cat "$SSL_DIR/private.key")" = 'INITIAL PRIVATE KEY' ] \
  || fail "missing order ID changed the active private key"
[ "$(cat "$SSL_DIR/order.id")" = 'order-1' ] \
  || fail "missing order ID removed the active order"
TEST_ISSUE_ORDER_ID=order-1

# Renewal must leave the complete old certificate visible until the atomic
# rename. The private key is unchanged by the renewal contract.
rm -f "$STATE_DIR/mv-reached" "$STATE_DIR/continue"
start_setup before cert.pem renew "$TEST_ROOT/renew.log"
wait_for_mv || fail "renewal did not reach the certificate rename"
[ "$(cat "$SSL_DIR/cert.pem")" = 'INITIAL CERTIFICATE' ] \
  || fail "renewal modified the active certificate before rename"
[ "$(cat "$SSL_DIR/private.key")" = 'INITIAL PRIVATE KEY' ] \
  || fail "renewal modified the private key"
[ "$(cat "$SSL_DIR/order.id")" = 'order-1' ] \
  || fail "renewal modified the order ID"
: > "$STATE_DIR/continue"
wait_for_setup 10
grep -q '^RENEWED CERTIFICATE$' "$SSL_DIR/cert.pem" \
  || fail "renewed certificate was not published"
[ "$(cat "$SSL_DIR/private.key")" = 'INITIAL PRIVATE KEY' ] \
  || fail "renewal replaced the private key"
[ "$(cat "$SSL_DIR/order.id")" = 'order-1' ] \
  || fail "renewal replaced the order ID"
assert_no_staging_files

# A successful 404 fallback can replace the key, so it must request an nginx
# reload instead of reporting "no renewal needed".
TEST_RENEW_STATUS=404
TEST_ISSUE_CERT='FALLBACK CERTIFICATE'
TEST_ISSUE_KEY='FALLBACK PRIVATE KEY'
TEST_ISSUE_ORDER_ID='order-2'
rm -f "$STATE_DIR/mv-reached" "$STATE_DIR/continue" "$STATE_DIR/mv-failed"
start_setup before cert.pem renew "$TEST_ROOT/fallback-success.log"
wait_for_mv || fail "404 fallback did not reach the certificate rename"
[ ! -e "$SSL_DIR/cert.pem" ] \
  || fail "404 fallback exposed its certificate before the bundle was ready"
[ "$(cat "$SSL_DIR/private.key")" = 'FALLBACK PRIVATE KEY' ] \
  || fail "404 fallback did not publish the complete private key"
[ "$(cat "$SSL_DIR/order.id")" = 'order-2' ] \
  || fail "404 fallback did not publish the replacement order ID first"
: > "$STATE_DIR/continue"
wait_for_setup 10
[ "$(cat "$SSL_DIR/cert.pem")" = 'FALLBACK CERTIFICATE' ] \
  || fail "404 fallback did not publish the replacement certificate"
assert_no_staging_files

# A failed final rename leaves cert.pem absent rather than exposing a mismatched
# bundle. The next renewal invocation detects that marker and self-heals by
# issuing a complete replacement without using rollback files.
TEST_ISSUE_CERT='RECOVERED CERTIFICATE'
TEST_ISSUE_KEY='RECOVERED PRIVATE KEY'
TEST_ISSUE_ORDER_ID='order-3'
TEST_MV_FAIL_TARGET=cert.pem
rm -f "$STATE_DIR/mv-reached" "$STATE_DIR/continue" "$STATE_DIR/mv-failed"
start_setup none none renew "$TEST_ROOT/fallback-failure.log"
wait_for_setup 1
[ -f "$STATE_DIR/mv-failed" ] \
  || fail "fallback test did not inject the certificate rename failure"
[ ! -e "$SSL_DIR/cert.pem" ] \
  || fail "failed fallback left a certificate marker"
[ ! -e "$SSL_DIR/private.key" ] \
  || fail "failed fallback left private-key material"
[ ! -e "$SSL_DIR/order.id" ] \
  || fail "failed fallback left order metadata"
assert_no_staging_files
if find "$SSL_DIR" \( -name '.private.key.*' -o -name '.order.id.*' \) \
    -print -quit | grep -q .; then
  fail "failed fallback left secret staging files"
fi

TEST_MV_FAIL_TARGET=
rm -f "$STATE_DIR/mv-reached" "$STATE_DIR/continue" "$STATE_DIR/mv-failed"
start_setup before cert.pem renew "$TEST_ROOT/fallback-recovery.log"
wait_for_mv || fail "missing-marker recovery did not reach certificate rename"
[ ! -e "$SSL_DIR/cert.pem" ] \
  || fail "recovery exposed its certificate before the bundle was ready"
: > "$STATE_DIR/continue"
wait_for_setup 10
[ "$(cat "$SSL_DIR/cert.pem")" = 'RECOVERED CERTIFICATE' ] \
  || fail "missing-marker recovery did not publish the certificate"
[ "$(cat "$SSL_DIR/private.key")" = 'RECOVERED PRIVATE KEY' ] \
  || fail "missing-marker recovery did not publish the private key"
[ "$(cat "$SSL_DIR/order.id")" = 'order-3' ] \
  || fail "missing-marker recovery did not publish the order ID"
[ "$(file_mode "$SSL_DIR/cert.pem")" = 644 ] \
  || fail "recovered certificate mode is not 0644"
[ "$(file_mode "$SSL_DIR/private.key")" = 600 ] \
  || fail "recovered private key mode is not 0600"
[ "$(file_mode "$SSL_DIR/order.id")" = 600 ] \
  || fail "recovered order ID mode is not 0600"

assert_no_staging_files

echo "setup-ssl_test: ok"
