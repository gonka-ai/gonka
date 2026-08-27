#!/bin/sh

set -eu

entrypoint_dir=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
runtime_contract=${ROUTER_RUNTIME_VERSION_CONTRACT:-}
if [ -z "$runtime_contract" ]; then
    if [ -r "$entrypoint_dir/../router-runtime/version-contract" ]; then
        runtime_contract=$entrypoint_dir/../router-runtime/version-contract
    else
        runtime_contract=/usr/local/lib/router-runtime/version-contract
    fi
fi
# shellcheck disable=SC1090,SC1091
. "$runtime_contract"

TEMPLATE="${PROXY_ROUTER_TEMPLATE:-/etc/haproxy/haproxy.cfg.template}"
BACKEND_TEMPLATE="${PROXY_ROUTER_BACKEND_TEMPLATE:-/etc/haproxy/versiond-backend.cfg.template}"
OUT="${PROXY_ROUTER_OUT:-/etc/haproxy/haproxy.cfg}"
VERSION_MAP="${PROXY_ROUTER_VERSION_MAP:-/etc/haproxy/version-router.map}"
SLOT_MAP="${OUT}.version-slots.map"
HAPROXY_BIN="${HAPROXY_BIN:-haproxy}"

POLICY_POOL_HOST="${PROXY_POLICY_POOL_HOST:-proxy-policy}"
POLICY_POOL_SLOTS="${PROXY_POLICY_POOL_SLOTS:-4}"
ROUTER_POOL_HOST="${VERSIOND_ROUTER_POOL_HOST:-versiond-router-fleet}"
ROUTER_POOL_SLOTS="${VERSIOND_ROUTER_FLEET_CAPACITY:-16}"
ROUTER_PORT="${VERSIOND_ROUTER_PORT:-8080}"
ROUTER_ADMIN_PORT="${VERSIOND_ROUTER_ADMIN_PORT:-8404}"
ROUTER_HEALTH_CONTRACT="${VERSIOND_ROUTER_HEALTH_CONTRACT:-readyz}"
VERSIOND_FRONTEND_PORT="${PROXY_VERSIOND_PORT:-18081}"
ADMIN_PORT=8404
MAX_CONNECTIONS=8192
CONNECT_TIMEOUT="${PROXY_ROUTER_CONNECT_TIMEOUT_SECONDS:-2}"
STREAM_IDLE="${PROXY_ROUTER_STREAM_IDLE_SECONDS:-1200}"
PUBLIC_IDLE="${PROXY_ROUTER_PUBLIC_IDLE_SECONDS:-86400}"
VERSION_CAPACITY="${PROXY_ROUTER_VERSION_CAPACITY:-32}"
CATALOG_URL="${VERSIOND_ROUTING_CATALOG_URL:-}"
CATALOG_POLL="${VERSIOND_ROUTING_CATALOG_POLL_SECONDS:-5}"
CATALOG_FETCH_TIMEOUT="${VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS:-3}"
CATALOG_MAX_BYTES="${VERSIOND_ROUTING_CATALOG_MAX_BYTES:-1048576}"
CATALOG_RUNTIME_TIMEOUT="${VERSIOND_ROUTING_CATALOG_RUNTIME_TIMEOUT_SECONDS:-2}"
CATALOG_ACTIVATION_MIN_READY="${PROXY_ROUTER_ACTIVATION_MIN_READY:-1}"
CATALOG_CACHE_FILE=/var/lib/gonka-router/catalog.json
CATALOG_CACHE_MAX_AGE="${VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS:-86400}"
CATALOG_STATUS_FILE=/var/run/haproxy/catalog-status.json
CATALOG_CACHE_BIN="${ROUTING_CATALOG_CACHE_BIN:-/usr/local/lib/router-runtime/catalog-cache}"
NGINX_MODE="${NGINX_MODE:-http}"
POLICY_BIND_HOST="${PROXY_ROUTER_POLICY_BIND_HOST:-}"
METRICS_BIND_HOST="${PROXY_ROUTER_METRICS_BIND_HOST:-}"
CATALOG_BIND_HOST="${PROXY_ROUTER_CATALOG_BIND_HOST:-}"
CATALOG_PROXY_PORT="${PROXY_ROUTER_CATALOG_PORT:-9100}"
CATALOG_UPSTREAM_HOST="${PROXY_ROUTER_CATALOG_UPSTREAM_HOST:-}"
CATALOG_UPSTREAM_PORT="${PROXY_ROUTER_CATALOG_UPSTREAM_PORT:-9100}"
DNS_RESOLVER="${HAPROXY_DNS_RESOLVER:-127.0.0.11:53}"

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

if [ -n "$CATALOG_BIND_HOST" ]; then
    if [ -z "$CATALOG_UPSTREAM_HOST" ]; then
        echo "proxy-router: PROXY_ROUTER_CATALOG_UPSTREAM_HOST is required when the catalog bridge is enabled" >&2
        exit 1
    fi
    CATALOG_BIND_ADDRESS=$(resolve_ipv4 "$CATALOG_BIND_HOST")
    case "$CATALOG_BIND_ADDRESS" in
        '' | *[!0-9.]*)
            echo "proxy-router: cannot resolve catalog bind host '$CATALOG_BIND_HOST' to IPv4" >&2
            exit 1
            ;;
    esac
elif [ -n "$CATALOG_UPSTREAM_HOST" ]; then
    echo "proxy-router: PROXY_ROUTER_CATALOG_BIND_HOST is required when the catalog upstream is set" >&2
    exit 1
fi

if [ -n "$METRICS_BIND_HOST" ]; then
    METRICS_BIND_ADDRESS=$(resolve_ipv4 "$METRICS_BIND_HOST")
    case "$METRICS_BIND_ADDRESS" in
        '' | *[!0-9.]*)
            echo "proxy-router: cannot resolve metrics bind host '$METRICS_BIND_HOST' to IPv4" >&2
            exit 1
            ;;
    esac
    METRICS_NETWORK_BIND="    bind $METRICS_BIND_ADDRESS:8405"
else
    METRICS_BIND_ADDRESS=127.0.0.1
    METRICS_NETWORK_BIND="    # No network metrics bind configured."
fi

bool_env() {
    raw=$(eval "printf '%s' \"\${$1:-}\"")
    case "$(printf '%s' "$raw" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')" in
        1 | t | true | yes | on) printf '1' ;;
        '' | 0 | f | false | no | off) ;;
        *)
            echo "proxy-router: $1='$raw' is not a boolean; use 1/t/true/yes/on or empty/0/f/false/no/off" >&2
            exit 1
            ;;
    esac
}
CATALOG_ALLOW_REMOVALS=$(bool_env VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS)
RENDER_ONLY=$(bool_env PROXY_ROUTER_RENDER_ONLY)

for host in "$POLICY_POOL_HOST" "$ROUTER_POOL_HOST"; do
    case "$host" in
        '' | *[!A-Za-z0-9._-]*)
            echo "proxy-router: invalid hostname '$host'" >&2
            exit 1
            ;;
    esac
done
if [ -n "$CATALOG_UPSTREAM_HOST" ]; then
    case "$CATALOG_UPSTREAM_HOST" in
        *[!A-Za-z0-9._-]*)
            echo "proxy-router: invalid catalog upstream hostname '$CATALOG_UPSTREAM_HOST'" >&2
            exit 1
            ;;
    esac
fi
for value in "$POLICY_POOL_SLOTS" "$ROUTER_POOL_SLOTS" "$ROUTER_PORT" \
    "$ROUTER_ADMIN_PORT" "$VERSIOND_FRONTEND_PORT" "$ADMIN_PORT" \
    "$MAX_CONNECTIONS" "$CONNECT_TIMEOUT" "$STREAM_IDLE" "$PUBLIC_IDLE" \
    "$VERSION_CAPACITY" "$CATALOG_POLL" "$CATALOG_FETCH_TIMEOUT" \
    "$CATALOG_MAX_BYTES" "$CATALOG_RUNTIME_TIMEOUT" \
    "$CATALOG_ACTIVATION_MIN_READY" \
    "$CATALOG_CACHE_MAX_AGE" "$CATALOG_PROXY_PORT" "$CATALOG_UPSTREAM_PORT"; do
    case "$value" in
        '' | *[!0-9]*)
            echo "proxy-router: invalid numeric setting '$value'" >&2
            exit 1
            ;;
    esac
done
if [ "$VERSION_CAPACITY" -eq 0 ] || [ "$CATALOG_POLL" -eq 0 ] || \
    [ "$CATALOG_FETCH_TIMEOUT" -eq 0 ] || [ "$CATALOG_CACHE_MAX_AGE" -eq 0 ] || \
    [ "$CATALOG_MAX_BYTES" -eq 0 ] || [ "$CATALOG_RUNTIME_TIMEOUT" -eq 0 ] || \
    [ "$CATALOG_ACTIVATION_MIN_READY" -eq 0 ] || \
    [ "$CATALOG_ACTIVATION_MIN_READY" -gt "$ROUTER_POOL_SLOTS" ] || \
    [ "$CATALOG_PROXY_PORT" -eq 0 ] || [ "$CATALOG_UPSTREAM_PORT" -eq 0 ]; then
    echo "proxy-router: catalog capacity and timing values must be positive" >&2
    exit 1
fi
if [ -n "$CATALOG_URL" ] && [ "$ROUTER_POOL_SLOTS" -gt "$ROUTER_RUNTIME_BATCH_SERVER_LIMIT" ]; then
    echo "proxy-router: VERSIOND_ROUTER_FLEET_CAPACITY exceeds the catalog Runtime API batch limit ($ROUTER_RUNTIME_BATCH_SERVER_LIMIT)" >&2
    exit 1
fi
case "$CATALOG_URL" in
    '' | http://* | https://*) ;;
    *)
        echo "proxy-router: VERSIOND_ROUTING_CATALOG_URL must use http or https" >&2
        exit 1
        ;;
esac
if ! printf '%s\n' "$DNS_RESOLVER" | grep -Eq '^(\[[0-9A-Fa-f:]+\]|[0-9A-Fa-f:.]+)(:[0-9]+)?$'; then
    echo "proxy-router: HAPROXY_DNS_RESOLVER must be a numeric IP with an optional port" >&2
    exit 1
fi

case "$NGINX_MODE" in
    http | https | both) ;;
    *)
        echo "proxy-router: NGINX_MODE must be http, https, or both" >&2
        exit 1
        ;;
esac

case "$ROUTER_HEALTH_CONTRACT" in
    legacy | readyz) ;;
    *)
        echo "proxy-router: VERSIOND_ROUTER_HEALTH_CONTRACT must be legacy or readyz" >&2
        exit 1
        ;;
esac

safe_id() {
    printf '%s_%s' \
        "$(printf '%s' "$1" | tr -c 'A-Za-z0-9_' '_')" \
        "$(printf '%s' "$1" | sha256sum | cut -c1-8)"
}

validate_static_version() {
    case "$1" in
        *[/?#%]* | *[[:space:]]* | *\\* | *\"* | *\'*)
            echo "proxy-router: version '$1' cannot be routed as a literal path segment" >&2
            exit 1
            ;;
        . | ..)
            echo "proxy-router: version '$1' is not a safe path segment" >&2
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
CATALOG_PROXY_FILE=$(mktemp)
trap 'rm -f "$BACKENDS_FILE" "$ADMIN_RULES_FILE" "$VERSION_READY_RULES_FILE" "$STATIC_VERSIONS_FILE" "$CACHED_VERSIONS_FILE" "$CACHED_DYNAMIC_VERSIONS_FILE" "$CATALOG_PROXY_FILE"' EXIT
if [ -n "$CATALOG_BIND_HOST" ]; then
    cat > "$CATALOG_PROXY_FILE" <<EOF
frontend routing_catalog
    option httplog
    bind ${CATALOG_BIND_ADDRESS}:${CATALOG_PROXY_PORT}
    http-request return status 405 content-type text/plain string "method not allowed\\n" unless METH_GET
    http-request return status 404 content-type text/plain string "not found\\n" unless { path /versions }
    default_backend routing_catalog_api

backend routing_catalog_api
    option httpchk
    http-check send meth GET uri /versions
    http-check expect status 200
    server catalog ${CATALOG_UPSTREAM_HOST}:${CATALOG_UPSTREAM_PORT} check resolvers docker init-addr none
EOF
else
    printf '%s\n' '# Read-only routing catalog bridge is disabled.' > "$CATALOG_PROXY_FILE"
fi
: > "$VERSION_MAP"
: > "$SLOT_MAP"
: > "$BACKENDS_FILE"
: > "$VERSION_READY_RULES_FILE"
printf '%s\n%s\n' "${VERSIOND_NON_HA_VERSIONS:-}" "${VERSIOND_VERSIONS:-}" \
    | tr ',;[:space:]' '\n' > "$STATIC_VERSIONS_FILE"
: > "$CACHED_VERSIONS_FILE"
if [ -n "$CATALOG_URL" ] && [ -f "$CATALOG_CACHE_FILE" ]; then
    cache_status=0
    "$CATALOG_CACHE_BIN" read "$CATALOG_CACHE_FILE" "$CATALOG_CACHE_MAX_AGE" \
        > "$CACHED_VERSIONS_FILE" || cache_status=$?
    case "$cache_status" in
        0) echo "proxy-router: loaded the fresh routing catalog cache" >&2 ;;
        2)
            echo "proxy-router: loaded stale accepted routes; waiting for a fresh catalog" >&2
            ;;
        *)
            echo "proxy-router: ignoring invalid routing catalog cache" >&2
            : > "$CACHED_VERSIONS_FILE"
            ;;
    esac
fi

render_router_backend() {
    backend=$1
    data_check=$2
    ready_check=$3
    server_state=$4
    retry_on='retry-on conn-failure empty-response 502'
    if [ "$backend" = versiond_router_coarse ]; then
        retry_on="$retry_on 404"
    fi
    if [ "$ROUTER_HEALTH_CONTRACT" = readyz ]; then
        ready_connect="http-check connect port $ROUTER_ADMIN_PORT"
        ready_expect='http-check expect status 200'
    else
        ready_connect='# Legacy router readiness is its data-path health check.'
        ready_check='# No separate readiness listener in the legacy router.'
        ready_expect='# End of the legacy health-check sequence.'
    fi
    sed \
        -e "s|\${BACKEND_NAME}|$backend|g" \
        -e "s|\${DATA_CHECK_SEND}|$data_check|g" \
        -e "s|\${READY_CHECK_CONNECT}|$ready_connect|g" \
        -e "s|\${READY_CHECK_SEND}|$ready_check|g" \
        -e "s|\${READY_CHECK_EXPECT}|$ready_expect|g" \
        -e "s|\${ROUTER_POOL_SLOTS}|$ROUTER_POOL_SLOTS|g" \
        -e "s|\${ROUTER_POOL_HOST}|$ROUTER_POOL_HOST|g" \
        -e "s|\${ROUTER_PORT}|$ROUTER_PORT|g" \
        -e "s|\${ROUTER_ADMIN_PORT}|$ROUTER_ADMIN_PORT|g" \
        -e "s|\${RETRY_ON}|$retry_on|g" \
        -e "s|\${SERVER_STATE}|$server_state|g" \
        "$BACKEND_TEMPLATE" >> "$BACKENDS_FILE"
}

render_router_backend versiond_router_coarse \
    "http-check send meth GET uri /healthz hdr Host $ROUTER_POOL_HOST" \
    "http-check send meth GET uri /readyz hdr Host $ROUTER_POOL_HOST" ''

declare_version() {
        version=$1
        [ -n "$version" ] || return 0
        validate_static_version "$version"
        if awk -v v="$version" '$1 "" == v "" { found = 1 } END { exit !found }' "$VERSION_MAP"; then
            echo "proxy-router: version '$version' is declared more than once" >&2
            exit 1
        fi
        backend=$(backend_name "$version")
        encoded=$(router_urlencode "$version")
        printf '%s %s\n' "$version" "$backend" >> "$VERSION_MAP"
        render_router_backend "$backend" \
            "http-check send meth GET uri /$encoded/healthz hdr Host $ROUTER_POOL_HOST" \
            "http-check send meth GET uri /readyz?version=$encoded hdr Host $ROUTER_POOL_HOST" ''
        printf '%s\n' \
            "    http-request return status 200 content-type text/plain string \"ready\\n\" if { path /readyz } { var(txn.ready_ver),map_str($VERSION_MAP) -m str $backend } { nbsrv($backend) gt 0 }" \
            "    http-request return status 503 content-type text/plain string \"not ready\\n\" if { path /readyz } { var(txn.ready_ver),map_str($VERSION_MAP) -m str $backend }" \
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
        encoded=$(router_urlencode "$cached_version")
        printf '%s %s\n' "$backend" "$encoded" >> "$SLOT_MAP"
        printf '%s %s\n' "$cached_version" "$backend" >> "$VERSION_MAP"
        server_state=
    else
        printf '%s %s\n' "$backend" __unassigned__ >> "$SLOT_MAP"
    fi
    render_router_backend "$backend" \
        "http-check send meth GET uri-lf /%[be_name,map($SLOT_MAP)]/healthz hdr Host $ROUTER_POOL_HOST" \
        "http-check send meth GET uri-lf /readyz?version=%[be_name,map($SLOT_MAP)] hdr Host $ROUTER_POOL_HOST" \
        "$server_state"
    printf '%s\n' \
        "    http-request return status 200 content-type text/plain string \"ready\\n\" if { path /readyz } { var(txn.ready_ver),map_str($VERSION_MAP) -m str $backend } { nbsrv($backend) gt 0 }" \
        "    http-request return status 503 content-type text/plain string \"not ready\\n\" if { path /readyz } { var(txn.ready_ver),map_str($VERSION_MAP) -m str $backend }" \
        >> "$VERSION_READY_RULES_FILE"
    index=$((index + 1))
done

if [ -n "$CATALOG_URL" ]; then
    UNDECLARED_VERSION_GUARD="http-request return status 503 content-type \"text/plain\" string \"version-is-not-in-governance-catalog\" if { var(txn.ver) -m reg . } !versionless_request !{ var(txn.ver),map_str($VERSION_MAP) -m found }"
    DYNAMIC_READY_GUARD="http-request return status 503 content-type \"text/plain\" string \"version-is-not-declared-or-ready\" if { path /readyz } { url_param(version) -m found } !{ var(txn.ready_ver),map_str($VERSION_MAP) -m found }"
elif [ -s "$VERSION_MAP" ]; then
    UNDECLARED_VERSION_GUARD="http-request return status 503 content-type \"text/plain\" string \"version-is-not-declared\" if { var(txn.ver) -m reg . } !versionless_request !{ var(txn.ver),map_str($VERSION_MAP) -m found }"
    DYNAMIC_READY_GUARD="http-request return status 503 content-type \"text/plain\" string \"version-is-not-declared-or-ready\" if { path /readyz } { url_param(version) -m found } !{ var(txn.ready_ver),map_str($VERSION_MAP) -m found }"
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
EOF
cat "$VERSION_READY_RULES_FILE" >> "$ADMIN_RULES_FILE"

sed \
    -e "s|\${POLICY_POOL_HOST}|$POLICY_POOL_HOST|g" \
    -e "s|\${POLICY_POOL_SLOTS}|$POLICY_POOL_SLOTS|g" \
    -e "s|\${VERSION_BACKEND_MAP}|$VERSION_MAP|g" \
    -e "s|\${UNDECLARED_VERSION_GUARD}|$UNDECLARED_VERSION_GUARD|g" \
    -e "s|\${DYNAMIC_READY_GUARD}|$DYNAMIC_READY_GUARD|g" \
    -e "s|\${VERSIOND_FRONTEND_PORT}|$VERSIOND_FRONTEND_PORT|g" \
    -e "s|\${POLICY_BIND_ADDRESS}|$POLICY_BIND_ADDRESS|g" \
    -e "s|\${METRICS_NETWORK_BIND}|$METRICS_NETWORK_BIND|g" \
    -e "s|\${ADMIN_PORT}|$ADMIN_PORT|g" \
    -e "s|\${MAX_CONNECTIONS}|$MAX_CONNECTIONS|g" \
    -e "s|\${CONNECT_TIMEOUT_SECONDS}|$CONNECT_TIMEOUT|g" \
    -e "s|\${STREAM_IDLE_SECONDS}|$STREAM_IDLE|g" \
    -e "s|\${PUBLIC_IDLE_SECONDS}|$PUBLIC_IDLE|g" \
    -e "s|\${DNS_RESOLVER}|$DNS_RESOLVER|g" \
	-e "/\${CATALOG_PROXY_CONFIG}/{
		r $CATALOG_PROXY_FILE
		d
	}" \
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
        ROUTING_CATALOG_PROJECTION_MAP="$VERSION_MAP" \
        ROUTING_CATALOG_SLOT_MAP="$SLOT_MAP" \
        ROUTING_CATALOG_BACKEND_PREFIX=versiond_routers_dynamic_ \
        ROUTING_CATALOG_BACKEND_CAPACITY="$VERSION_CAPACITY" \
        ROUTING_CATALOG_SERVER_PREFIX=router \
        ROUTING_CATALOG_SERVER_CAPACITY="$ROUTER_POOL_SLOTS" \
        ROUTING_CATALOG_ACTIVATION_MIN_READY="$CATALOG_ACTIVATION_MIN_READY" \
        ROUTING_CATALOG_ALLOW_REMOVALS="$CATALOG_ALLOW_REMOVALS" \
        ROUTING_CATALOG_EXCLUDE="${VERSIOND_NON_HA_VERSIONS:-} ${VERSIOND_VERSIONS:-}" \
        ROUTING_CATALOG_POLL_SECONDS="$CATALOG_POLL" \
        ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS="$CATALOG_FETCH_TIMEOUT" \
        ROUTING_CATALOG_MAX_BYTES="$CATALOG_MAX_BYTES" \
        ROUTING_CATALOG_RUNTIME_TIMEOUT_SECONDS="$CATALOG_RUNTIME_TIMEOUT" \
        ROUTING_CATALOG_CACHE_FILE="$CATALOG_CACHE_FILE" \
        ROUTING_CATALOG_CACHE_BIN="$CATALOG_CACHE_BIN" \
        ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS="$CATALOG_CACHE_MAX_AGE" \
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
