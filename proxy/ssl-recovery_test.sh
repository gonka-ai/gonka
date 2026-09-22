#!/usr/bin/env bash

set -euo pipefail

IMAGE=${PROXY_SSL_RECOVERY_TEST_IMAGE:-gonka-proxy-ssl-recovery-test}
TEST_ROOT=$(mktemp -d)
CONTAINER="gonka-proxy-ssl-recovery-test-$$"

cleanup() {
  docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
  rm -rf "$TEST_ROOT"
}
trap cleanup EXIT

fail() {
  echo "ssl-recovery_test: $*" >&2
  docker logs "$CONTAINER" >&2 || true
  exit 1
}

mkdir -p "$TEST_ROOT/ssl"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
  -subj /CN=example.test \
  -keyout "$TEST_ROOT/ssl/private.key" \
  -out "$TEST_ROOT/recovered.crt" >/dev/null 2>&1
: > "$TEST_ROOT/ssl/cert.pem"
printf '%s\n' order-legacy > "$TEST_ROOT/ssl/order.id"
: > "$TEST_ROOT/setup-calls"
: > "$TEST_ROOT/defer-repair"

cat > "$TEST_ROOT/setup-ssl.sh" <<'EOF'
#!/bin/sh
set -eu

printf '%s\n' "$1" >> /test/setup-calls
case "$1" in
  repair)
    if [ -f /test/defer-repair ] \
        && [ "$(grep -c '^repair$' /test/setup-calls)" -eq 1 ]; then
      exit 0
    fi
    cp /test/recovered.crt /etc/nginx/ssl/cert.pem.tmp
    chmod 644 /etc/nginx/ssl/cert.pem.tmp
    mv -f /etc/nginx/ssl/cert.pem.tmp /etc/nginx/ssl/cert.pem
    exit 10
    ;;
  renew-if-needed)
    exit 0
    ;;
esac
exit 1
EOF
chmod +x "$TEST_ROOT/setup-ssl.sh"

docker run -d --name "$CONTAINER" \
  -v "$TEST_ROOT/ssl:/etc/nginx/ssl" \
  -v "$TEST_ROOT/recovered.crt:/test/recovered.crt:ro" \
  -v "$TEST_ROOT/setup-ssl.sh:/setup-ssl.sh:ro" \
  -v "$TEST_ROOT/setup-calls:/test/setup-calls" \
  -v "$TEST_ROOT/defer-repair:/test/defer-repair:ro" \
  -e NGINX_MODE=both \
  -e CERT_ISSUER_DOMAIN=example.test \
  -e PROXY_SSL_RETRY_SECONDS=1 \
  -e RENEW_INTERVAL_HOURS=1 \
  -e DISABLE_DEVSHARD_PROXY=true \
  "$IMAGE" >/dev/null

for _ in $(seq 1 100); do
  if docker logs "$CONTAINER" 2>&1 \
      | grep -q 'Certificate configuration reloaded'; then
    break
  fi
  sleep 0.1
done

logs=$(docker logs "$CONTAINER" 2>&1)
grep -q 'Falling back to HTTP-only configuration' <<< "$logs" \
  || fail "nginx did not enter HTTP fallback"
grep -q 'TLS bundle repaired; retrying HTTPS configuration' <<< "$logs" \
  || fail "the invalid TLS bundle was not repaired"
grep -q 'Certificate configuration reloaded' <<< "$logs" \
  || fail "HTTPS was not restored"
[ "$(grep -c '^repair$' "$TEST_ROOT/setup-calls")" -eq 2 ] \
  || fail "the worker did not use the repair operation"
docker exec "$CONTAINER" curl -ksSf https://127.0.0.1/health >/dev/null \
  || fail "HTTPS health endpoint is unavailable after repair"

docker rm -f "$CONTAINER" >/dev/null
: > "$TEST_ROOT/setup-calls"
rm -f "$TEST_ROOT/ssl/cert.pem"
docker run -d --name "$CONTAINER" \
  -v "$TEST_ROOT/ssl:/etc/nginx/ssl" \
  -v "$TEST_ROOT/recovered.crt:/test/recovered.crt:ro" \
  -v "$TEST_ROOT/setup-ssl.sh:/setup-ssl.sh:ro" \
  -v "$TEST_ROOT/setup-calls:/test/setup-calls" \
  -e NGINX_MODE=both \
  -e CERT_ISSUER_DOMAIN=example.test \
  -e DISABLE_DEVSHARD_PROXY=true \
  "$IMAGE" >/dev/null
for _ in $(seq 1 100); do
  if docker exec "$CONTAINER" \
      curl -ksSf https://127.0.0.1/health >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
[ "$(head -n 1 "$TEST_ROOT/setup-calls")" = repair ] \
  || fail "startup did not recover the missing certificate through repair"
docker exec "$CONTAINER" curl -ksSf https://127.0.0.1/health >/dev/null \
  || fail "HTTPS did not start after missing-marker recovery"

docker rm -f "$CONTAINER" >/dev/null
: > "$TEST_ROOT/setup-calls"
rm -f "$TEST_ROOT/ssl/cert.pem"
: > "$TEST_ROOT/ssl/cert.pem"
docker run -d --name "$CONTAINER" \
  -v "$TEST_ROOT/ssl:/etc/nginx/ssl" \
  -v "$TEST_ROOT/recovered.crt:/test/recovered.crt:ro" \
  -v "$TEST_ROOT/setup-ssl.sh:/setup-ssl.sh:ro" \
  -v "$TEST_ROOT/setup-calls:/test/setup-calls" \
  -e NGINX_MODE=https \
  -e CERT_ISSUER_DOMAIN=example.test \
  -e DISABLE_DEVSHARD_PROXY=true \
  "$IMAGE" >/dev/null
for _ in $(seq 1 100); do
  if docker exec "$CONTAINER" \
      curl -ksSf https://127.0.0.1/health >/dev/null 2>&1; then
    break
  fi
  sleep 0.1
done
[ "$(head -n 1 "$TEST_ROOT/setup-calls")" = repair ] \
  || fail "HTTPS-only startup did not repair the invalid certificate"
docker exec "$CONTAINER" curl -ksSf https://127.0.0.1/health >/dev/null \
  || fail "HTTPS-only startup did not recover"

assert_invalid_interval() {
  local name=$1 value=$2 log="$TEST_ROOT/$1.log"

  docker rm -f "$CONTAINER" >/dev/null
  if docker run --name "$CONTAINER" \
      -v "$TEST_ROOT/ssl:/etc/nginx/ssl" \
      -e NGINX_MODE=both \
      -e CERT_ISSUER_DOMAIN=example.test \
      -e DISABLE_DEVSHARD_PROXY=true \
      -e "$name=$value" \
      "$IMAGE" > "$log" 2>&1; then
    fail "$name=$value was accepted"
  fi
  grep -q "ERROR: $name must be a positive integer" "$log" \
    || fail "$name=$value did not report a validation error"
}

assert_invalid_interval PROXY_SSL_RETRY_SECONDS 0
assert_invalid_interval PROXY_SSL_RETRY_SECONDS 00
assert_invalid_interval RENEW_INTERVAL_HOURS -1

echo "ssl-recovery_test: ok"
