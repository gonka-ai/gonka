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
#   VERSIOND_VERSIONS         static bootstrap versions to health-check
#                             individually. Governance additions are learned
#                             from VERSIOND_ROUTING_CATALOG_URL at runtime.
#   VERSIOND_ROUTING_CATALOG_URL
#                             dapi's read-only approved-version endpoint.
#   GONKA_HA                  set by the HA compose overlay; keeps the
#                             Devshard-Ha request header on the HA backend even
#                             while all but one pool member are unavailable
#   VERSIOND_ROUTER_*         proxy policy, see README
#
# Pool membership is discovered from DNS, protocol names from governance, and
# health from active /readyz checks. Host and version additions need no reload.
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

TEMPLATE="${VERSIOND_ROUTER_TEMPLATE:-/etc/haproxy/haproxy.cfg.template}"
OUT="${VERSIOND_ROUTER_OUT:-/etc/haproxy/haproxy.cfg}"
MAP="${VERSIOND_ROUTER_NON_HA_MAP:-/etc/haproxy/non_ha.map}"
VERSIONS_MAP="${VERSIOND_ROUTER_VERSIONS_MAP:-/etc/haproxy/versions.map}"
SLOT_MAP="${OUT}.version-slots.map"
READY_RULES="${VERSIOND_ROUTER_READY_RULES:-${OUT}.ready.rules}"
POOL_TEMPLATE="${VERSIOND_ROUTER_POOL_TEMPLATE:-/etc/haproxy/pool-backend.cfg.template}"
# Overridable so `make test-render` can render without a local HAProxy.
HAPROXY_BIN="${HAPROXY_BIN:-haproxy}"

POOL_HOST="${VERSIOND_POOL_HOST:-versiond-pool}"
PORT="${VERSIOND_PORT:-8080}"
ADMIN_PORT="${VERSIOND_ROUTER_ADMIN_PORT:-8404}"
LEGACY_HOST="${VERSIOND_LEGACY_HOST:-$POOL_HOST}"
SLOTS="${VERSIOND_ROUTER_POOL_SLOTS:-64}"
MAXCONN="${VERSIOND_ROUTER_MAX_CONNECTIONS:-4096}"
# Same default the nginx router shipped; keep aligned with the outer API proxy.
MAX_BODY_BYTES="${VERSIOND_ROUTER_MAX_BODY_BYTES:-10485760}"
CONNECT_TIMEOUT="${VERSIOND_ROUTER_CONNECT_TIMEOUT_SECONDS:-2}"
STREAM_IDLE="${VERSIOND_ROUTER_STREAM_IDLE_SECONDS:-1200}"
# Used only after HTTP Upgrade/CONNECT. SSE uses STREAM_IDLE because it remains
# an ordinary HTX response.
TUNNEL_TIMEOUT="${VERSIOND_ROUTER_TUNNEL_TIMEOUT_SECONDS:-86400}"
VERSION_CAPACITY="${VERSIOND_ROUTER_VERSION_CAPACITY:-32}"
CATALOG_URL="${VERSIOND_ROUTING_CATALOG_URL:-}"
CATALOG_POLL="${VERSIOND_ROUTING_CATALOG_POLL_SECONDS:-5}"
CATALOG_FETCH_TIMEOUT="${VERSIOND_ROUTING_CATALOG_FETCH_TIMEOUT_SECONDS:-3}"
CATALOG_MAX_BYTES="${VERSIOND_ROUTING_CATALOG_MAX_BYTES:-1048576}"
CATALOG_RUNTIME_TIMEOUT="${VERSIOND_ROUTING_CATALOG_RUNTIME_TIMEOUT_SECONDS:-2}"
CATALOG_ACTIVATION_MIN_READY="${VERSIOND_ROUTING_ACTIVATION_MIN_READY:-1}"
CATALOG_CACHE_FILE=/var/lib/gonka-router/catalog.json
CATALOG_CACHE_MAX_AGE="${VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS:-86400}"
CATALOG_STATUS_FILE=/var/run/haproxy/catalog-status.json
CATALOG_CACHE_BIN="${ROUTING_CATALOG_CACHE_BIN:-/usr/local/lib/router-runtime/catalog-cache}"
FRONT_BIND_HOST="${VERSIOND_ROUTER_FRONT_BIND_HOST:-}"
METRICS_BIND_HOST="${VERSIOND_ROUTER_METRICS_BIND_HOST:-}"
DNS_RESOLVER="${HAPROXY_DNS_RESOLVER:-127.0.0.11:53}"

resolve_ipv4() {
    getent ahostsv4 "$1" | awk 'NR == 1 { print $1 }'
}

if [ -n "$FRONT_BIND_HOST" ]; then
    FRONT_BIND_ADDRESS=$(resolve_ipv4 "$FRONT_BIND_HOST")
    case "$FRONT_BIND_ADDRESS" in
        '' | *[!0-9.]*)
            echo "versiond-router: cannot resolve front bind host '$FRONT_BIND_HOST' to IPv4" >&2
            exit 1
            ;;
    esac
    ADMIN_LOOPBACK_BIND="    bind 127.0.0.1:$ADMIN_PORT"
else
    FRONT_BIND_ADDRESS=
    ADMIN_LOOPBACK_BIND="    # The wildcard admin bind also covers loopback."
fi

if [ -n "$METRICS_BIND_HOST" ]; then
    METRICS_BIND_ADDRESS=$(resolve_ipv4 "$METRICS_BIND_HOST")
    case "$METRICS_BIND_ADDRESS" in
        '' | *[!0-9.]*)
            echo "versiond-router: cannot resolve metrics bind host '$METRICS_BIND_HOST' to IPv4" >&2
            exit 1
            ;;
    esac
    METRICS_NETWORK_BIND="    bind $METRICS_BIND_ADDRESS:8405"
else
    METRICS_BIND_ADDRESS=127.0.0.1
    METRICS_NETWORK_BIND="    # No network metrics bind configured."
fi

# Booleans use one grammar, shared with devshardd's reading of GONKA_HA:
# 1/t/true/yes/on are on; empty/0/f/false/no/off are off. Anything else
# refuses to start.
# Parsed once, here — a boolean read as "non-empty" at each use site treats
# "false" as true, and one read as "anything unknown is off" treats a typo as
# off; each fails open in whichever direction its author was not thinking about.
bool_env() {
    raw=$(eval "printf '%s' \"\${$1:-}\"")
    # Trim edges only, matching Go's TrimSpace on the same variables: deleting
    # all whitespace would accept 't rue', which Go rejects.
    case "$(printf '%s' "$raw" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')" in
        1 | t | true | yes | on) printf '1' ;;
        '' | 0 | f | false | no | off) ;;
        *)
            echo "versiond-router: $1='$raw' is not a boolean; use 1/t/true/yes/on or empty/0/f/false/no/off" >&2
            exit 1
            ;;
    esac
}
HA_DEPLOYMENT=$(bool_env GONKA_HA)
ALLOW_COARSE_READINESS=$(bool_env VERSIOND_ROUTER_ALLOW_COARSE_READINESS)
CATALOG_ALLOW_REMOVALS=$(bool_env VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS)
RENDER_ONLY=$(bool_env VERSIOND_ROUTER_RENDER_ONLY)
TRUST_FORWARDED_HEADERS=$(bool_env VERSIOND_ROUTER_TRUST_FORWARDED_HEADERS)

if [ -n "$TRUST_FORWARDED_HEADERS" ]; then
    FORWARDED_PROTO_RULE='# Preserve X-Forwarded-Proto from the isolated trusted ingress.'
    REAL_IP_RULE='# Preserve X-Real-IP from the isolated trusted ingress.'
else
    FORWARDED_PROTO_RULE='http-request set-header X-Forwarded-Proto %[ssl_fc,iif(https,http)]'
    REAL_IP_RULE='http-request set-header X-Real-IP %[src]'
fi

# Hostnames are substituted into the config by sed, and sed is not inert to
# them: '&' expands to the matched placeholder and '|' terminates the
# expression. The failure is not a refused render — with init-addr none HAProxy
# accepts a mangled server address as an unresolvable name, so the config checks
# green and the pool is simply empty forever. Validate like every other input.
for name in "$POOL_HOST" "$LEGACY_HOST"; do
    case "$name" in
        '' | *[!A-Za-z0-9._-]*)
            echo "versiond-router: invalid hostname '$name'" >&2
            exit 1
            ;;
    esac
done

for value in "$SLOTS" "$MAXCONN" "$MAX_BODY_BYTES" "$CONNECT_TIMEOUT" "$STREAM_IDLE" "$TUNNEL_TIMEOUT" "$PORT" "$ADMIN_PORT" "$VERSION_CAPACITY" "$CATALOG_POLL" "$CATALOG_FETCH_TIMEOUT" "$CATALOG_MAX_BYTES" "$CATALOG_RUNTIME_TIMEOUT" "$CATALOG_ACTIVATION_MIN_READY" "$CATALOG_CACHE_MAX_AGE"; do
    case "$value" in
        ''|*[!0-9]*)
            echo "versiond-router: invalid numeric setting '$value'" >&2
            exit 1
            ;;
    esac
done
if [ "$VERSION_CAPACITY" -eq 0 ] || [ "$CATALOG_POLL" -eq 0 ] || \
    [ "$CATALOG_FETCH_TIMEOUT" -eq 0 ] || \
    [ "$CATALOG_MAX_BYTES" -eq 0 ] || \
    [ "$CATALOG_RUNTIME_TIMEOUT" -eq 0 ] || \
    [ "$CATALOG_ACTIVATION_MIN_READY" -eq 0 ] || \
    [ "$CATALOG_ACTIVATION_MIN_READY" -gt "$SLOTS" ] || \
    [ "$CATALOG_CACHE_MAX_AGE" -eq 0 ]; then
    echo "versiond-router: catalog capacity, body limit, and timing values must be positive" >&2
    exit 1
fi
if [ -n "$CATALOG_URL" ] && [ "$SLOTS" -gt "$ROUTER_RUNTIME_BATCH_SERVER_LIMIT" ]; then
    echo "versiond-router: VERSIOND_ROUTER_POOL_SLOTS exceeds the catalog Runtime API batch limit ($ROUTER_RUNTIME_BATCH_SERVER_LIMIT)" >&2
    exit 1
fi
case "$CATALOG_URL" in
    '' | http://* | https://*) ;;
    *)
        echo "versiond-router: VERSIOND_ROUTING_CATALOG_URL must use http or https" >&2
        exit 1
        ;;
esac
if ! printf '%s\n' "$DNS_RESOLVER" | grep -Eq '^(\[[0-9A-Fa-f:]+\]|[0-9A-Fa-f:.]+)(:[0-9]+)?$'; then
    echo "versiond-router: HAPROXY_DNS_RESOLVER must be a numeric IP with an optional port" >&2
    exit 1
fi

ha_header_for() {
    if [ -n "$HA_DEPLOYMENT" ]; then
        # The deployment declaration is a latch, not a live-host count. Keep the
        # storage guard enabled while siblings restart or drain: they can return.
        printf '%s' 'http-request set-header Devshard-Ha true'
    else
        # Recover the old router's fail-closed behaviour when an operator scales
        # the pool but forgets the HA overlay. Count the backend selected for this
        # request: per-version readiness can admit more hosts than the coarse pool.
        # This is a safety net rather than a replacement for GONKA_HA because a
        # partial outage can still reduce the selected backend to one server.
        printf '%s' "http-request set-header Devshard-Ha true if { nbsrv($1) gt 1 }"
    fi
}

# An HAProxy identifier for a name that is not one. The hash keeps names that
# sanitise to the same string apart; the readable part keeps it diagnosable.
safe_id() {
    printf '%s_%s' \
        "$(printf '%s' "$1" | tr -c 'A-Za-z0-9_' '_')" \
        "$(printf '%s' "$1" | sha256sum | cut -c1-8)"
}

# Startup maps are rendered as files, so they can preserve the router's original
# path-segment contract. Dynamically reconciled names use the narrower shared
# grammar: they are also interpolated into HAProxy Runtime API commands.
validate_static_version() {
    case "$1" in
        *[/?#%]* | *[[:space:]]* | *\\* | *\"* | *\'* | . | ..)
            echo "versiond-router: version '$1' cannot be routed: a path segment" >&2
            echo "  cannot carry / ? # % or whitespace literally, so the request path" >&2
            echo "  would not match the configured version name" >&2
            exit 1
            ;;
    esac
}

# Return an HAProxy-safe backend name without losing the version's identity.
backend_name() {
    prefix=$1
    version=$2
    case "$version" in
        [A-Za-z0-9]*)
            case "$version" in
                *[!A-Za-z0-9._-]*) printf '%s_%s' "$prefix" "$(safe_id "$version")" ;;
                *) printf '%s_%s' "$prefix" "$version" ;;
            esac
            ;;
        *) printf '%s_%s' "$prefix" "$(safe_id "$version")" ;;
    esac
}

# One shared fragment renders both HA pools and single-owner legacy backends, so
# their hashing, retry, and path-rewrite policies cannot drift apart.
DEFAULT_RETRY_ON='retry-on conn-failure empty-response 502'
VERSIONLESS_RETRY_ON="$DEFAULT_RETRY_ON 404"
render_backend() {
    sed \
        -e "s|\${BACKEND_NAME}|$1|g" \
        -e "s|\${READY_CHECK_SEND}|$2|g" \
        -e "s|\${ROUTE_CHECK_SEND}|$3|g" \
        -e "s|\${BACKEND_HOST}|$4|g" \
        -e "s|\${VERSIOND_PORT}|$PORT|g" \
        -e "s|\${BACKEND_SLOTS}|$5|g" \
        -e "s|\${REQUEST_HA_HEADER}|$6|g" \
        -e "s|\${RESPONSE_BACKEND}|$7|g" \
        -e "s|\${RETRY_ON}|$9|g" \
        -e "s|\${SERVER_STATE}|$8|g" \
        "$POOL_TEMPLATE"
}

: > "$MAP"
: > "$VERSIONS_MAP"
: > "$SLOT_MAP"
POOL_BACKENDS_FILE="$(mktemp)"
STATIC_VERSIONS_FILE="$(mktemp)"
LEGACY_VERSIONS_FILE="$(mktemp)"
CACHED_VERSIONS_FILE="$(mktemp)"
CACHED_DYNAMIC_VERSIONS_FILE="$(mktemp)"
trap 'rm -f "$POOL_BACKENDS_FILE" "$STATIC_VERSIONS_FILE" "$LEGACY_VERSIONS_FILE" "$CACHED_VERSIONS_FILE" "$CACHED_DYNAMIC_VERSIONS_FILE"' EXIT

printf '%s\n' "${VERSIOND_VERSIONS:-}" | tr ',;[:space:]' '\n' > "$STATIC_VERSIONS_FILE"
printf '%s\n' "${VERSIOND_NON_HA_VERSIONS:-}" | tr ',;[:space:]' '\n' > "$LEGACY_VERSIONS_FILE"
: > "$CACHED_VERSIONS_FILE"
if [ -n "$CATALOG_URL" ] && [ -f "$CATALOG_CACHE_FILE" ]; then
    cache_status=0
    "$CATALOG_CACHE_BIN" read "$CATALOG_CACHE_FILE" "$CATALOG_CACHE_MAX_AGE" \
        > "$CACHED_VERSIONS_FILE" || cache_status=$?
    case "$cache_status" in
        0) echo "versiond-router: loaded the fresh routing catalog cache" >&2 ;;
        2)
            echo "versiond-router: loaded stale accepted routes; waiting for a fresh catalog" >&2
            ;;
        *)
            echo "versiond-router: ignoring invalid routing catalog cache" >&2
            : > "$CACHED_VERSIONS_FILE"
            ;;
    esac
fi

render_backend versiond_ha_pool \
    'http-check send meth GET uri /readyz' \
    'http-check send meth GET uri /healthz' \
    "$POOL_HOST" "$SLOTS" "$(ha_header_for versiond_ha_pool)" \
    versiond_ha_pool '' "$VERSIONLESS_RETRY_ON" > "$POOL_BACKENDS_FILE"
declare_ha_version() {
    version=$1
    [ -n "$version" ] || return 0
    # A version name is whatever governance approved. Three things are derived
    # from it and each has its own rules:
    #
    #   the map key       must equal the path segment HAProxy sees, so it is the
    #                     name exactly as written;
    #   the check query   is percent-encoded, because '+' in a query decodes to a
    #                     space on the versiond side and the check would ask
    #                     about a version that does not exist;
    #   the backend name  is an HAProxy identifier, so a name that is not already
    #                     one gets a hash appended to keep it unique.
    #
    # What cannot be handled is a name that cannot appear literally in a path
    # segment: those characters would have to be encoded by the client, and then
    # the segment no longer equals the name. Refuse rather than route on a guess.
    validate_static_version "$version"
    # The comparison is forced to strings: awk would otherwise treat 1, 01 and
    # 1.0 as the same version, and 1e2 as 100.
    if awk -v v="$version" '$1 "" == v "" { found = 1 } END { exit !found }' "$VERSIONS_MAP"; then
        echo "versiond-router: version '$version' is declared twice" >&2
        exit 1
    fi
    backend=$(backend_name versiond_pool "$version")
    echo "$version $backend" >> "$VERSIONS_MAP"
    encoded_version=$(router_urlencode "$version")
    render_backend "$backend" \
        "http-check send meth GET uri /readyz?version=$encoded_version" \
        "http-check send meth GET uri /$encoded_version/healthz" \
        "$POOL_HOST" "$SLOTS" "$(ha_header_for "$backend")" "$backend" '' \
        "$DEFAULT_RETRY_ON" \
        >> "$POOL_BACKENDS_FILE"
}

while IFS= read -r version; do
    [ -n "$version" ] || continue
    declare_ha_version "$version"
done < "$STATIC_VERSIONS_FILE"

# Restore only names that still require dynamic capacity. Rendering cached names
# as new static backends would free their old slots on every restart and make the
# configured capacity grow without bound. Static and legacy declarations remain
# authoritative when an operator intentionally changes a version's ownership.
: > "$CACHED_DYNAMIC_VERSIONS_FILE"
while IFS= read -r version; do
    [ -n "$version" ] || continue
    router_name_in_file "$LEGACY_VERSIONS_FILE" "$version" && continue
    if awk -v v="$version" '$1 "" == v "" { found = 1 } END { exit !found }' "$VERSIONS_MAP"; then
        continue
    fi
    printf '%s\n' "$version" >> "$CACHED_DYNAMIC_VERSIONS_FILE"
done < "$CACHED_VERSIONS_FILE"
cached_dynamic_count=$(wc -l < "$CACHED_DYNAMIC_VERSIONS_FILE")
if [ "$cached_dynamic_count" -gt "$VERSION_CAPACITY" ]; then
    echo "versiond-router: accepted catalog cache needs $cached_dynamic_count dynamic slots," >&2
    echo "  but VERSIOND_ROUTER_VERSION_CAPACITY is $VERSION_CAPACITY" >&2
    exit 1
fi

# Reserve inert backends for governance names that appear after this container
# starts. The reconciler assigns a check URI, enables the slot, and only then
# publishes the version -> backend map entry. Unused capacity performs no DNS
# resolution, health checks, or traffic.
index=1
while [ "$index" -le "$VERSION_CAPACITY" ]; do
    backend="versiond_dynamic_$index"
    cached_version=$(sed -n "${index}p" "$CACHED_DYNAMIC_VERSIONS_FILE")
    server_state=disabled
    if [ -n "$cached_version" ]; then
        encoded_version=$(router_urlencode "$cached_version")
        printf '%s %s\n' "$backend" "$encoded_version" >> "$SLOT_MAP"
        printf '%s %s\n' "$cached_version" "$backend" >> "$VERSIONS_MAP"
        server_state=
    else
        printf '%s %s\n' "$backend" __unassigned__ >> "$SLOT_MAP"
    fi
    render_backend "$backend" \
        "http-check send meth GET uri-lf /readyz?version=%[be_name,map($SLOT_MAP)]" \
        "http-check send meth GET uri-lf /%[be_name,map($SLOT_MAP)]/healthz" \
        "$POOL_HOST" "$SLOTS" "$(ha_header_for "$backend")" "$backend" "$server_state" \
        "$DEFAULT_RETRY_ON" \
        >> "$POOL_BACKENDS_FILE"
    index=$((index + 1))
done

# Legacy versions share one SQLite owner, but not one health result. Rendering a
# backend per version lets a missing archive take only that version out of
# rotation; a generic /readyz failure must not hide the owner's healthy children.
# Trailing newline is load-bearing: without it `read` swallows the last field.
while IFS= read -r version; do
    [ -n "$version" ] || continue
    validate_static_version "$version"
    if awk -v v="$version" '$1 "" == v "" { found = 1 } END { exit !found }' "$MAP"; then
        echo "versiond-router: legacy version '$version' is declared twice" >&2
        exit 1
    fi
    if awk -v v="$version" '$1 "" == v "" { found = 1 } END { exit !found }' "$VERSIONS_MAP"; then
        echo "versiond-router: version '$version' is declared as both HA and legacy" >&2
        exit 1
    fi
    backend=$(backend_name versiond_legacy "$version")
    echo "$version $backend" >> "$MAP"
    encoded_version=$(router_urlencode "$version")
    render_backend "$backend" \
        "http-check send meth GET uri /readyz?version=$encoded_version" \
        "http-check send meth GET uri /$encoded_version/healthz" \
        "$LEGACY_HOST" 1 'http-request del-header Devshard-Ha' \
        versiond_legacy '' "$DEFAULT_RETRY_ON" \
        >> "$POOL_BACKENDS_FILE"
done < "$LEGACY_VERSIONS_FILE"

# Versions pinned to the legacy host exist because exactly one host owns their
# SQLite data dirs. Defaulting that owner to the pool alias would resolve "the
# single owner" to whichever pool member DNS answers with — the split state the
# pin exists to prevent, reached through an omission.
if [ -s "$MAP" ] && [ -z "${VERSIOND_LEGACY_HOST:-}" ]; then
    echo "versiond-router: VERSIOND_NON_HA_VERSIONS pins versions to a legacy host," >&2
    echo "  but VERSIOND_LEGACY_HOST is not set. The owner of pre-HA SQLite data" >&2
    echo "  is one specific host and must be named explicitly." >&2
    exit 1
fi

# Render one static readiness rule per immutable startup route. HAProxy's
# nbsrv() argument is a configuration-time backend name, so bootstrap and
# legacy backends use an explicit table. Dynamic slots are deliberately skipped:
# the reconciler can reuse them for another name, so their readiness falls
# through to the live versions map and its currently assigned backend below.
: > "$READY_RULES"
if [ -n "$CATALOG_URL" ]; then
    CATALOG_STATUS_SERVER_STATE=disabled
    CATALOG_SERVING_STATUS_SERVER_STATE=disabled
    cat >> "$READY_RULES" <<'EOF'
    http-request return status 200 content-type text/plain string "catalog ready\n" if { path /readyz } { url_param(component) -m str catalog } { nbsrv(router_catalog_status) gt 0 }
    http-request return status 503 content-type text/plain string "catalog not ready\n" if { path /readyz } { url_param(component) -m str catalog }
    http-request return status 200 content-type text/plain string "accepted routes ready\n" if { path /readyz } { url_param(component) -m str serving } { nbsrv(router_catalog_serving_status) gt 0 }
    http-request return status 503 content-type text/plain string "accepted routes not ready\n" if { path /readyz } { url_param(component) -m str serving }
EOF
else
    CATALOG_STATUS_SERVER_STATE=
    CATALOG_SERVING_STATUS_SERVER_STATE=
    cat >> "$READY_RULES" <<'EOF'
    http-request return status 200 content-type text/plain string "catalog disabled\n" if { path /readyz } { url_param(component) -m str catalog }
    http-request return status 200 content-type text/plain string "catalog disabled\n" if { path /readyz } { url_param(component) -m str serving }
EOF
fi
static_ready_acl_defined=0
append_static_ready_acl() {
    printf '%s\n' "    acl routed_version_ready nbsrv($1) gt 0" >> "$READY_RULES"
    static_ready_acl_defined=1
}
render_ready_rules() {
    source_map=$1
    while read -r version backend; do
        [ -n "$version" ] || continue
        case "$backend" in
            versiond_dynamic_*) continue ;;
        esac
        append_static_ready_acl "$backend"
        encoded_version=$(router_urlencode "$version")
        printf '%s\n' \
            "    http-request return status 200 content-type text/plain string \"ready\\n\" if { path /readyz } { query -m str version=$encoded_version } { nbsrv($backend) gt 0 }" \
            "    http-request return status 503 content-type text/plain string \"not ready\\n\" if { path /readyz } { query -m str version=$encoded_version }" \
            >> "$READY_RULES"
    done < "$source_map"
}
render_ready_rules "$MAP"
render_ready_rules "$VERSIONS_MAP"
if [ -n "$CATALOG_URL" ]; then
    while read -r backend _; do
        [ -n "$backend" ] || continue
        # A staged slot can become healthy before its batch is published. The
        # nested lookup proves that the live version map currently points back
        # to this slot, so readiness cannot expose an unpublished assignment.
        printf '%s\n' \
            "    http-request return status 200 content-type text/plain string \"ready\\n\" if { path /readyz } !{ query -m found } { str($backend),map_str($SLOT_MAP),url_dec(0),map_str($VERSIONS_MAP) -m str $backend } { nbsrv($backend) gt 0 }" \
            >> "$READY_RULES"
    done < "$SLOT_MAP"
fi
if [ -n "$CATALOG_URL" ] || [ -s "$MAP" ] || [ -s "$VERSIONS_MAP" ]; then
    if [ "$static_ready_acl_defined" -eq 1 ]; then
        cat >> "$READY_RULES" <<'EOF'
    http-request return status 200 content-type text/plain string "ready\n" if { path /readyz } !{ query -m found } routed_version_ready
EOF
    fi
    cat >> "$READY_RULES" <<'EOF'
    http-request return status 503 content-type text/plain string "not ready\n" if { path /readyz } !{ query -m found }
EOF
else
    cat >> "$READY_RULES" <<'EOF'
    http-request return status 200 content-type text/plain string "ready\n" if { path /readyz } !{ query -m found } { nbsrv(versiond_ha_pool) gt 0 }
    http-request return status 503 content-type text/plain string "not ready\n" if { path /readyz } !{ query -m found }
EOF
fi

# An HA deployment must declare its versions. The host-level check the coarse
# mode falls back to answers "can this host serve anything", so a host whose v5
# child went unready keeps receiving v5 traffic as long as v4 is healthy —
# hash-dependent failures the per-version pools exist to prevent. The override
# names exactly what it accepts; test stacks with dynamic version names use it.
if [ -n "$HA_DEPLOYMENT" ] && [ ! -s "$VERSIONS_MAP" ] \
    && [ -z "$CATALOG_URL" ] \
    && [ -z "$ALLOW_COARSE_READINESS" ]; then
    echo "versiond-router: GONKA_HA is set without bootstrap versions or a catalog." >&2
    echo "  Without a version source every version shares the host-level readiness" >&2
    echo "  check, and a host with one unready version keeps receiving that version's" >&2
    echo "  traffic. Declare the versions this deployment serves, or set" >&2
    echo "  VERSIOND_ROUTER_ALLOW_COARSE_READINESS=1 to accept that behaviour." >&2
    exit 1
fi

# Once either version source is active, an unknown version must not quietly
# fall back to the host-level pool: that is the coarse check again, and it would
# route to a host that may not run this version at all. Fail it here, where the
# answer can name the fix, rather than as a 404 from whichever host the hash
# happened to pick.
if [ -n "$CATALOG_URL" ]; then
    UNDECLARED_GUARD="http-request return status 503 hdr X-Devshard-Error undeclared_version hdr X-Devshard-Router-Error undeclared_version content-type \"text/plain\" lf-string \"version %[var(txn.ver)] is not present in the governance routing catalog\" if { var(txn.ver) -m reg . } !versionless_request !{ var(txn.ver),map_str($MAP) -m found } !{ var(txn.ver),map_str($VERSIONS_MAP) -m found }"
    DYNAMIC_READY_GUARD="http-request return status 503 content-type \"text/plain\" string \"version-is-not-declared-or-ready\" if { path /readyz } { url_param(version) -m found } !{ var(txn.ready_ver),map_str($MAP) -m found } !{ var(txn.ready_ver),map_str($VERSIONS_MAP) -m found }"
elif [ -s "$VERSIONS_MAP" ]; then
    UNDECLARED_GUARD="http-request return status 503 hdr X-Devshard-Error undeclared_version hdr X-Devshard-Router-Error undeclared_version content-type \"text/plain\" lf-string \"version %[var(txn.ver)] is not declared in VERSIOND_VERSIONS on this router\" if { var(txn.ver) -m reg . } !versionless_request !{ var(txn.ver),map_str($MAP) -m found } !{ var(txn.ver),map_str($VERSIONS_MAP) -m found }"
    DYNAMIC_READY_GUARD="http-request return status 503 content-type \"text/plain\" string \"version-is-not-declared-or-ready\" if { path /readyz } { url_param(version) -m found } !{ var(txn.ready_ver),map_str($MAP) -m found } !{ var(txn.ready_ver),map_str($VERSIONS_MAP) -m found }"
else
    UNDECLARED_GUARD="# No versions declared: every version uses the host-level pool."
    DYNAMIC_READY_GUARD="# Dynamic version readiness is disabled."
fi

sed \
    -e "/\${POOL_BACKENDS}/{
        r $POOL_BACKENDS_FILE
        d
    }" \
    -e "s|\${NON_HA_MAP}|$MAP|g" \
    -e "s|\${VERSIONS_MAP}|$VERSIONS_MAP|g" \
    -e "s|\${UNDECLARED_VERSION_GUARD}|$UNDECLARED_GUARD|g" \
    -e "s|\${DYNAMIC_READY_GUARD}|$DYNAMIC_READY_GUARD|g" \
    -e "s|\${MAX_CONNECTIONS}|$MAXCONN|g" \
    -e "s|\${ADMIN_PORT}|$ADMIN_PORT|g" \
    -e "s|\${FRONT_BIND_ADDRESS}|$FRONT_BIND_ADDRESS|g" \
    -e "s|\${ADMIN_LOOPBACK_BIND}|$ADMIN_LOOPBACK_BIND|g" \
    -e "s|\${METRICS_NETWORK_BIND}|$METRICS_NETWORK_BIND|g" \
    -e "s|\${CATALOG_STATUS_SERVER_STATE}|$CATALOG_STATUS_SERVER_STATE|g" \
    -e "s|\${CATALOG_SERVING_STATUS_SERVER_STATE}|$CATALOG_SERVING_STATUS_SERVER_STATE|g" \
    -e "s|\${MAX_BODY_BYTES}|$MAX_BODY_BYTES|g" \
    -e "s|\${CONNECT_TIMEOUT_SECONDS}|$CONNECT_TIMEOUT|g" \
    -e "s|\${STREAM_IDLE_SECONDS}|$STREAM_IDLE|g" \
    -e "s|\${TUNNEL_TIMEOUT_SECONDS}|$TUNNEL_TIMEOUT|g" \
    -e "s|\${DNS_RESOLVER}|$DNS_RESOLVER|g" \
    -e "s|\${FORWARDED_PROTO_RULE}|$FORWARDED_PROTO_RULE|g" \
    -e "s|\${REAL_IP_RULE}|$REAL_IP_RULE|g" \
    -e "/\${ROUTER_READY_RULES}/{
        r $READY_RULES
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
        ROUTING_CATALOG_COMPONENT=versiond-router \
        ROUTING_CATALOG_URL="$CATALOG_URL" \
        ROUTING_CATALOG_RUNTIME_SOCKET=/var/run/haproxy/reconciler.sock \
        ROUTING_CATALOG_PROJECTION_MAP="$VERSIONS_MAP" \
        ROUTING_CATALOG_SLOT_MAP="$SLOT_MAP" \
        ROUTING_CATALOG_BACKEND_PREFIX=versiond_dynamic_ \
        ROUTING_CATALOG_BACKEND_CAPACITY="$VERSION_CAPACITY" \
        ROUTING_CATALOG_SERVER_PREFIX=versiond \
        ROUTING_CATALOG_SERVER_CAPACITY="$SLOTS" \
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
        ROUTING_CATALOG_STATUS_BACKEND=router_catalog_status \
        ROUTING_CATALOG_STATUS_SERVER=catalog \
        ROUTING_CATALOG_SERVING_STATUS_BACKEND=router_catalog_serving_status \
        ROUTING_CATALOG_SERVING_STATUS_SERVER=serving \
            /usr/local/lib/router-runtime/catalog-reconciler || status=$?
        echo "versiond-router: catalog reconciler exited with status $status; restarting" >&2
        sleep 1
    done
}

if [ -n "$CATALOG_URL" ]; then
    run_catalog_reconciler &
fi

exec "$HAPROXY_BIN" -W -db -f "$OUT"
