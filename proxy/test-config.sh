#!/usr/bin/env bash
# Render nginx.unified.conf.template via entrypoint.sh and run nginx -t.
#
# Exercises the major env-driven template injection branches (except PROXY_REAL_IP_*).
# Requires Docker. Uses the same nginx base image as Dockerfile.
#
# Run from repo root:  make -C proxy test-config
# Or from proxy/:      ./test-config.sh

set -euo pipefail

PROXY_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "$PROXY_DIR"

NGINX_IMAGE="${PROXY_NGINX_CHECK_IMAGE:-nginx:1.28-alpine3.21}"

if ! command -v docker >/dev/null 2>&1; then
	echo "test-config: error: docker is required" >&2
	exit 1
fi

for f in entrypoint.sh nginx.unified.conf.template setup-ssl.sh; do
	if [[ ! -f "$f" ]]; then
		echo "test-config: error: missing $PROXY_DIR/$f" >&2
		exit 1
	fi
done

WORKDIR="$(mktemp -d "${TMPDIR:-/tmp}/test-config.XXXXXX")"
cleanup() { rm -rf "$WORKDIR"; }
trap cleanup EXIT

mkdir -p "$WORKDIR/ssl"
# Self-signed certs so NGINX_MODE=https|both can pass nginx -t without proxy-ssl.
openssl req -x509 -nodes -newkey rsa:2048 \
	-keyout "$WORKDIR/ssl/private.key" \
	-out "$WORKDIR/ssl/cert.pem" \
	-days 1 \
	-subj "/CN=localhost" \
	>/dev/null 2>&1

# Pre-install gettext once into a local image tag so each case is fast.
# Rebuild when the base image pin changes.
CHECK_IMAGE="gonka-proxy-nginx-check:local"
if ! docker image inspect "$CHECK_IMAGE" >/dev/null 2>&1; then
	echo "test-config: building helper image from $NGINX_IMAGE ..."
	docker build -t "$CHECK_IMAGE" - <<EOF
FROM ${NGINX_IMAGE}
RUN apk add --no-cache gettext curl jq bash apache2-utils openssl
EOF
fi

run_case() {
	local name="$1"
	shift
	echo "test-config: [$name] rendering + nginx -t ..."
	docker run --rm \
		-v "$PROXY_DIR/entrypoint.sh:/entrypoint.sh:ro" \
		-v "$PROXY_DIR/nginx.unified.conf.template:/etc/nginx/nginx.unified.conf.template:ro" \
		-v "$PROXY_DIR/setup-ssl.sh:/setup-ssl.sh:ro" \
		-v "$WORKDIR/ssl:/etc/nginx/ssl:ro" \
		-e KEY_NAME= \
		-e API_SERVICE_NAME=api \
		-e NODE_SERVICE_NAME=node \
		-e EXPLORER_SERVICE_NAME=explorer \
		-e VERSIOND_SERVICE_NAME=versiond \
		-e VERSIOND_PORT=8080 \
		-e GONKA_API_PORT=9000 \
		-e JAEGER_ENABLED=false \
		-e GRAFANA_ENABLED=false \
		"$@" \
		"$CHECK_IMAGE" \
		sh -c '
			set -e
			mkdir -p /etc/nginx/conf.d /var/log/nginx
			printf "%s\n" "#!/bin/true" > /usr/local/bin/sidecar
			chmod +x /usr/local/bin/sidecar
			/entrypoint.sh nginx -t
		'
	echo "test-config: [$name] ok"
}

# --- Mutually exclusive / important branches ---------------------------------

# 1) Defaults: versionless obs → dapi, HTTP only, chain disabled, no extras.
run_case "obs→dapi (defaults)" \
	-e NGINX_MODE=http \
	-e DISABLE_DEVSHARD_PROXY=false \
	-e EDGE_API_SERVICE_NAME=

# 2) Edge-api present; optional verify/debug stay blocked.
run_case "obs→edge-api (optional private)" \
	-e NGINX_MODE=http \
	-e DISABLE_DEVSHARD_PROXY=false \
	-e EDGE_API_SERVICE_NAME=edge-api \
	-e EDGE_API_PORT=18080 \
	-e EDGE_API_EXPOSE_OPTIONAL_ROUTES=false

# 3) Optional Tier A verify/debug published (+ router host name).
run_case "obs→edge-api-router (optional exposed)" \
	-e NGINX_MODE=http \
	-e DISABLE_DEVSHARD_PROXY=false \
	-e EDGE_API_SERVICE_NAME=edge-api-router \
	-e EDGE_API_PORT=18080 \
	-e EDGE_API_EXPOSE_OPTIONAL_ROUTES=true

# 4) Devshard proxy off (no versiond upstream / locations).
run_case "devshard-disabled" \
	-e NGINX_MODE=http \
	-e DISABLE_DEVSHARD_PROXY=true \
	-e EDGE_API_SERVICE_NAME=

# 5) HTTPS-only (LISTEN_HTTP disabled branch) with dummy certs.
run_case "https-only" \
	-e NGINX_MODE=https \
	-e SERVER_NAME=localhost \
	-e DISABLE_DEVSHARD_PROXY=false \
	-e EDGE_API_SERVICE_NAME=

# 6) Conn limits off (empty LIMIT_CONN_* rules).
run_case "conn-limits-off" \
	-e NGINX_MODE=http \
	-e ENABLE_CONN_LIMITS=false \
	-e DISABLE_DEVSHARD_PROXY=false \
	-e EDGE_API_SERVICE_NAME=

# 7) App API disabled status injection.
run_case "gonka-api-disabled" \
	-e NGINX_MODE=http \
	-e DISABLE_GONKA_API=true \
	-e DISABLE_DEVSHARD_PROXY=false \
	-e EDGE_API_SERVICE_NAME=

# 8) Kitchen sink: nearly every fragment enabled except PROXY_REAL_IP_*.
run_case "kitchen-sink (all features except real-ip)" \
	-e NGINX_MODE=both \
	-e SERVER_NAME=localhost \
	-e KEY_NAME=genesis \
	-e DISABLE_DEVSHARD_PROXY=false \
	-e EDGE_API_SERVICE_NAME=edge-api \
	-e EDGE_API_PORT=18080 \
	-e EDGE_API_EXPOSE_OPTIONAL_ROUTES=true \
	-e DISABLE_CHAIN_RPC=false \
	-e DISABLE_CHAIN_API=false \
	-e DISABLE_CHAIN_GRPC=false \
	-e DISABLE_GONKA_API=false \
	-e ENABLE_CONN_LIMITS=true \
	-e DASHBOARD_PORT=5173 \
	-e JAEGER_ENABLED=true \
	-e JAEGER_BASIC_AUTH_USER=jaeger \
	-e JAEGER_BASIC_AUTH_PASSWORD=ci-not-a-placeholder \
	-e JAEGER_SERVICE_NAME=jaeger \
	-e JAEGER_PORT=16686 \
	-e JAEGER_BASE_PATH=/jaeger \
	-e GRAFANA_ENABLED=true \
	-e GRAFANA_ADMIN_PASSWORD=ci-not-a-placeholder \
	-e GRAFANA_SERVICE_NAME=grafana \
	-e GRAFANA_PORT=3000 \
	-e GRAFANA_BASE_PATH=/grafana \
	-e RESOLVER=127.0.0.11

# 9) Dynamic blocked/exempt locations for HTTP, WebSocket RPC, and gRPC.
run_case "chain blocked/exempt routes" \
	-e NGINX_MODE=http \
	-e DISABLE_CHAIN_API=false \
	-e DISABLE_CHAIN_RPC=false \
	-e DISABLE_CHAIN_GRPC=false \
	-e CHAIN_API_BLOCKED_ROUTES=blocked-api \
	-e CHAIN_API_EXEMPT_ROUTES=exempt-api \
	-e CHAIN_RPC_BLOCKED_ROUTES=blocked-rpc \
	-e CHAIN_RPC_EXEMPT_ROUTES=exempt-rpc \
	-e CHAIN_GRPC_BLOCKED_ROUTES=blocked-grpc \
	-e CHAIN_GRPC_EXEMPT_ROUTES=exempt-grpc

echo "test-config: ok"
