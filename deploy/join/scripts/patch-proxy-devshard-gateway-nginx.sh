#!/usr/bin/env bash
# Patch a running proxy container so /devshard-gateway/* forwards to devshard-gateway:8080
# (including POST /v1/admin/*). Safe to re-run; skips if already patched.
#
# Usage (on genesis host):
#   cd /srv/dai/gonka/deploy/join
#   ./scripts/patch-proxy-devshard-gateway-nginx.sh
#   ./scripts/patch-proxy-devshard-gateway-nginx.sh --verify
#   ./scripts/patch-proxy-devshard-gateway-nginx.sh --restore   # undo failed patch
set -euo pipefail

PROXY_CONTAINER="${PROXY_CONTAINER:-proxy}"
GATEWAY_HOST="${DEVSHARD_GATEWAY_SERVICE:-devshard-gateway}"
GATEWAY_PORT="${DEVSHARD_GATEWAY_PORT:-8080}"
VERIFY_ONLY=0
RESTORE=0
for arg in "$@"; do
  case "$arg" in
    --verify) VERIFY_ONLY=1 ;;
    --restore) RESTORE=1 ;;
  esac
done

if ! docker inspect "$PROXY_CONTAINER" >/dev/null 2>&1; then
  echo "ERROR: container $PROXY_CONTAINER not found" >&2
  exit 1
fi

latest_backup() {
  docker exec "$PROXY_CONTAINER" sh -c 'ls -1t /etc/nginx/nginx.conf.bak.* 2>/dev/null | head -1' || true
}

if [[ "$RESTORE" == "1" ]]; then
  bak="$(latest_backup)"
  if [[ -z "$bak" ]]; then
    echo "ERROR: no nginx.conf.bak.* found in $PROXY_CONTAINER" >&2
    exit 1
  fi
  docker exec "$PROXY_CONTAINER" cp "$bak" /etc/nginx/nginx.conf
  docker exec "$PROXY_CONTAINER" nginx -t
  docker exec "$PROXY_CONTAINER" nginx -s reload
  echo "restored from $bak and reloaded nginx"
  exit 0
fi

nginx_ok() {
  docker exec "$PROXY_CONTAINER" nginx -t >/dev/null 2>&1
}

if docker exec "$PROXY_CONTAINER" grep -q 'devshard_gateway_backend' /etc/nginx/nginx.conf 2>/dev/null; then
  if nginx_ok; then
    echo "nginx already patched (devshard_gateway_backend present, config valid)"
    if [[ "$VERIFY_ONLY" == "1" ]]; then
      docker exec "$PROXY_CONTAINER" nginx -t
      exit 0
    fi
    docker exec "$PROXY_CONTAINER" nginx -s reload
    echo "reloaded nginx"
    exit 0
  fi
  echo "WARN: devshard_gateway_backend present but nginx -t failed; attempting repair"
fi

if [[ "$VERIFY_ONLY" == "1" ]]; then
  if nginx_ok && docker exec "$PROXY_CONTAINER" grep -q 'devshard_gateway_backend' /etc/nginx/nginx.conf 2>/dev/null; then
    docker exec "$PROXY_CONTAINER" nginx -t
    exit 0
  fi
  echo "ERROR: nginx not patched yet (or config invalid)" >&2
  exit 1
fi

TS="$(date +%s)"
docker exec "$PROXY_CONTAINER" cp /etc/nginx/nginx.conf "/etc/nginx/nginx.conf.bak.${TS}"
echo "backup: /etc/nginx/nginx.conf.bak.${TS}"

WORKDIR="$(mktemp -d)"
trap 'rm -rf "$WORKDIR"' EXIT
docker cp "${PROXY_CONTAINER}:/etc/nginx/nginx.conf" "${WORKDIR}/nginx.conf"

UPSTREAM=$(
  cat <<EOF

    upstream devshard_gateway_backend {
        zone devshard_gateway_backend 64k;
        server ${GATEWAY_HOST}:${GATEWAY_PORT} resolve;
    }
EOF
)

GATEWAY_ONLY=$(
  cat <<'EOF'

        location /devshard-gateway/ {
            set $limit_zone_name "EXEMPT";
            proxy_pass http://devshard_gateway_backend/;
            proxy_set_header Host $host;
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_set_header Authorization $http_authorization;
            proxy_connect_timeout 60s;
            proxy_send_timeout 86400s;
            proxy_read_timeout 86400s;
            client_max_body_size 10M;
        }
EOF
)

ASSETS_ONLY=$(
  cat <<'EOF'

        location /devshard-assets/ {
            alias /usr/share/nginx/html/devshard-assets/;
            default_type application/octet-stream;
        }
EOF
)

export UPSTREAM GATEWAY_ONLY ASSETS_ONLY
python3 - "${WORKDIR}/nginx.conf" <<'PY'
import os
import re
import sys
from pathlib import Path

path = Path(sys.argv[1])
text = path.read_text()
upstream = os.environ["UPSTREAM"]
gateway_only = os.environ["GATEWAY_ONLY"]
assets_only = os.environ["ASSETS_ONLY"]

# Remove duplicate /devshard-assets/ blocks (keep the first).
assets_pat = re.compile(
    r"\n        location /devshard-assets/ \{[^}]+\}\n",
    re.MULTILINE | re.DOTALL,
)
matches = list(assets_pat.finditer(text))
if len(matches) > 1:
    for m in reversed(matches[1:]):
        text = text[: m.start()] + text[m.end() :]
    print(f"removed {len(matches) - 1} duplicate /devshard-assets/ block(s)")

if "devshard_gateway_backend" not in text:
    marker = "    server {"
    idx = text.find(marker)
    if idx == -1:
        raise SystemExit("could not find server block in nginx.conf")
    text = text[:idx] + upstream + text[idx:]

insert = ""
if "location /devshard-gateway/" not in text:
    insert += gateway_only
if "location /devshard-assets/" not in text:
    insert += assets_only

if insert:
    for marker in ("        # Blocked Routes", "        location /health {"):
        idx = text.find(marker)
        if idx != -1:
            text = text[:idx] + insert + text[idx:]
            break
    else:
        raise SystemExit("could not find insertion point for gateway location")

path.write_text(text)
print("patched nginx.conf locally")
PY

docker cp "${WORKDIR}/nginx.conf" "${PROXY_CONTAINER}:/etc/nginx/nginx.conf"
if ! docker exec "$PROXY_CONTAINER" nginx -t; then
  echo "ERROR: nginx -t failed after patch; restoring backup" >&2
  docker exec "$PROXY_CONTAINER" cp "/etc/nginx/nginx.conf.bak.${TS}" /etc/nginx/nginx.conf
  exit 1
fi
docker exec "$PROXY_CONTAINER" nginx -s reload
echo "OK: proxy reloaded — /devshard-gateway/ now forwards to ${GATEWAY_HOST}:${GATEWAY_PORT}"
