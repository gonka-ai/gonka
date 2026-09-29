#!/bin/sh
set -eu

cd "$(dirname "$0")"
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

render() {
    HAPROXY_BIN=true PROXY_ROUTER_RENDER_ONLY=1 \
        PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
        PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
        PROXY_ROUTER_OUT="$tmpdir/proxy.cfg" \
        PROXY_ROUTER_VERSION_MAP="$tmpdir/versions.map" \
        PROXY_ROUTER_POLICY_BIND_HOST=localhost \
        PROXY_POLICY_POOL_HOST=127.0.0.1 \
        PROXY_POLICY_POOL_SLOTS=1 \
        VERSIOND_VERSIONS=v6 \
        ./entrypoint.sh
}

unset PROXY_ROUTER_PUBLIC_BIND_ADDRESS
render
grep -q '^    bind 0.0.0.0:80$' "$tmpdir/proxy.cfg"
grep -q '^    bind 0.0.0.0:443$' "$tmpdir/proxy.cfg"

export PROXY_ROUTER_PUBLIC_BIND_ADDRESS=192.0.2.7
render
grep -q '^    bind 192.0.2.7:80$' "$tmpdir/proxy.cfg"
grep -q '^    bind 192.0.2.7:443$' "$tmpdir/proxy.cfg"
grep -q '^    bind 127.0.0.1:18081$' "$tmpdir/proxy.cfg"
grep -q '^    bind 127.0.0.1:8404$' "$tmpdir/proxy.cfg"
grep -q 'server-template policy 1 127.0.0.1:80 .*send-proxy-v2' "$tmpdir/proxy.cfg"

for invalid in localhost 1.2.3 256.1.2.3 1.2.3.4:80 '1.2.3.4 ssl' '::1' \
    '1.2.3.4|injected' '1.2.3.4
2.3.4.5'; do
    export PROXY_ROUTER_PUBLIC_BIND_ADDRESS="$invalid"
    if render >"$tmpdir/invalid.log" 2>&1; then
        echo "public-bind: accepted invalid address: $invalid" >&2
        exit 1
    fi
    grep -q 'PROXY_ROUTER_PUBLIC_BIND_ADDRESS must be an IPv4 address' \
        "$tmpdir/invalid.log"
done

echo 'public-bind: ok'
