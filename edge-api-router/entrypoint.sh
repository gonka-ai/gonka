#!/bin/sh
# Renders /etc/nginx/conf.d/default.conf from nginx.conf.template.
#
# Env:
#   EDGE_API_HOSTS  (required) space-separated round-robin pool hosts
#   EDGE_API_PORT   upstream port (default 18080)
#
# EDGE_API_ROUTER_TEMPLATE / EDGE_API_ROUTER_OUT relocate input/output (citest
# renders the config outside a container; see harness/router_config.go).
set -e

PORT="${EDGE_API_PORT:-18080}"
TEMPLATE="${EDGE_API_ROUTER_TEMPLATE:-/etc/nginx/template/nginx.conf.template}"
OUT_CONF="${EDGE_API_ROUTER_OUT:-/etc/nginx/conf.d/default.conf}"

if [ -z "${EDGE_API_HOSTS}" ]; then
    echo "[edge-api-router] FATAL: EDGE_API_HOSTS is required (space-separated host list)" >&2
    exit 1
fi

LINES=""
for host in ${EDGE_API_HOSTS}; do
    LINES="${LINES}    server ${host}:${PORT} resolve;
"
done

# Line-oriented substitution keeps the multiline server list portable across
# busybox awk (container) and BSD awk (citest renders this on the host).
TMP=$(mktemp)
while IFS= read -r line || [ -n "${line}" ]; do
    case "${line}" in
        '${UPSTREAM_SERVERS}')
            printf '%s' "${LINES}"
            ;;
        *)
            printf '%s\n' "${line}"
            ;;
    esac
done < "${TEMPLATE}" > "${TMP}"
mv "${TMP}" "${OUT_CONF}"

echo "[edge-api-router] rendered config (EDGE_API_HOSTS='${EDGE_API_HOSTS}'):"
echo '----------------------------------------'
cat "${OUT_CONF}"
echo '----------------------------------------'
