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
#   VERSIOND_VERSIONS         versions to health-check individually, so a host
#                             missing one of them leaves that version's pool and
#                             keeps serving the rest. Empty = every version uses
#                             the coarse host-level check.
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
VERSIONS_MAP="${VERSIOND_ROUTER_VERSIONS_MAP:-/etc/haproxy/versions.map}"
POOL_TEMPLATE="${VERSIOND_ROUTER_POOL_TEMPLATE:-/etc/haproxy/pool-backend.cfg.template}"
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

# One backend per version the router was told about, plus the host-level pool
# every unlisted version falls back to. All of them are the same fragment, so the
# routing policy cannot drift between them; only the name and the readiness
# question differ.
render_pool_backend() {
    sed \
        -e "s|\${BACKEND_NAME}|$1|g" \
        -e "s|\${READYZ_URI}|$2|g" \
        -e "s|\${VERSIOND_POOL_HOST}|$POOL_HOST|g" \
        -e "s|\${VERSIOND_PORT}|$PORT|g" \
        -e "s|\${POOL_SLOTS}|$SLOTS|g" \
        -e "s|\${DEVSHARD_HA_HEADER}|$HA_HEADER|g" \
        "$POOL_TEMPLATE"
}

: > "$VERSIONS_MAP"
POOL_BACKENDS_FILE="$(mktemp)"
trap 'rm -f "$POOL_BACKENDS_FILE"' EXIT
render_pool_backend versiond_ha_pool /readyz > "$POOL_BACKENDS_FILE"
printf '%s\n' "${VERSIOND_VERSIONS:-}" | tr ',;' '  ' | tr -s ' ' '\n' | while read -r version; do
    [ -n "$version" ] || continue
    # One grammar, used verbatim in three places that would each mangle a name
    # differently: the HAProxy backend identifier, the query string of the health
    # check, and the map key matched against the path segment. Deriving a safe
    # identifier instead would be lossy — 'v5+cuda' and 'v5-cuda' collapse to the
    # same thing — and '+' in a query string decodes to a space on the versiond
    # side, so the check would ask about a version that does not exist and the
    # host would stay down forever. Refuse instead of guessing.
    case "$version" in
        [A-Za-z0-9]*) ;;
        *)
            echo "versiond-router: version '$version' must start with a letter or digit" >&2
            exit 1
            ;;
    esac
    case "$version" in
        *[!A-Za-z0-9._-]*)
            echo "versiond-router: version '$version' may only contain A-Za-z0-9._-" >&2
            echo "  a name outside that grammar cannot be carried unambiguously in a" >&2
            echo "  backend name and a health-check query at the same time" >&2
            exit 1
            ;;
    esac
    backend="versiond_pool_$version"
    if grep -q " $backend\$" "$VERSIONS_MAP" 2>/dev/null; then
        echo "versiond-router: version '$version' is declared twice" >&2
        exit 1
    fi
    echo "$version $backend" >> "$VERSIONS_MAP"
    render_pool_backend "$backend" "/readyz?version=$version" >> "$POOL_BACKENDS_FILE"
done

# Once any version is declared, a version that is not declared must not quietly
# fall back to the host-level pool: that is the coarse check again, and it would
# route to a host that may not run this version at all. Fail it here, where the
# answer can name the fix, rather than as a 404 from whichever host the hash
# happened to pick.
if [ -s "$VERSIONS_MAP" ]; then
    UNDECLARED_GUARD="http-request return status 503 content-type \"text/plain\" lf-string \"version %[var(txn.ver)] is not declared in VERSIOND_VERSIONS on this router\" if { var(txn.ver) -m reg . } !versionless_request !{ var(txn.ver),map_str($MAP) -m found } !{ var(txn.ver),map_str($VERSIONS_MAP) -m found }"
else
    UNDECLARED_GUARD="# No versions declared: every version uses the host-level pool."
fi

sed \
    -e "/\${POOL_BACKENDS}/{
        r $POOL_BACKENDS_FILE
        d
    }" \
    -e "s|\${NON_HA_MAP}|$MAP|g" \
    -e "s|\${VERSIONS_MAP}|$VERSIONS_MAP|g" \
    -e "s|\${UNDECLARED_VERSION_GUARD}|$UNDECLARED_GUARD|g" \
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
