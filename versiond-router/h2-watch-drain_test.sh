#!/usr/bin/env bash
# Parser checks, then one HAProxy process: Chat sends headers and holds the
# body, and SIGUSR1 closes only Watch. The body still arrives. The helper
# opens its runtime CLI only after SIGUSR1, so an idle gap longer than
# `stats timeout` still drains. If that CLI cannot be opened, soft-stop is
# not forwarded.
set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
drain=$script_dir/h2-watch-drain.sh
haproxy_image=${HAPROXY_IMAGE:-haproxy:3.2-alpine}
tmpdir=$(mktemp -d)
proxy=
client_pid=
backend_pid=
trap cleanup EXIT

fail() {
    failed=1
    echo "h2-watch-drain_test: $*" >&2
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

expect_ids() {
    local name=$1 table=$2 sess=$3 want=$4 got=
    got=$("$drain" --select "$table" "$sess" | sort)
    want=$(printf '%s\n' "$want" | sed '/^$/d' | sort)
    [[ $got == "$want" ]] || fail "$name: got [$got] want [$want]"
}

mkdir -p "$tmpdir/fix"
cat >"$tmpdir/fix/table" <<'EOF'
# table: h2_watch_ids, type: string, size:200000, used:3
0x1: key=3 use=1 exp=1 shard=0 gpc0=0
0x2: key=4 use=1 exp=1 shard=0 gpc0=0
0x3: key=9 use=1 exp=1 shard=0 gpc0=0
EOF
cat >"$tmpdir/fix/sess" <<'EOF'
0xffffa48c9aa0: [ts] id=1 proto=tcpv4 source=10.0.0.2:4000
  h2c=0xffffa17f1550 mux=H2 h2s.id=1
0xffffa48c9bb0: [ts] id=3 proto=tcpv4 source=10.0.0.2:4000
  h2c=0xffffa17f1550 mux=H2 h2s.id=3
0xffffa48c9cc0: [ts] id=3 proto=tcpv4 source=10.0.0.3:4001
  mux=H1
0xffffa48c9dd0: [ts] id=4 proto=tcpv4 source=10.0.0.4:4002 h2c=0xffffa17f1770 mux=H2
0xffffa48c9ee0: [ts] id=9 proto=tcpv4 source=10.0.0.5:4003
  backend=app
0xffffa48ca000: [ts] id=6 proto=unix_stream frontend=GLOBAL
EOF

# id=1 is a Chat on the same connection as Watch id=3. id=3 on HTTP/1.1 shares
# the Watch key and must be kept. id=9 is in the table but is not HTTP/2.
expect_ids "watch streams only" "$tmpdir/fix/table" "$tmpdir/fix/sess" \
    $'0xffffa48c9bb0\n0xffffa48c9dd0'

runtime_show() {
    printf '%s\n' "$1" | docker exec -i "$proxy" socat -t 1 stdio /var/run/haproxy/reconciler.sock
}

launch_proxy() {
    proxy=gonka-h2-watch-drain-$1
    docker rm -f "$proxy" >/dev/null 2>&1 || true
    docker run -d --name "$proxy" --user root \
        --add-host=host.docker.internal:host-gateway \
        -p 127.0.0.1::8080 \
        -v "$drain:/usr/local/lib/versiond-router/h2-watch-drain.sh:ro" \
        -v "$tmpdir/haproxy.cfg:/tmp/haproxy.cfg:ro" \
        "$haproxy_image" \
        /bin/sh -c 'apk add --no-cache socat >/dev/null && mkdir -p /var/run/haproxy && exec /usr/local/lib/versiond-router/h2-watch-drain.sh --supervise "$(command -v haproxy)" /tmp/haproxy.cfg' \
        >/dev/null
}

wait_haproxy() {
    local _
    for _ in $(seq 1 300); do
        if runtime_show 'show info' 2>/dev/null | grep -q '^Name: HAProxy$'; then
            return 0
        fi
        sleep 0.2
    done
    return 1
}

command -v python3 >/dev/null || fail "python3 is required for the backend"
command -v docker >/dev/null || fail "docker is required"

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
            time.sleep(60)
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
        time.sleep(8)
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return

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

cat >"$tmpdir/haproxy.cfg" <<EOF
global
    stats socket /var/run/haproxy/reconciler.sock level admin mode 600
    # Shorter than the idle below. The helper must not keep a CLI open across
    # this gap. It opens one when SIGUSR1 arrives, then soft-stops HAProxy.
    stats timeout 2s
    tune.h2.max-concurrent-streams 100

defaults
    mode http
    timeout connect 3s
    timeout client 60s
    timeout server 60s
    timeout http-request 60s

frontend fe
    bind :8080 proto h2
    http-request set-var-fmt(txn.wid) %[txn.id32] if { path_reg PeerAuthService/Watch\$\$ }
    http-request track-sc0 var(txn.wid) table h2_watch_ids if { var(txn.wid) -m found }
    default_backend app

backend app
    server app host.docker.internal:${port}

backend h2_watch_ids
    stick-table type string len 16 size 1000 expire 7d store gpc0
EOF

suffix=$$
# Unlink the stats socket after the idle gap. A CLI opened at process start
# would still be connected to the inode and would still forward SIGUSR1.
# Opening at stop time fails, and the listener has to stay up.
launch_proxy "held-$suffix"
wait_haproxy || fail "haproxy did not open the runtime socket for the hold check"
sleep 3
docker exec "$proxy" rm -f /var/run/haproxy/reconciler.sock
docker kill --signal SIGUSR1 "$proxy" >/dev/null
sleep 2
[[ $(docker inspect --format '{{.State.Running}}' "$proxy") == true ]] \
    || fail "proxy exited when the runtime CLI could not be opened"
held_port=$(docker port "$proxy" 8080 | head -n 1 | awk -F: '{print $NF}')
[[ -n $held_port ]] || fail "hold-check haproxy published no host port"
python3 -c "import socket; socket.create_connection(('127.0.0.1', int('$held_port')), 2).close()" \
    || fail "listener closed when the runtime CLI could not be opened"
held_logs=$(docker logs "$proxy" 2>&1 || true)
grep -q 'runtime CLI did not open; soft-stop held' <<<"$held_logs" \
    || fail "soft-stop hold was not logged: $held_logs"
if grep -E '^h2-watch-drain: soft-stop$' <<<"$held_logs"; then
    fail "soft-stop was forwarded without a runtime CLI"
fi
docker rm -f "$proxy" >/dev/null

launch_proxy "$suffix"
wait_haproxy || fail "haproxy did not open the runtime socket"
# Sit past stats timeout before any stream exists. A CLI opened at process
# start would already be gone, which is the gap a cold `go mod tidy` hits in CI.
sleep 3

hostport=$(docker port "$proxy" 8080 | head -n 1)
[[ -n $hostport ]] || fail "haproxy published no host port"
cat >"$tmpdir/client.go" <<'EOF'
package main

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

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
	conn, err := net.Dial("tcp", os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	cc, err := (&http2.Transport{AllowHTTP: true}).NewClientConn(conn)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	status := os.Args[2]
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		req, err := http.NewRequest(http.MethodGet, "http://h2c/devshard.transport.v1.PeerAuthService/Watch", nil)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			note(status, "watch-done")
			return
		}
		resp, err := cc.RoundTrip(req)
		if err != nil {
			fmt.Fprintln(os.Stderr, "watch:", err)
			note(status, "watch-done")
			return
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, resp.Body)
		note(status, "watch-done")
	}()
	req, err := http.NewRequest(http.MethodGet, "http://h2c/v1/chat", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	resp, err := cc.RoundTrip(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chat:", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	note(status, "chat-headers")
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		fmt.Fprintln(os.Stderr, "chat body:", err)
		os.Exit(1)
	}
	note(status, string(body))
	<-watchDone
}
EOF
cat >"$tmpdir/go.mod" <<'EOF'
module h2client

go 1.23

require golang.org/x/net v0.49.0
EOF
: >"$tmpdir/client.status"
(
    cd "$tmpdir"
    GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}" GOCACHE="${GOCACHE:-$HOME/Library/Caches/go-build}" \
        go mod tidy
    GOMODCACHE="${GOMODCACHE:-$HOME/go/pkg/mod}" GOCACHE="${GOCACHE:-$HOME/Library/Caches/go-build}" \
        go run . "$hostport" "$tmpdir/client.status"
) >"$tmpdir/client.out" 2>"$tmpdir/client.err" &
client_pid=$!

ready=0
for _ in $(seq 1 250); do
    if [[ -f $tmpdir/paths ]] && grep -q '/v1/chat' "$tmpdir/paths" && grep -q 'PeerAuthService/Watch' "$tmpdir/paths" \
        && grep -q 'chat-headers' "$tmpdir/client.status" && ! grep -q 'chat-ok' "$tmpdir/client.status"; then
        table=$(runtime_show 'show table h2_watch_ids' 2>/dev/null || true)
        runtime_show 'show sess all' >"$tmpdir/sess.dump" 2>/dev/null || true
        streams=$(grep -c 'h2c=' "$tmpdir/sess.dump" || true)
        if [[ $table == *'key='* && $streams -ge 2 ]]; then
            ready=1
            break
        fi
    fi
    if grep -q 'chat-ok' "$tmpdir/client.status" 2>/dev/null; then
        fail "chat body finished before the drain was signaled: $(cat "$tmpdir/client.status")"
    fi
    if [[ -s $tmpdir/client.err ]] && ! kill -0 "$client_pid" 2>/dev/null; then
        break
    fi
    sleep 0.2
done
[[ $ready == 1 ]] || fail "chat headers and watch were not both in flight: paths=[$(cat "$tmpdir/paths" 2>/dev/null)] table=[$(runtime_show 'show table h2_watch_ids' 2>/dev/null || true)] sess=[$(cat "$tmpdir/sess.dump" 2>/dev/null)] client=[$(cat "$tmpdir/client.err" 2>/dev/null)] status=[$(cat "$tmpdir/client.status" 2>/dev/null)]"
if docker exec "$proxy" test -e /tmp/h2-watch-drain.in; then
    fail "runtime CLI was opened before soft-stop"
fi

docker kill --signal SIGUSR1 "$proxy" >/dev/null
watch_closed=0
for _ in $(seq 1 20); do
    if grep -q 'watch-done' "$tmpdir/client.status"; then
        watch_closed=1
        break
    fi
    if grep -q 'chat-ok' "$tmpdir/client.status"; then
        fail "chat body arrived before Watch was closed: $(cat "$tmpdir/client.status")"
    fi
    sleep 0.25
done
[[ $watch_closed == 1 ]] || fail "Watch stayed open while the chat body was streaming: $(cat "$tmpdir/client.status")"
if grep -q 'chat-ok' "$tmpdir/client.status"; then
    fail "chat body was cut off with Watch: $(cat "$tmpdir/client.status")"
fi
[[ $(docker inspect --format '{{.State.Running}}' "$proxy") == true ]] \
    || fail "proxy exited while the chat body was still held"

chat_ok=0
for _ in $(seq 1 40); do
    if grep -q 'chat-ok' "$tmpdir/client.status"; then
        chat_ok=1
        break
    fi
    sleep 0.25
done
[[ $chat_ok == 1 ]] || fail "inference response was not delivered"

watch_done=0
for _ in $(seq 1 20); do
    if grep -q 'watch-done' "$tmpdir/client.status"; then
        watch_done=1
        break
    fi
    sleep 0.25
done
[[ $watch_done == 1 ]] || fail "watch stayed open after the inference finished"

stopped=0
for _ in $(seq 1 40); do
    if [[ $(docker inspect --format '{{.State.Running}}' "$proxy") == false ]]; then
        stopped=1
        break
    fi
    sleep 0.25
done
[[ $stopped == 1 ]] || fail "soft-stop did not finish after watch was released"
stop_logs=$(docker logs "$proxy" 2>&1 || true)
grep -q '^h2-watch-drain: soft-stop$' <<<"$stop_logs" \
    || fail "soft-stop was not forwarded after the runtime CLI opened: $stop_logs"
if grep -F 'soft-stop held' <<<"$stop_logs"; then
    fail "soft-stop was held on the success path: $stop_logs"
fi

echo "h2-watch-drain_test: ok"
