#!/usr/bin/env bash
# The HA peer-RPC backend is proto h2 on 8081, but its health check is
# HTTP/1.1 GET /readyz on the router admin port. A live show stat must say
# DOWN until that check passes, then UP/L7OK, and a 503 must take it off UP.
# The pre-fix line, with no check, stays "no check" beside it.
set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
haproxy_image=${HAPROXY_IMAGE:-haproxy:3.2-alpine}
tmpdir=$(mktemp -d)
suffix=$$
net=h2-upstream-check-$suffix
proxy=
router=
trap cleanup EXIT

fail() {
    echo "h2-upstream-check_test: $*" >&2
    exit 1
}

cleanup() {
    local failed=$?
    if [[ -n $proxy ]]; then
        docker logs "$proxy" >"$tmpdir/proxy.log" 2>&1 || true
        docker rm -f "$proxy" >/dev/null 2>&1 || true
    fi
    if [[ -n $router ]]; then
        docker logs "$router" >"$tmpdir/router.log" 2>&1 || true
        docker rm -f "$router" >/dev/null 2>&1 || true
    fi
    docker network rm "$net" >/dev/null 2>&1 || true
    if [[ $failed -ne 0 ]]; then
        echo "--- rendered backend ---" >&2
        cat "$tmpdir/backend.cfg" >&2 || true
        echo "--- show stat ---" >&2
        cat "$tmpdir/stat.csv" >&2 || true
        echo "--- proxy ---" >&2
        cat "$tmpdir/proxy.log" >&2 || true
        echo "--- router ---" >&2
        cat "$tmpdir/router.log" >&2 || true
    fi
    rm -rf "$tmpdir"
    exit "$failed"
}

command -v docker >/dev/null || fail "docker is required"

mkdir -p "$tmpdir/ready"
printf '503\n' >"$tmpdir/ready/code"
cat >"$tmpdir/ready.py" <<'PY'
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from pathlib import Path

CODE = Path("/ready/code")


class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        if self.path.split("?", 1)[0] != "/readyz":
            self.send_error(404)
            return
        code = int(CODE.read_text().strip())
        body = b"ready\n" if code == 200 else b"not ready\n"
        self.send_response(code)
        self.send_header("content-type", "text/plain")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return


ThreadingHTTPServer(("0.0.0.0", 8404), Handler).serve_forever()
PY

cd "$script_dir"
HAPROXY_BIN=true \
PROXY_ROUTER_RENDER_ONLY=1 \
PROXY_ROUTER_TEMPLATE=./haproxy.cfg.template \
PROXY_ROUTER_BACKEND_TEMPLATE=./versiond-backend.cfg.template \
PROXY_ROUTER_OUT="$tmpdir/ha.cfg" \
PROXY_ROUTER_VERSION_MAP="$tmpdir/ha.map" \
VERSIOND_ROUTER_POOL_HOST=versiond-router-fleet \
VERSIOND_ROUTER_FLEET_CAPACITY=1 \
    ./entrypoint.sh

awk '
    /^backend rpc_h2_upstream$/ { inside = 1; print; next }
    inside && /^(frontend|backend) / { exit }
    inside { print }
' "$tmpdir/ha.cfg" | sed 's/versiond-router-fleet/router/g' >"$tmpdir/backend.cfg"
grep -q 'http-check connect port 8404 proto h1' "$tmpdir/backend.cfg" \
    || fail "rendered peer RPC check does not use HTTP/1.1 on the admin port"
grep -q 'http-check send meth GET uri /readyz hdr Host router' "$tmpdir/backend.cfg" \
    || fail "rendered peer RPC check is not GET /readyz"
grep -q 'http-check expect status 200' "$tmpdir/backend.cfg" \
    || fail "rendered peer RPC check does not expect 200"
grep -q 'server-template router 1 router:8081 proto h2 check inter 1s fall 1 rise 2 check-proto h1 resolvers docker init-addr none init-state fully-down hash-key addr' \
    "$tmpdir/backend.cfg" \
    || fail "rendered peer RPC server line is not a checked proto h2 dial"

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
    default_backend rpc_h2_upstream

$(cat "$tmpdir/backend.cfg")

backend rpc_h2_unchecked
    server router router:8081 proto h2 resolvers docker init-addr none
EOF

docker network create "$net" >/dev/null
router=h2-upstream-check-router-$suffix
docker run -d --name "$router" --network "$net" --network-alias router \
    -v "$tmpdir/ready.py:/ready.py:ro" \
    -v "$tmpdir/ready:/ready" \
    python:3.12-alpine python /ready.py >/dev/null
for _ in $(seq 1 50); do
    if docker exec "$router" python -c \
        'import socket; socket.create_connection(("127.0.0.1", 8404), 1).close()' \
        >/dev/null 2>&1; then
        break
    fi
    sleep 0.2
done
docker exec "$router" python -c \
    'import socket; socket.create_connection(("127.0.0.1", 8404), 1).close()' \
    >/dev/null 2>&1 || fail "mock router did not accept /readyz"

proxy=h2-upstream-check-proxy-$suffix
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

# /readyz is 503, so rise 2 cannot admit the slot. init-state fully-down
# keeps it DOWN, and the first failed check fills check_status. The old
# line has no check and stays out of that state machine.
down_ready=
for _ in $(seq 1 40); do
    checked_status=$(stat_field rpc_h2_upstream router1 status || true)
    checked_check=$(stat_field rpc_h2_upstream router1 check_status || true)
    unchecked_status=$(stat_field rpc_h2_unchecked router status || true)
    if [[ $checked_status == UP* ]]; then
        fail "peer RPC was admitted while /readyz returned 503"
    fi
    if [[ $checked_status == DOWN* && -n $checked_check && $unchecked_status == "no check" ]]; then
        down_ready=1
        break
    fi
    sleep 0.5
done
[[ -n $down_ready ]] || fail "checked peer RPC did not stay DOWN with a check while /readyz returned 503 (status=${checked_status:-} check=${checked_check:-} unchecked=${unchecked_status:-})"

printf '200\n' >"$tmpdir/ready/code"
up_ready=
for _ in $(seq 1 40); do
    checked_status=$(stat_field rpc_h2_upstream router1 status || true)
    checked_check=$(stat_field rpc_h2_upstream router1 check_status || true)
    checked_code=$(stat_field rpc_h2_upstream router1 check_code || true)
    unchecked_status=$(stat_field rpc_h2_unchecked router status || true)
    if [[ $checked_status == UP* && $checked_check == L7OK && $checked_code == 200 && $unchecked_status == "no check" ]]; then
        up_ready=1
        break
    fi
    sleep 0.5
done
[[ -n $up_ready ]] || fail "peer RPC did not become UP/L7OK/200 after /readyz returned 200 (status=${checked_status:-} check=${checked_check:-} code=${checked_code:-} unchecked=${unchecked_status:-})"

printf '503\n' >"$tmpdir/ready/code"
fell=
for _ in $(seq 1 40); do
    checked_status=$(stat_field rpc_h2_upstream router1 status || true)
    unchecked_status=$(stat_field rpc_h2_unchecked router status || true)
    if [[ $checked_status == DOWN* && $unchecked_status == "no check" ]]; then
        fell=1
        break
    fi
    sleep 0.5
done
[[ -n $fell ]] || fail "fall 1 left peer RPC UP after /readyz returned 503 (status=${checked_status:-} unchecked=${unchecked_status:-})"

echo "h2-upstream-check_test: ok"
