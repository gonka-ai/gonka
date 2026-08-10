#!/usr/bin/env bash

set -Eeuo pipefail

network=gonka-proxy-router-test-$$
image=gonka-proxy-router-test:$$
containers=(
    gonka-pr-proxy gonka-pr-probe
    gonka-pr-policy-a gonka-pr-policy-b
    gonka-pr-router-a gonka-pr-router-b gonka-pr-router-bad
    gonka-pr-edge-a gonka-pr-edge-b
)
tmpdir=$(mktemp -d)

cleanup() {
    docker rm -f "${containers[@]}" >/dev/null 2>&1 || true
    docker network rm "$network" >/dev/null 2>&1 || true
    docker image rm "$image" >/dev/null 2>&1 || true
    rm -rf "$tmpdir"
}
trap cleanup EXIT

fail() {
    echo "test-routing: $*" >&2
    exit 1
}

cat >"$tmpdir/upstream.py" <<'PY'
import http.server
import os
import threading
import urllib.parse

name = os.environ["NAME"]
serves = set(os.environ.get("SERVES", "").split())
generic_ready = os.environ.get("GENERIC_READY", "true") == "true"
data_enabled = os.environ.get("DATA_ENABLED", "true") == "true"
data_port = int(os.environ.get("DATA_PORT", "8080"))
post_count = 0
lock = threading.Lock()


class Data(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def reply(self, code, body):
        body = body.encode()
        self.send_response(code)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/readyz":
            return self.reply(200, "ready")
        if self.path.endswith("/headers"):
            return self.reply(
                200,
                f'{self.headers.get("X-Real-IP", "")}|'
                f'{self.headers.get("X-Forwarded-Proto", "")}',
            )
        self.reply(200, name)

    def do_POST(self):
        global post_count
        length = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(length)
        with lock:
            post_count += 1
            count = post_count
        self.reply(200, f"{name}:{count}")

    def log_message(self, *_):
        pass


class Admin(http.server.BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def reply(self, code, body):
        body = body.encode()
        self.send_response(code)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_GET(self):
        if self.path == "/count":
            with lock:
                return self.reply(200, str(post_count))
        url = urllib.parse.urlparse(self.path)
        if url.path != "/readyz":
            return self.reply(404, "not found")
        version = urllib.parse.parse_qs(url.query).get("version", [""])[0]
        ready = version in serves if version else generic_ready
        self.reply(200 if ready else 503, "ready" if ready else "not ready")

    def log_message(self, *_):
        pass


threading.Thread(
    target=lambda: http.server.ThreadingHTTPServer(("", 8404), Admin).serve_forever(),
    daemon=True,
).start()
if data_enabled:
    threading.Thread(
        target=lambda: http.server.ThreadingHTTPServer(("", data_port), Data).serve_forever(),
        daemon=True,
    ).start()
threading.Event().wait()
PY

cat >"$tmpdir/policy.conf" <<'EOF'
events {}
http {
    access_log off;
    server {
        listen 80 proxy_protocol;
        set_real_ip_from 0.0.0.0/0;
        real_ip_header proxy_protocol;
        location /devshard/ {
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_pass http://proxy-router:18081/;
        }
        location /tier-a/ {
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_pass http://proxy-router:18082/;
        }
        location / { return 200 "$hostname:$remote_addr\n"; }
    }
}
EOF

docker network create "$network" >/dev/null
docker build -q -t "$image" . >/dev/null

for spec in 'a:v4:true:true' 'b:v4 v5:true:true' 'bad:v4:true:false'; do
    name=${spec%%:*}
    rest=${spec#*:}
    serves=${rest%%:*}
    rest=${rest#*:}
    generic=${rest%%:*}
    enabled=${rest##*:}
    docker run -d --name "gonka-pr-router-$name" --network "$network" \
        --network-alias versiond-router \
        -e "NAME=$name" -e "SERVES=$serves" \
        -e "GENERIC_READY=$generic" -e "DATA_ENABLED=$enabled" \
        -v "$tmpdir/upstream.py:/app.py:ro" \
        python:3.12-alpine python /app.py >/dev/null
done

for name in a b; do
    docker run -d --name "gonka-pr-edge-$name" --hostname "edge-$name" \
        --network "$network" --network-alias edge-api-pool \
        -e "NAME=edge-$name" -e 'SERVES=' -e DATA_PORT=18080 \
        -v "$tmpdir/upstream.py:/app.py:ro" \
        python:3.12-alpine python /app.py >/dev/null
done

docker run -d --name gonka-pr-proxy --network "$network" \
    --network-alias proxy-router \
    -e 'VERSIOND_VERSIONS=v4 v5' -e 'VERSIOND_NON_HA_VERSIONS=' \
    "$image" >/dev/null
for name in a b; do
    docker run -d --name "gonka-pr-policy-$name" --hostname "policy-$name" \
        --network "$network" --network-alias proxy-policy \
        -v "$tmpdir/policy.conf:/etc/nginx/nginx.conf:ro" \
        nginx:1.28-alpine3.21 >/dev/null
done
docker run -d --name gonka-pr-probe --network "$network" \
    curlimages/curl:8.12.1 sleep 300 >/dev/null

probe() {
    docker exec gonka-pr-probe curl -fsS --connect-timeout 2 --max-time 5 "$@"
}

proxy_admin() {
    docker exec gonka-pr-proxy /bin/busybox wget -q -T 3 -O - \
        "http://127.0.0.1:8404$1"
}

for _ in $(seq 60); do
    if proxy_admin /readyz >/dev/null 2>&1 &&
        proxy_admin '/readyz?component=versiond' >/dev/null 2>&1 &&
        proxy_admin '/readyz?component=edge-api' >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done
proxy_admin /readyz >/dev/null || fail "public policy pool did not become ready"
proxy_admin '/readyz?version=v4' >/dev/null \
    || fail "top-level v4 readiness did not follow the router pool"
proxy_admin '/readyz?version=v5' >/dev/null \
    || fail "top-level v5 readiness did not follow the router pool"
unknown_response=$(docker exec gonka-pr-proxy /bin/busybox wget -S -O /dev/null \
    'http://127.0.0.1:8404/readyz?version=v9' 2>&1 || true)
[[ $unknown_response == *'404 Not Found'* ]] || fail \
    "unknown top-level version readiness did not return 404"

policy=$(probe http://proxy-router/)
case "$policy" in policy-a:* | policy-b:*) ;; *) fail "public TCP frontend returned '$policy'" ;; esac
selected=${policy%%:*}
docker stop "gonka-pr-$selected" >/dev/null

# The first request after a worker disappears reaches HAProxy before the active
# check necessarily updates. TCP redispatch must move this non-idempotent POST
# before any application bytes are accepted, and execute it exactly once.
failover_before=$(probe http://gonka-pr-router-b:8404/count)
failover_response=$(probe -X POST -H 'Content-Type: application/json' \
    -d request=first-after-stop \
    http://proxy-router/devshard/v5/sessions/policy-failover/chat)
failover_after=$(probe http://gonka-pr-router-b:8404/count)
[[ $failover_response == b:* && $((failover_after - failover_before)) == 1 ]] \
    || fail "first POST after policy loss was dropped or replayed"

for _ in $(seq 30); do
    next=$(probe http://proxy-router/ 2>/dev/null || true)
    if [[ -n $next && $next != "$policy" ]]; then
        break
    fi
    sleep 0.25
done
[[ -n ${next:-} && $next != "$policy" ]] || fail "public policy worker did not fail over"

# Exercise the real two-level request path. nginx performs the same trailing-
# slash rewrite as the production policy worker before returning to HAProxy's
# private frontend.
[[ $(probe http://proxy-router/devshard/v5/sessions/full-path/healthz) == b ]] \
    || fail "public devshard path did not reach the route-ready inner router"
full_before=$(probe http://gonka-pr-router-b:8404/count)
full_response=$(probe -X POST -H 'Content-Type: application/json' -d request=full \
    http://proxy-router/devshard/v5/sessions/full-path/chat)
full_after=$(probe http://gonka-pr-router-b:8404/count)
[[ $full_response == b:* && $((full_after - full_before)) == 1 ]] \
    || fail "public POST was not executed exactly once through both HAProxy layers"
probe_ip=$(docker inspect -f \
    "{{with index .NetworkSettings.Networks \"$network\"}}{{.IPAddress}}{{end}}" \
    gonka-pr-probe)
[[ $(probe http://proxy-router/devshard/v5/sessions/full-path/headers) == "$probe_ip|http" ]] \
    || fail "client IP or forwarded protocol was overwritten after nginx policy"
case $(probe http://proxy-router/tier-a/status) in
    edge-a | edge-b) ;;
    *) fail "public Tier A path did not reach the direct edge-api pool" ;;
esac

for _ in $(seq 8); do
    [[ $(probe http://proxy-router:18081/v5/sessions/test/healthz) == b ]] \
        || fail "v5 reached a router whose v5 pool is not ready"
done

before_a=$(probe http://gonka-pr-router-a:8404/count)
before_b=$(probe http://gonka-pr-router-b:8404/count)
requests=12
for i in $(seq "$requests"); do
    probe -X POST -H 'Content-Type: application/json' -d "{\"request\":$i}" \
        http://proxy-router:18081/v4/sessions/test/chat >/dev/null \
        || fail "POST $i did not fail over from an unavailable router data port"
done
after_a=$(probe http://gonka-pr-router-a:8404/count)
after_b=$(probe http://gonka-pr-router-b:8404/count)
executed=$((after_a - before_a + after_b - before_b))
[[ $executed == "$requests" ]] \
    || fail "$requests POSTs produced $executed upstream executions"

edge_seen=
for _ in $(seq 12); do
    edge_seen+=" $(probe http://proxy-router:18082/status)"
done
[[ $edge_seen == *edge-a* && $edge_seen == *edge-b* ]] \
    || fail "edge-api pool did not use both healthy replicas: $edge_seen"

echo "test-routing: ok"
