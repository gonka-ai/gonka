#!/bin/sh
set -e

PORT="${EDGE_API_PORT:-18080}"

if [ -z "${EDGE_API_HOSTS}" ]; then
    echo "[edge-api-router] FATAL: EDGE_API_HOSTS is required (space-separated host list)" >&2
    exit 1
fi

LINES=""
for host in ${EDGE_API_HOSTS}; do
    LINES="${LINES}    server ${host}:${PORT} resolve;
"
done

awk -v s="${LINES}" '{gsub(/\$\{UPSTREAM_SERVERS\}/, s); print}' \
    /etc/nginx/template/nginx.conf.template \
    > /etc/nginx/conf.d/default.conf

echo "[edge-api-router] rendered config (EDGE_API_HOSTS='${EDGE_API_HOSTS}'):"
echo '----------------------------------------'
cat /etc/nginx/conf.d/default.conf
echo '----------------------------------------'
