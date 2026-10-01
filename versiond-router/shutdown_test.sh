#!/usr/bin/env bash
set -euo pipefail

repo_root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
tmpdir=$(mktemp -d)
suffix="$$-$RANDOM"
network="gonka-router-shutdown-$suffix"
backend="gonka-router-shutdown-backend-$suffix"
router="gonka-router-shutdown-router-$suffix"
image=${ROUTER_SHUTDOWN_IMAGE:-gonka-router-shutdown-test:$suffix}
built=false
client_pid=
cleanup() {
    docker rm -f "$router" "$backend" >/dev/null 2>&1 || true
    [[ -z $client_pid ]] || wait "$client_pid" 2>/dev/null || true
    docker network rm "$network" >/dev/null 2>&1 || true
    if [[ $built == true ]]; then docker image rm "$image" >/dev/null 2>&1 || true; fi
    rm -rf "$tmpdir"
}
trap cleanup EXIT
fail() { echo "shutdown_test: $*" >&2; docker logs "$router" >&2 || true; exit 1; }

if [[ -z ${ROUTER_SHUTDOWN_IMAGE:-} ]]; then
    docker build -q -t "$image" -f "$repo_root/versiond-router/Dockerfile" "$repo_root" >/dev/null
    built=true
fi
cat >"$tmpdir/backend.py" <<'PY'
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import time


class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path == "/versions":
            body = json.dumps({"versions": [{"name": "v5", "binary": "http://backend/v5.zip", "sha256": "a" * 64}]}).encode()
        elif self.path.endswith("/slow"):
            body = b"data: start\n\ndata: done\n\n"
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(b"data: start\n\n")
            self.wfile.flush()
            time.sleep(3)
            self.wfile.write(b"data: done\n\n")
            return
        else:
            body = b"ready\n"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


ThreadingHTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
PY
docker network create "$network" >/dev/null
docker run -d --name "$backend" --network "$network" --network-alias backend \
    -v "$tmpdir/backend.py:/backend.py:ro" python:3.12-alpine python /backend.py >/dev/null

start_router() {
    docker run -d --name "$router" --network "$network" --network-alias router \
        -e VERSIOND_POOL_HOST=backend -e VERSIOND_VERSIONS=v5 \
        -e VERSIOND_NON_HA_VERSIONS= -e GONKA_HA=true \
        -e VERSIOND_ROUTING_CATALOG_URL=http://backend:8080/versions \
        "$image" >/dev/null
    local ready=false
    for _ in $(seq 30); do
        if docker exec "$router" wget -qO- http://127.0.0.1:8404/readyz >/dev/null 2>&1; then
            ready=true
            break
        fi
        sleep 1
    done
    [[ $ready == true ]] || fail 'router did not become ready'
}
stop_router() {
    docker stop -t 8 "$router" >/dev/null
    [[ $(docker inspect -f '{{.State.ExitCode}}' "$router") == 0 ]] || \
        fail 'router needed a forced stop while the catalog reconciler was running'
}

start_router
stop_router
docker rm "$router" >/dev/null
echo 'shutdown_test: idle router exits without SIGKILL'

start_router
docker exec "$backend" python -u -c '
import urllib.request
with urllib.request.urlopen("http://router:8080/v5/sessions/shutdown/slow", timeout=15) as response:
    for line in response:
        print(line.decode(), end="", flush=True)
' >"$tmpdir/stream" &
client_pid=$!
for _ in $(seq 50); do
    grep -q '^data: start$' "$tmpdir/stream" && break
    sleep 0.1
done
grep -q '^data: start$' "$tmpdir/stream" || fail 'stream did not start'
stop_router
wait "$client_pid" || fail 'accepted stream was interrupted'
client_pid=
grep -q '^data: done$' "$tmpdir/stream" || fail 'stream did not finish'
echo 'shutdown_test: accepted stream finishes before router exits'
