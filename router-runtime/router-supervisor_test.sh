#!/usr/bin/env bash
# Real-image regression for catalog restart, HAProxy reload, and stream drain.
set -Eeuo pipefail
cd "$(dirname "$0")/.."
task_id="gonka-router-lifecycle-$$"
tmpdir=$(mktemp -d)
containers=("$task_id-fixture" "$task_id-versiond-router" "$task_id-proxy-router")
images=("$task_id-versiond-router" "$task_id-proxy-router")
cleanup() {
    status=$?
    if [ "$status" -ne 0 ]; then
        for container in "${containers[@]}"; do
            docker logs --tail 60 "$container" >&2 2>/dev/null || true
        done
    fi
    docker rm -f "${containers[@]}" >/dev/null 2>&1 || true
    docker network rm "$task_id" >/dev/null 2>&1 || true
    docker image rm "${images[@]}" >/dev/null 2>&1 || true
    rm -rf "$tmpdir"
}
trap cleanup EXIT
fail() { echo "router-supervisor: $*" >&2; exit 1; }

cat >"$tmpdir/fixture.py" <<'PY'
import http.server
import threading

release = {}

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = b"ready\n"
        if self.path == "/versions":
            body = b'{"versions":[{"name":"v6"},{"name":"v7"}]}'
        if self.path.startswith("/release/"):
            release.setdefault(self.path.split("/")[-1], threading.Event()).set()
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        event = release.setdefault(self.path.split("/")[-2], threading.Event())
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Connection", "close")
        self.end_headers()
        self.wfile.write(b"data: started\n\n")
        self.wfile.flush()
        if event.wait(45):
            self.wfile.write(b"data: completed\n\n")
            self.wfile.flush()

    def log_message(self, *_):
        pass

for port in (8080, 8404):
    server = http.server.ThreadingHTTPServer(("", port), Handler)
    threading.Thread(target=server.serve_forever, daemon=True).start()
threading.Event().wait()
PY
cat >"$tmpdir/client.py" <<'PY'
import http.client
import sys

connection = http.client.HTTPConnection(sys.argv[1], int(sys.argv[2]), timeout=45)
connection.request("POST", "/v6/sessions/" + sys.argv[1] + "/stream", body=b"{}")
response = connection.getresponse()
assert response.status == 200, response.status
for line in response:
    sys.stdout.buffer.write(line)
    sys.stdout.buffer.flush()
PY

docker network create "$task_id" >/dev/null
docker run -d --name "$task_id-fixture" --network "$task_id" \
    --network-alias fixture -v "$tmpdir:/fixture:ro" \
    python:3.12-alpine python /fixture/fixture.py >/dev/null

for component in versiond-router proxy-router; do
    container="$task_id-$component"
    docker build -q -t "$container" -f "$component/Dockerfile" . >/dev/null
    args=()
    if [ "$component" = versiond-router ]; then
        port=8080
        args=(-e VERSIOND_POOL_HOST=fixture -e VERSIOND_ROUTER_POOL_SLOTS=1 \
            -e VERSIOND_ROUTER_VERSION_CAPACITY=1)
    else
        port=18081
        args=(-e VERSIOND_ROUTER_POOL_HOST=fixture -e VERSIOND_ROUTER_FLEET_CAPACITY=1 \
            -e PROXY_ROUTER_VERSION_CAPACITY=1 -e PROXY_POLICY_POOL_SLOTS=1)
    fi
    docker run -d --name "$container" --network "$task_id" --network-alias "$component" \
        --user 99:99 --cap-drop ALL --cap-add NET_BIND_SERVICE \
        --security-opt no-new-privileges \
        -e VERSIOND_VERSIONS=v6 \
        -e VERSIOND_ROUTING_CATALOG_URL=http://fixture:8080/versions \
        -e VERSIOND_ROUTING_CATALOG_POLL_SECONDS=1 "${args[@]}" "$container" >/dev/null

    ready() {
        docker exec "$container" curl -fsS \
            "http://127.0.0.1:8404/readyz?version=${1:-v6}" >/dev/null 2>&1
    }
    for _ in $(seq 150); do ready v7 && break; sleep 0.2; done
    ready v7 || fail "$component did not publish the dynamic catalog version"

    catalog_pid() {
        docker exec "$container" sh -c '
            for file in /proc/[0-9]*/cmdline; do
                args=$(tr "\000" " " 2>/dev/null < "$file") || continue
                case "$args" in
                    "/bin/sh /usr/local/lib/router-runtime/catalog-reconciler "*)
                        process=${file%/cmdline}; printf "%s\n" "${process##*/}" ;;
                esac
            done'
    }
    original_catalog=$(catalog_pid)
    [ -n "$original_catalog" ] || fail "$component catalog process is missing"
    docker exec "$container" kill -KILL "$original_catalog"
    replacement_catalog=
    for _ in $(seq 60); do
        replacement_catalog=$(catalog_pid)
        if [ -n "$replacement_catalog" ] && [ "$replacement_catalog" != "$original_catalog" ]; then break; fi
        sleep 0.2
    done
    [ -n "$replacement_catalog" ] && [ "$replacement_catalog" != "$original_catalog" ] \
        || fail "$component did not restart a failed catalog process"

    worker_pid() {
        docker exec "$container" sh -c \
            'printf "show info\n" | socat - UNIX-CONNECT:/var/run/haproxy/haproxy.sock | sed -n "s/^Pid: //p"'
    }
    original_worker=$(worker_pid)
    [ -n "$original_worker" ] || fail "$component worker PID is missing"
    docker kill --signal USR2 "$container" >/dev/null
    replacement_worker=$original_worker
    for _ in $(seq 150); do
        replacement_worker=$(worker_pid 2>/dev/null || true)
        if [ -n "$replacement_worker" ] && [ "$replacement_worker" != "$original_worker" ] && ready; then break; fi
        sleep 0.2
    done
    if [ -z "$replacement_worker" ] || [ "$replacement_worker" = "$original_worker" ] || ! ready; then
        fail "$component did not reload"
    fi

    docker exec "$task_id-fixture" python /fixture/client.py "$component" "$port" \
        >"$tmpdir/$component.stream" &
    stream_pid=$!
    for _ in $(seq 60); do
        grep -q 'data: started' "$tmpdir/$component.stream" && break
        sleep 0.1
    done
    grep -q 'data: started' "$tmpdir/$component.stream" \
        || fail "$component did not accept the POST stream"

    # Use the production image's stop signal and grace. A stuck reconciler used
    # to hold the master forever after its final worker had drained.
    docker stop --time 20 "$container" >"$tmpdir/$component.stop" &
    stop_pid=$!
    sleep 1
    kill -0 "$stream_pid" || fail "$component interrupted the accepted stream"
    [ "$(docker inspect -f '{{.State.Running}}' "$container")" = true ] \
        || fail "$component exited before its stream finished"
    docker exec "$task_id-fixture" python -c \
        'import urllib.request, sys; urllib.request.urlopen("http://fixture:8080/release/" + sys.argv[1]).read()' \
        "$component"
    wait "$stream_pid"
    grep -q 'data: completed' "$tmpdir/$component.stream" \
        || fail "$component truncated its accepted stream"
    wait "$stop_pid"
    [ "$(docker inspect -f '{{.State.ExitCode}}' "$container")" = 0 ] \
        || fail "$component required a forced stop after its stream drained"
    docker logs "$container" 2>&1 | grep 'reconciler exited with status 137; restarting' >/dev/null \
        || fail "$component did not report the reconciler restart"

    # An idle master may exit before the shell's signal-interrupted wait
    # returns. The container must report the master's exit 0, not status 138.
    for _ in $(seq 3); do
        docker start "$container" >/dev/null
        for _ in $(seq 60); do ready && break; sleep 0.2; done
        ready || fail "$component did not restart for idle soft-stop"
        docker stop --time 10 "$container" >/dev/null
        [ "$(docker inspect -f '{{.State.ExitCode}}' "$container")" = 0 ] \
            || fail "$component lost the exit status during idle soft-stop"
    done

    # Unexpected master failure must stop the catalog and preserve its status.
    docker start "$container" >/dev/null
    for _ in $(seq 60); do ready && break; sleep 0.2; done
    ready || fail "$component did not restart for master-failure test"
    master_pid=$(docker exec "$container" ps -o pid,ppid,comm | awk '$2 == 1 && $3 == "haproxy" { print $1 }')
    [ -n "$master_pid" ] || fail "$component master process is missing"
    docker exec "$container" kill -KILL "$master_pid"
    for _ in $(seq 60); do
        [ "$(docker inspect -f '{{.State.Running}}' "$container")" = false ] && break
        sleep 0.2
    done
    [ "$(docker inspect -f '{{.State.Running}} {{.State.ExitCode}}' "$container")" = 'false 137' ] \
        || fail "$component did not preserve the master failure status"
    echo "router-supervisor: $component catalog restart, reload, SSE/idle drain, and exit status passed"
done
