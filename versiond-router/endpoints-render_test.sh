#!/usr/bin/env bash

# Renders the router with an explicit versiond endpoint list, with the legacy
# VERSIOND_HOSTS list, and with DNS discovery, then checks the resulting server
# lines and validates each configuration with the real HAProxy image.

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
haproxy_image=${HAPROXY_IMAGE:-haproxy:3.2-alpine}
tmpdir=$(mktemp -d)
chmod 755 "$tmpdir"
trap 'rm -rf "$tmpdir"' EXIT

fail() {
    echo "endpoints-render_test: $*" >&2
    exit 1
}

render() {
    local name=$1
    shift
    env "$@" \
        HAPROXY_BIN=true \
        VERSIOND_ROUTER_RENDER_ONLY=1 \
        VERSIOND_ROUTER_TEMPLATE="$script_dir/haproxy.cfg.template" \
        VERSIOND_ROUTER_POOL_TEMPLATE="$script_dir/pool-backend.cfg.template" \
        VERSIOND_ROUTER_OUT="$tmpdir/$name.cfg" \
        VERSIOND_ROUTER_NON_HA_MAP="$tmpdir/$name.map" \
        VERSIOND_ROUTER_VERSIONS_MAP="$tmpdir/$name.versions.map" \
        "$script_dir/entrypoint.sh"
}

pool_servers() {
    grep -E '^ *server(-template)? ' "$tmpdir/$1.cfg" | grep -v '127.0.0.1:1 '
}

cat >"$tmpdir/endpoints.json" <<'EOF'
[
  {"id": "versiond", "host": "versiond", "port": 8080},
  {"id": "versiond-b", "host": "10.20.0.12", "port": 18080},
  {"id": "versiond-c", "host": "10.20.0.13"}
]
EOF

# Explicit endpoints: one server per entry, names versiond1..N for the catalog
# reconciler, the default port applied, IPv4 literals without a resolver, and
# the legacy owner selected by endpoint id.
render endpoints \
    GONKA_HA=true VERSIOND_VERSIONS="v4 v5" VERSIOND_NON_HA_VERSIONS=v1 \
    VERSIOND_LEGACY_HOST=versiond \
    VERSIOND_POOL_ENDPOINTS_FILE="$tmpdir/endpoints.json" \
    VERSIOND_ROUTING_CATALOG_URL=http://oracle:9100/versions \
    VERSIOND_ROUTING_ACTIVATION_MIN_READY=2 \
    VERSIOND_ROUTER_VERSION_CAPACITY=2 2>"$tmpdir/endpoints.err"
grep -q 'explicit list of 3 endpoint(s)' "$tmpdir/endpoints.err" || \
    fail "endpoint mode did not report its membership"
! pool_servers endpoints | grep -q 'server-template' || \
    fail "endpoint mode still renders a DNS server-template"
pool_servers endpoints | grep -q '^ *server versiond1 versiond:8080 .*resolvers docker init-addr none' || \
    fail "a named endpoint must keep re-resolving through the Docker resolver"
pool_servers endpoints | grep -q '^ *server versiond2 10.20.0.12:18080 check inter 1s fall 1 rise 2 init-state fully-down hash-key addr' || \
    fail "an explicit port was not rendered"
! pool_servers endpoints | grep '10.20.0.12' | grep -q resolvers || \
    fail "an IPv4 literal must not carry resolver options"
pool_servers endpoints | grep -q '^ *server versiond3 10.20.0.13:8080 ' || \
    fail "VERSIOND_PORT was not applied as the default endpoint port"
# Every HA backend (coarse, two static versions, two dynamic slots) lists all
# three members; the legacy backend lists only the owner.
[[ $(pool_servers endpoints | grep -c '^ *server versiond2 ') -eq 5 ]] || \
    fail "expected five HA backends with the second endpoint"
[[ $(grep -c '^backend versiond_legacy_v1' "$tmpdir/endpoints.cfg") -eq 1 ]] || \
    fail "legacy backend is missing"
sed -n '/^backend versiond_legacy_v1/,/^backend /p' "$tmpdir/endpoints.cfg" | \
    grep -q '^ *server versiond1 versiond:8080 ' || \
    fail "legacy backend must resolve VERSIOND_LEGACY_HOST through the endpoint list"
! sed -n '/^backend versiond_legacy_v1/,/^backend /p' "$tmpdir/endpoints.cfg" | \
    grep -q '10.20.0' || fail "legacy backend must list only the owner"
grep -q '^ *server versiond1 .* disabled$' "$tmpdir/endpoints.cfg" || \
    fail "dynamic slots must start disabled in endpoint mode"

# A legacy owner that is not an endpoint id keeps the single-host DNS contract.
render legacy-dns \
    GONKA_HA=true VERSIOND_VERSIONS=v4 VERSIOND_NON_HA_VERSIONS=v1 \
    VERSIOND_LEGACY_HOST=versiond-old \
    VERSIOND_POOL_ENDPOINTS_FILE="$tmpdir/endpoints.json" 2>/dev/null
sed -n '/^backend versiond_legacy_v1/,/^backend /p' "$tmpdir/legacy-dns.cfg" | \
    grep -q '^ *server-template versiond 1 versiond-old:8080 ' || \
    fail "an unlisted legacy owner must fall back to its DNS name"

# Legacy VERSIOND_HOSTS renders the same explicit list, with optional ports.
render hosts \
    GONKA_HA=true VERSIOND_VERSIONS=v4 \
    VERSIOND_HOSTS="versiond versiond2:18080" 2>"$tmpdir/hosts.err"
grep -q 'VERSIOND_HOSTS is a legacy setting' "$tmpdir/hosts.err" || \
    fail "legacy VERSIOND_HOSTS must be reported"
pool_servers hosts | grep -q '^ *server versiond1 versiond:8080 ' || \
    fail "VERSIOND_HOSTS entry without a port did not get VERSIOND_PORT"
pool_servers hosts | grep -q '^ *server versiond2 versiond2:18080 ' || \
    fail "VERSIOND_HOSTS host:port entry was not rendered"

# An endpoint file wins over VERSIOND_HOSTS.
render precedence \
    GONKA_HA=true VERSIOND_VERSIONS=v4 \
    VERSIOND_HOSTS="ignored-host" \
    VERSIOND_POOL_ENDPOINTS_FILE="$tmpdir/endpoints.json" 2>/dev/null
! grep -q 'ignored-host' "$tmpdir/precedence.cfg" || \
    fail "VERSIOND_HOSTS must be ignored when an endpoint file is set"

# DNS discovery is unchanged when neither explicit form is set.
render dns GONKA_HA=true VERSIOND_VERSIONS=v4 2>/dev/null
pool_servers dns | grep -q '^ *server-template versiond 64 versiond-pool:8080 .*resolvers docker init-addr none' || \
    fail "DNS mode no longer renders the pool server-template"
! pool_servers dns | grep -q '^ *server versiond1 ' || \
    fail "DNS mode must not render explicit servers"

# Rejected inputs.
expect_rejected() {
    local name=$1 message=$2 body=$3
    printf '%s\n' "$body" >"$tmpdir/$name.json"
    if render "$name" GONKA_HA=true VERSIOND_VERSIONS=v4 \
        VERSIOND_POOL_ENDPOINTS_FILE="$tmpdir/$name.json" 2>"$tmpdir/$name.err"; then
        fail "$name: an invalid endpoint file was accepted"
    fi
    grep -q -- "$message" "$tmpdir/$name.err" || \
        fail "$name: unexpected rejection: $(cat "$tmpdir/$name.err")"
}
expect_rejected empty 'non-empty JSON array' '[]'
expect_rejected object 'cannot parse' '{"id": "a", "host": "1.1.1.1"}'
expect_rejected duplicate 'declared twice' \
    '[{"id":"a","host":"1.1.1.1"},{"id":"a","host":"1.1.1.2"}]'
expect_rejected port 'out-of-range port' '[{"id":"a","host":"1.1.1.1","port":70000}]'
expect_rejected host 'invalid host' '[{"id":"a","host":"bad host"}]'
expect_rejected id 'invalid id' '[{"id":"a b","host":"1.1.1.1"}]'
if render missing GONKA_HA=true VERSIOND_VERSIONS=v4 \
    VERSIOND_POOL_ENDPOINTS_FILE="$tmpdir/does-not-exist.json" 2>"$tmpdir/missing.err"; then
    fail "a missing endpoint file was accepted"
fi
grep -q 'is not readable' "$tmpdir/missing.err" || \
    fail "missing endpoint file: unexpected rejection"

# The real HAProxy must accept every rendered shape.
for name in endpoints legacy-dns hosts dns; do
    docker run --rm --user 0 -v "$tmpdir:$tmpdir:ro" "$haproxy_image" \
        haproxy -c -f "$tmpdir/$name.cfg" >/dev/null 2>"$tmpdir/$name.check" || \
        fail "$name: HAProxy rejected the rendered configuration: $(cat "$tmpdir/$name.check")"
done

echo "endpoints-render_test: ok"
