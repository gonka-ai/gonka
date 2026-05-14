#!/bin/sh
# Renders /etc/nginx/conf.d/default.conf from nginx.conf.template by
# substituting ${UPSTREAM_SERVERS} with `server <host>:<port>;` lines built
# from the VERSIOND_HOSTS env (space-separated host list).
#
# Mounted into nginx:alpine as /docker-entrypoint.d/40-render-versiond-upstream.sh
# so the stock entrypoint runs it before exec'ing nginx. The template lives at
# /etc/nginx/template/ (NOT /etc/nginx/templates/) to bypass the stock
# 20-envsubst-on-templates.sh which would clobber ${UPSTREAM_SERVERS} with an
# empty value (env var doesn't exist at process scope).
set -e

PORT="${VERSIOND_PORT:-8080}"

if [ -z "${VERSIOND_HOSTS}" ]; then
    echo "[versiond-router] FATAL: VERSIOND_HOSTS is required (space-separated host list)" >&2
    exit 1
fi

LINES=""
for host in ${VERSIOND_HOSTS}; do
    LINES="${LINES}    server ${host}:${PORT} resolve;
"
done

awk -v s="${LINES}" '{gsub(/\$\{UPSTREAM_SERVERS\}/, s); print}' \
    /etc/nginx/template/nginx.conf.template \
    > /etc/nginx/conf.d/default.conf

echo "[versiond-router] rendered config (VERSIOND_HOSTS='${VERSIOND_HOSTS}'):"
echo '----------------------------------------'
cat /etc/nginx/conf.d/default.conf
echo '----------------------------------------'
