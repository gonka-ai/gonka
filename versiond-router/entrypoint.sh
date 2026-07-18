#!/bin/sh
# Renders /etc/nginx/conf.d/default.conf from nginx.conf.template.
#
# Env:
#   VERSIOND_HOSTS              (required) space-separated HA pool hosts
#   VERSIOND_PORT               upstream port (default 8080)
#   VERSIOND_LEGACY_HOST        single host that owns pre-HA SQLite data dirs
#                               (default: first host in VERSIOND_HOSTS)
#   VERSIOND_NON_HA_VERSIONS    version path segments that must pin to
#                               VERSIOND_LEGACY_HOST (whitespace and/or
#                               comma-separated, e.g. "v1 v2 v3" or "v1,v2,v3").
#                               Empty = every version uses the HA pool.
#                               Future versions are HA by default.
#
# When len(VERSIOND_HOSTS) > 1 and the request uses versiond_ha_pool, nginx
# sets request header Devshard-Ha: true so devshardd can require
# DEVSHARD_STORAGE_MODE=postgres + PGHOST.
#
# HA servers use max_fails=1; location sets proxy_next_upstream for first-502
# failover (see nginx.conf.template).
#
# Mounted into nginx:alpine as /docker-entrypoint.d/40-render-versiond-upstream.sh
# so the stock entrypoint runs it before exec'ing nginx. The template lives at
# /etc/nginx/template/ (NOT /etc/nginx/templates/) to bypass the stock
# 20-envsubst-on-templates.sh which would clobber placeholders with empty
# values (those env vars are process-local to this script).
set -e

PORT="${VERSIOND_PORT:-8080}"
TEMPLATE="${VERSIOND_ROUTER_TEMPLATE:-/etc/nginx/template/nginx.conf.template}"
OUT_CONF="${VERSIOND_ROUTER_OUT:-/etc/nginx/conf.d/default.conf}"

if [ -z "${VERSIOND_HOSTS}" ]; then
    echo "[versiond-router] FATAL: VERSIOND_HOSTS is required (space-separated host list)" >&2
    exit 1
fi

# First host is the legacy/SQLite anchor unless overridden.
LEGACY_HOST="${VERSIOND_LEGACY_HOST:-}"
if [ -z "${LEGACY_HOST}" ]; then
    # shellcheck disable=SC2086
    LEGACY_HOST=$(set -- ${VERSIOND_HOSTS}; echo "$1")
fi

# max_fails=1: first unsuccessful attempt (see proxy_next_upstream) marks the
# peer down for fail_timeout so sticky hash skips it without waiting on DNS.
HA_LINES=""
HOST_COUNT=0
for host in ${VERSIOND_HOSTS}; do
    HA_LINES="${HA_LINES}    server ${host}:${PORT} resolve max_fails=1 fail_timeout=10s;
"
    HOST_COUNT=$((HOST_COUNT + 1))
done

LEGACY_LINES="    server ${LEGACY_HOST}:${PORT} resolve;
"

# Normalize commas/semicolons to spaces, then split on IFS whitespace.
NON_HA_RAW="${VERSIOND_NON_HA_VERSIONS-}"
NON_HA_NORM=$(printf '%s' "${NON_HA_RAW}" | tr ',;' ' ')
# shellcheck disable=SC2086
set -- ${NON_HA_NORM}

MAP_BODY="    default versiond_ha_pool;
"
ROUTE_MODE="all-ha"
NON_HA_LIST=""
if [ "$#" -gt 0 ]; then
    ROUTE_MODE="mixed non-ha=["
    first=1
    for ver in "$@"; do
        [ -z "${ver}" ] && continue
        MAP_BODY="${MAP_BODY}    ${ver} versiond_legacy;
"
        if [ "${first}" -eq 1 ]; then
            NON_HA_LIST="${ver}"
            first=0
        else
            NON_HA_LIST="${NON_HA_LIST} ${ver}"
        fi
    done
    ROUTE_MODE="${ROUTE_MODE}${NON_HA_LIST}]"
fi

# Inject Devshard-Ha only for multi-host HA pool traffic.
if [ "${HOST_COUNT}" -gt 1 ]; then
    HA_HEADER_MAP="    default \"\";
    versiond_ha_pool \"true\";
"
else
    HA_HEADER_MAP="    default \"\";
"
fi

# Line-oriented substitution keeps multiline server/map bodies portable across
# macOS awk and busybox awk (neither accepts raw newlines in -v strings).
TMP=$(mktemp)
while IFS= read -r line || [ -n "${line}" ]; do
    case "${line}" in
        '${HA_UPSTREAM_SERVERS}')
            printf '%s' "${HA_LINES}"
            ;;
        '${LEGACY_UPSTREAM_SERVERS}')
            printf '%s' "${LEGACY_LINES}"
            ;;
        '${VERSIOND_BACKEND_MAP}')
            printf '%s' "${MAP_BODY}"
            ;;
        '${DEVSHARD_HA_HEADER_MAP}')
            printf '%s' "${HA_HEADER_MAP}"
            ;;
        *)
            printf '%s\n' "${line}"
            ;;
    esac
done < "${TEMPLATE}" > "${TMP}"
mv "${TMP}" "${OUT_CONF}"

echo "[versiond-router] rendered config:"
echo "  VERSIOND_HOSTS='${VERSIOND_HOSTS}' (count=${HOST_COUNT})"
echo "  VERSIOND_LEGACY_HOST='${LEGACY_HOST}'"
echo "  VERSIOND_NON_HA_VERSIONS='${NON_HA_RAW}' (mode=${ROUTE_MODE})"
echo "  Devshard-Ha header on multi-host HA pool: $([ "${HOST_COUNT}" -gt 1 ] && echo yes || echo no)"
echo '----------------------------------------'
cat "${OUT_CONF}"
echo '----------------------------------------'
