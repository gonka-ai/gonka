#!/bin/bash

set -euo pipefail

MODE=${1:-issue}
SSL_DIR=${SSL_DIR:-/etc/nginx/ssl}
CERT_FILE="$SSL_DIR/cert.pem"
KEY_FILE="$SSL_DIR/private.key"
ORDER_ID_FILE="$SSL_DIR/order.id"

if [ -z "${CERT_ISSUER_DOMAIN:-}" ]; then
  echo "ERROR: CERT_ISSUER_DOMAIN is not set"
  exit 1
fi

echo "Getting SSL certificate for proxy..."

echo "setup-ssl.sh mode: $MODE"

mkdir -p "$SSL_DIR"

atomic_write_file() {
  local content="$1"
  local target="$2"
  local mode="$3"
  local temporary

  temporary=$(mktemp "$SSL_DIR/.${target##*/}.tmp.XXXXXX")
  if printf '%s\n' "$content" > "$temporary" \
      && chmod "$mode" "$temporary" \
      && mv -f "$temporary" "$target"; then
    return 0
  fi

  rm -f "$temporary"
  return 1
}

install_certificate_pair() {
  local certificate="$1"
  local private_key="$2"
  local cert_temporary key_temporary

  cert_temporary=$(mktemp "$SSL_DIR/.cert.pem.tmp.XXXXXX")
  key_temporary=$(mktemp "$SSL_DIR/.private.key.tmp.XXXXXX")

  if ! printf '%s\n' "$certificate" > "$cert_temporary" \
      || ! chmod 644 "$cert_temporary" \
      || ! printf '%s\n' "$private_key" > "$key_temporary" \
      || ! chmod 600 "$key_temporary"; then
    rm -f "$cert_temporary" "$key_temporary"
    return 1
  fi

  # Do not let a stale half-pair combine with one newly published file. Publish
  # the key first and cert.pem last as the pair's commit marker.
  if ! rm -f "$CERT_FILE" "$KEY_FILE" \
      || ! mv -f "$key_temporary" "$KEY_FILE"; then
    rm -f "$cert_temporary" "$key_temporary" "$CERT_FILE" "$KEY_FILE"
    return 1
  fi
  if ! mv -f "$cert_temporary" "$CERT_FILE"; then
    rm -f "$cert_temporary" "$CERT_FILE" "$KEY_FILE"
    return 1
  fi
}

# Resolve proxy-ssl host/port (respect KEY_NAME_PREFIX) and node_id
PROXY_SSL_SERVICE_NAME=${PROXY_SSL_SERVICE_NAME:-proxy-ssl}
PROXY_SSL_PORT=${PROXY_SSL_PORT:-8080}
KEY_NAME_PREFIX=${KEY_NAME_PREFIX:-}
FINAL_PROXY_SSL_SERVICE="${KEY_NAME_PREFIX}${PROXY_SSL_SERVICE_NAME}"
PROXY_SSL_BASE_URL="http://${FINAL_PROXY_SSL_SERVICE}:${PROXY_SSL_PORT}"
NODE_ID=${NODE_ID:-proxy}

# Wait for proxy-ssl to become available (default 60s)
MAX_WAIT=${PROXY_SSL_WAIT_SECONDS:-60}
echo "Waiting for ${FINAL_PROXY_SSL_SERVICE}:${PROXY_SSL_PORT} to be ready (up to ${MAX_WAIT}s)..."
for i in $(seq 1 "$MAX_WAIT"); do
  if curl -sSf "${PROXY_SSL_BASE_URL}/health" > /dev/null 2>&1; then
    echo "Service \"proxy-ssl\" is reachable"
    break
  fi
  if [ "$i" -eq "${MAX_WAIT}" ]; then
    echo "ERROR: Service \"proxy-ssl\" is not reachable at ${FINAL_PROXY_SSL_SERVICE}:${PROXY_SSL_PORT} after ${MAX_WAIT}s"
    exit 1
  fi
  sleep 1
done

# Get JWT token (retry few times)
TOKEN_RESPONSE=""
for i in 1 2 3 4 5; do
  TOKEN_RESPONSE=$(curl -sS -X POST "${PROXY_SSL_BASE_URL}/v1/tokens" \
    -H "Content-Type: application/json" \
    -d "{\"node_id\":\"${NODE_ID}\",\"expires_in_days\":30}" || true)
  TOKEN=$(echo "$TOKEN_RESPONSE" | jq -r '.token // empty' 2>/dev/null || true)
  if [ -n "${TOKEN:-}" ] && [ "${TOKEN}" != "null" ]; then
    break
  fi
  sleep 2
done

TOKEN=$(echo "$TOKEN_RESPONSE" | jq -r '.token // empty')
if [ -z "$TOKEN" ] || [ "$TOKEN" = "null" ]; then
  echo "ERROR: Failed to obtain JWT token from ${FINAL_PROXY_SSL_SERVICE}:${PROXY_SSL_PORT}"
  echo "$TOKEN_RESPONSE"
  exit 1
fi
# Helper: check if cert expires within N days using openssl -checkend
will_expire_within_days() {
  local days="$1"
  local seconds=$(( days * 24 * 3600 ))
  if [ ! -f "$CERT_FILE" ]; then
    return 0
  fi
  if openssl x509 -checkend "$seconds" -noout -in "$CERT_FILE" > /dev/null 2>&1; then
    # returns 0 when cert will NOT expire within N seconds
    return 1
  else
    # returns non-zero when cert WILL expire within N seconds
    return 0
  fi
}

if [ "$MODE" = "renew" ] || [ "$MODE" = "renew-if-needed" ]; then
  if [ ! -f "$ORDER_ID_FILE" ]; then
    echo "ERROR: Cannot renew: missing $ORDER_ID_FILE"
    exit 1
  fi
  ORDER_ID=$(cat "$ORDER_ID_FILE")

  if [ "$MODE" = "renew-if-needed" ]; then
    RENEW_BEFORE_DAYS=${RENEW_BEFORE_DAYS:-30}
    if ! will_expire_within_days "$RENEW_BEFORE_DAYS"; then
      echo "🔎 Certificate does not require renewal (>${RENEW_BEFORE_DAYS} days left)"
      exit 0
    fi
  fi

  echo "Initiating renewal for order $ORDER_ID"
  HTTP_STATUS=$(curl -sS -o /dev/null -w "%{http_code}" -X POST "${PROXY_SSL_BASE_URL}/v1/certs/orders/${ORDER_ID}/renew" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" || true)

  if [ "$HTTP_STATUS" = "404" ]; then
    echo "WARNING: Order $ORDER_ID not found (404). Clearing stale order ID and falling back to new issuance."
    rm "$ORDER_ID_FILE"
    # Fall through to the default "issue" logic below
  elif [ "$HTTP_STATUS" != "200" ]; then
    echo "ERROR: Failed to initiate renewal. HTTP status: $HTTP_STATUS"
    exit 1
  else
    echo "Renewal initiated. Waiting for renewed certificate bundle..."
    # Poll for new bundle (up to 5 minutes)
    for i in $(seq 1 60); do
      BUNDLE=$(curl -sS -X GET "${PROXY_SSL_BASE_URL}/v1/certs/orders/${ORDER_ID}/bundle" \
        -H "Authorization: Bearer $TOKEN" || true)
      if [ -n "$BUNDLE" ]; then
        # Basic sanity: ensure it looks like PEM
        if echo "$BUNDLE" | grep -q "BEGIN CERTIFICATE"; then
          atomic_write_file "$BUNDLE" "$CERT_FILE" 644
          echo "Installed renewed certificate for order $ORDER_ID"
          # exit code 10 indicates renewed
          exit 10
        fi
      fi
      sleep 5
    done
    
    echo "ERROR: Timed out waiting for renewed certificate bundle"
    exit 1
  fi
fi

# Only proceed to issue logic if we are not in pure renewal mode, or if we fell through due to 404
if [ "$MODE" = "renew" ] && [ -f "$ORDER_ID_FILE" ]; then 
   # If we are here, it means we attempted renewal and it wasn't a 404 (which deletes the file), 
   # but we didn't exit 10 (success) or exit 1 (failure) above? 
   # Actually the logic above exits on success or failure unless 404.
   # If 404, file is deleted, so condition -f "$ORDER_ID_FILE" fails, so we continue to issue logic.
   exit 1
fi

# Default mode: issue (initial one-shot)
echo "Getting SSL certificate (issue) for proxy..."

if [ -z "${CERT_ISSUER_DOMAIN:-}" ]; then
  echo "ERROR: CERT_ISSUER_DOMAIN is not set"
  exit 1
fi

# Request certificate bundle (retry few times)
RESPONSE=""
for i in 1 2 3 4 5; do
  RESPONSE=$(curl -sS -X POST "${PROXY_SSL_BASE_URL}/v1/certs/auto" \
    -H "Authorization: Bearer $TOKEN" \
    -H "Content-Type: application/json" \
    -d "{\"node_id\":\"${NODE_ID}\",\"fqdns\":[\"${CERT_ISSUER_DOMAIN}\"]}" || true)
  CERT=$(echo "$RESPONSE" | jq -r '.certificate // empty' 2>/dev/null || true)
  KEY=$(echo "$RESPONSE" | jq -r '.private_key // empty' 2>/dev/null || true)
  ORDER_ID=$(echo "$RESPONSE" | jq -r '.order_id // empty' 2>/dev/null || true)
  if [ -n "${CERT:-}" ] && [ -n "${KEY:-}" ] && [ "${CERT}" != "null" ] && [ "${KEY}" != "null" ]; then
    break
  fi
  sleep 2
done

CERT=$(echo "$RESPONSE" | jq -r '.certificate // empty')
KEY=$(echo "$RESPONSE" | jq -r '.private_key // empty')
ORDER_ID=$(echo "$RESPONSE" | jq -r '.order_id // empty')

if [ -z "$CERT" ] || [ -z "$KEY" ] || [ "$CERT" = "null" ] || [ "$KEY" = "null" ]; then
  echo "ERROR: Failed to obtain certificate bundle from ${FINAL_PROXY_SSL_SERVICE}:${PROXY_SSL_PORT}"
  echo "$RESPONSE"
  exit 1
fi

install_certificate_pair "$CERT" "$KEY"
if [ -n "$ORDER_ID" ] && [ "$ORDER_ID" != "null" ]; then
  atomic_write_file "$ORDER_ID" "$ORDER_ID_FILE" 600
fi

echo "SSL certificate obtained and installed for ${CERT_ISSUER_DOMAIN}"

# Do not reload nginx here; entrypoint manages configuration and startup
