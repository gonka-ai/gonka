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

make_pair() {
  local name=$1 key
  key=${2:-$TEST_ROOT/$name.key}
  openssl req -x509 -new -keyout "$key" -out "$TEST_ROOT/$name.crt" \
    -newkey rsa:2048 -nodes -days 365 -subj "/CN=$name.test" \
    >/dev/null 2>&1
}

make_pair initial
openssl req -x509 -new -key "$TEST_ROOT/initial.key" \
  -out "$TEST_ROOT/renewed.crt" -days 365 -subj '/CN=initial.test' \
  >/dev/null 2>&1
make_pair fallback
make_pair recovered
INITIAL_CERT=$(cat "$TEST_ROOT/initial.crt")
INITIAL_KEY=$(cat "$TEST_ROOT/initial.key")
RENEWED_CERT=$(cat "$TEST_ROOT/renewed.crt")
FALLBACK_CERT=$(cat "$TEST_ROOT/fallback.crt")
FALLBACK_KEY=$(cat "$TEST_ROOT/fallback.key")
RECOVERED_CERT=$(cat "$TEST_ROOT/recovered.crt")
RECOVERED_KEY=$(cat "$TEST_ROOT/recovered.key")
export INITIAL_CERT INITIAL_KEY RENEWED_CERT FALLBACK_CERT FALLBACK_KEY
export RECOVERED_CERT RECOVERED_KEY
CURL_LOG="$TEST_ROOT/curl.log"
export CURL_LOG

cat > "$TEST_ROOT/bin/curl" <<'EOF'
#!/bin/sh
set -eu

printf '%s\n' "$*" >> "$CURL_LOG"

case " $* " in
  *"/health "*)
    ;;
  *"/v1/tokens "*)
    printf '%s\n' '{"token":"test-token"}'
    ;;
  *"/v1/certs/auto "*)
    jq -nc --arg certificate "${TEST_ISSUE_CERT:-$INITIAL_CERT}" \
      --arg private_key "${TEST_ISSUE_KEY:-$INITIAL_KEY}" \
      --arg order_id "${TEST_ISSUE_ORDER_ID-order-1}" \
      '{certificate:$certificate,private_key:$private_key,order_id:$order_id}'
    ;;
  *"/renew "*)
    printf '%s' "${TEST_RENEW_STATUS:-200}"
    ;;
  *"/bundle "*)
    bundle=${TEST_BUNDLE_CERT:-${TEST_RENEWED_CERT:-$RENEWED_CERT}}
    status=${TEST_BUNDLE_STATUS:-200}
    output_file=
    previous=
    for argument in "$@"; do
      if [ "$previous" = -o ]; then output_file=$argument; fi
      previous=$argument
    done
    if [ -n "$output_file" ] && [ "$status" = 200 ]; then
      printf '%s\n' "$bundle" > "$output_file"
    elif [ -z "$output_file" ] && [ "$status" = 200 ]; then
      printf '%s\n' "$bundle"
    fi
    case " $* " in *" -w "*) printf '%s' "$status" ;; esac
    ;;
  *)
    echo "unexpected curl request: $*" >&2
    exit 1
    ;;
esac
EOF

REAL_MV=$(command -v mv)
REAL_CHMOD=$(command -v chmod)
export REAL_MV REAL_CHMOD STATE_DIR
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
    [ -d "$STATE_DIR" ] || exit 1
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
cat > "$TEST_ROOT/bin/chmod" <<'EOF'
#!/bin/sh
set -eu

[ "${TEST_CHMOD_FAIL:-}" != true ] || exit 1
exec "$REAL_CHMOD" "$@"
EOF
chmod +x "$TEST_ROOT/bin/curl" "$TEST_ROOT/bin/mv" "$TEST_ROOT/bin/chmod"

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
    RENEW_BEFORE_DAYS=400 \
    TEST_MV_PAUSE="$pause" \
    TEST_MV_TARGET="$target" \
    TEST_MV_FAIL_TARGET="${TEST_MV_FAIL_TARGET:-}" \
    TEST_CHMOD_FAIL="${TEST_CHMOD_FAIL:-false}" \
    TEST_ISSUE_CERT="${TEST_ISSUE_CERT:-$INITIAL_CERT}" \
    TEST_ISSUE_KEY="${TEST_ISSUE_KEY:-$INITIAL_KEY}" \
    TEST_ISSUE_ORDER_ID="${TEST_ISSUE_ORDER_ID-order-1}" \
    TEST_RENEW_STATUS="${TEST_RENEW_STATUS:-200}" \
    TEST_RENEWED_CERT="${TEST_RENEWED_CERT:-$RENEWED_CERT}" \
    TEST_BUNDLE_CERT="${TEST_BUNDLE_CERT:-$RENEWED_CERT}" \
    TEST_BUNDLE_STATUS="${TEST_BUNDLE_STATUS:-200}" \
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
  if find "$SSL_DIR" -name '*.tmp.*' -print -quit | grep -q .; then
    fail "certificate publication left staging files behind"
  fi
}

# A stale half-pair must not combine with a newly issued bundle. Pause before
# the certificate rename and verify that metadata and key are complete first.
printf '%s\n' 'STALE CERTIFICATE' > "$SSL_DIR/cert.pem"
printf '%s\n' 'stale-order' > "$SSL_DIR/order.id"
printf '%s\n' 'ORPHANED CERTIFICATE' > "$SSL_DIR/cert.pem.tmp.999"
printf '%s\n' 'ORPHANED PRIVATE KEY' > "$SSL_DIR/private.key.tmp.999"
printf '%s\n' 'orphaned-order' > "$SSL_DIR/order.id.tmp.999"
start_setup before cert.pem issue "$TEST_ROOT/issue.log"
wait_for_mv || fail "initial publication did not reach the certificate rename"
[ "$(cat "$SSL_DIR/private.key")" = "$INITIAL_KEY" ] \
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
[ "$(cat "$SSL_DIR/cert.pem")" = "$INITIAL_CERT" ] \
  || fail "initial certificate content changed"
[ "$(cat "$SSL_DIR/private.key")" = "$INITIAL_KEY" ] \
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

# A failure while staging the public certificate happens before commit and
# must not touch the active certificate, key, or order metadata.
TEST_CHMOD_FAIL=true
start_setup none none issue "$TEST_ROOT/staging-failure.log"
wait_for_setup 1
[ "$(cat "$SSL_DIR/cert.pem")" = "$INITIAL_CERT" ] \
  || fail "staging failure changed the active certificate"
[ "$(cat "$SSL_DIR/private.key")" = "$INITIAL_KEY" ] \
  || fail "staging failure changed the active private key"
[ "$(cat "$SSL_DIR/order.id")" = 'order-1' ] \
  || fail "staging failure changed the active order"
assert_no_staging_files
TEST_CHMOD_FAIL=false

# The issuer always supplies an order ID. Rejecting a malformed response before
# changing live files is safer than silently disabling future renewal.
TEST_ISSUE_ORDER_ID=
start_setup none none issue "$TEST_ROOT/missing-order.log"
wait_for_setup 1
[ "$(cat "$SSL_DIR/cert.pem")" = "$INITIAL_CERT" ] \
  || fail "missing order ID changed the active certificate"
[ "$(cat "$SSL_DIR/private.key")" = "$INITIAL_KEY" ] \
  || fail "missing order ID changed the active private key"
[ "$(cat "$SSL_DIR/order.id")" = 'order-1' ] \
  || fail "missing order ID removed the active order"
TEST_ISSUE_ORDER_ID=order-1

# Renewal must leave the complete old certificate visible until the atomic
# rename. The private key is unchanged by the renewal contract.
TEST_RENEWED_CERT=$RENEWED_CERT
TEST_MV_FAIL_TARGET=cert.pem
rm -f "$STATE_DIR/mv-reached" "$STATE_DIR/continue" "$STATE_DIR/mv-failed"
start_setup none none renew-if-needed "$TEST_ROOT/renew-failure.log"
wait_for_setup 1
[ -f "$STATE_DIR/mv-failed" ] \
  || fail "renew-if-needed test did not inject the certificate rename failure"
[ "$(cat "$SSL_DIR/cert.pem")" = "$INITIAL_CERT" ] \
  || fail "failed renewal changed the active certificate"
[ "$(cat "$SSL_DIR/private.key")" = "$INITIAL_KEY" ] \
  || fail "failed renewal changed the private key"
[ "$(cat "$SSL_DIR/order.id")" = 'order-1' ] \
  || fail "failed renewal changed the order ID"
assert_no_staging_files

TEST_RENEWED_CERT=$RENEWED_CERT
TEST_MV_FAIL_TARGET=
rm -f "$STATE_DIR/mv-reached" "$STATE_DIR/continue"
start_setup before cert.pem renew "$TEST_ROOT/renew.log"
wait_for_mv || fail "renewal did not reach the certificate rename"
[ "$(cat "$SSL_DIR/cert.pem")" = "$INITIAL_CERT" ] \
  || fail "renewal modified the active certificate before rename"
[ "$(cat "$SSL_DIR/private.key")" = "$INITIAL_KEY" ] \
  || fail "renewal modified the private key"
[ "$(cat "$SSL_DIR/order.id")" = 'order-1' ] \
  || fail "renewal modified the order ID"
: > "$STATE_DIR/continue"
wait_for_setup 10
[ "$(cat "$SSL_DIR/cert.pem")" = "$RENEWED_CERT" ] \
  || fail "renewed certificate was not published"
[ "$(cat "$SSL_DIR/private.key")" = "$INITIAL_KEY" ] \
  || fail "renewal replaced the private key"
[ "$(cat "$SSL_DIR/order.id")" = 'order-1' ] \
  || fail "renewal replaced the order ID"
assert_no_staging_files

# A successful 404 fallback can replace the key, so it must request an nginx
# reload instead of reporting "no renewal needed".
TEST_RENEW_STATUS=404
TEST_ISSUE_CERT=$FALLBACK_CERT
TEST_ISSUE_KEY=$FALLBACK_KEY
TEST_ISSUE_ORDER_ID='order-2'
rm -f "$STATE_DIR/mv-reached" "$STATE_DIR/continue" "$STATE_DIR/mv-failed"
start_setup before cert.pem renew "$TEST_ROOT/fallback-success.log"
wait_for_mv || fail "404 fallback did not reach the certificate rename"
[ ! -e "$SSL_DIR/cert.pem" ] \
  || fail "404 fallback exposed its certificate before the bundle was ready"
[ "$(cat "$SSL_DIR/private.key")" = "$FALLBACK_KEY" ] \
  || fail "404 fallback did not publish the complete private key"
[ "$(cat "$SSL_DIR/order.id")" = 'order-2' ] \
  || fail "404 fallback did not publish the replacement order ID first"
: > "$STATE_DIR/continue"
wait_for_setup 10
[ "$(cat "$SSL_DIR/cert.pem")" = "$FALLBACK_CERT" ] \
  || fail "404 fallback did not publish the replacement certificate"
assert_no_staging_files

# A failed final rename leaves cert.pem absent rather than exposing a mismatched
# bundle. The next renewal invocation detects that marker and self-heals by
# issuing a complete replacement without using rollback files.
TEST_ISSUE_CERT=$RECOVERED_CERT
TEST_ISSUE_KEY=$RECOVERED_KEY
TEST_ISSUE_ORDER_ID='order-3'
TEST_RENEW_STATUS=404
TEST_MV_FAIL_TARGET=cert.pem
rm -f "$STATE_DIR/mv-reached" "$STATE_DIR/continue" "$STATE_DIR/mv-failed"
start_setup none none renew "$TEST_ROOT/fallback-failure.log"
wait_for_setup 1
[ -f "$STATE_DIR/mv-failed" ] \
  || fail "fallback test did not inject the certificate rename failure"
[ ! -e "$SSL_DIR/cert.pem" ] \
  || fail "failed fallback left a certificate marker"
[ "$(cat "$SSL_DIR/private.key")" = "$RECOVERED_KEY" ] \
  || fail "failed fallback lost the staged private key"
[ "$(cat "$SSL_DIR/order.id")" = 'order-3' ] \
  || fail "failed fallback lost the order needed for background recovery"
assert_no_staging_files

TEST_MV_FAIL_TARGET=
TEST_BUNDLE_CERT=$RECOVERED_CERT
rm -f "$STATE_DIR/mv-reached" "$STATE_DIR/continue" "$STATE_DIR/mv-failed"
: > "$CURL_LOG"
start_setup before cert.pem renew "$TEST_ROOT/fallback-recovery.log"
wait_for_mv || fail "missing-marker recovery did not reach certificate rename"
[ ! -e "$SSL_DIR/cert.pem" ] \
  || fail "recovery exposed its certificate before the bundle was ready"
: > "$STATE_DIR/continue"
wait_for_setup 10
[ "$(cat "$SSL_DIR/cert.pem")" = "$RECOVERED_CERT" ] \
  || fail "missing-marker recovery did not publish the certificate"
[ "$(cat "$SSL_DIR/private.key")" = "$RECOVERED_KEY" ] \
  || fail "missing-marker recovery did not publish the private key"
[ "$(cat "$SSL_DIR/order.id")" = 'order-3' ] \
  || fail "missing-marker recovery did not publish the order ID"
[ "$(file_mode "$SSL_DIR/cert.pem")" = 644 ] \
  || fail "recovered certificate mode is not 0644"
[ "$(file_mode "$SSL_DIR/private.key")" = 600 ] \
  || fail "recovered private key mode is not 0600"
[ "$(file_mode "$SSL_DIR/order.id")" = 600 ] \
  || fail "recovered order ID mode is not 0600"
grep -q '/orders/order-3/bundle' "$CURL_LOG" \
  || fail "missing-marker recovery did not reuse the stored order"
if grep -q '/v1/certs/auto' "$CURL_LOG"; then
  fail "missing-marker recovery created an unnecessary order"
fi

# Existing deployments can contain a truncated certificate or a valid
# certificate paired with the wrong key. Repair must recover both from the
# stored order without waiting for certificate expiry or creating a new order.
: > "$SSL_DIR/cert.pem"
: > "$CURL_LOG"
start_setup none none repair "$TEST_ROOT/truncated-repair.log"
wait_for_setup 10
[ "$(cat "$SSL_DIR/cert.pem")" = "$RECOVERED_CERT" ] \
  || fail "repair did not replace a truncated certificate"
if grep -q '/v1/certs/auto' "$CURL_LOG"; then
  fail "truncated-certificate repair created an unnecessary order"
fi

printf '%s\n' "$FALLBACK_CERT" > "$SSL_DIR/cert.pem"
: > "$CURL_LOG"
start_setup none none repair "$TEST_ROOT/mismatched-repair.log"
wait_for_setup 10
[ "$(cat "$SSL_DIR/cert.pem")" = "$RECOVERED_CERT" ] \
  || fail "repair did not replace a certificate that mismatched its key"
if grep -q '/v1/certs/auto' "$CURL_LOG"; then
  fail "mismatched-pair repair created an unnecessary order"
fi

# Reject malformed or mismatched issuer responses before changing the live
# completion marker.
TEST_ISSUE_CERT='not a certificate'
TEST_ISSUE_KEY=$RECOVERED_KEY
start_setup none none issue "$TEST_ROOT/invalid-issue.log"
wait_for_setup 1
[ "$(cat "$SSL_DIR/cert.pem")" = "$RECOVERED_CERT" ] \
  || fail "invalid issuer response changed the active certificate"

assert_no_staging_files

echo "setup-ssl_test: ok"
