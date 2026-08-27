#!/usr/bin/env bash

set -euo pipefail

ROOT=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
TMPDIR=$(mktemp -d)
SSL_DIR="$TMPDIR/ssl"
STATE_DIR="$TMPDIR/state"
PID=

cleanup() {
  if [ -n "$PID" ]; then
    kill "$PID" >/dev/null 2>&1 || true
    wait "$PID" >/dev/null 2>&1 || true
  fi
  rm -rf "$TMPDIR"
}
trap cleanup EXIT

fail() {
  echo "setup-ssl_test: $*" >&2
  for log in "$TMPDIR"/*.log; do
    [ -f "$log" ] && cat "$log" >&2
  done
  exit 1
}

mkdir -p "$TMPDIR/bin" "$SSL_DIR" "$STATE_DIR"

cat > "$TMPDIR/bin/curl" <<'EOF'
#!/bin/sh
set -eu

case " $* " in
  *"/health "*)
    ;;
  *"/v1/tokens "*)
    printf '%s\n' '{"token":"test-token"}'
    ;;
  *"/v1/certs/auto "*)
    printf '%s\n' '{"certificate":"INITIAL CERTIFICATE","private_key":"INITIAL PRIVATE KEY","order_id":"order-1"}'
    ;;
  *"/v1/certs/orders/order-1/renew "*)
    printf '200'
    ;;
  *"/v1/certs/orders/order-1/bundle "*)
    printf '%s\n' '-----BEGIN CERTIFICATE-----' 'RENEWED CERTIFICATE' '-----END CERTIFICATE-----'
    ;;
  *)
    echo "unexpected curl request: $*" >&2
    exit 1
    ;;
esac
EOF

REAL_MV=$(command -v mv)
export REAL_MV STATE_DIR
cat > "$TMPDIR/bin/mv" <<'EOF'
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
"$REAL_MV" "$@"
if [ "${TEST_MV_PAUSE:-}" = after ] \
    && [ "${target##*/}" = "${TEST_MV_TARGET:-cert.pem}" ]; then
  pause
fi
EOF
chmod +x "$TMPDIR/bin/curl" "$TMPDIR/bin/mv"

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
  PATH="$TMPDIR/bin:$PATH" \
    SSL_DIR="$SSL_DIR" \
    CERT_ISSUER_DOMAIN=example.test \
    PROXY_SSL_WAIT_SECONDS=1 \
    TEST_MV_PAUSE="$pause" \
    TEST_MV_TARGET="$target" \
    bash "$ROOT/setup-ssl.sh" "$mode" > "$log" 2>&1 &
  PID=$!
}

# A stale half-pair must not combine with one newly published file. Pause after
# the key rename and verify cert.pem, the pair's commit marker, is still absent.
printf '%s\n' 'STALE CERTIFICATE' > "$SSL_DIR/cert.pem"
start_setup after private.key issue "$TMPDIR/issue.log"
wait_for_mv || fail "initial publication did not reach the private-key rename"
[ "$(cat "$SSL_DIR/private.key")" = 'INITIAL PRIVATE KEY' ] \
  || fail "initial private key was not published as a complete file"
[ ! -e "$SSL_DIR/cert.pem" ] \
  || fail "initial publication exposed a certificate before the pair was ready"
: > "$STATE_DIR/continue"
if wait "$PID"; then
  issue_status=0
else
  issue_status=$?
fi
PID=
[ "$issue_status" -eq 0 ] || fail "initial certificate setup failed with $issue_status"
[ "$(cat "$SSL_DIR/cert.pem")" = 'INITIAL CERTIFICATE' ] \
  || fail "initial certificate content changed"
[ "$(cat "$SSL_DIR/private.key")" = 'INITIAL PRIVATE KEY' ] \
  || fail "initial private key was not published completely"
[ "$(stat -c '%a' "$SSL_DIR/cert.pem")" = 644 ] \
  || fail "initial certificate mode is not 0644"
[ "$(stat -c '%a' "$SSL_DIR/private.key")" = 600 ] \
  || fail "initial private key mode is not 0600"

# Renewal must leave the complete old certificate visible until the atomic
# rename. The private key is unchanged by the renewal contract.
rm -f "$STATE_DIR/mv-reached" "$STATE_DIR/continue"
start_setup before cert.pem renew "$TMPDIR/renew.log"
wait_for_mv || fail "renewal did not reach the certificate rename"
[ "$(cat "$SSL_DIR/cert.pem")" = 'INITIAL CERTIFICATE' ] \
  || fail "renewal modified the active certificate before rename"
[ "$(cat "$SSL_DIR/private.key")" = 'INITIAL PRIVATE KEY' ] \
  || fail "renewal modified the private key"
: > "$STATE_DIR/continue"
if wait "$PID"; then
  renew_status=0
else
  renew_status=$?
fi
PID=
[ "$renew_status" -eq 10 ] || fail "renewal returned $renew_status instead of 10"
grep -q '^RENEWED CERTIFICATE$' "$SSL_DIR/cert.pem" \
  || fail "renewed certificate was not published"
[ "$(cat "$SSL_DIR/private.key")" = 'INITIAL PRIVATE KEY' ] \
  || fail "renewal replaced the private key"

if find "$SSL_DIR" -name '.*.tmp.*' -print -quit | grep -q .; then
  fail "successful publication left temporary files behind"
fi

echo "setup-ssl_test: ok"
