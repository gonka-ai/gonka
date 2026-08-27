#!/usr/bin/env bash

set -Eeuo pipefail

network=gonka-proxy-router-test-$$
image=gonka-proxy-router-test:$$
state=gonka-proxy-router-state-$$
capacity_state=gonka-proxy-router-capacity-state-$$
containers=(
    gonka-pr-proxy gonka-pr-proxy-lb gonka-pr-probe gonka-pr-catalog
    gonka-pr-proxy-both
    gonka-pr-proxy-cache-floor
    gonka-pr-policy-a gonka-pr-policy-b
    gonka-pr-router-a gonka-pr-router-b gonka-pr-router-bad
    gonka-pr-router-migration
    gonka-pr-router-legacy gonka-pr-proxy-legacy
    gonka-pr-edge-router
)
tmpdir=$(mktemp -d)

cleanup() {
    status=$?
    if (( status != 0 )); then
        docker logs gonka-pr-proxy >&2 || true
    fi
    docker rm -f "${containers[@]}" >/dev/null 2>&1 || true
    docker network rm "$network" >/dev/null 2>&1 || true
    docker volume rm "$state" "$capacity_state" >/dev/null 2>&1 || true
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
import time
import urllib.parse

name = os.environ["NAME"]
serves = set(os.environ.get("SERVES", "").split())
generic_ready = os.environ.get("GENERIC_READY", "true") == "true"
data_enabled = os.environ.get("DATA_ENABLED", "true") == "true"
data_port = int(os.environ.get("DATA_PORT", "8080"))
missing_versionless = os.environ.get("MISSING_VERSIONLESS", "false") == "true"
post_count = 0
lock = threading.Lock()
data_server = None


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
        if self.path.endswith("/slow"):
            time.sleep(2)
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
        if self.path == "/disable-data":
            if data_server is not None:
                data_server.shutdown()
                data_server.server_close()
            return self.reply(200, "disabled")
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
    data_server = http.server.ThreadingHTTPServer(("", data_port), Data)
    threading.Thread(
        target=data_server.serve_forever,
        daemon=True,
    ).start()
threading.Event().wait()
PY

mkdir "$tmpdir/catalog"
printf '%s\n' '{"versions":[{"name":"v4"},{"name":"v5"}]}' \
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
    upstream edge_router {
        zone edge_router 64k;
        server edge-api-router:18080 resolve;
    }
    server {
        listen 80 proxy_protocol;
        listen 8081;
        set_real_ip_from 0.0.0.0/0;
        real_ip_header proxy_protocol;
        location /devshard/ {
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_pass http://versiond_distributor/;
        }
        location = /health { return 200 "ready\n"; }
        location /tier-a/ {
            proxy_set_header X-Real-IP $remote_addr;
            proxy_set_header X-Forwarded-Proto $scheme;
            proxy_pass http://edge_router/;
        }
        location / { return 200 "$hostname:$remote_addr\n"; }
    }
}
EOF

docker network create "$network" >/dev/null
docker volume create "$state" >/dev/null
docker volume create "$capacity_state" >/dev/null
docker build -q -t "$image" -f Dockerfile .. >/dev/null
[[ $(docker image inspect -f \
    '{{index .Config.Labels "ai.gonka.proxy-policy-contract"}}' "$image") == 1 ]] \
    || fail "public proxy image has no stable policy contract label"
cache_now=$(date +%s)
docker run --rm --user 0:0 -e "CACHE_NOW=$cache_now" \
    -v "$state:/state" --entrypoint sh "$image" -c \
    'printf "{\"schema\":1,\"fetched_at_unix\":%s,\"versions\":[\"v4\",\"v5\"]}\n" "$CACHE_NOW" > /state/catalog.json; chown -R haproxy:haproxy /state'
docker run --rm --user 0:0 -e "CACHE_NOW=$cache_now" \
    -v "$capacity_state:/state" --entrypoint sh "$image" -c \
    'printf "{\"schema\":1,\"fetched_at_unix\":%s,\"versions\":[\"v4\",\"v5\",\"v9\",\"v10\"]}\n" "$CACHE_NOW" > /state/catalog.json; chown -R haproxy:haproxy /state'

docker run -d --name gonka-pr-catalog --network "$network" \
    --network-alias routing-catalog \
    -v "$tmpdir/catalog:/data:ro" -v "$tmpdir/catalog.py:/app.py:ro" \
    python:3.12-alpine python /app.py >/dev/null

for spec in 'a:v4:true:true' 'b:v4 v5 v9 v10:true:true' 'bad:v4:true:true'; do
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

# A valid LKG projection is authoritative across restarts. Reducing the
# configured reservation must not make already accepted routes unrepresentable.
docker run -d --name gonka-pr-proxy-cache-floor --network "$network" \
    -v "$capacity_state:/var/lib/gonka-router" \
    -e 'VERSIOND_VERSIONS=v4 v5' -e 'VERSIOND_NON_HA_VERSIONS=' \
    -e VERSIOND_ROUTING_CATALOG_URL=http://missing-catalog:8080/versions \
    -e PROXY_ROUTER_VERSION_CAPACITY=1 \
    -e VERSIOND_ROUTER_POOL_HOST=versiond-router-fleet \
    -e VERSIOND_ROUTER_FLEET_CAPACITY=3 \
    "$image" >/dev/null
for version in v9 v10; do
    for _ in $(seq 40); do
        if docker exec gonka-pr-proxy-cache-floor curl -fsS \
            "http://127.0.0.1:8404/readyz?version=$version" >/dev/null 2>&1; then
            break
        fi
        sleep 0.25
    done
    docker exec gonka-pr-proxy-cache-floor curl -fsS \
        "http://127.0.0.1:8404/readyz?version=$version" >/dev/null \
        || fail "reduced capacity dropped cached route $version"
done
[[ $(docker exec gonka-pr-proxy-cache-floor grep -c \
    '^backend versiond_routers_dynamic_' /etc/haproxy/haproxy.cfg) == 2 ]] \
    || fail "LKG routes did not raise the effective dynamic capacity"
docker rm -f gonka-pr-proxy-cache-floor >/dev/null

# The reversible upgrade keeps a healthy singleton for v4 nginx rollback. Its
# historical DNS name must never enter the steady-state fleet pool, otherwise
# it can mask a route-dead fleet at the cutover commit boundary.
docker run -d --name gonka-pr-router-migration --network "$network" \
    --network-alias versiond-router \
    -e NAME=migration -e 'SERVES=v4 v5' \
    -e GENERIC_READY=true -e DATA_ENABLED=true \
    -v "$tmpdir/upstream.py:/app.py:ro" \
    python:3.12-alpine python /app.py >/dev/null

# The standard v5 transition starts with the published 0.2.15 nginx router.
# It has no admin listener, so the outer distributor must use the explicit
# legacy data-path health contract until the router fleet is installed.
docker run -d --name gonka-pr-router-legacy --network "$network" \
    --network-alias versiond-router-legacy \
    -e VERSIOND_HOSTS=gonka-pr-router-a -e VERSIOND_PORT=8080 \
    -e VERSIOND_VERSIONS=v4 -e VERSIOND_NON_HA_VERSIONS= \
    -e VERSIOND_ROUTER_FRONT_BIND_HOST=versiond-router-legacy \
    -e VERSIOND_ROUTER_TRUST_FORWARDED_HEADERS=true \
    -v "$PWD/../versiond-router/legacy-public-tier-compat.sh:/docker-entrypoint.d/50-gonka-public-tier-compat.sh:ro" \
    ghcr.io/product-science/versiond-router:0.2.15 >/dev/null
legacy_address=$(docker inspect -f \
    "{{with index .NetworkSettings.Networks \"$network\"}}{{.IPAddress}}{{end}}" \
    gonka-pr-router-legacy)
for _ in $(seq 40); do
    if docker exec gonka-pr-router-legacy wget -q -O /dev/null \
        "http://$legacy_address:8080/v4/healthz" 2>/dev/null; then
        break
    fi
    sleep 0.25
done
docker exec gonka-pr-router-legacy wget -q -O /dev/null \
    "http://$legacy_address:8080/v4/healthz" \
    || fail "published legacy versiond-router did not become healthy"
docker exec gonka-pr-router-legacy grep -q "listen $legacy_address:8080" \
    /etc/nginx/conf.d/default.conf \
    || fail "legacy versiond-router still listens outside the isolated ingress network"
legacy_headers=$(docker exec gonka-pr-router-legacy wget -q -O - \
    --header='X-Real-IP: 203.0.113.45' \
    --header='X-Forwarded-Proto: https' \
    "http://$legacy_address:8080/v4/sessions/legacy/headers")
[[ $legacy_headers == '203.0.113.45|https' ]] \
    || fail "legacy versiond-router overwrote trusted policy identity: $legacy_headers"
docker run -d --name gonka-pr-proxy-legacy --network "$network" \
    -e PROXY_POLICY_POOL_HOST=missing-policy \
    -e VERSIOND_ROUTER_POOL_HOST=versiond-router-legacy \
    -e VERSIOND_ROUTER_FLEET_CAPACITY=1 \
    -e PROXY_ROUTER_VERSION_CAPACITY=1 \
    -e VERSIOND_ROUTER_HEALTH_CONTRACT=legacy \
    -e VERSIOND_VERSIONS=v4 "$image" >/dev/null
for _ in $(seq 40); do
    if docker exec gonka-pr-proxy-legacy curl -fsS \
        'http://127.0.0.1:8404/readyz?version=v4' >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done
docker exec gonka-pr-proxy-legacy curl -fsS \
    'http://127.0.0.1:8404/readyz?version=v4' >/dev/null \
    || fail "outer distributor rejected the published legacy versiond-router"
docker rm -f gonka-pr-proxy-legacy gonka-pr-router-legacy >/dev/null

# This fixture represents the nginx edge-api-router shipped by the previous
# release. The public HAProxy must leave that HTTP routing hop unchanged.
docker run -d --name gonka-pr-edge-router --hostname edge-api-router \
    --network "$network" --network-alias edge-api-router \
    -e NAME=legacy-edge-router -e 'SERVES=' -e DATA_PORT=18080 \
    -v "$tmpdir/upstream.py:/app.py:ro" \
    python:3.12-alpine python /app.py >/dev/null

docker run -d --name gonka-pr-proxy --network "$network" \
    --network-alias proxy-router \
    -v "$state:/var/lib/gonka-router" \
    -e 'VERSIOND_VERSIONS=v4 v5' -e 'VERSIOND_NON_HA_VERSIONS=' \
    -e VERSIOND_ROUTING_CATALOG_URL=http://routing-catalog:8080/versions \
    -e VERSIOND_ROUTING_CATALOG_POLL_SECONDS=1 \
    -e PROXY_ROUTER_VERSION_CAPACITY=1 \
    -e PROXY_ROUTER_ACTIVATION_MIN_READY=2 \
    -e PROXY_ROUTER_METRICS_BIND_HOST=proxy-router \
    -e PROXY_ROUTER_CATALOG_BIND_HOST=proxy-router \
    -e PROXY_ROUTER_CATALOG_UPSTREAM_HOST=routing-catalog \
    -e PROXY_ROUTER_CATALOG_UPSTREAM_PORT=8080 \
    "$image" >/dev/null
for _ in $(seq 40); do
    docker exec gonka-pr-proxy test -s /var/lib/gonka-router/catalog.json \
        >/dev/null 2>&1 && break
    sleep 0.25
done
docker exec gonka-pr-proxy test -s /var/lib/gonka-router/catalog.json \
    || fail "top distributor did not load the shared catalog cache contract"
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

# A healthy sidecar cannot make an absent TLS listener look ready. This is the
# production HTTP-fallback shape after nginx rejects an invalid certificate.
docker run -d --name gonka-pr-proxy-both --network "$network" \
    -e PROXY_POLICY_POOL_HOST=proxy-policy \
    -e NGINX_MODE=both \
    -e 'VERSIOND_VERSIONS=v4 v5' -e 'VERSIOND_NON_HA_VERSIONS=' \
    "$image" >/dev/null
for _ in $(seq 60); do
    if docker exec gonka-pr-proxy-both sh -c \
        "printf 'show stat\\n' | socat - UNIX-CONNECT:/var/run/haproxy/haproxy.sock" \
        2>/dev/null | awk -F, '
            $1 == "policy_http" && $18 ~ /^UP/ { http_up = 1 }
            $1 == "policy_https" && $18 ~ /^UP/ { https_up = 1 }
            END { exit !(http_up && !https_up) }
        '; then
        break
    fi
    sleep 0.25
done
docker exec gonka-pr-proxy-both sh -c \
    "printf 'show stat\\n' | socat - UNIX-CONNECT:/var/run/haproxy/haproxy.sock" \
    | awk -F, '
        $1 == "policy_http" && $18 ~ /^UP/ { http_up = 1 }
        $1 == "policy_https" && $18 ~ /^UP/ { https_up = 1 }
        END { exit !(http_up && !https_up) }
    ' || fail "missing TLS listener did not withdraw only the HTTPS policy pool"
both_ready=$(docker exec gonka-pr-proxy-both curl -sS -o /dev/null \
    -w '%{http_code}' http://127.0.0.1:8404/readyz)
[[ $both_ready == 503 ]] \
    || fail "both-mode readiness stayed green with every TLS listener absent"
docker rm -f gonka-pr-proxy-both >/dev/null

for _ in $(seq 60); do
    if proxy_admin /readyz >/dev/null 2>&1 &&
        proxy_admin '/readyz?component=versiond' >/dev/null 2>&1 &&
        proxy_admin '/readyz?version=v4' >/dev/null 2>&1 &&
        proxy_admin '/readyz?version=v5' >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done
proxy_admin /readyz >/dev/null || fail "public policy pool did not become ready"
proxy_admin '/readyz?version=v4' >/dev/null \
    || fail "top-level v4 readiness did not follow the router pool"
proxy_admin '/readyz?version=v5' >/dev/null \
    || fail "top-level v5 readiness did not follow the router pool"

# nginx's SIGQUIT stop must leave an accepted long response alive. Compose
# supplies the larger production budget; this short probe exercises the signal
# and confirms Docker does not reach its SIGKILL backstop.
slow_response=$tmpdir/policy-slow-response
probe --haproxy-protocol \
    http://gonka-pr-policy-a/devshard/v5/sessions/graceful/slow \
    >"$slow_response" &
slow_pid=$!
sleep 0.5
docker stop --time 5 gonka-pr-policy-a >/dev/null
wait "$slow_pid" || fail "accepted policy response was reset during SIGQUIT drain"
case "$(cat "$slow_response")" in a | b) ;; *) fail "slow policy response was incomplete" ;; esac
docker rm gonka-pr-policy-a >/dev/null
start_policy a

# A trusted external L4 balancer supplies the client address in a PROXY
# preamble. Other source networks remain ordinary direct ingress.
probe_ip=$(docker inspect -f \
    "{{with index .NetworkSettings.Networks \"$network\"}}{{.IPAddress}}{{end}}" \
    gonka-pr-probe)
docker run -d --name gonka-pr-proxy-lb --network "$network" \
    -e PROXY_POLICY_POOL_HOST=proxy-policy \
    -e PROXY_ROUTER_PROXY_PROTOCOL_FROM="$probe_ip/32" \
    -e VERSIOND_ROUTER_POOL_HOST=missing-router \
    -e PROXY_ROUTER_VERSION_CAPACITY=1 "$image" >/dev/null
for _ in $(seq 40); do
    if docker exec gonka-pr-proxy-lb curl -fsS \
        http://127.0.0.1:8404/readyz >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done
external=$(probe --haproxy-clientip 203.0.113.77 http://gonka-pr-proxy-lb/)
case "$external" in
    policy-a:203.0.113.77 | policy-b:203.0.113.77) ;;
    *) fail "trusted external PROXY address was not preserved: '$external'" ;;
esac
if probe http://gonka-pr-proxy-lb/ >/dev/null 2>&1; then
    fail "trusted external LB source was accepted without a PROXY preamble"
fi
docker rm -f gonka-pr-proxy-lb >/dev/null

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
    # Compose honors the nginx image's SIGQUIT stop signal during replacement.
    # A forced removal can reset an already accepted POST, which no proxy may
    # safely replay because the application might have executed it.
    docker stop --time 10 "gonka-pr-policy-$name" >/dev/null
    docker rm "gonka-pr-policy-$name" >/dev/null
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
probe http://proxy-router:9100/versions | grep -q '"versions"' \
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
printf '%s\n' '{"versions":[{"name":"v4"},{"name":"v5"},{"name":"v9"}]}' \
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
proxy_admin '/readyz?version=v9' >/dev/null || {
    docker exec gonka-pr-proxy \
        /usr/local/lib/router-runtime/catalog-status --state >&2 || true
    fail "top distributor did not admit governance v9"
}
router_b_ip=$(docker inspect -f \
    "{{with index .NetworkSettings.Networks \"$network\"}}{{.IPAddress}}{{end}}" \
    gonka-pr-router-b)
docker exec gonka-pr-proxy /usr/local/lib/proxy-router/route-status \
    v9 "$router_b_ip" >/dev/null \
    || fail "route-status did not report the admitted dynamic v9 slot"
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
[[ $(probe http://proxy-router:18081/v4/sessions/still-live/healthz) =~ ^(a|b|bad)$ ]] \
    || fail "learning v9 disrupted the existing v4 route"
docker exec gonka-pr-proxy test -s /var/lib/gonka-router/catalog.json \
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
    -e PROXY_ROUTER_ACTIVATION_MIN_READY=2 \
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
        proxy_admin '/readyz?component=versiond' >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done
proxy_admin /readyz >/dev/null \
    || fail "public policy pool did not recover after cached-route restart"
for worker in a b; do
    worker_route=
    # DNS cache expiry and nginx's default upstream fail_timeout can overlap if
    # the worker reaches the replacement before HAProxy has bound its listener.
    for _ in $(seq 120); do
        worker_route=$(probe --haproxy-protocol \
            "http://gonka-pr-policy-$worker/devshard/v5/sessions/dns-refresh/healthz" \
            2>/dev/null || true)
        [[ $worker_route == b ]] && break
        sleep 0.25
    done
    [[ $worker_route == b ]] || fail \
        "policy worker $worker did not re-resolve the replaced proxy-router (last response: '$worker_route')"
done
printf '%s\n' '{"versions":"not-an-array"}' \
    >"$tmpdir/catalog/versions.next"
mv "$tmpdir/catalog/versions.next" "$tmpdir/catalog/versions.json"
sleep 2
proxy_admin '/readyz?version=v9' >/dev/null \
    || fail "a malformed catalog replaced the last accepted route view"
printf '%s\n' '{"versions":[{"name":"v4"},{"name":"v5"}]}' \
    >"$tmpdir/catalog/versions.next"
mv "$tmpdir/catalog/versions.next" "$tmpdir/catalog/versions.json"
for _ in $(seq 20); do
    if docker exec gonka-pr-proxy /usr/local/lib/router-runtime/catalog-status --state \
        | jq -e '.state == "withdrawal-pending"' >/dev/null 2>&1; then
        break
    fi
    sleep 0.25
done
docker exec gonka-pr-proxy /usr/local/lib/router-runtime/catalog-status --state \
    | jq -e '.state == "withdrawal-pending"' >/dev/null \
    || fail "a top-level catalog removal was not rejected"
printf '%s\n' '{"versions":[{"name":"v4"},{"name":"v5"},{"name":"v9"},{"name":"v10"}]}' \
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

[[ $(probe http://proxy-router/tier-a/status) == legacy-edge-router ]] \
    || fail "public Tier A path did not preserve the existing edge-api-router hop"
edge_admin_status=$(docker exec gonka-pr-proxy curl -sS -o /dev/null \
    -w '%{http_code}' 'http://127.0.0.1:8404/readyz?component=edge-api')
[[ $edge_admin_status == 404 ]] \
    || fail "public HAProxy unexpectedly exposes edge-api readiness"

for _ in $(seq 8); do
    [[ $(probe http://proxy-router:18081/v5/sessions/test/healthz) == b ]] \
        || fail "v5 reached a router whose v5 pool is not ready"
done

# Select a key that is demonstrably pinned to the fixture whose admin readiness
# remains healthy, then remove only its data listener. This makes the following
# connect-failure test deterministic without changing the consistent-hash ring.
unavailable_escrow=
for candidate in $(seq 1 100); do
    unavailable_escrow="unavailable-$candidate"
    [[ $(probe "http://proxy-router:18081/v4/sessions/$unavailable_escrow/healthz") == bad ]] \
        && break
    unavailable_escrow=
done
[[ -n $unavailable_escrow ]] || fail \
    "could not select the route whose data listener will be disabled"
probe http://gonka-pr-router-bad:8404/disable-data >/dev/null
for _ in $(seq 20); do
    if ! probe http://gonka-pr-router-bad:8080/healthz >/dev/null 2>&1; then
        break
    fi
    sleep 0.1
done
if probe http://gonka-pr-router-bad:8080/healthz >/dev/null 2>&1; then
    fail "route fixture kept accepting data after its listener was disabled"
fi

before_a=$(probe http://gonka-pr-router-a:8404/count)
before_b=$(probe http://gonka-pr-router-b:8404/count)
requests=12
for i in $(seq "$requests"); do
    probe -X POST -H 'Content-Type: application/json' -d "{\"request\":$i}" \
        "http://proxy-router:18081/v4/sessions/$unavailable_escrow/chat" >/dev/null \
        || fail "POST $i did not fail over from an unavailable router data port"
done
after_a=$(probe http://gonka-pr-router-a:8404/count)
after_b=$(probe http://gonka-pr-router-b:8404/count)
executed=$((after_a - before_a + after_b - before_b))
[[ $executed == "$requests" ]] \
    || fail "$requests POSTs produced $executed upstream executions"

echo "test-routing: ok"
