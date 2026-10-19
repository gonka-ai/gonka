#!/bin/sh
# Planner for non-HA peer-RPC projection. No HAProxy: the plan is the contract
# watch applies through the Runtime API.

set -eu

fail() {
    echo "nonha-peer-rpc_test: $*" >&2
    exit 1
}

command -v jq >/dev/null 2>&1 || fail "jq is required"

root=$(CDPATH='' cd -- "$(dirname -- "$0")" && pwd)
planner=$root/nonha-peer-rpc.sh
tmpdir=$(mktemp -d)
trap 'rm -rf "$tmpdir"' EXIT

plan() {
    NONHA_PEER_RPC_VERSION_MAP=/dev/null \
    NONHA_PEER_RPC_SLOT_MAP=/dev/null \
        "$planner" plan "$1" "$2" "$3" "$4"
}

cat > "$tmpdir/health.json" <<'EOF'
[
  {"name":"v4","status":"running"},
  {"name":"v4.2","status":"running"},
  {"name":"v6","status":"running"},
  {"name":"v5","status":"starting"},
  {"name":"bad/name","status":"running"}
]
EOF
cat > "$tmpdir/static" <<'EOF'
v4
EOF
cat > "$tmpdir/versions.dump" <<'EOF'
EOF
cat > "$tmpdir/slots.dump" <<'EOF'
0 versiond_routers_dynamic_1 __unassigned__
0 versiond_routers_dynamic_2 __unassigned__
0 versiond_routers_dynamic_10 __unassigned__
EOF

plan "$tmpdir/health.json" "$tmpdir/versions.dump" "$tmpdir/slots.dump" "$tmpdir/static" \
    > "$tmpdir/got"
cat > "$tmpdir/want" <<'EOF'
assign versiond_routers_dynamic_1 v4.2 v4.2
assign versiond_routers_dynamic_2 v6 v6
EOF
cmp -s "$tmpdir/want" "$tmpdir/got" || {
    diff -u "$tmpdir/want" "$tmpdir/got" >&2 || true
    fail "a running HTTP child was not split from a static version and a starting child"
}

cat > "$tmpdir/versions.dump" <<'EOF'
0 v4 versiond_routers_v4
0 v4.2 versiond_routers_dynamic_1
0 v6 versiond_routers_dynamic_2
EOF
cat > "$tmpdir/slots.dump" <<'EOF'
0 versiond_routers_dynamic_1 v4.2
0 versiond_routers_dynamic_2 v6
0 versiond_routers_dynamic_10 __unassigned__
EOF
plan "$tmpdir/health.json" "$tmpdir/versions.dump" "$tmpdir/slots.dump" "$tmpdir/static" \
    > "$tmpdir/got"
[ ! -s "$tmpdir/got" ] || fail "an already projected pair was assigned again: $(cat "$tmpdir/got")"

cat > "$tmpdir/health.json" <<'EOF'
[{"name":"v6","status":"running"},{"name":"v4","status":"draining"}]
EOF
plan "$tmpdir/health.json" "$tmpdir/versions.dump" "$tmpdir/slots.dump" "$tmpdir/static" \
    > "$tmpdir/got"
printf '%s\n' 'release versiond_routers_dynamic_1 v4.2' > "$tmpdir/want"
cmp -s "$tmpdir/want" "$tmpdir/got" || {
    diff -u "$tmpdir/want" "$tmpdir/got" >&2 || true
    fail "a child that left /healthz running did not release its twin"
}

# A crash after the slot was written and before the version map was published
# must reuse that slot, not leak it and not touch the static version.
cat > "$tmpdir/health.json" <<'EOF'
[{"name":"v6","status":"running"},{"name":"v4","status":"running"}]
EOF
cat > "$tmpdir/versions.dump" <<'EOF'
0 v4 versiond_routers_v4
EOF
cat > "$tmpdir/slots.dump" <<'EOF'
0 versiond_routers_dynamic_1 v6
0 versiond_routers_dynamic_2 __unassigned__
EOF
plan "$tmpdir/health.json" "$tmpdir/versions.dump" "$tmpdir/slots.dump" "$tmpdir/static" \
    > "$tmpdir/got"
printf '%s\n' 'assign versiond_routers_dynamic_1 v6 v6' > "$tmpdir/want"
cmp -s "$tmpdir/want" "$tmpdir/got" || {
    diff -u "$tmpdir/want" "$tmpdir/got" >&2 || true
    fail "a half-written v6 slot was not reused"
}

# An encoding nobody wants is cleared, and the static map entry is left alone.
cat > "$tmpdir/health.json" <<'EOF'
[]
EOF
cat > "$tmpdir/versions.dump" <<'EOF'
0 v4 versiond_routers_v4
EOF
cat > "$tmpdir/slots.dump" <<'EOF'
0 versiond_routers_dynamic_1 v4.2
0 versiond_routers_dynamic_2 __unassigned__
EOF
plan "$tmpdir/health.json" "$tmpdir/versions.dump" "$tmpdir/slots.dump" "$tmpdir/static" \
    > "$tmpdir/got"
printf '%s\n' 'reset versiond_routers_dynamic_1' > "$tmpdir/want"
cmp -s "$tmpdir/want" "$tmpdir/got" || {
    diff -u "$tmpdir/want" "$tmpdir/got" >&2 || true
    fail "an unclaimed slot survived an empty /healthz"
}

# A failed poll is logged once, and the next success is logged once.
# Later successes stay quiet. The stand-in answers the Runtime API so this
# check does not need socat.
py=
watch=
set +m 2>/dev/null || true
trap 'if [ -n "${py:-}" ]; then kill "$py" 2>/dev/null || true; fi
    if [ -n "${watch:-}" ]; then kill "$watch" 2>/dev/null || true; fi
    rm -rf "$tmpdir"' EXIT
command -v python3 >/dev/null 2>&1 || fail "python3 is required"
mkdir -p "$tmpdir/bin"
cat > "$tmpdir/bin/socat" <<'EOF'
#!/bin/sh
cat >/dev/null
printf '%s\n' 'Name: HAProxy'
EOF
chmod +x "$tmpdir/bin/socat"
: > "$tmpdir/versions.map"
: > "$tmpdir/slots.map"
python3 -u - > "$tmpdir/port" <<'PY' &
from http.server import BaseHTTPRequestHandler, HTTPServer

seen = {"n": 0}

class Handler(BaseHTTPRequestHandler):
    def do_GET(self):
        seen["n"] += 1
        if seen["n"] == 1:
            self.send_response(500)
            self.end_headers()
            return
        body = b"[]"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        return

httpd = HTTPServer(("127.0.0.1", 0), Handler)
print(httpd.server_address[1], flush=True)
httpd.serve_forever()
PY
py=$!
disown "$py" 2>/dev/null || true
i=0
while [ ! -s "$tmpdir/port" ]; do
    i=$((i + 1))
    [ "$i" -lt 50 ] || fail "projection log fixture did not listen"
    sleep 0.1
done
port=$(cat "$tmpdir/port")
PATH="$tmpdir/bin:$PATH" \
NONHA_PEER_RPC_VERSIOND_URL="http://127.0.0.1:${port}/healthz" \
NONHA_PEER_RPC_VERSION_MAP="$tmpdir/versions.map" \
NONHA_PEER_RPC_SLOT_MAP="$tmpdir/slots.map" \
NONHA_PEER_RPC_STATIC_MAP="$tmpdir/versions.map" \
NONHA_PEER_RPC_RUNTIME_SOCKET="$tmpdir/runtime.sock" \
NONHA_PEER_RPC_POLL_SECONDS=1 \
NONHA_PEER_RPC_FETCH_TIMEOUT_SECONDS=1 \
NONHA_PEER_RPC_RUNTIME_TIMEOUT_SECONDS=1 \
    "$planner" watch >"$tmpdir/watch.log" 2>&1 &
watch=$!
sleep 4
kill "$watch" 2>/dev/null || true
wait "$watch" 2>/dev/null || true
watch=
failed=$(grep -c 'non-HA peer RPC projection failed; keeping the last assignment' "$tmpdir/watch.log" || true)
recovered=$(grep -c 'non-HA peer RPC projection recovered' "$tmpdir/watch.log" || true)
[ "$failed" = 1 ] && [ "$recovered" = 1 ] || {
    cat "$tmpdir/watch.log" >&2
    fail "projection failure was not logged once on the way down and once on recovery (failed=$failed recovered=$recovered)"
}

echo "nonha-peer-rpc_test: ok"
