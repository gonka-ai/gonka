#!/bin/sh
# Renders /etc/haproxy/haproxy.cfg and /etc/haproxy/non_ha.map from the
# environment, then execs HAProxy.
#
# Env:
#   VERSIOND_POOL_HOST        DNS name resolving to every versiond in the HA
#                             pool (compose network alias). Default versiond-pool.
#   VERSIOND_PORT             upstream port (default 8080)
#   VERSIOND_LEGACY_HOST      single host owning pre-HA SQLite data dirs
#   VERSIOND_NON_HA_VERSIONS  version path segments pinned to the legacy host
#                             (whitespace and/or comma separated). Empty = every
#                             version uses the HA pool.
#   GONKA_HA                  set by the HA compose overlay; adds the
#                             Devshard-Ha request header on the HA backend
#   VERSIOND_ROUTER_*         proxy policy, see README
#
# Pool membership is discovered from DNS and health from active /readyz checks,
# so neither adding a host nor draining one needs a config change or a reload.
set -eu

TEMPLATE="${VERSIOND_ROUTER_TEMPLATE:-/etc/haproxy/haproxy.cfg.template}"
OUT="${VERSIOND_ROUTER_OUT:-/etc/haproxy/haproxy.cfg}"
MAP="${VERSIOND_ROUTER_NON_HA_MAP:-/etc/haproxy/non_ha.map}"
# Overridable so `make test-render` can render without a local HAProxy.
HAPROXY_BIN="${HAPROXY_BIN:-haproxy}"

POOL_HOST="${VERSIOND_POOL_HOST:-versiond-pool}"
PORT="${VERSIOND_PORT:-8080}"
LEGACY_HOST="${VERSIOND_LEGACY_HOST:-$POOL_HOST}"
SLOTS="${VERSIOND_ROUTER_POOL_SLOTS:-64}"
MAXCONN="${VERSIOND_ROUTER_MAX_CONNECTIONS:-4096}"
CONNECT_TIMEOUT="${VERSIOND_ROUTER_CONNECT_TIMEOUT_SECONDS:-2}"
STREAM_IDLE="${VERSIOND_ROUTER_STREAM_IDLE_SECONDS:-1200}"
# Deliberately far above the client-facing idle timeout: the outer proxy is the
# only hop that should cut a stream.
TUNNEL_TIMEOUT="${VERSIOND_ROUTER_TUNNEL_TIMEOUT_SECONDS:-86400}"

for value in "$SLOTS" "$MAXCONN" "$CONNECT_TIMEOUT" "$STREAM_IDLE" "$TUNNEL_TIMEOUT" "$PORT"; do
    case "$value" in
        ''|*[!0-9]*)
            echo "versiond-router: invalid numeric setting '$value'" >&2
            exit 1
            ;;
    esac
done
if [ "$TUNNEL_TIMEOUT" -lt "$STREAM_IDLE" ]; then
    echo "versiond-router: tunnel timeout ${TUNNEL_TIMEOUT}s must not be below stream idle ${STREAM_IDLE}s" >&2
    exit 1
fi

# One line per pinned version, rewritten on every boot from the environment.
# Live edits use the HAProxy Runtime API (`add map` / `del map`), which changes
# memory only — write this file too if the change must survive a restart.
: > "$MAP"
# Trailing newline is load-bearing: without it `read` swallows the last field.
printf '%s\n' "${VERSIOND_NON_HA_VERSIONS:-}" | tr ',;' '  ' | tr -s ' ' '\n' | while read -r version; do
    [ -n "$version" ] || continue
    echo "$version legacy" >> "$MAP"
done

if [ -n "${GONKA_HA:-}" ]; then
    # Overwrites any client-supplied value: the guard must reflect the
    # deployment, not the request.
    HA_HEADER='http-request set-header Devshard-Ha true'
else
    HA_HEADER='http-request del-header Devshard-Ha'
fi

sed \
    -e "s|\${NON_HA_MAP}|$MAP|g" \
    -e "s|\${VERSIOND_POOL_HOST}|$POOL_HOST|g" \
    -e "s|\${VERSIOND_PORT}|$PORT|g" \
    -e "s|\${VERSIOND_LEGACY_HOST}|$LEGACY_HOST|g" \
    -e "s|\${POOL_SLOTS}|$SLOTS|g" \
    -e "s|\${MAX_CONNECTIONS}|$MAXCONN|g" \
    -e "s|\${CONNECT_TIMEOUT_SECONDS}|$CONNECT_TIMEOUT|g" \
    -e "s|\${STREAM_IDLE_SECONDS}|$STREAM_IDLE|g" \
    -e "s|\${TUNNEL_TIMEOUT_SECONDS}|$TUNNEL_TIMEOUT|g" \
    -e "s|\${DEVSHARD_HA_HEADER}|$HA_HEADER|g" \
    "$TEMPLATE" > "$OUT"

"$HAPROXY_BIN" -c -f "$OUT" >/dev/null

if [ -n "${VERSIOND_ROUTER_RENDER_ONLY:-}" ]; then
    exit 0
fi

exec "$HAPROXY_BIN" -W -db -f "$OUT"
