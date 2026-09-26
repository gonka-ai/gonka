#!/usr/bin/env bash
# Parser checks, then one HAProxy process: a held inference keeps Watch open,
# and finishing that inference lets SIGUSR1 close Watch.
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
        echo "--- drain ---" >&2
        cat "$tmpdir/drain.log" >&2 || true
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
# table: h2_stream_acct, type: string, size:200000, used:3
0x1: key=10.0.0.2:4000 use=1 exp=1 shard=0 gpc0=1 gpc1=0
0x2: key=10.0.0.3:4001 use=1 exp=1 shard=0 gpc0=1 gpc1=1
0x3: key=10.0.0.4:4002 use=0 exp=1 shard=0 gpc0=0 gpc1=0
0x4: key=10.0.0.5:4003 use=1 exp=1 shard=0 gpc0=2 gpc1=1
EOF
cat >"$tmpdir/fix/sess" <<'EOF'
0xffffa48c9aa0: id=0 proto=tcpv4 source=10.0.0.2:4000
  h2c=0xffffa17f1550 mux=H2
0xffffa48c9bb0: id=1 proto=tcpv4 source=10.0.0.2:4000
  h2c=0xffffa17f1550 mux=H2
0xffffa48c9cc0: id=2 proto=tcpv4 source=10.0.0.3:4001
  mux=H1
0xffffa48c9dd0: id=3 proto=tcpv4 source=10.0.0.3:4001
  h2c=0xffffa17f1660 mux=H2
0xffffa48c9ee0: id=4 proto=tcpv4 source=10.0.0.4:4002 h2c=0xffffa17f1770 mux=H2
0xffffa48c9ff0: id=5 proto=tcpv4 source=10.0.0.5:4003
  h2c=0xffffa17f1880 mux=H2
0xffffa48ca000: id=6 proto=unix_stream frontend=GLOBAL
EOF

# 10.0.0.2 still has inference in flight. 10.0.0.5 started two and finished
# one, so the Watch stays. 10.0.0.3 is idle but its HTTP/1.1 session is kept.
expect_ids "idle watches only" "$tmpdir/fix/table" "$tmpdir/fix/sess" \
    $'0xffffa48c9dd0\n0xffffa48c9ee0'

runtime_show() {
    printf '%s\n' "$1" | docker exec -i "$proxy" socat -t 1 stdio /var/run/haproxy/reconciler.sock
}

h2_count() {
    runtime_show 'show sess all' | grep -c 'h2c=' || true
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
        else:
            time.sleep(8)
            body = b"chat-ok"
        self.send_response(200)
        self.send_header("content-type", "text/plain")
        self.send_header("content-length", str(len(body)))
        self.end_headers()
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
    tune.h2.max-concurrent-streams 100

defaults
    mode http
    timeout connect 3s
    timeout client 60s
    timeout server 60s
    timeout http-request 60s

frontend fe
    bind :8080 proto h2
    http-request set-var(txn.h2watch) str(1) if { path_reg PeerAuthService/Watch\$\$ }
    http-request set-var-fmt(txn.ckey) %[src]:%[src_port]
    http-request track-sc0 var(txn.ckey) table h2_stream_acct
    http-request sc-inc-gpc0(0) if !{ var(txn.h2watch) -m str 1 }
    http-after-response sc-inc-gpc1(0) if !{ var(txn.h2watch) -m str 1 }
    default_backend app

backend app
    server app host.docker.internal:${port}

backend h2_stream_acct
    stick-table type string len 80 size 1000 expire 1h store gpc0,gpc1
EOF

suffix=$$
proxy=gonka-h2-watch-drain-$suffix
docker rm -f "$proxy" >/dev/null 2>&1 || true
: >"$tmpdir/drain.log"
docker run -d --name "$proxy" --user root \
    --add-host=host.docker.internal:host-gateway \
    -p 127.0.0.1::8080 \
    -e H2_WATCH_DRAIN_LOG=/tmp/drain.log \
    -v "$tmpdir/drain.log:/tmp/drain.log" \
    -v "$drain:/usr/local/lib/versiond-router/h2-watch-drain.sh:ro" \
    -v "$tmpdir/haproxy.cfg:/tmp/haproxy.cfg:ro" \
    "$haproxy_image" \
    /bin/sh -c 'apk add --no-cache socat >/dev/null && mkdir -p /var/run/haproxy && exec /usr/local/lib/versiond-router/h2-watch-drain.sh --supervise "$(command -v haproxy)" /tmp/haproxy.cfg' \
    >/dev/null

for _ in $(seq 1 100); do
    if runtime_show 'show info' 2>/dev/null | grep -q '^Name: HAProxy$'; then
        break
    fi
    sleep 0.2
done
runtime_show 'show info' 2>/dev/null | grep -q '^Name: HAProxy$' || fail "haproxy did not open the runtime socket"

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
    if [[ -f $tmpdir/paths ]] && grep -q '/v1/chat' "$tmpdir/paths" && grep -q 'PeerAuthService/Watch' "$tmpdir/paths"; then
        table=$(runtime_show 'show table h2_stream_acct' 2>/dev/null || true)
        runtime_show 'show sess all' >"$tmpdir/sess.dump" 2>/dev/null || true
        streams=$(grep -c 'h2c=' "$tmpdir/sess.dump" || true)
        if [[ $table == *'gpc0=1 '* && $table == *'gpc1=0'* && $streams -ge 2 ]]; then
            ready=1
            break
        fi
        if [[ $table == *'gpc1=0'* ]]; then
            fail "inference was in flight but not both streams were visible (streams=$streams) table=[$table] sess=[$(cat "$tmpdir/sess.dump")]"
        fi
        fail "response was counted before the backend finished holding it (streams=$streams) table=[$table] sess=[$(cat "$tmpdir/sess.dump")]"
    fi
    if [[ -s $tmpdir/client.err ]] && ! kill -0 "$client_pid" 2>/dev/null; then
        break
    fi
    sleep 0.2
done
[[ $ready == 1 ]] || fail "inference and watch were not both in flight: paths=[$(cat "$tmpdir/paths" 2>/dev/null)] table=[$(runtime_show 'show table h2_stream_acct' 2>/dev/null || true)] client=[$(cat "$tmpdir/client.err" 2>/dev/null)]"

docker kill --signal SIGUSR1 "$proxy" >/dev/null
inflight=$tmpdir/inflight-table
inflight_sess=$tmpdir/inflight-sess
assert_inflight() {
    runtime_show 'show table h2_stream_acct' >"$inflight" || fail "lost the stick table during the inference"
    runtime_show 'show sess all' >"$inflight_sess" || fail "lost sessions during the inference"
    grep -q 'gpc1=0' "$inflight" || fail "inference was counted finished while the backend was still holding it: $(cat "$inflight")"
    [[ $(grep -c 'h2c=' "$inflight_sess" || true) -ge 2 ]] || fail "a stream was closed while the inference was in flight"
    [[ -z $("$drain" --select "$inflight" "$inflight_sess") ]] || fail "drain selected a session while the inference was in flight: $("$drain" --select "$inflight" "$inflight_sess")"
    if grep -q 'chat-ok' "$tmpdir/client.status"; then
        fail "inference response arrived during the in-flight window"
    fi
}
assert_inflight
sleep 1
assert_inflight

chat_ok=0
for _ in $(seq 1 40); do
    if grep -q 'chat-ok' "$tmpdir/client.status"; then
        chat_ok=1
        break
    fi
    sleep 0.25
done
[[ $chat_ok == 1 ]] || fail "inference response was not delivered"

released=0
for _ in $(seq 1 25); do
    if [[ $(h2_count) -eq 0 ]]; then
        released=1
        break
    fi
    sleep 0.2
done
[[ $released == 1 ]] || fail "watch was still open after the inference finished"

stopped=0
for _ in $(seq 1 50); do
    if [[ $(docker inspect --format '{{.State.Running}}' "$proxy") == false ]]; then
        stopped=1
        break
    fi
    sleep 0.2
done
[[ $stopped == 1 ]] || fail "soft-stop did not finish after watch was released"

echo "h2-watch-drain_test: ok"
