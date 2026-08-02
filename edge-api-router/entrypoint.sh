#!/bin/sh
# Renders /etc/haproxy/haproxy.cfg from the environment, then execs HAProxy.
#
# Env:
#   EDGE_API_POOL_HOST  DNS name resolving to every edge-api replica (compose
#                       network alias). Default edge-api-pool.
#   EDGE_API_PORT       upstream port (default 18080)
#   EDGE_API_ROUTER_PORT listen port (default 18080)
set -eu

TEMPLATE="${EDGE_API_ROUTER_TEMPLATE:-/etc/haproxy/haproxy.cfg.template}"
OUT="${EDGE_API_ROUTER_OUT:-/etc/haproxy/haproxy.cfg}"
# Overridable so `make test-render` can render without a local HAProxy.
HAPROXY_BIN="${HAPROXY_BIN:-haproxy}"

POOL_HOST="${EDGE_API_POOL_HOST:-edge-api-pool}"
PORT="${EDGE_API_PORT:-18080}"
LISTEN_PORT="${EDGE_API_ROUTER_PORT:-18080}"
SLOTS="${EDGE_API_ROUTER_POOL_SLOTS:-64}"
MAXCONN="${EDGE_API_ROUTER_MAX_CONNECTIONS:-4096}"
CONNECT_TIMEOUT="${EDGE_API_ROUTER_CONNECT_TIMEOUT_SECONDS:-30}"
READ_TIMEOUT="${EDGE_API_ROUTER_READ_TIMEOUT_SECONDS:-120}"

for value in "$PORT" "$LISTEN_PORT" "$SLOTS" "$MAXCONN" "$CONNECT_TIMEOUT" "$READ_TIMEOUT"; do
    case "$value" in
        ''|*[!0-9]*)
            echo "edge-api-router: invalid numeric setting '$value'" >&2
            exit 1
            ;;
    esac
done

sed \
    -e "s|\${EDGE_API_POOL_HOST}|$POOL_HOST|g" \
    -e "s|\${EDGE_API_PORT}|$PORT|g" \
    -e "s|\${EDGE_API_ROUTER_PORT}|$LISTEN_PORT|g" \
    -e "s|\${POOL_SLOTS}|$SLOTS|g" \
    -e "s|\${MAX_CONNECTIONS}|$MAXCONN|g" \
    -e "s|\${CONNECT_TIMEOUT_SECONDS}|$CONNECT_TIMEOUT|g" \
    -e "s|\${READ_TIMEOUT_SECONDS}|$READ_TIMEOUT|g" \
    "$TEMPLATE" > "$OUT"

"$HAPROXY_BIN" -c -f "$OUT" >/dev/null

if [ -n "${EDGE_API_ROUTER_RENDER_ONLY:-}" ]; then
    exit 0
fi

exec "$HAPROXY_BIN" -W -db -f "$OUT"
