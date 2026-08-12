#!/usr/bin/env bash

set -Eeuo pipefail

network=gonka-proxy-router-test-$$
image=gonka-proxy-router-test:$$
state=gonka-proxy-router-state-$$
containers=(
    gonka-pr-proxy gonka-pr-probe gonka-pr-catalog
    gonka-pr-policy-a gonka-pr-policy-b
    gonka-pr-router-a gonka-pr-router-b gonka-pr-router-bad
    gonka-pr-router-migration
    gonka-pr-edge-a gonka-pr-edge-b
)
tmpdir=$(mktemp -d)

cleanup() {
    status=$?
    if (( status != 0 )); then
        docker logs gonka-pr-proxy >&2 || true
    fi
    docker rm -f "${containers[@]}" >/dev/null 2>&1 || true
    docker network rm "$network" >/dev/null 2>&1 || true
    docker volume rm "$state" >/dev/null 2>&1 || true
    docker image rm "$image" >/dev/null 2>&1 || true
    rm -rf "$tmpdir"
	return "$status"
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
missing_versionless = os.environ.get("MISSING_VERSIONLESS", "false") == "true"
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
        if missing_versionless and self.path.startswith("/sessions/retry-404-"):
            return self.reply(404, "route not owned here")
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

mkdir "$tmpdir/catalog"
printf '%s\n' '{"schema":1,"initialized":true,"revision":1,"versions":[{"name":"v4"},{"name":"v5"}]}' \
    >"$tmpdir/catalog/versions.json"
cat >"$tmpdir/catalog.py" <<'PY'
import http.server


class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/versions":
            self.send_error(404)
            return
        with open("/data/versions.json", "rb") as source:
            body = source.read()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()
PY

cat >"$tmpdir/policy.conf" <<'EOF'
events {}
http {
    access_log off;
    resolver 127.0.0.11 valid=1s ipv6=off;
    upstream versiond_distributor {
        zone versiond_distributor 64k;
        server proxy-router:18081 resolve;
    }
    upstream edge_distributor {
        zone edge_distributor 64k;
        server proxy-router:18082 resolve;
    }
    server {
        listen 80 proxy_protocol;
        set_real_ip_from 0.0.0.0/0;
        real_ip_header proxy_protocol;
        location /devshard/ {
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_pass http://versiond_distributor/;
        }
        location /tier-a/ {
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_pass http://edge_distributor/;
        }
        location / { return 200 "$hostname:$remote_addr\n"; }
    }
}
EOF

docker network create "$network" >/dev/null
docker volume create "$state" >/dev/null
docker build -q -t "$image" -f Dockerfile .. >/dev/null
[[ $(docker image inspect -f \
    '{{index .Config.Labels "ai.gonka.proxy-policy-contract"}}' "$image") == 1 ]] \
    || fail "public proxy image has no stable policy contract label"
cache_now=$(date +%s)
docker run --rm --user 0:0 -e "CACHE_NOW=$cache_now" \
    -v "$state:/state" --entrypoint sh "$image" -c \
    'printf "{\"schema\":1,\"fetched_at_unix\":%s,\"versions\":[\"v4\",\"v5\"]}\n" "$CACHE_NOW" > /state/catalog.json; chown -R haproxy:haproxy /state'

docker run -d --name gonka-pr-catalog --network "$network" \
    --network-alias routing-catalog \
    -v "$tmpdir/catalog:/data:ro" -v "$tmpdir/catalog.py:/app.py:ro" \
    python:3.12-alpine python /app.py >/dev/null

for spec in 'a:v4:true:true' 'b:v4 v5 v9:true:true' 'bad:v4:true:false'; do
    name=${spec%%:*}
    rest=${spec#*:}
    serves=${rest%%:*}
    rest=${rest#*:}
    generic=${rest%%:*}
    enabled=${rest##*:}
    docker run -d --name "gonka-pr-router-$name" --network "$network" \
        --network-alias versiond-router-fleet \
        -e "NAME=$name" -e "SERVES=$serves" \
        -e "GENERIC_READY=$generic" -e "DATA_ENABLED=$enabled" \
        -e "MISSING_VERSIONLESS=$([[ $name == a ]] && echo true || echo false)" \
        -v "$tmpdir/upstream.py:/app.py:ro" \
        python:3.12-alpine python /app.py >/dev/null
done

# The reversible upgrade keeps a healthy singleton for v4 nginx rollback. Its
# historical DNS name must never enter the steady-state fleet pool, otherwise
# it can mask a route-dead fleet at the cutover commit boundary.
docker run -d --name gonka-pr-router-migration --network "$network" \
    --network-alias versiond-router \
    -e NAME=migration -e 'SERVES=v4 v5' \
    -e GENERIC_READY=true -e DATA_ENABLED=true \
    -v "$tmpdir/upstream.py:/app.py:ro" \
    python:3.12-alpine python /app.py >/dev/null

for name in a b; do
    docker run -d --name "gonka-pr-edge-$name" --hostname "edge-$name" \
        --network "$network" --network-alias edge-api-pool \
        -e "NAME=edge-$name" -e 'SERVES=' -e DATA_PORT=18080 \
        -v "$tmpdir/upstream.py:/app.py:ro" \
        python:3.12-alpine python /app.py >/dev/null
done

docker run -d --name gonka-pr-proxy --network "$network" \
    --network-alias proxy-router \
    -v "$state:/var/lib/gonka-router" \
    -e 'VERSIOND_VERSIONS=v4 v5' -e 'VERSIOND_NON_HA_VERSIONS=' \
    -e VERSIOND_ROUTING_CATALOG_URL=http://routing-catalog:8080/versions \
    -e VERSIOND_ROUTING_CATALOG_POLL_SECONDS=1 \
    -e PROXY_ROUTER_VERSION_CAPACITY=1 \
    -e PROXY_ROUTER_METRICS_BIND_HOST=proxy-router \
    -e PROXY_ROUTER_CATALOG_BIND_HOST=proxy-router \
    -e PROXY_ROUTER_CATALOG_UPSTREAM_HOST=routing-catalog \
    -e PROXY_ROUTER_CATALOG_UPSTREAM_PORT=8080 \
    "$image" >/dev/null
for _ in $(seq 40); do
    docker exec gonka-pr-proxy test -s /var/lib/gonka-router/catalog-v2.json \
        >/dev/null 2>&1 && break
    sleep 0.25
done
docker exec gonka-pr-proxy test -s /var/lib/gonka-router/catalog-v2.json \
    || fail "top distributor did not migrate the legacy catalog cache"
docker exec gonka-pr-proxy test -s /var/lib/gonka-router/catalog.json \
    || fail "top distributor removed the rollback catalog cache"
start_policy() {
    local name=$1
    docker run -d --name "gonka-pr-policy-$name" --hostname "policy-$name" \
        --network "$network" --network-alias proxy-policy \
        -v "$tmpdir/policy.conf:/etc/nginx/nginx.conf:ro" \
        nginx:1.28-alpine3.21 >/dev/null
}
for name in a b; do
    start_policy "$name"
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

proxy_backend_addr_up() {
    local backend=$1 address=$2
    docker exec gonka-pr-proxy sh -c \
        "printf 'show stat\\n' | socat - UNIX-CONNECT:/var/run/haproxy/haproxy.sock" \
        | awk -F, -v backend="$backend" -v address="$address" '
            $1 == backend && $18 ~ /^UP/ && index($0, address) { found = 1 }
            END { exit !found }
        '
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

# The deployment rolls the two fixed policy slots reserve-first. Keep POSTs in
# flight while each slot is replaced and require both continuity and exactly-once
# execution through the surviving worker.
rollout_errors=$tmpdir/policy-rollout-errors
: >"$rollout_errors"
rollout_before=$(probe http://gonka-pr-router-b:8404/count)
(
    for i in $(seq 1 80); do
        if ! probe -X POST -H 'Content-Type: application/json' \
            -d "request=policy-roll-$i" \
            http://proxy-router/devshard/v5/sessions/policy-roll/chat \
            >/dev/null; then
            printf '%s\n' "$i" >>"$rollout_errors"
        fi
        sleep 0.02
    done
) &
rollout_probe_pid=$!
for name in b a; do
    docker rm -f "gonka-pr-policy-$name" >/dev/null
    start_policy "$name"
    policy_ip=$(docker inspect -f \
        "{{with index .NetworkSettings.Networks \"$network\"}}{{.IPAddress}}{{end}}" \
        "gonka-pr-policy-$name")
    for _ in $(seq 30); do
        proxy_backend_addr_up policy_http "$policy_ip" && break
        sleep 0.1
    done
    proxy_backend_addr_up policy_http "$policy_ip" || fail \
        "replacement policy-$name was not admitted before the next slot"
done
wait "$rollout_probe_pid"
[[ ! -s $rollout_errors ]] || fail \
    "policy rollout dropped POSTs: $(tr '\n' ' ' <"$rollout_errors")"
rollout_after=$(probe http://gonka-pr-router-b:8404/count)
[[ $((rollout_after - rollout_before)) == 80 ]] || fail \
    "80 policy-rollout POSTs produced $((rollout_after - rollout_before)) executions"

unknown_response=$(docker exec gonka-pr-proxy /bin/busybox wget -S -O /dev/null \
    'http://127.0.0.1:8404/readyz?version=v9' 2>&1 || true)
[[ $unknown_response == *'503 Service Unavailable'* ]] || fail \
    "unknown governance version readiness did not fail closed"
probe 'http://proxy-router:8405/metrics?scope=frontend' >"$tmpdir/proxy.metrics" || fail \
    "parent proxy metrics are not reachable on their internal network alias"
grep -q '^haproxy_' "$tmpdir/proxy.metrics" || fail \
    "parent proxy returned no HAProxy metrics"
probe http://proxy-router:9100/versions | grep -q '"initialized":true' \
    || fail "read-only catalog bridge did not forward GET /versions"
catalog_post_status=$(docker exec gonka-pr-probe curl -sS -o /dev/null \
    -w '%{http_code}' -X POST http://proxy-router:9100/versions)
[[ $catalog_post_status == 405 ]] \
    || fail "catalog bridge forwarded a mutating method (status $catalog_post_status)"
catalog_path_status=$(docker exec gonka-pr-probe curl -sS -o /dev/null \
    -w '%{http_code}' http://proxy-router:9100/private)
[[ $catalog_path_status == 404 ]] \
    || fail "catalog bridge exposed a non-catalog path (status $catalog_path_status)"

proxy_id=$(docker inspect -f '{{.Id}}' gonka-pr-proxy)
printf '%s\n' '{"schema":1,"initialized":true,"revision":2,"versions":[{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"}]}' \
    >"$tmpdir/catalog/versions.next"
mv "$tmpdir/catalog/versions.next" "$tmpdir/catalog/versions.json"
for _ in $(seq 30); do
    if docker exec gonka-pr-proxy /usr/local/lib/router-runtime/catalog-status --state \
        | jq -e '.state == "capacity-exhausted" and .dynamic_slots_used == 0' \
            >/dev/null 2>&1; then
        break
    fi
    sleep 0.5
done
docker exec gonka-pr-proxy /usr/local/lib/router-runtime/catalog-status --state \
    | jq -e '.state == "capacity-exhausted" and .dynamic_slots_used == 0' \
        >/dev/null || fail "catalog capacity preflight did not report atomic exhaustion"
for version in v9 v10; do
    unknown_response=$(docker exec gonka-pr-proxy /bin/busybox wget -S -O /dev/null \
        "http://127.0.0.1:8404/readyz?version=$version" 2>&1 || true)
    [[ $unknown_response == *'503 Service Unavailable'* ]] || fail \
        "capacity preflight partially published $version"
done
printf '%s\n' '{"schema":1,"initialized":true,"revision":2,"versions":[{"name":"v4"},{"name":"v5"},{"name":"v9"}]}' \
    >"$tmpdir/catalog/versions.next"
mv "$tmpdir/catalog/versions.next" "$tmpdir/catalog/versions.json"
for _ in $(seq 30); do
    if docker exec gonka-pr-proxy /usr/local/lib/router-runtime/catalog-status --state \
        | jq -e '.state == "activation-pending" and .dynamic_slots_used == 1' \
            >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done
docker exec gonka-pr-proxy /usr/local/lib/router-runtime/catalog-status --state \
    | jq -e '.state == "activation-pending"' >/dev/null \
    || fail "a one-router governance version did not remain a candidate"
unknown_response=$(docker exec gonka-pr-proxy /bin/busybox wget -S -O /dev/null \
    'http://127.0.0.1:8404/readyz?version=v9' 2>&1 || true)
[[ $unknown_response == *'503 Service Unavailable'* ]] || fail \
    "top distributor published v9 before its two-router activation reserve"
docker rm -f gonka-pr-router-a >/dev/null
docker run -d --name gonka-pr-router-a --network "$network" \
    --network-alias versiond-router-fleet \
    -e NAME=a -e 'SERVES=v4 v5 v9' -e GENERIC_READY=true \
    -e DATA_ENABLED=true -e MISSING_VERSIONLESS=true \
    -v "$tmpdir/upstream.py:/app.py:ro" \
    python:3.12-alpine python /app.py >/dev/null
for _ in $(seq 40); do
    if proxy_admin '/readyz?version=v9' >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done
proxy_admin '/readyz?version=v9' >/dev/null \
    || fail "top distributor did not admit governance v9"
docker rm -f gonka-pr-router-a >/dev/null
docker run -d --name gonka-pr-router-a --network "$network" \
    --network-alias versiond-router-fleet \
    -e NAME=a -e SERVES=v4 -e GENERIC_READY=true \
    -e DATA_ENABLED=true -e MISSING_VERSIONLESS=true \
    -v "$tmpdir/upstream.py:/app.py:ro" \
    python:3.12-alpine python /app.py >/dev/null
router_a_ip=$(docker inspect -f "{{with index .NetworkSettings.Networks \"$network\"}}{{.IPAddress}}{{end}}" \
    gonka-pr-router-a)
for _ in $(seq 40); do
    proxy_backend_addr_up versiond_routers_v4 "$router_a_ip" && break
    sleep 0.25
done
proxy_backend_addr_up versiond_routers_v4 "$router_a_ip" \
    || fail "parent router did not admit replacement router-a for v4"
[[ $(probe http://proxy-router:18081/v9/sessions/dynamic/healthz) == b ]] \
    || fail "dynamically learned v9 did not reach its route-ready router"
docker exec gonka-pr-proxy /usr/local/lib/router-runtime/catalog-status \
    /etc/haproxy/version-router.map | grep -qx v9 \
    || fail "top distributor runtime catalog does not report v9"
if docker exec gonka-pr-proxy /usr/local/lib/router-runtime/catalog-status \
    /etc/haproxy/version-router.map | grep -q '^version='; then
    fail "top distributor diagnostics exposed readiness projection keys as versions"
fi
[[ $(docker inspect -f '{{.Id}}' gonka-pr-proxy) == "$proxy_id" ]] \
    || fail "learning v9 replaced the top distributor"
[[ $(probe http://proxy-router:18081/v4/sessions/still-live/healthz) =~ ^(a|b)$ ]] \
    || fail "learning v9 disrupted the existing v4 route"
docker exec gonka-pr-proxy test -s /var/lib/gonka-router/catalog-v2.json \
    || fail "top distributor did not persist its learned catalog"
docker stop gonka-pr-catalog >/dev/null
docker rm -f gonka-pr-proxy >/dev/null
docker run -d --name gonka-pr-proxy --network "$network" \
    --network-alias proxy-router \
    -v "$state:/var/lib/gonka-router" \
    -e 'VERSIOND_VERSIONS=v4 v5' -e 'VERSIOND_NON_HA_VERSIONS=' \
    -e VERSIOND_ROUTING_CATALOG_URL=http://routing-catalog:8080/versions \
    -e VERSIOND_ROUTING_CATALOG_POLL_SECONDS=1 \
    -e PROXY_ROUTER_VERSION_CAPACITY=1 \
    -e PROXY_ROUTER_METRICS_BIND_HOST=proxy-router \
    -e PROXY_ROUTER_CATALOG_BIND_HOST=proxy-router \
    -e PROXY_ROUTER_CATALOG_UPSTREAM_HOST=routing-catalog \
    -e PROXY_ROUTER_CATALOG_UPSTREAM_PORT=8080 \
    "$image" >/dev/null
for _ in $(seq 40); do
    if proxy_admin '/readyz?version=v9' >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done
proxy_admin '/readyz?version=v9' >/dev/null \
    || fail "top distributor lost a learned route when restarted without dapi"
[[ $(probe http://proxy-router:18081/v9/sessions/cache/healthz) == b ]] \
    || fail "cached top-level v9 did not reach its route-ready router"
docker start gonka-pr-catalog >/dev/null
for _ in $(seq 60); do
    if proxy_admin /readyz >/dev/null 2>&1 &&
        proxy_admin '/readyz?component=versiond' >/dev/null 2>&1 &&
        proxy_admin '/readyz?component=edge-api' >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done
proxy_admin /readyz >/dev/null \
    || fail "public policy pool did not recover after cached-route restart"
for worker in a b; do
    worker_route=
    for _ in $(seq 40); do
        worker_route=$(probe --haproxy-protocol \
            "http://gonka-pr-policy-$worker/devshard/v5/sessions/dns-refresh/healthz" \
            2>/dev/null || true)
        [[ $worker_route == b ]] && break
        sleep 0.25
    done
    [[ $worker_route == b ]] || fail \
        "policy worker $worker did not re-resolve the replaced proxy-router"
done
printf '%s\n' '{"schema":1,"initialized":false,"revision":3,"versions":[]}' \
    >"$tmpdir/catalog/versions.next"
mv "$tmpdir/catalog/versions.next" "$tmpdir/catalog/versions.json"
sleep 2
proxy_admin '/readyz?version=v9' >/dev/null \
    || fail "an uninitialized catalog replaced the last accepted route view"
printf '%s\n' '{"schema":1,"initialized":true,"revision":1,"versions":[{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"}]}' \
    >"$tmpdir/catalog/versions.next"
mv "$tmpdir/catalog/versions.next" "$tmpdir/catalog/versions.json"
for _ in $(seq 20); do
    if docker exec gonka-pr-proxy /usr/local/lib/router-runtime/catalog-status --state \
        | jq -e '.state == "revision-error"' >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done
docker exec gonka-pr-proxy /usr/local/lib/router-runtime/catalog-status --state \
    | jq -e '.state == "revision-error"' >/dev/null \
    || fail "a regressing top-level catalog revision was not rejected"
printf '%s\n' '{"schema":1,"initialized":true,"revision":3,"versions":[{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"}]}' \
    >"$tmpdir/catalog/versions.next"
mv "$tmpdir/catalog/versions.next" "$tmpdir/catalog/versions.json"
for _ in $(seq 30); do
    if docker exec gonka-pr-proxy /usr/local/lib/router-runtime/catalog-status --state \
        | jq -e '.state == "capacity-exhausted" and .dynamic_slots_used == 1' \
            >/dev/null 2>&1; then
        break
    fi
    sleep 0.5
done
docker exec gonka-pr-proxy /usr/local/lib/router-runtime/catalog-status --state \
    | jq -e '.state == "capacity-exhausted" and .dynamic_slots_used == 1' \
        >/dev/null || fail "top distributor did not expose catalog capacity exhaustion"
unknown_response=$(docker exec gonka-pr-proxy /bin/busybox wget -S -O /dev/null \
    'http://127.0.0.1:8404/readyz?version=v10' 2>&1 || true)
[[ $unknown_response == *'503 Service Unavailable'* ]] || fail \
    "capacity-exhausted top distributor published v10"
[[ $(probe http://proxy-router:18081/v9/sessions/after-capacity/healthz) == b ]] \
    || fail "capacity exhaustion disrupted the last admitted top-level route"

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

# Both routing layers derive the same key from every escrow-scoped path. The
# outer choice must therefore stay stable when a client switches between the
# versioned API and its versionless observability endpoints.
for escrow in sticky-a sticky-b sticky-c sticky-d; do
    versioned=$(probe "http://proxy-router:18081/v4/sessions/$escrow/healthz")
    diffs=$(probe "http://proxy-router:18081/sessions/$escrow/diffs")
    stats=$(probe "http://proxy-router:18081/stats/shards/$escrow")
    [[ $versioned == "$diffs" && $versioned == "$stats" ]] || fail \
        "escrow $escrow moved between router replicas: $versioned/$diffs/$stats"
done

# A short health-view divergence can send a versionless lookup to a router that
# does not own the local route. GET may retry its 404; the matching POST policy
# remains protected by disable-l7-retry.
docker stop gonka-pr-router-bad >/dev/null
sleep 2
retry_escrow=
for candidate in $(seq 1 100); do
    retry_escrow="retry-404-$candidate"
    [[ $(probe "http://proxy-router:18081/v4/sessions/$retry_escrow/healthz") == a ]] \
        && break
    retry_escrow=
done
[[ -n $retry_escrow ]] || fail "could not select router a for the 404 retry probe"
[[ $(probe "http://proxy-router:18081/sessions/$retry_escrow/diffs") == b ]] \
    || fail "versionless GET did not recover from a route-local 404"
docker start gonka-pr-router-bad >/dev/null
sleep 2

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
