#!/usr/bin/env bash
# One HAProxy process with `bind proto h2` and `hard-stop-after`, started the
# way the entrypoint starts it (`haproxy -W -db`), and a Go client that holds
# a Watch and a Chat on one HTTP/2 connection.
#
# Soft stop: SIGUSR1 while the Chat body is still held. The body still
# arrives, the listener is closed, and once the client ends its Watch stream
# HAProxy exits on its own, long before hard-stop-after, with the TCP
# connection still open.
#
# Hard stop: SIGUSR1 while a Watch is held and never released. HAProxy exits
# at hard-stop-after, so a held stream cannot outlive the stop budget.
set -Eeuo pipefail

haproxy_image=${HAPROXY_IMAGE:-haproxy:3.2-alpine}
tmpdir=$(mktemp -d)
# The container runs as the unprivileged haproxy user and reads the config
# through a bind mount, so the directory and the files must be world-readable.
chmod 755 "$tmpdir"
proxy=
client_pid=
backend_pid=
trap cleanup EXIT

fail() {
    failed=1
    echo "soft-stop_test: $*" >&2
    exit 1
}

cleanup() {
    if [[ -n $proxy ]]; then
        docker logs "$proxy" >"$tmpdir/proxy.log" 2>&1 || true
        docker rm -f "$proxy" >/dev/null 2>&1 || true
    fi
    if [[ -n $client_pid ]]; then
        kill "$client_pid" >/dev/null 2>&1 || true
        wait "$client_pid" >/dev/null 2>&1 || true
    fi
    if [[ -n $backend_pid ]]; then
        kill "$backend_pid" >/dev/null 2>&1 || true
    fi
    if [[ ${failed:-0} -ne 0 ]]; then
        echo "--- proxy ---" >&2
        cat "$tmpdir/proxy.log" >&2 || true
        echo "--- paths ---" >&2
        cat "$tmpdir/paths" >&2 || true
        echo "--- client ---" >&2
        cat "$tmpdir/client.status" "$tmpdir/client.err" >&2 || true
    fi
    rm -rf "$tmpdir"
}

command -v python3 >/dev/null || fail "python3 is required for the backend"
command -v docker >/dev/null || fail "docker is required"
command -v go >/dev/null || fail "go is required for the client"

port=$(python3 -c 'import socket; s=socket.socket(); s.bind(("0.0.0.0", 0)); print(s.getsockname()[1]); s.close()')
cat >"$tmpdir/backend.py" <<'EOF'
import sys, time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

class Handler(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"

    def do_GET(self):
        with open(sys.argv[2], "a", encoding="utf-8") as paths:
            paths.write(self.path + "\n")
        if self.path.endswith("PeerAuthService/Watch"):
            # A Watch answers only when the peer ends it or the proxy resets it.
            time.sleep(120)
            body = b"watch-ok"
            self.send_response(200)
            self.send_header("content-type", "text/plain")
            self.send_header("content-length", str(len(body)))
            self.end_headers()
            self.wfile.write(body)
            return
        body = b"chat-ok"
        self.send_response(200)
        self.send_header("content-type", "text/plain")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
        self.wfile.flush()
        time.sleep(6)
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return

    def handle(self):
        # A reset Watch (hard stop) closes this socket under the handler.
        try:
            super().handle()
        except (ConnectionResetError, BrokenPipeError):
            pass

ThreadingHTTPServer(("0.0.0.0", int(sys.argv[1])), Handler).serve_forever()
EOF
: >"$tmpdir/paths"
python3 "$tmpdir/backend.py" "$port" "$tmpdir/paths" &
backend_pid=$!
for _ in $(seq 1 50); do
    python3 -c "import socket; socket.create_connection(('127.0.0.1', $port), 1).close()" 2>/dev/null && break
    sleep 0.1
done
python3 -c "import socket; socket.create_connection(('127.0.0.1', $port), 1).close()" 2>/dev/null \
    || fail "backend did not accept connections on $port"

# The same global and defaults shape as the rendered router config: proto h2
# frontend, hard-stop-after in global. The value is the only difference
# between the two runs.
write_config() {
    cat >"$tmpdir/$1.cfg" <<EOF
global
    log stdout format raw local0
    hard-stop-after $2s
    tune.h2.max-concurrent-streams 100

defaults
    mode http
    option httplog
    log global
    timeout connect 3s
    timeout client 180s
    timeout server 180s
    timeout http-request 60s

frontend fe
    bind :8080 proto h2
    default_backend app

backend app
    server app host.docker.internal:${port}
EOF
    chmod 644 "$tmpdir/$1.cfg"
}

launch_proxy() {
    proxy=gonka-soft-stop-$1
    docker rm -f "$proxy" >/dev/null 2>&1 || true
    docker run -d --name "$proxy" \
        --add-host=host.docker.internal:host-gateway \
        -p 127.0.0.1::8080 \
        -v "$tmpdir/$2.cfg:/tmp/haproxy.cfg:ro" \
        "$haproxy_image" \
        haproxy -W -db -f /tmp/haproxy.cfg \
        >/dev/null
}

proxy_running() {
    [[ $(docker inspect --format '{{.State.Running}}' "$proxy" 2>/dev/null) == true ]]
}

# Docker publishes the host port before HAProxy binds inside the container,
# and a closed listener still accepts the TCP connect at the port proxy. Only
# an HTTP/2 preface answered with SETTINGS proves that HAProxy is serving.
h2_answers() {
    python3 - "$1" <<'EOF'
import socket, sys
try:
    s = socket.create_connection(("127.0.0.1", int(sys.argv[1])), 2)
    s.sendall(b"PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n" + b"\x00\x00\x00\x04\x00\x00\x00\x00\x00")
    s.settimeout(2)
    data = s.recv(64)
except (OSError, socket.timeout):
    data = b""
sys.exit(0 if data else 1)
EOF
}

wait_listener() {
    local hostport=$1 _
    for _ in $(seq 1 100); do
        h2_answers "${hostport##*:}" && return 0
        sleep 0.1
    done
    return 1
}

# Seconds since the container started, for the timing assertions.
proxy_age() {
    python3 - "$(docker inspect --format '{{.State.StartedAt}}' "$proxy")" <<'EOF'
import datetime, sys
started = datetime.datetime.fromisoformat(sys.argv[1].replace("Z", "+00:00")[:26] + "+00:00") if "." in sys.argv[1] else datetime.datetime.fromisoformat(sys.argv[1].replace("Z", "+00:00"))
print(int((datetime.datetime.now(datetime.timezone.utc) - started).total_seconds()))
EOF
}

wait_for() {
    local pattern=$1 tries=$2 _
    for _ in $(seq 1 "$tries"); do
        grep -q "$pattern" "$tmpdir/client.status" 2>/dev/null && return 0
        sleep 0.25
    done
    return 1
}

wait_exit() {
    local tries=$1 _
    for _ in $(seq 1 "$tries"); do
        proxy_running || return 0
        sleep 0.25
    done
    return 1
}

# One HTTP/2 connection. Mode "release": open Watch and Chat, read the Chat
# body, then end the Watch stream and keep the connection open and idle.
# Mode "hold": open Watch only and keep it until the proxy resets it.
cat >"$tmpdir/client.go" <<'EOF'
package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"golang.org/x/net/http2"
)

func note(path, line string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return
	}
	fmt.Fprintln(f, line)
	_ = f.Sync()
	_ = f.Close()
}

func main() {
	hostport, status, mode := os.Args[1], os.Args[2], os.Args[3]
	conn, err := net.Dial("tcp", hostport)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cc, err := (&http2.Transport{AllowHTTP: true}).NewClientConn(conn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	// Watch has no response until the peer ends it or the proxy resets it, so
	// RoundTrip blocks on the headers; the stream is open from its first frame.
	watchCtx, endWatch := context.WithCancel(context.Background())
	defer endWatch()
	watchReq, _ := http.NewRequestWithContext(watchCtx, http.MethodGet, "http://h2c/devshard.transport.v1.PeerAuthService/Watch", nil)
	note(status, "watch-open")
	watchEnd := make(chan string, 1)
	go func() {
		resp, err := cc.RoundTrip(watchReq)
		if err != nil {
			if watchCtx.Err() != nil {
				watchEnd <- "watch-ended"
			} else {
				watchEnd <- "watch-reset"
			}
			return
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		watchEnd <- "watch-done"
	}()
	if mode == "hold" {
		note(status, <-watchEnd)
		return
	}
	chatReq, _ := http.NewRequest(http.MethodGet, "http://h2c/v1/chat", nil)
	chat, err := cc.RoundTrip(chatReq)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chat:", err)
		os.Exit(1)
	}
	note(status, "chat-headers")
	body, err := io.ReadAll(chat.Body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chat body:", err)
		os.Exit(1)
	}
	note(status, string(body))
	// The peer ends its Watch (token refresh reopens it elsewhere). The TCP
	// connection stays open: the proxy must not need the client to hang up.
	endWatch()
	note(status, <-watchEnd)
	note(status, "watch-released")
	time.Sleep(120 * time.Second)
}
EOF
cat >"$tmpdir/go.mod" <<'EOF'
module h2client

go 1.23

require golang.org/x/net v0.49.0
EOF
(
    cd "$tmpdir"
    GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}" GOCACHE="${GOCACHE:-$HOME/.cache/go-build}" \
        go mod tidy >/dev/null 2>&1
    GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}" GOCACHE="${GOCACHE:-$HOME/.cache/go-build}" \
        go build -o client .
) || fail "client build failed"

run_client() {
    : >"$tmpdir/client.status"
    "$tmpdir/client" "$1" "$tmpdir/client.status" "$2" >"$tmpdir/client.out" 2>"$tmpdir/client.err" &
    client_pid=$!
}

stop_client() {
    if [[ -n $client_pid ]]; then
        kill "$client_pid" >/dev/null 2>&1 || true
        wait "$client_pid" >/dev/null 2>&1 || true
        client_pid=
    fi
}

# --- soft stop: streams finish, exit after the last one, well before hard-stop-after ---
write_config soft 60
launch_proxy "soft-$$" soft
hostport=$(docker port "$proxy" 8080 | head -n 1)
[[ -n $hostport ]] || fail "haproxy published no host port"
wait_listener "$hostport" || fail "haproxy did not open its listener"
run_client "$hostport" release
wait_for chat-headers 100 || fail "chat headers did not arrive: $(cat "$tmpdir/client.err")"
grep -q 'PeerAuthService/Watch' "$tmpdir/paths" || fail "watch did not reach the backend"
grep -q chat-ok "$tmpdir/client.status" && fail "chat body finished before soft stop was signaled"

docker kill --signal SIGUSR1 "$proxy" >/dev/null
signaled_at=$(proxy_age)
sleep 1
proxy_running || fail "proxy exited at soft stop while the chat body was still held"
if h2_answers "${hostport##*:}"; then
    fail "listener still serves new connections after soft stop"
fi
# The body arrives while the stop is pending; once the client ends its Watch
# the proxy may exit at once, so no liveness check belongs after this line.
wait_for chat-ok 60 || fail "chat body was cut off by soft stop: $(cat "$tmpdir/client.status")"
wait_for watch-released 20 || fail "client did not release its watch: $(cat "$tmpdir/client.status")"
wait_exit 40 || fail "haproxy did not exit after the last stream ended (connection still open): $(cat "$tmpdir/client.status")"
exited_at=$(proxy_age)
(( exited_at - signaled_at < 30 )) || fail "haproxy exited only at hard-stop-after: signaled ${signaled_at}s, exited ${exited_at}s"
grep -q 'exit status\|chat body:' "$tmpdir/client.err" && fail "client error: $(cat "$tmpdir/client.err")"
stop_client
docker rm -f "$proxy" >/dev/null
proxy=

# --- hard stop: a held Watch is reset at hard-stop-after and the process exits ---
write_config hard 5
launch_proxy "hard-$$" hard
hostport=$(docker port "$proxy" 8080 | head -n 1)
wait_listener "$hostport" || fail "haproxy did not open its listener for the hard stop"
run_client "$hostport" hold
wait_for watch-open 100 || fail "watch did not open: $(cat "$tmpdir/client.err")"
for _ in $(seq 1 100); do
    grep -q 'PeerAuthService/Watch' "$tmpdir/paths" && break
    sleep 0.1
done
grep -q 'PeerAuthService/Watch' "$tmpdir/paths" || fail "held watch did not reach the backend"
docker kill --signal SIGUSR1 "$proxy" >/dev/null
signaled_at=$(proxy_age)
sleep 2
proxy_running || fail "proxy exited before hard-stop-after with a watch held"
wait_exit 60 || fail "haproxy did not exit at hard-stop-after with a watch held"
exited_at=$(proxy_age)
(( exited_at - signaled_at <= 12 )) || fail "hard stop took too long: signaled ${signaled_at}s, exited ${exited_at}s"
wait_for watch-reset 20 || fail "held watch was not reset by the hard stop: $(cat "$tmpdir/client.status")"
stop_client

echo "soft-stop_test: ok"
