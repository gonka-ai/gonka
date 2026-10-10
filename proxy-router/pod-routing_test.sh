#!/usr/bin/env bash
# Exercise the Kubernetes network namespace contract with the actual images.
# Docker's shared network namespace stands in for a pod; no cluster is needed.
set -Eeuo pipefail
cd "$(dirname "$0")"
task_id="gonka-pod-routing-$$"
tmpdir=$(mktemp -d)
router_image="$task_id-router"
policy_image="$task_id-policy"
containers=("$task_id-policy" "$task_id-router" "$task_id-pod" "$task_id-fixture")
cleanup() {
    status=$?
    if [ "$status" -ne 0 ]; then
        for container in "${containers[@]}"; do
            docker logs --tail 40 "$container" >&2 2>/dev/null || true
        done
    fi
    docker rm -f "${containers[@]}" >/dev/null 2>&1 || true
    docker network rm "$task_id" >/dev/null 2>&1 || true
    docker image rm "$router_image" "$policy_image" >/dev/null 2>&1 || true
    rm -rf "$tmpdir"
}
trap cleanup EXIT
fail() { echo "pod-routing: $*" >&2; exit 1; }

cat >"$tmpdir/fixture.py" <<'PY'
import http.server
import threading

release = threading.Event()
started = threading.Event()

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/release":
            release.set()
        if self.path == "/started" and not started.is_set():
            self.send_error(503)
            return
        body = b"ready\n"
        if self.path == "/v6/client":
            body = (self.headers.get("X-Real-IP", "missing") + "|" +
                    self.headers.get("X-Forwarded-Proto", "missing")).encode()
        if self.path == "/v6/sessions/test/slow":
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Connection", "close")
            self.end_headers()
            self.wfile.write(b"data: started\n\n")
            self.wfile.flush()
            started.set()
            if not release.wait(30):
                return
            self.wfile.write(b"data: completed\n\n")
            self.wfile.flush()
            return
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass

for port in (8080, 8404, 9000):
    server = http.server.ThreadingHTTPServer(("", port), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
threading.Event().wait()
PY

docker build -q -t "$router_image" -f Dockerfile .. >/dev/null
docker build -q -t "$policy_image" ../proxy >/dev/null
[ "$(docker image inspect -f '{{.Config.StopSignal}}' "$router_image")" = SIGUSR1 ] \
    || fail 'router image does not declare graceful SIGUSR1'
[ "$(docker image inspect -f '{{.Config.StopSignal}}' "$policy_image")" = SIGQUIT ] \
    || fail 'policy image does not declare graceful SIGQUIT'
docker network create "$task_id" >/dev/null
docker run -d --name "$task_id-fixture" --network "$task_id" \
    --network-alias router-fixture -v "$tmpdir:/fixture:ro" \
    python:3.12-alpine python /fixture/fixture.py >/dev/null
docker run -d --name "$task_id-pod" --network "$task_id" \
    alpine:3.21 sleep infinity >/dev/null
pod_ip=$(docker inspect -f \
    "{{with index .NetworkSettings.Networks \"$task_id\"}}{{.IPAddress}}{{end}}" \
    "$task_id-pod")

# Match the chart's read-only TLS Secret projection. These credentials exist
# only for this isolated fixture and are removed with its temporary directory.
mkdir "$tmpdir/tls"
openssl req -x509 -newkey rsa:2048 -nodes -days 1 \
    -subj /CN=gonka-pod-test -addext "subjectAltName=IP:$pod_ip" \
    -keyout "$tmpdir/tls/private.key" -out "$tmpdir/tls/cert.pem" \
    >/dev/null 2>&1
chmod 644 "$tmpdir/tls/private.key" "$tmpdir/tls/cert.pem"

docker run -d --name "$task_id-router" --network "container:$task_id-pod" \
    --user 99:99 --group-add 99 \
    --cap-drop ALL --cap-add NET_BIND_SERVICE --security-opt no-new-privileges \
    -v "$tmpdir/tls/cert.pem:/fixture/ca.pem:ro" -e NGINX_MODE=both \
    -v "$tmpdir/tls:/etc/haproxy/ssl:ro" \
    -e PROXY_ROUTER_PUBLIC_BIND_ADDRESS="$pod_ip" \
    -e PROXY_ROUTER_POLICY_BIND_HOST=127.0.0.1 \
    -e PROXY_POLICY_POOL_HOST=127.0.0.1 -e PROXY_POLICY_POOL_SLOTS=1 \
    -e VERSIOND_ROUTER_POOL_HOST=router-fixture \
    -e VERSIOND_ROUTER_FLEET_CAPACITY=1 \
    -e PROXY_ROUTER_VERSION_CAPACITY=1 -e VERSIOND_VERSIONS=v6 \
    "$router_image" >/dev/null

# The native sidecar must report live before policy starts, without falsely
# reporting ready. This is the chart's startup/readiness dependency boundary.
for _ in $(seq 60); do
    if docker exec "$task_id-router" curl -fsS http://127.0.0.1:8404/livez \
        >/dev/null 2>&1; then break; fi
    sleep 0.2
done
docker exec "$task_id-router" curl -fsS http://127.0.0.1:8404/livez >/dev/null
if docker exec "$task_id-router" curl -fsS http://127.0.0.1:8404/readyz \
    >/dev/null 2>&1; then fail 'router became ready before nginx started'; fi

docker run -d --name "$task_id-policy" --network "container:$task_id-pod" \
    --user 0:0 --group-add 99 \
    --cap-drop ALL --cap-add NET_BIND_SERVICE --cap-add CHOWN \
    --cap-add SETUID --cap-add SETGID --security-opt no-new-privileges \
    -v "$tmpdir/tls:/etc/nginx/ssl:ro" \
    -e NGINX_MODE=both -e PROXY_PROTOCOL=true \
    -e PROXY_PROTOCOL_BIND_ADDRESS=127.0.0.1 \
    -e PROXY_PROTOCOL_TRUSTED_FROM=127.0.0.1/32 \
    -e PROXY_POLICY_READINESS_HOST=router-fixture \
    -e API_SERVICE_NAME=router-fixture -e NODE_SERVICE_NAME=router-fixture \
    -e VERSIOND_SERVICE_NAME=127.0.0.1 -e VERSIOND_SERVICE_IS_ABSOLUTE=true \
    -e VERSIOND_PORT=18081 "$policy_image" >/dev/null

for _ in $(seq 90); do
    if docker exec "$task_id-router" curl -fsS http://127.0.0.1:8404/readyz \
        >/dev/null 2>&1 && docker exec "$task_id-router" curl -fsS \
        'http://127.0.0.1:8404/readyz?version=v6' >/dev/null 2>&1; then break; fi
    sleep 0.2
done
docker exec "$task_id-router" curl -fsS http://127.0.0.1:8404/readyz >/dev/null
docker exec "$task_id-router" curl -fsS \
    'http://127.0.0.1:8404/readyz?version=v6' >/dev/null
client_ip=$(docker exec "$task_id-router" curl -fsS \
    -H 'X-Real-IP: 198.51.100.77' -H 'X-Forwarded-Proto: forged' \
    "http://$pod_ip/devshard/v6/client")
[ "$client_ip" = "$pod_ip|http" ] || fail "HTTP client identity changed: $client_ip"
tls_client=$(docker exec "$task_id-router" curl -fsS --cacert /fixture/ca.pem \
    -H 'X-Real-IP: 198.51.100.77' -H 'X-Forwarded-Proto: http' \
    "https://$pod_ip/devshard/v6/client")
[ "$tls_client" = "$pod_ip|https" ] || fail "HTTPS client identity changed: $tls_client"
docker exec "$task_id-fixture" python -c '
import socket, sys
for port in (18081, 8404):
    try:
        s = socket.create_connection((sys.argv[1], port), timeout=2)
    except OSError:
        continue
    s.close()
    raise SystemExit("private listener exposed on pod IP: " + str(port))
' "$pod_ip"

docker exec "$task_id-router" curl -fsS --no-buffer --max-time 30 \
    "http://$pod_ip/devshard/v6/sessions/test/slow" >"$tmpdir/stream" &
stream_pid=$!
for _ in $(seq 60); do
    if docker exec "$task_id-router" curl -fsS http://router-fixture:9000/started \
        >/dev/null 2>&1; then break; fi
    sleep 0.1
done
docker exec "$task_id-router" curl -fsS http://router-fixture:9000/started >/dev/null

# Certificate rotation uses nginx -s reload. Exercise it with an accepted SSE
# response still open, under the chart's exact UID/groups and dropped caps.
worker_pids() {
    docker exec "$task_id-policy" /bin/sh -c '
        for file in /proc/[0-9]*/comm; do
            if [ "$(cat "$file" 2>/dev/null)" = nginx ]; then
                process=${file%/comm}
                pid=${process##*/}
                [ "$pid" = 1 ] || printf "%s\n" "$pid"
            fi
        done' | sort -n
}
workers_before=$(worker_pids)
[ -n "$workers_before" ] || fail 'nginx did not create a worker'
docker exec "$task_id-policy" nginx -s reload >/dev/null
workers_after=$workers_before
for _ in $(seq 60); do
    workers_after=$(worker_pids)
    if [ "$workers_after" != "$workers_before" ]; then break; fi
    sleep 0.1
done
[ "$workers_after" != "$workers_before" ] || fail 'reload did not replace nginx workers'
kill -0 "$stream_pid" || fail 'reload interrupted the active SSE response'
tls_client=$(docker exec "$task_id-router" curl -fsS --cacert /fixture/ca.pem \
    "https://$pod_ip/devshard/v6/client")
[ "$tls_client" = "$pod_ip|https" ] || fail 'new nginx worker did not serve HTTPS'

docker stop --time 30 "$task_id-policy" >"$tmpdir/stop" &
stop_pid=$!
for _ in $(seq 60); do
    if ! docker exec "$task_id-router" curl --haproxy-protocol -fsS \
        http://127.0.0.1/health >/dev/null 2>&1; then break; fi
    sleep 0.1
done
kill -0 "$stream_pid" || fail 'stream ended before graceful drain completed'
docker exec "$task_id-router" curl -fsS http://router-fixture:9000/release >/dev/null
wait "$stream_pid" || fail 'nginx drain reset an accepted stream'
grep -q '^data: completed$' "$tmpdir/stream" || fail 'stream response was truncated'
wait "$stop_pid"
[ "$(docker inspect -f '{{.State.ExitCode}}' "$task_id-policy")" = 0 ] \
    || fail 'nginx needed forced termination'
docker logs "$task_id-policy" >"$tmpdir/policy.log" 2>&1
if grep -Ei 'kill\(.*failed|operation not permitted|permission denied' "$tmpdir/policy.log"; then
    fail 'nginx encountered a capability or file-permission failure'
fi
docker exec "$task_id-router" curl -fsS http://127.0.0.1:8404/livez >/dev/null
docker stop --time 10 "$task_id-router" >/dev/null
[ "$(docker inspect -f '{{.State.ExitCode}}' "$task_id-router")" = 0 ] \
    || fail 'router needed forced termination'
echo 'pod-routing: ok'
