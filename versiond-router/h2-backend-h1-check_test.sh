#!/usr/bin/env bash
# Default versiond backends dial proto h2 and keep a health check on
# HTTP/1.1. An HTTP/1.1 /readyz takes that server to UP. A proto h2 line
# with no check stays "no check": drain does not match it, and a health-down
# command cannot bring it back.
set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
haproxy_image=${HAPROXY_IMAGE:-haproxy:3.2-alpine}
tmpdir=$(mktemp -d)
suffix=$$
net=h2-backend-h1-check-$suffix
proxy=
upstream=
trap cleanup EXIT

fail() {
    echo "h2-backend-h1-check_test: $*" >&2
    exit 1
}

cleanup() {
    local failed=$?
    if [[ -n $proxy ]]; then
        docker logs "$proxy" >"$tmpdir/proxy.log" 2>&1 || true
        docker rm -f "$proxy" >/dev/null 2>&1 || true
    fi
    if [[ -n $upstream ]]; then
        docker logs "$upstream" >"$tmpdir/upstream.log" 2>&1 || true
        docker rm -f "$upstream" >/dev/null 2>&1 || true
    fi
    docker network rm "$net" >/dev/null 2>&1 || true
    if [[ $failed -ne 0 ]]; then
        echo "--- rendered server ---" >&2
        cat "$tmpdir/server-line" >&2 || true
        echo "--- show stat ---" >&2
        cat "$tmpdir/stat.csv" >&2 || true
        echo "--- proxy ---" >&2
        cat "$tmpdir/proxy.log" >&2 || true
        echo "--- upstream ---" >&2
        cat "$tmpdir/upstream.log" >&2 || true
    fi
    rm -rf "$tmpdir"
    exit "$failed"
}

command -v docker >/dev/null || fail "docker is required"

cd "$script_dir"
HAPROXY_BIN=true \
VERSIOND_ROUTER_RENDER_ONLY=1 \
VERSIOND_ROUTER_TEMPLATE=./haproxy.cfg.template \
VERSIOND_ROUTER_POOL_TEMPLATE=./pool-backend.cfg.template \
VERSIOND_ROUTER_OUT="$tmpdir/rendered.cfg" \
VERSIOND_ROUTER_NON_HA_MAP="$tmpdir/non_ha.map" \
VERSIOND_ROUTER_VERSIONS_MAP="$tmpdir/versions.map" \
    ./entrypoint.sh >/dev/null

server_line=$(grep -m1 '^[[:space:]]*server-template versiond ' "$tmpdir/rendered.cfg" || true)
printf '%s\n' "$server_line" >"$tmpdir/server-line"
grep -q ' check inter 1s fall 1 rise 2 ' "$tmpdir/server-line" \
    || fail "rendered server line has no health check"
grep -q 'proto h2 check-proto h1' "$tmpdir/server-line" \
    || fail "rendered server line does not keep proto h2 with an HTTP/1.1 check"
options=${server_line#*versiond-pool:8080 }
options=${options#"${options%%[![:space:]]*}"}
[[ $options == *'check-proto h1'* ]] || fail "rendered options lost check-proto h1"

awk '
    $1 == "backend" && $2 == "versiond_ha_pool" { inside = 1; next }
    inside && $1 == "backend" { exit }
    inside && ($1 == "option" && $2 == "httpchk" || $1 == "http-check") { print }
' "$tmpdir/rendered.cfg" >"$tmpdir/checks.cfg"
grep -q 'uri /readyz$' "$tmpdir/checks.cfg" || fail "coarse check is not GET /readyz"
grep -q 'expect status 200,404' "$tmpdir/checks.cfg" || fail "coarse check dropped the 404 contract"
grep -q 'uri /healthz$' "$tmpdir/checks.cfg" || fail "coarse check dropped /healthz"

mkdir -p "$tmpdir/ready"
printf '503\n' >"$tmpdir/ready/code"
cat >"$tmpdir/ready.py" <<'PY'
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

CODE = Path("/ready/code")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path == "/healthz":
            code = 200
        elif path == "/readyz":
            code = int(CODE.read_text().strip())
        else:
            self.send_error(404)
            return
        body = b"ready\n" if code == 200 else b"not ready\n"
        self.send_response(code)
        self.send_header("content-type", "text/plain")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return


ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
PY

cat >"$tmpdir/haproxy.cfg" <<EOF
global
    stats socket /var/run/haproxy/reconciler.sock level admin mode 600
    stats timeout 30s

defaults
    mode http
    timeout connect 2s
    timeout client 5s
    timeout server 5s
    timeout check 2s

resolvers docker
    nameserver dns 127.0.0.11:53
    accepted_payload_size 8192
    resolve_retries 3
    timeout resolve 1s
    timeout retry 1s
    hold valid 1s
    hold other 1s
    hold nx 1s
    hold refused 1s
    hold timeout 1s

frontend fe
    bind :8080
    default_backend versiond_ha_pool

backend versiond_ha_pool
$(cat "$tmpdir/checks.cfg")
    server versiond1 upstream:8080 ${options}

backend versiond_unchecked
    server versiond1 upstream:8080 proto h2 resolvers docker init-addr none
EOF

docker network create "$net" >/dev/null
upstream=h2-backend-h1-check-upstream-$suffix
docker run -d --name "$upstream" --network "$net" --network-alias upstream \
    -v "$tmpdir/ready.py:/ready.py:ro" \
    -v "$tmpdir/ready:/ready" \
    python:3.12-alpine python /ready.py >/dev/null
for _ in $(seq 1 50); do
    if docker exec "$upstream" python -c \
        'import socket; socket.create_connection(("127.0.0.1", 8080), 1).close()' \
        >/dev/null 2>&1; then
        break
    fi
    sleep 0.2
done
docker exec "$upstream" python -c \
    'import socket; socket.create_connection(("127.0.0.1", 8080), 1).close()' \
    >/dev/null 2>&1 || fail "mock versiond did not accept connections"

proxy=h2-backend-h1-check-proxy-$suffix
docker run -d --name "$proxy" --network "$net" --user root \
    -v "$tmpdir/haproxy.cfg:/tmp/haproxy.cfg:ro" \
    "$haproxy_image" \
    /bin/sh -c 'apk add --no-cache socat >/dev/null && mkdir -p /var/run/haproxy && exec haproxy -W -db -f /tmp/haproxy.cfg' \
    >/dev/null

runtime_show() {
    printf '%s\n' "$1" | docker exec -i "$proxy" socat -t 1 stdio /var/run/haproxy/reconciler.sock
}

for _ in $(seq 1 300); do
    if runtime_show 'show info' 2>/dev/null | grep -q '^Name: HAProxy$'; then
        break
    fi
    sleep 0.2
done
runtime_show 'show info' 2>/dev/null | grep -q '^Name: HAProxy$' \
    || fail "haproxy did not open the runtime socket"

stat_field() {
    local backend=$1 server=$2 field=$3
    runtime_show 'show stat' | tee "$tmpdir/stat.csv" | awk -F, \
        -v backend="$backend" -v server="$server" -v field="$field" '
        NR == 1 {
            for (i = 1; i <= NF; i++) {
                name = $i
                sub(/^#[[:space:]]*/, "", name)
                column[name] = i
            }
            next
        }
        $(column["pxname"]) == backend && $(column["svname"]) == server {
            print $(column[field])
            exit
        }
    '
}

# /readyz is 503. The checked server stays DOWN. The line with no check is
# "no check" and would already be in the hash ring.
down_ready=
for _ in $(seq 1 40); do
    h1_status=$(stat_field versiond_ha_pool versiond1 status || true)
    h1_check=$(stat_field versiond_ha_pool versiond1 check_status || true)
    unchecked_status=$(stat_field versiond_unchecked versiond1 status || true)
    if [[ $h1_status == "no check" ]]; then
        fail "the checked versiond server was reported no check"
    fi
    if [[ $h1_status == UP* ]]; then
        fail "versiond was admitted while /readyz returned 503"
    fi
    if [[ $h1_status == DOWN* && -n $h1_check && $unchecked_status == "no check" ]]; then
        down_ready=1
        break
    fi
    sleep 0.5
done
[[ -n $down_ready ]] || fail "HTTP/1.1 check did not stay DOWN while /readyz returned 503 (h1=${h1_status:-}/${h1_check:-} unchecked=${unchecked_status:-})"

printf '200\n' >"$tmpdir/ready/code"
up_ready=
for _ in $(seq 1 40); do
    h1_status=$(stat_field versiond_ha_pool versiond1 status || true)
    h1_check=$(stat_field versiond_ha_pool versiond1 check_status || true)
    h1_code=$(stat_field versiond_ha_pool versiond1 check_code || true)
    unchecked_status=$(stat_field versiond_unchecked versiond1 status || true)
    if [[ $h1_status == "no check" ]]; then
        fail "the checked versiond server was reported no check"
    fi
    if [[ $h1_status == UP* && $h1_check == L7OK && $h1_code == 200 && $unchecked_status == "no check" ]]; then
        up_ready=1
        break
    fi
    sleep 0.5
done
[[ -n $up_ready ]] || fail "HTTP/1.1 check did not become UP/L7OK/200 (h1=${h1_status:-}/${h1_check:-}/${h1_code:-} unchecked=${unchecked_status:-})"

echo "h2-backend-h1-check_test: ok"
