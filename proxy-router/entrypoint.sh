#!/bin/sh

set -eu

TEMPLATE="${PROXY_ROUTER_TEMPLATE:-/etc/haproxy/haproxy.cfg.template}"
BACKEND_TEMPLATE="${PROXY_ROUTER_BACKEND_TEMPLATE:-/etc/haproxy/versiond-backend.cfg.template}"
OUT="${PROXY_ROUTER_OUT:-/etc/haproxy/haproxy.cfg}"
VERSION_MAP="${PROXY_ROUTER_VERSION_MAP:-/etc/haproxy/version-router.map}"
READY_VERSION_MAP="${PROXY_ROUTER_READY_VERSION_MAP:-${OUT}.ready-versions.map}"
SLOT_MAP="${PROXY_ROUTER_SLOT_MAP:-${OUT}.version-slots.map}"
HAPROXY_BIN="${HAPROXY_BIN:-haproxy}"

POLICY_POOL_HOST="${PROXY_POLICY_POOL_HOST:-proxy-policy}"
POLICY_POOL_SLOTS="${PROXY_POLICY_POOL_SLOTS:-4}"
ROUTER_POOL_HOST="${VERSIOND_ROUTER_POOL_HOST:-versiond-router-fleet}"
ROUTER_POOL_SLOTS="${VERSIOND_ROUTER_FLEET_CAPACITY:-16}"
ROUTER_PORT="${VERSIOND_ROUTER_PORT:-8080}"
ROUTER_ADMIN_PORT="${VERSIOND_ROUTER_ADMIN_PORT:-8404}"
EDGE_POOL_HOST="${EDGE_API_POOL_HOST:-edge-api-pool}"
EDGE_POOL_SLOTS="${EDGE_API_POOL_SLOTS:-64}"
EDGE_API_PORT="${EDGE_API_PORT:-18080}"
VERSIOND_FRONTEND_PORT="${PROXY_VERSIOND_PORT:-18081}"
EDGE_FRONTEND_PORT="${PROXY_EDGE_API_PORT:-18082}"
ADMIN_PORT="${PROXY_ROUTER_ADMIN_PORT:-8404}"
MAX_CONNECTIONS="${PROXY_ROUTER_MAX_CONNECTIONS:-8192}"
CONNECT_TIMEOUT="${PROXY_ROUTER_CONNECT_TIMEOUT_SECONDS:-2}"
STREAM_IDLE="${PROXY_ROUTER_STREAM_IDLE_SECONDS:-1200}"
PUBLIC_IDLE="${PROXY_ROUTER_PUBLIC_IDLE_SECONDS:-86400}"
VERSION_CAPACITY="${PROXY_ROUTER_VERSION_CAPACITY:-32}"
CATALOG_URL="${VERSIOND_ROUTING_CATALOG_URL:-}"
CATALOG_POLL="${VERSIOND_ROUTING_CATALOG_POLL_SECONDS:-5}"
CATALOG_FETCH_TIMEOUT="${VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS:-3}"
CATALOG_CACHE_FILE="${VERSIOND_ROUTING_CATALOG_CACHE_FILE:-/var/lib/gonka-router/catalog.json}"
CATALOG_CACHE_MAX_AGE="${VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS:-86400}"
CATALOG_STATUS_FILE="${VERSIOND_ROUTING_CATALOG_STATUS_FILE:-/var/lib/gonka-router/catalog-status.json}"
CATALOG_CACHE_BIN="${ROUTING_CATALOG_CACHE_BIN:-/usr/local/lib/router-runtime/catalog-cache}"
NGINX_MODE="${NGINX_MODE:-http}"
POLICY_BIND_HOST="${PROXY_ROUTER_POLICY_BIND_HOST:-}"

resolve_ipv4() {
    getent ahostsv4 "$1" | awk 'NR == 1 { print $1 }'
}

if [ -n "$POLICY_BIND_HOST" ]; then
    POLICY_BIND_ADDRESS=$(resolve_ipv4 "$POLICY_BIND_HOST")
    case "$POLICY_BIND_ADDRESS" in
        '' | *[!0-9.]*)
            echo "proxy-router: cannot resolve policy bind host '$POLICY_BIND_HOST' to IPv4" >&2
            exit 1
            ;;
    esac
else
    POLICY_BIND_ADDRESS=0.0.0.0
fi

bool_env() {
    raw=$(eval "printf '%s' \"\${$1:-}\"")
    case "$(printf '%s' "$raw" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')" in
        1 | true | yes) printf '1' ;;
        '' | 0 | false | no) ;;
        *)
            echo "proxy-router: $1='$raw' is not a boolean; use 1/true/yes or 0/false/no" >&2
            exit 1
            ;;
    esac
}
RENDER_ONLY=$(bool_env PROXY_ROUTER_RENDER_ONLY)

for host in "$POLICY_POOL_HOST" "$ROUTER_POOL_HOST" "$EDGE_POOL_HOST"; do
    case "$host" in
        '' | *[!A-Za-z0-9._-]*)
            echo "proxy-router: invalid hostname '$host'" >&2
            exit 1
            ;;
    esac
done
for value in "$POLICY_POOL_SLOTS" "$ROUTER_POOL_SLOTS" "$ROUTER_PORT" \
    "$ROUTER_ADMIN_PORT" "$EDGE_POOL_SLOTS" "$EDGE_API_PORT" \
    "$VERSIOND_FRONTEND_PORT" "$EDGE_FRONTEND_PORT" "$ADMIN_PORT" \
    "$MAX_CONNECTIONS" "$CONNECT_TIMEOUT" "$STREAM_IDLE" "$PUBLIC_IDLE" \
    "$VERSION_CAPACITY" "$CATALOG_POLL" "$CATALOG_FETCH_TIMEOUT" \
    "$CATALOG_CACHE_MAX_AGE"; do
    case "$value" in
        '' | *[!0-9]*)
            echo "proxy-router: invalid numeric setting '$value'" >&2
            exit 1
            ;;
    esac
done
if [ "$VERSION_CAPACITY" -eq 0 ] || [ "$CATALOG_POLL" -eq 0 ] || \
    [ "$CATALOG_FETCH_TIMEOUT" -eq 0 ] || [ "$CATALOG_CACHE_MAX_AGE" -eq 0 ]; then
    echo "proxy-router: catalog capacity and timing values must be positive" >&2
    exit 1
fi
case "$CATALOG_URL" in
    '' | http://* | https://*) ;;
    *)
        echo "proxy-router: VERSIOND_ROUTING_CATALOG_URL must use http or https" >&2
        exit 1
        ;;
esac

case "$NGINX_MODE" in
    http | https | both) ;;
    *)
        echo "proxy-router: NGINX_MODE must be http, https, or both" >&2
        exit 1
        ;;
esac

urlencode() {
    printf '%s' "$1" | awk '
        BEGIN { for (i = 0; i < 256; i++) ord[sprintf("%c", i)] = i }
        {
            n = split($0, c, "")
            for (i = 1; i <= n; i++) {
                if (c[i] ~ /[A-Za-z0-9._~-]/) printf "%s", c[i]
                else printf "%%%02X", ord[c[i]]
            }
        }'
}

safe_id() {
    printf '%s_%s' \
        "$(printf '%s' "$1" | tr -c 'A-Za-z0-9_' '_')" \
        "$(printf '%s' "$1" | sha256sum | cut -c1-8)"
}

validate_version() {
    case "$1" in
        *[/?#%]* | *[[:space:]]* | *\\* | *\"* | *\'* | . | ..)
            echo "proxy-router: version '$1' cannot be represented as a path segment" >&2
            exit 1
            ;;
    esac
}

backend_name() {
    case "$1" in
        [A-Za-z0-9]*[!A-Za-z0-9._-]* | [!A-Za-z0-9]*)
            printf 'versiond_routers_%s' "$(safe_id "$1")"
            ;;
        *) printf 'versiond_routers_%s' "$1" ;;
    esac
}

BACKENDS_FILE=$(mktemp)
ADMIN_RULES_FILE=$(mktemp)
VERSION_READY_RULES_FILE=$(mktemp)
STATIC_VERSIONS_FILE=$(mktemp)
CACHED_VERSIONS_FILE=$(mktemp)
CACHED_DYNAMIC_VERSIONS_FILE=$(mktemp)
trap 'rm -f "$BACKENDS_FILE" "$ADMIN_RULES_FILE" "$VERSION_READY_RULES_FILE" "$STATIC_VERSIONS_FILE" "$CACHED_VERSIONS_FILE" "$CACHED_DYNAMIC_VERSIONS_FILE"' EXIT
: > "$VERSION_MAP"
: > "$READY_VERSION_MAP"
: > "$SLOT_MAP"
: > "$BACKENDS_FILE"
: > "$VERSION_READY_RULES_FILE"
printf '%s\n%s\n' "${VERSIOND_NON_HA_VERSIONS:-}" "${VERSIOND_VERSIONS:-}" \
    | tr ',;' '  ' | tr -s ' ' '\n' > "$STATIC_VERSIONS_FILE"
: > "$CACHED_VERSIONS_FILE"
if [ -n "$CATALOG_URL" ] && [ -f "$CATALOG_CACHE_FILE" ]; then
    cache_status=0
    "$CATALOG_CACHE_BIN" read "$CATALOG_CACHE_FILE" "$CATALOG_CACHE_MAX_AGE" \
        > "$CACHED_VERSIONS_FILE" || cache_status=$?
    case "$cache_status" in
        0) echo "proxy-router: loaded the fresh routing catalog cache" >&2 ;;
        2)
            echo "proxy-router: ignoring stale routing catalog cache" >&2
            : > "$CACHED_VERSIONS_FILE"
            ;;
        *)
            echo "proxy-router: ignoring invalid routing catalog cache" >&2
            : > "$CACHED_VERSIONS_FILE"
            ;;
    esac
fi

render_router_backend() {
    backend=$1
    ready_check=$2
    server_state=$3
    sed \
        -e "s|\${BACKEND_NAME}|$backend|g" \
        -e "s|\${READY_CHECK_SEND}|$ready_check|g" \
        -e "s|\${ROUTER_POOL_SLOTS}|$ROUTER_POOL_SLOTS|g" \
        -e "s|\${ROUTER_POOL_HOST}|$ROUTER_POOL_HOST|g" \
        -e "s|\${ROUTER_PORT}|$ROUTER_PORT|g" \
        -e "s|\${ROUTER_ADMIN_PORT}|$ROUTER_ADMIN_PORT|g" \
        -e "s|\${SERVER_STATE}|$server_state|g" \
        "$BACKEND_TEMPLATE" >> "$BACKENDS_FILE"
}

render_router_backend versiond_router_coarse \
    'http-check send meth GET uri /readyz' ''

declare_version() {
        version=$1
        [ -n "$version" ] || return 0
        validate_version "$version"
        if awk -v v="$version" '$1 "" == v "" { found = 1 } END { exit !found }' "$VERSION_MAP"; then
            echo "proxy-router: version '$version' is declared more than once" >&2
            exit 1
        fi
        backend=$(backend_name "$version")
        encoded=$(urlencode "$version")
        printf '%s %s\n' "$version" "$backend" >> "$VERSION_MAP"
        printf 'version=%s %s\n' "$encoded" "$backend" >> "$READY_VERSION_MAP"
        render_router_backend "$backend" \
            "http-check send meth GET uri /readyz?version=$encoded" ''
        printf '%s\n' \
            "    http-request return status 200 content-type text/plain string \"ready\\n\" if { path /readyz } { query -m str version=$encoded } { nbsrv($backend) gt 0 }" \
            "    http-request return status 503 content-type text/plain string \"not ready\\n\" if { path /readyz } { query -m str version=$encoded }" \
            >> "$VERSION_READY_RULES_FILE"
}

while IFS= read -r version; do
    [ -n "$version" ] || continue
    declare_version "$version"
done < "$STATIC_VERSIONS_FILE"

# Keep learned names in the bounded dynamic-slot namespace across restarts.
# Promoting them to static backends here would silently reset capacity usage.
: > "$CACHED_DYNAMIC_VERSIONS_FILE"
while IFS= read -r version; do
    [ -n "$version" ] || continue
    if awk -v v="$version" '$1 "" == v "" { found = 1 } END { exit !found }' "$VERSION_MAP"; then
        continue
    fi
    printf '%s\n' "$version" >> "$CACHED_DYNAMIC_VERSIONS_FILE"
done < "$CACHED_VERSIONS_FILE"
cached_dynamic_count=$(wc -l < "$CACHED_DYNAMIC_VERSIONS_FILE")
if [ "$cached_dynamic_count" -gt "$VERSION_CAPACITY" ]; then
    echo "proxy-router: fresh catalog cache needs $cached_dynamic_count dynamic slots," >&2
    echo "  but PROXY_ROUTER_VERSION_CAPACITY is $VERSION_CAPACITY" >&2
    exit 1
fi

index=1
while [ "$index" -le "$VERSION_CAPACITY" ]; do
    backend="versiond_routers_dynamic_$index"
    cached_version=$(sed -n "${index}p" "$CACHED_DYNAMIC_VERSIONS_FILE")
    server_state=disabled
    if [ -n "$cached_version" ]; then
        encoded=$(urlencode "$cached_version")
        printf '%s %s\n' "$backend" "$encoded" >> "$SLOT_MAP"
        printf '%s %s\n' "$cached_version" "$backend" >> "$VERSION_MAP"
        printf 'version=%s %s\n' "$encoded" "$backend" >> "$READY_VERSION_MAP"
        printf '%s\n' \
            "    http-request return status 200 content-type text/plain string \"ready\\n\" if { path /readyz } { query -m str version=$encoded } { nbsrv($backend) gt 0 }" \
            "    http-request return status 503 content-type text/plain string \"not ready\\n\" if { path /readyz } { query -m str version=$encoded }" \
            >> "$VERSION_READY_RULES_FILE"
        server_state=
    else
        printf '%s %s\n' "$backend" __unassigned__ >> "$SLOT_MAP"
    fi
    render_router_backend "$backend" \
        "http-check send meth GET uri-lf /readyz?version=%[be_name,map($SLOT_MAP)]" \
        "$server_state"
    index=$((index + 1))
done

if [ -n "$CATALOG_URL" ]; then
    UNDECLARED_VERSION_GUARD="http-request return status 503 content-type \"text/plain\" string \"version-is-not-in-governance-catalog\" if { var(txn.ver) -m reg . } !versionless_request !{ var(txn.ver),map_str($VERSION_MAP) -m found }"
    DYNAMIC_READY_GUARD="http-request return status 503 content-type \"text/plain\" string \"version-is-not-declared-or-ready\" if { path /readyz } { url_param(version) -m found } !{ query,map_str($READY_VERSION_MAP) -m found }"
elif [ -s "$VERSION_MAP" ]; then
    UNDECLARED_VERSION_GUARD="http-request return status 503 content-type \"text/plain\" string \"version-is-not-declared\" if { var(txn.ver) -m reg . } !versionless_request !{ var(txn.ver),map_str($VERSION_MAP) -m found }"
    DYNAMIC_READY_GUARD="http-request return status 503 content-type \"text/plain\" string \"version-is-not-declared-or-ready\" if { path /readyz } { url_param(version) -m found } !{ query,map_str($READY_VERSION_MAP) -m found }"
else
    UNDECLARED_VERSION_GUARD="# No version catalog: use the coarse router pool."
    DYNAMIC_READY_GUARD="# Dynamic version readiness is disabled."
fi

case "$NGINX_MODE" in
    http)
        cat > "$ADMIN_RULES_FILE" <<'EOF'
    http-request return status 200 content-type text/plain string "ready\n" if { path /readyz } !{ query -m found } { nbsrv(policy_http) gt 0 }
    http-request return status 503 content-type text/plain string "not ready\n" if { path /readyz } !{ query -m found }
EOF
        ;;
    https)
        cat > "$ADMIN_RULES_FILE" <<'EOF'
    http-request return status 200 content-type text/plain string "ready\n" if { path /readyz } !{ query -m found } { nbsrv(policy_https) gt 0 }
    http-request return status 503 content-type text/plain string "not ready\n" if { path /readyz } !{ query -m found }
EOF
        ;;
    both)
        cat > "$ADMIN_RULES_FILE" <<'EOF'
    http-request return status 200 content-type text/plain string "ready\n" if { path /readyz } !{ query -m found } { nbsrv(policy_http) gt 0 } { nbsrv(policy_https) gt 0 }
    http-request return status 503 content-type text/plain string "not ready\n" if { path /readyz } !{ query -m found }
EOF
        ;;
esac
cat >> "$ADMIN_RULES_FILE" <<'EOF'
    http-request return status 200 content-type text/plain string "ready\n" if { path /readyz } { query -m str component=versiond } { nbsrv(versiond_router_coarse) gt 0 }
    http-request return status 503 content-type text/plain string "not ready\n" if { path /readyz } { query -m str component=versiond }
    http-request return status 200 content-type text/plain string "ready\n" if { path /readyz } { query -m str component=edge-api } { nbsrv(edge_api_pool) gt 0 }
    http-request return status 503 content-type text/plain string "not ready\n" if { path /readyz } { query -m str component=edge-api }
EOF
cat "$VERSION_READY_RULES_FILE" >> "$ADMIN_RULES_FILE"

sed \
    -e "s|\${POLICY_POOL_HOST}|$POLICY_POOL_HOST|g" \
    -e "s|\${POLICY_POOL_SLOTS}|$POLICY_POOL_SLOTS|g" \
    -e "s|\${VERSION_BACKEND_MAP}|$VERSION_MAP|g" \
    -e "s|\${READY_VERSION_BACKEND_MAP}|$READY_VERSION_MAP|g" \
    -e "s|\${UNDECLARED_VERSION_GUARD}|$UNDECLARED_VERSION_GUARD|g" \
    -e "s|\${DYNAMIC_READY_GUARD}|$DYNAMIC_READY_GUARD|g" \
    -e "s|\${VERSIOND_FRONTEND_PORT}|$VERSIOND_FRONTEND_PORT|g" \
    -e "s|\${EDGE_FRONTEND_PORT}|$EDGE_FRONTEND_PORT|g" \
    -e "s|\${POLICY_BIND_ADDRESS}|$POLICY_BIND_ADDRESS|g" \
    -e "s|\${EDGE_POOL_HOST}|$EDGE_POOL_HOST|g" \
    -e "s|\${EDGE_POOL_SLOTS}|$EDGE_POOL_SLOTS|g" \
    -e "s|\${EDGE_API_PORT}|$EDGE_API_PORT|g" \
    -e "s|\${ADMIN_PORT}|$ADMIN_PORT|g" \
    -e "s|\${MAX_CONNECTIONS}|$MAX_CONNECTIONS|g" \
    -e "s|\${CONNECT_TIMEOUT_SECONDS}|$CONNECT_TIMEOUT|g" \
    -e "s|\${STREAM_IDLE_SECONDS}|$STREAM_IDLE|g" \
    -e "s|\${PUBLIC_IDLE_SECONDS}|$PUBLIC_IDLE|g" \
    -e "/\${VERSIOND_ROUTER_BACKENDS}/{
        r $BACKENDS_FILE
        d
    }" \
    -e "/\${ADMIN_READY_RULES}/{
        r $ADMIN_RULES_FILE
        d
    }" \
    "$TEMPLATE" > "$OUT"

"$HAPROXY_BIN" -c -f "$OUT" >/dev/null

if [ -n "$RENDER_ONLY" ]; then
    exit 0
fi

run_catalog_reconciler() {
    while :; do
        status=0
        ROUTING_CATALOG_COMPONENT=proxy-router \
        ROUTING_CATALOG_URL="$CATALOG_URL" \
        ROUTING_CATALOG_RUNTIME_SOCKET=/var/run/haproxy/reconciler.sock \
        ROUTING_CATALOG_VERSION_MAP="$VERSION_MAP" \
        ROUTING_CATALOG_READY_MAP="$READY_VERSION_MAP" \
        ROUTING_CATALOG_SLOT_MAP="$SLOT_MAP" \
        ROUTING_CATALOG_BACKEND_PREFIX=versiond_routers_dynamic_ \
        ROUTING_CATALOG_BACKEND_CAPACITY="$VERSION_CAPACITY" \
        ROUTING_CATALOG_SERVER_PREFIX=router \
        ROUTING_CATALOG_SERVER_CAPACITY="$ROUTER_POOL_SLOTS" \
        ROUTING_CATALOG_POLL_SECONDS="$CATALOG_POLL" \
        ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS="$CATALOG_FETCH_TIMEOUT" \
        ROUTING_CATALOG_CACHE_FILE="$CATALOG_CACHE_FILE" \
        ROUTING_CATALOG_CACHE_BIN="$CATALOG_CACHE_BIN" \
        ROUTING_CATALOG_STATUS_FILE="$CATALOG_STATUS_FILE" \
            /usr/local/lib/router-runtime/catalog-reconciler || status=$?
        echo "proxy-router: catalog reconciler exited with status $status; restarting" >&2
        sleep 1
    done
}

if [ -n "$CATALOG_URL" ]; then
    run_catalog_reconciler &
fi

exec "$HAPROXY_BIN" -W -db -f "$OUT"
