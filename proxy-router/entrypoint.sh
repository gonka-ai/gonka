#!/bin/sh

set -eu

TEMPLATE="${PROXY_ROUTER_TEMPLATE:-/etc/haproxy/haproxy.cfg.template}"
BACKEND_TEMPLATE="${PROXY_ROUTER_BACKEND_TEMPLATE:-/etc/haproxy/versiond-backend.cfg.template}"
OUT="${PROXY_ROUTER_OUT:-/etc/haproxy/haproxy.cfg}"
VERSION_MAP="${PROXY_ROUTER_VERSION_MAP:-/etc/haproxy/version-router.map}"
HAPROXY_BIN="${HAPROXY_BIN:-haproxy}"

POLICY_POOL_HOST="${PROXY_POLICY_POOL_HOST:-proxy-policy}"
POLICY_POOL_SLOTS="${PROXY_POLICY_POOL_SLOTS:-4}"
ROUTER_POOL_HOST="${VERSIOND_ROUTER_POOL_HOST:-versiond-router}"
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
    "$MAX_CONNECTIONS" "$CONNECT_TIMEOUT" "$STREAM_IDLE" "$PUBLIC_IDLE"; do
    case "$value" in
        '' | *[!0-9]*)
            echo "proxy-router: invalid numeric setting '$value'" >&2
            exit 1
            ;;
    esac
done

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
trap 'rm -f "$BACKENDS_FILE" "$ADMIN_RULES_FILE" "$VERSION_READY_RULES_FILE"' EXIT
: > "$VERSION_MAP"
: > "$BACKENDS_FILE"
: > "$VERSION_READY_RULES_FILE"

render_router_backend() {
    backend=$1
    ready_uri=$2
    sed \
        -e "s|\${BACKEND_NAME}|$backend|g" \
        -e "s|\${READYZ_URI}|$ready_uri|g" \
        -e "s|\${ROUTER_POOL_SLOTS}|$ROUTER_POOL_SLOTS|g" \
        -e "s|\${ROUTER_POOL_HOST}|$ROUTER_POOL_HOST|g" \
        -e "s|\${ROUTER_PORT}|$ROUTER_PORT|g" \
        -e "s|\${ROUTER_ADMIN_PORT}|$ROUTER_ADMIN_PORT|g" \
        "$BACKEND_TEMPLATE" >> "$BACKENDS_FILE"
}

render_router_backend versiond_router_coarse /readyz

printf '%s\n%s\n' "${VERSIOND_NON_HA_VERSIONS:-}" "${VERSIOND_VERSIONS:-}" \
    | tr ',;' '  ' | tr -s ' ' '\n' | while read -r version; do
        [ -n "$version" ] || continue
        validate_version "$version"
        if awk -v v="$version" '$1 "" == v "" { found = 1 } END { exit !found }' "$VERSION_MAP"; then
            echo "proxy-router: version '$version' is declared more than once" >&2
            exit 1
        fi
        backend=$(backend_name "$version")
        encoded=$(urlencode "$version")
        printf '%s %s\n' "$version" "$backend" >> "$VERSION_MAP"
        render_router_backend "$backend" "/readyz?version=$encoded"
        printf '%s\n' \
            "    http-request return status 200 content-type text/plain string \"ready\\n\" if { path /readyz } { query -m str version=$encoded } { nbsrv($backend) gt 0 }" \
            "    http-request return status 503 content-type text/plain string \"not ready\\n\" if { path /readyz } { query -m str version=$encoded }" \
            >> "$VERSION_READY_RULES_FILE"
    done

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

exec "$HAPROXY_BIN" -W -db -f "$OUT"
