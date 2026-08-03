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
#   GONKA_HA                  set by the HA compose overlay; keeps the
#                             Devshard-Ha request header on the HA backend even
#                             while all but one pool member are unavailable
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
# Same default the nginx router shipped; keep aligned with the outer API proxy.
MAX_BODY_BYTES="${VERSIOND_ROUTER_MAX_BODY_BYTES:-10485760}"
CONNECT_TIMEOUT="${VERSIOND_ROUTER_CONNECT_TIMEOUT_SECONDS:-2}"
STREAM_IDLE="${VERSIOND_ROUTER_STREAM_IDLE_SECONDS:-1200}"
# Used only after HTTP Upgrade/CONNECT. SSE uses STREAM_IDLE because it remains
# an ordinary HTX response.
TUNNEL_TIMEOUT="${VERSIOND_ROUTER_TUNNEL_TIMEOUT_SECONDS:-86400}"

# Booleans use one grammar, shared with devshardd's reading of GONKA_HA:
# 1/true/yes are on, empty/0/false/no are off, anything else refuses to start.
# Parsed once, here — a boolean read as "non-empty" at each use site treats
# "false" as true, and one read as "anything unknown is off" treats a typo as
# off; each fails open in whichever direction its author was not thinking about.
bool_env() {
    raw=$(eval "printf '%s' \"\${$1:-}\"")
    # Trim edges only, matching Go's TrimSpace on the same variables: deleting
    # all whitespace would accept 't rue', which Go rejects.
    case "$(printf '%s' "$raw" | sed 's/^[[:space:]]*//; s/[[:space:]]*$//' | tr '[:upper:]' '[:lower:]')" in
        1 | true | yes) printf '1' ;;
        '' | 0 | false | no) ;;
        *)
            echo "versiond-router: $1='$raw' is not a boolean; use 1/true/yes or 0/false/no" >&2
            exit 1
            ;;
    esac
}
HA_DEPLOYMENT=$(bool_env GONKA_HA)
ALLOW_COARSE_READINESS=$(bool_env VERSIOND_ROUTER_ALLOW_COARSE_READINESS)
RENDER_ONLY=$(bool_env VERSIOND_ROUTER_RENDER_ONLY)

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

for value in "$SLOTS" "$MAXCONN" "$MAX_BODY_BYTES" "$CONNECT_TIMEOUT" "$STREAM_IDLE" "$TUNNEL_TIMEOUT" "$PORT"; do
    case "$value" in
        ''|*[!0-9]*)
            echo "versiond-router: invalid numeric setting '$value'" >&2
            exit 1
            ;;
    esac
done

if [ -n "$HA_DEPLOYMENT" ]; then
    # The deployment declaration is a latch, not a live-host count. Keep the
    # storage guard enabled while siblings restart or drain: they can return.
    HA_HEADER='http-request set-header Devshard-Ha true'
else
    # Recover the old router's fail-closed behaviour when an operator scales the
    # pool but forgets the HA overlay. nbsrv counts usable servers, so this is a
    # safety net rather than a replacement for GONKA_HA: a partial outage can
    # make the count one, while an explicitly declared HA deployment stays HA.
    # The frontend has already stripped any client-supplied value.
    HA_HEADER='http-request set-header Devshard-Ha true if { nbsrv(versiond_ha_pool) gt 1 }'
fi

# Percent-encode everything that is not unreserved, so the check asks about the
# name governance approved rather than about whatever the query parser made of it.
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

# An HAProxy identifier for a name that is not one. The hash keeps names that
# sanitise to the same string apart; the readable part keeps it diagnosable.
safe_id() {
    printf '%s_%s' \
        "$(printf '%s' "$1" | tr -c 'A-Za-z0-9_' '_')" \
        "$(printf '%s' "$1" | sha256sum | cut -c1-8)"
}

# Refuse names that cannot be represented as the literal path segment used for
# routing. This applies equally to HA and legacy declarations.
validate_version() {
    case "$1" in
        *[/?#%]* | *[[:space:]]* | *\\* | *\"* | *\'* | . | ..)
            echo "versiond-router: version '$1' cannot be routed: a path segment" >&2
            echo "  cannot carry / ? # % or whitespace literally, so the request path" >&2
            echo "  would not match the name governance approved" >&2
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
render_backend() {
    sed \
        -e "s|\${BACKEND_NAME}|$1|g" \
        -e "s|\${READYZ_URI}|$2|g" \
        -e "s|\${BACKEND_HOST}|$3|g" \
        -e "s|\${VERSIOND_PORT}|$PORT|g" \
        -e "s|\${BACKEND_SLOTS}|$4|g" \
        -e "s|\${REQUEST_HA_HEADER}|$5|g" \
        -e "s|\${RESPONSE_BACKEND}|$6|g" \
        "$POOL_TEMPLATE"
}

: > "$MAP"
: > "$VERSIONS_MAP"
POOL_BACKENDS_FILE="$(mktemp)"
trap 'rm -f "$POOL_BACKENDS_FILE"' EXIT
render_backend versiond_ha_pool /readyz "$POOL_HOST" "$SLOTS" \
    "$HA_HEADER" versiond_ha_pool > "$POOL_BACKENDS_FILE"
printf '%s\n' "${VERSIOND_VERSIONS:-}" | tr ',;' '  ' | tr -s ' ' '\n' | while read -r version; do
    [ -n "$version" ] || continue
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
    validate_version "$version"
    # The comparison is forced to strings: awk would otherwise treat 1, 01 and
    # 1.0 as the same version, and 1e2 as 100.
    if awk -v v="$version" '$1 "" == v "" { found = 1 } END { exit !found }' "$VERSIONS_MAP"; then
        echo "versiond-router: version '$version' is declared twice" >&2
        exit 1
    fi
    backend=$(backend_name versiond_pool "$version")
    echo "$version $backend" >> "$VERSIONS_MAP"
    render_backend "$backend" "/readyz?version=$(urlencode "$version")" \
        "$POOL_HOST" "$SLOTS" "$HA_HEADER" "$backend" >> "$POOL_BACKENDS_FILE"
done

# Legacy versions share one SQLite owner, but not one health result. Rendering a
# backend per version lets a missing archive take only that version out of
# rotation; a generic /readyz failure must not hide the owner's healthy children.
# Trailing newline is load-bearing: without it `read` swallows the last field.
printf '%s\n' "${VERSIOND_NON_HA_VERSIONS:-}" | tr ',;' '  ' | tr -s ' ' '\n' | while read -r version; do
    [ -n "$version" ] || continue
    validate_version "$version"
    if awk -v v="$version" '$1 "" == v "" { found = 1 } END { exit !found }' "$MAP"; then
        echo "versiond-router: legacy version '$version' is declared twice" >&2
        exit 1
    fi
    backend=$(backend_name versiond_legacy "$version")
    echo "$version $backend" >> "$MAP"
    render_backend "$backend" "/readyz?version=$(urlencode "$version")" \
        "$LEGACY_HOST" 1 'http-request del-header Devshard-Ha' \
        versiond_legacy >> "$POOL_BACKENDS_FILE"
done

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

# An HA deployment must declare its versions. The host-level check the coarse
# mode falls back to answers "can this host serve anything", so a host whose v5
# child went unready keeps receiving v5 traffic as long as v4 is healthy —
# hash-dependent failures the per-version pools exist to prevent. The override
# names exactly what it accepts; test stacks with dynamic version names use it.
if [ -n "$HA_DEPLOYMENT" ] && [ ! -s "$VERSIONS_MAP" ] \
    && [ -z "$ALLOW_COARSE_READINESS" ]; then
    echo "versiond-router: GONKA_HA is set but VERSIOND_VERSIONS is empty." >&2
    echo "  Without declared versions every version shares the host-level readiness" >&2
    echo "  check, and a host with one unready version keeps receiving that version's" >&2
    echo "  traffic. Declare the versions this deployment serves, or set" >&2
    echo "  VERSIOND_ROUTER_ALLOW_COARSE_READINESS=1 to accept that behaviour." >&2
    exit 1
fi

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
    -e "s|\${MAX_CONNECTIONS}|$MAXCONN|g" \
    -e "s|\${MAX_BODY_BYTES}|$MAX_BODY_BYTES|g" \
    -e "s|\${CONNECT_TIMEOUT_SECONDS}|$CONNECT_TIMEOUT|g" \
    -e "s|\${STREAM_IDLE_SECONDS}|$STREAM_IDLE|g" \
    -e "s|\${TUNNEL_TIMEOUT_SECONDS}|$TUNNEL_TIMEOUT|g" \
    "$TEMPLATE" > "$OUT"

"$HAPROXY_BIN" -c -f "$OUT" >/dev/null

if [ -n "$RENDER_ONLY" ]; then
    exit 0
fi

exec "$HAPROXY_BIN" -W -db -f "$OUT"
