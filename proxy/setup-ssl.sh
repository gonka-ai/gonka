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
  local target_dir target_name temporary

  target_dir=$(dirname -- "$target")
  target_name=${target##*/}
  if ! temporary=$(mktemp "$target_dir/.${target_name}.tmp.XXXXXX"); then
    return 1
  fi
  if printf '%s\n' "$content" > "$temporary" \
      && chmod "$mode" "$temporary" \
      && mv -f "$temporary" "$target"; then
    return 0
  fi

  rm -f "$temporary"
  return 1
}

remove_staged_files() {
  local path

  for path in "$@"; do
    if [ -n "$path" ]; then
      rm -f -- "$path" || true
    fi
  done
}

restore_certificate_bundle() {
  local had_pair="$1"
  local had_order_id="$2"
  local cert_backup="$3"
  local key_backup="$4"
  local order_id_backup="$5"

  if ! rm -f -- "$CERT_FILE" "$KEY_FILE" "$ORDER_ID_FILE"; then
    return 1
  fi
  if [ "$had_pair" != true ]; then
    return 0
  fi

  # Restore the certificate last so a visible cert.pem still means that all
  # other files belonging to the previous bundle are complete.
  if ! mv -f "$key_backup" "$KEY_FILE"; then
    return 1
  fi
  if [ "$had_order_id" = true ] \
      && ! mv -f "$order_id_backup" "$ORDER_ID_FILE"; then
    rm -f -- "$KEY_FILE"
    return 1
  fi
  if ! mv -f "$cert_backup" "$CERT_FILE"; then
    rm -f -- "$KEY_FILE" "$ORDER_ID_FILE"
    return 1
  fi
}

install_certificate_bundle() {
  local certificate="$1"
  local private_key="$2"
  local order_id="$3"
  local cert_temporary="" key_temporary="" order_id_temporary=""
  local cert_backup="" key_backup="" order_id_backup=""
  local had_pair=false had_order_id=false

  if ! cert_temporary=$(mktemp "$SSL_DIR/.cert.pem.tmp.XXXXXX"); then
    return 1
  fi
  if ! key_temporary=$(mktemp "$SSL_DIR/.private.key.tmp.XXXXXX"); then
    remove_staged_files "$cert_temporary"
    return 1
  fi
  if [ -n "$order_id" ] \
      && ! order_id_temporary=$(mktemp "$SSL_DIR/.order.id.tmp.XXXXXX"); then
    remove_staged_files "$cert_temporary" "$key_temporary"
    return 1
  fi

  if ! printf '%s\n' "$certificate" > "$cert_temporary" \
      || ! chmod 644 "$cert_temporary" \
      || ! printf '%s\n' "$private_key" > "$key_temporary" \
      || ! chmod 600 "$key_temporary"; then
    remove_staged_files "$cert_temporary" "$key_temporary" "$order_id_temporary"
    return 1
  fi
  if [ -n "$order_id" ] \
      && { ! printf '%s\n' "$order_id" > "$order_id_temporary" \
        || ! chmod 600 "$order_id_temporary"; }; then
    remove_staged_files "$cert_temporary" "$key_temporary" "$order_id_temporary"
    return 1
  fi

  # Preserve a complete previous bundle so handled publication failures can
  # return the running proxy to its exact pre-update state.
  if [ -f "$CERT_FILE" ] && [ -f "$KEY_FILE" ]; then
    had_pair=true
    if ! cert_backup=$(mktemp "$SSL_DIR/.cert.pem.backup.XXXXXX") \
        || ! key_backup=$(mktemp "$SSL_DIR/.private.key.backup.XXXXXX") \
        || ! cp -p -- "$CERT_FILE" "$cert_backup" \
        || ! cp -p -- "$KEY_FILE" "$key_backup"; then
      remove_staged_files "$cert_temporary" "$key_temporary" \
        "$order_id_temporary" "$cert_backup" "$key_backup"
      return 1
    fi
    if [ -f "$ORDER_ID_FILE" ]; then
      had_order_id=true
      if ! order_id_backup=$(mktemp "$SSL_DIR/.order.id.backup.XXXXXX") \
          || ! cp -p -- "$ORDER_ID_FILE" "$order_id_backup"; then
        remove_staged_files "$cert_temporary" "$key_temporary" \
          "$order_id_temporary" "$cert_backup" "$key_backup" \
          "$order_id_backup"
        return 1
      fi
    fi
  fi

  # Remove cert.pem before changing the key or order ID. This creates an
  # intentional absent-marker window, but prevents a crash from leaving an old
  # certificate next to a newly published key as an apparently complete pair.
  if ! rm -f -- "$CERT_FILE" "$KEY_FILE" "$ORDER_ID_FILE"; then
    if restore_certificate_bundle "$had_pair" "$had_order_id" \
        "$cert_backup" "$key_backup" "$order_id_backup"; then
      remove_staged_files "$cert_temporary" "$key_temporary" \
        "$order_id_temporary" "$cert_backup" "$key_backup" "$order_id_backup"
    else
      echo "ERROR: Failed to restore the previous certificate bundle; backup files remain in $SSL_DIR"
      remove_staged_files "$cert_temporary" "$key_temporary" "$order_id_temporary"
    fi
    return 1
  fi

  # Metadata and key are committed before cert.pem. The certificate is the
  # bundle marker: once it is visible, every required companion file is ready.
  if { [ -n "$order_id_temporary" ] \
        && ! mv -f "$order_id_temporary" "$ORDER_ID_FILE"; } \
      || ! mv -f "$key_temporary" "$KEY_FILE" \
      || ! mv -f "$cert_temporary" "$CERT_FILE"; then
    if restore_certificate_bundle "$had_pair" "$had_order_id" \
        "$cert_backup" "$key_backup" "$order_id_backup"; then
      remove_staged_files "$cert_temporary" "$key_temporary" \
        "$order_id_temporary" "$cert_backup" "$key_backup" "$order_id_backup"
    else
      echo "ERROR: Failed to restore the previous certificate bundle; backup files remain in $SSL_DIR"
      remove_staged_files "$cert_temporary" "$key_temporary" "$order_id_temporary"
    fi
    return 1
  fi

  remove_staged_files "$cert_backup" "$key_backup" "$order_id_backup"
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

FALLBACK_TO_ISSUE=false
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
    echo "WARNING: Order $ORDER_ID not found (404). Falling back to new issuance."
    FALLBACK_TO_ISSUE=true
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

# A pure renewal reaches issuance only after the server rejected its old order.
if [ "$MODE" = "renew" ] && [ "$FALLBACK_TO_ISSUE" != true ]; then
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

if [ "$ORDER_ID" = "null" ]; then
  ORDER_ID=""
fi
install_certificate_bundle "$CERT" "$KEY" "$ORDER_ID"

echo "SSL certificate obtained and installed for ${CERT_ISSUER_DOMAIN}"

# Do not reload nginx here; entrypoint manages configuration and startup
