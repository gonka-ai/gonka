#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
suffix=$$
base_image=gonka-versiond-router-fleet-test:$suffix
updated_image=gonka-versiond-router-fleet-updated-test:$suffix
image=$base_image
bad_image=gonka-versiond-router-fleet-bad-test:$suffix
proxy_image=gonka-proxy-router-fleet-test:$suffix
front=gonka-versiond-router-front-$suffix
back=gonka-versiond-router-back-$suffix
prefix=gonka-versiond-router-test-$suffix
fleet_id=gonka-versiond-router-test-$suffix
tmpdir=$(mktemp -d)
slots=(0 1 2)

cleanup() {
    local slot
    for slot in "${slots[@]}"; do
        VERSIOND_ROUTER_SLOT=$slot \
            VERSIOND_ROUTER_FRONT_NETWORK=$front \
            VERSIOND_ROUTER_BACK_NETWORK=$back \
            VERSIOND_ROUTER_IMAGE=$image \
            docker compose --project-directory "$script_dir" \
                --project-name "$prefix-$slot" \
                -f "$script_dir/versiond-router-slot/docker-compose.yml" \
                down --timeout 1 --remove-orphans >/dev/null 2>&1 || true
    done
    docker rm -f gonka-router-fleet-backend-$suffix \
        gonka-router-fleet-proxy-$suffix \
        gonka-router-fleet-probe-$suffix \
        gonka-router-fleet-duplicate-$suffix \
        gonka-router-fleet-orphan-$suffix \
        gonka-router-fleet-foreign-$suffix >/dev/null 2>&1 || true
    docker compose -p "gonka-router-fleet-main-$suffix" \
        -f "$tmpdir/main.yml" down --timeout 1 >/dev/null 2>&1 || true
    docker network rm "$front" "$back" \
        "gonka-versiond-router-orphan-$suffix" >/dev/null 2>&1 || true
    docker image rm "$base_image" "$updated_image" "$bad_image" \
        "$proxy_image" >/dev/null 2>&1 || true
    rm -rf "$tmpdir"
}
trap cleanup EXIT

fail() {
    echo "versiond-router-fleet_test: $*" >&2
    exit 1
}

cat >"$tmpdir/backend.py" <<'PY'
import http.server
import time


class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path.endswith("/slow"):
            body = b"start\ndone\n"
            self.send_response(200)
            self.send_header("Content-Type", "text/event-stream")
            self.send_header("Content-Length", str(len(body)))
            self.end_headers()
            self.wfile.write(b"start\n")
            self.wfile.flush()
            time.sleep(3)
            self.wfile.write(b"done\n")
            return
        body = b"ok"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        self.rfile.read(length)
        body = b"ok"
        self.send_response(200)
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


http.server.ThreadingHTTPServer(("", 8080), H).serve_forever()
PY

cat >"$tmpdir/config.env" <<EOF
VERSIOND_ROUTER_IMAGE=$image
VERSIOND_ROUTER_FRONT_NETWORK=$front
VERSIOND_ROUTER_BACK_NETWORK=$back
VERSIOND_ROUTER_PROJECT_PREFIX=$prefix
VERSIOND_ROUTER_FLEET_ID=$fleet_id
VERSIOND_ROUTER_FLEET_SLOTS="0 1 2"
VERSIOND_ROUTER_MIN_READY=2
VERSIOND_ROUTER_DRAIN_TIMEOUT_SECONDS=10
VERSIOND_ROUTER_START_TIMEOUT_SECONDS=30
VERSIOND_ROUTER_PULL_POLICY=missing
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=false
PROXY_ROUTER_CONTAINER=gonka-router-fleet-proxy-$suffix
VERSIOND_NON_HA_VERSIONS=
VERSIOND_VERSIONS=v4
EOF
fleet=(env GONKA_CONFIG_ENV="$tmpdir/config.env" "$script_dir/versiond-router-fleet.sh")

cat >"$tmpdir/main.yml" <<EOF
services:
  unrelated:
    image: alpine:3.21
    command: ["sleep", "300"]
    networks: [front]
networks:
  front:
    name: $front
    external: true
EOF

cat >"$tmpdir/bad-router-entrypoint.sh" <<'EOF'
#!/bin/sh
set -eu
cat >/tmp/haproxy.cfg <<'HAPROXY'
global
    log stdout format raw local0
defaults
    mode http
    timeout connect 1s
    timeout client 10s
    timeout server 10s
frontend data
    bind :8080
    http-request return status 200 content-type text/plain string "bad candidate\n"
frontend admin
    bind :8404
    http-request return status 200 content-type text/plain string "ready\n" if { path /readyz } !{ query -m found }
    http-request return status 503 content-type text/plain string "route unavailable\n"
HAPROXY
exec haproxy -W -db -f /tmp/haproxy.cfg
EOF
chmod +x "$tmpdir/bad-router-entrypoint.sh"
cat >"$tmpdir/bad-router.Dockerfile" <<EOF
FROM $image
COPY bad-router-entrypoint.sh /usr/local/bin/bad-router-entrypoint.sh
ENTRYPOINT ["/usr/local/bin/bad-router-entrypoint.sh"]
EOF
cat >"$tmpdir/updated-router.Dockerfile" <<EOF
FROM $image
LABEL ai.gonka.test-revision="updated"
EOF

"${fleet[@]}" prepare-networks
[[ $(docker network inspect --format \
    '{{index .Labels "ai.gonka.component"}}|{{index .Labels "ai.gonka.fleet"}}|{{index .Labels "ai.gonka.network-role"}}' \
    "$front") == "versiond-router-network|$fleet_id|front" ]] || fail \
    "front network does not carry stable fleet ownership"
[[ $(docker network inspect --format \
    '{{index .Labels "ai.gonka.component"}}|{{index .Labels "ai.gonka.fleet"}}|{{index .Labels "ai.gonka.network-role"}}' \
    "$back") == "versiond-router-network|$fleet_id|back" ]] || fail \
    "back network does not carry stable fleet ownership"
docker build -q -t "$image" "$script_dir/../../versiond-router" >/dev/null
docker build -q -t "$updated_image" \
    -f "$tmpdir/updated-router.Dockerfile" "$tmpdir" >/dev/null
docker build -q -t "$bad_image" -f "$tmpdir/bad-router.Dockerfile" "$tmpdir" >/dev/null
docker build -q -t "$proxy_image" "$script_dir/../../proxy-router" >/dev/null
docker run -d --name "gonka-router-fleet-backend-$suffix" \
    --network "$back" --network-alias versiond-pool \
    -v "$tmpdir/backend.py:/app.py:ro" \
    python:3.12-alpine python /app.py >/dev/null

"${fleet[@]}" up >/dev/null
"${fleet[@]}" status >/dev/null

# The standard updater calls `apply` on every run. An unchanged fleet must not
# pay a drain/recreate cycle merely because the updater is idempotently rerun.
declare -A initial_ids=()
for slot in "${slots[@]}"; do
    initial_ids[$slot]=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
done
"${fleet[@]}" apply >/dev/null
for slot in "${slots[@]}"; do
    current_id=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    [[ $current_id == "${initial_ids[$slot]}" ]] || fail \
        "idempotent fleet apply recreated unchanged slot $slot"
done

# A tag update with the same rendered environment is detected from the pulled
# image ID, not from the textual Compose image reference alone.
sed -i "s|^VERSIOND_ROUTER_IMAGE=.*|VERSIOND_ROUTER_IMAGE=$updated_image|" \
    "$tmpdir/config.env"
"${fleet[@]}" apply >/dev/null
for slot in "${slots[@]}"; do
    current_id=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    [[ -n $current_id && $current_id != "${initial_ids[$slot]}" ]] || fail \
        "fleet apply did not replace slot $slot after an image update"
done
image=$updated_image

# The default lock is tied to the deployment config, not to a caller's
# per-user XDG runtime directory. A second user context must see the same lock.
lock_file=$tmpdir/.gonka-versiond-router-$fleet_id.lock
lock_ready=$tmpdir/lock-ready
lock_release=$tmpdir/lock-release
chmod 0444 "$lock_file"
mkfifo "$lock_release"
(
    exec 8<"$lock_file"
    flock 8
    touch "$lock_ready"
    read -r _ <"$lock_release"
) &
lock_pid=$!
while [[ ! -f $lock_ready ]]; do sleep 0.05; done
if XDG_RUNTIME_DIR="$tmpdir/another-user" "${fleet[@]}" status \
    >"$tmpdir/lock.out" 2>&1; then
    printf 'release\n' >"$lock_release"
    wait "$lock_pid"
    fail "different XDG runtime directories acquired independent fleet locks"
fi
printf 'release\n' >"$lock_release"
wait "$lock_pid"
grep -q 'another fleet operation holds' "$tmpdir/lock.out" || fail \
    "contended deployment lock did not identify the active operation"

# Legacy pinning changes the escrow placement function. A mixed rolling fleet
# is invalid; the explicit maintenance path drains every old router before any
# router with the new contract becomes visible.
sed -i 's/^VERSIOND_NON_HA_VERSIONS=$/VERSIOND_NON_HA_VERSIONS="v4 v5"/' \
    "$tmpdir/config.env"
sed -i 's/^VERSIOND_VERSIONS=v4$/VERSIOND_VERSIONS=/' "$tmpdir/config.env"
cat >>"$tmpdir/config.env" <<'EOF'
VERSIOND_LEGACY_HOST=versiond-pool
EOF
if "${fleet[@]}" rollout >"$tmpdir/placement-rollout.out" 2>&1; then
    fail "ordinary rollout accepted a mixed placement contract"
fi
grep -q 'use maintenance-rollout to avoid mixed escrow placement' \
    "$tmpdir/placement-rollout.out" || {
    cat "$tmpdir/placement-rollout.out" >&2
    fail "placement-contract rejection did not explain the safe operation"
}
if "${fleet[@]}" maintenance-rollout >"$tmpdir/unacked-maintenance.out" 2>&1; then
    fail "maintenance outage was accepted without explicit acknowledgement"
fi
if VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
    "${fleet[@]}" maintenance-rollout \
    >"$tmpdir/maintenance-rollback.out" 2>&1; then
    fail "invalid maintenance candidate unexpectedly started"
fi
grep -q 'the exact previous router fleet was restored' \
    "$tmpdir/maintenance-rollback.out" || fail \
    "failed maintenance candidate did not restore the previous fleet"
cat >>"$tmpdir/config.env" <<'EOF'
VERSIOND_ROUTER_ALLOW_COARSE_READINESS=true
EOF
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
    "${fleet[@]}" maintenance-rollout >/dev/null
# Removing a route is a valid maintenance target. Only routes declared by the
# candidate remain postconditions; the removed v5 route must not force rollback.
sed -i 's/^VERSIOND_NON_HA_VERSIONS="v4 v5"$/VERSIOND_NON_HA_VERSIONS=v4/' \
    "$tmpdir/config.env"
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
    "${fleet[@]}" maintenance-rollout >/dev/null
for slot in "${slots[@]}"; do
    id=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    docker exec "$id" /bin/busybox wget -q -O /dev/null \
        'http://127.0.0.1:8404/readyz?version=v4' || fail \
        "maintenance placement change lost v4 on slot $slot"
done
sed -i 's/^VERSIOND_NON_HA_VERSIONS=v4$/VERSIOND_NON_HA_VERSIONS=/' \
    "$tmpdir/config.env"
sed -i 's/^VERSIOND_VERSIONS=$/VERSIOND_VERSIONS=v4/' "$tmpdir/config.env"
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
    "${fleet[@]}" maintenance-rollout >/dev/null
sed -i '/^VERSIOND_LEGACY_HOST=versiond-pool$/d' "$tmpdir/config.env"
sed -i '/^VERSIOND_ROUTER_ALLOW_COARSE_READINESS=true$/d' "$tmpdir/config.env"

docker run -d --name "gonka-router-fleet-proxy-$suffix" \
    --network "$front" --network-alias proxy-router \
    -e VERSIOND_NON_HA_VERSIONS= -e VERSIOND_VERSIONS=v4 \
    "$proxy_image" >/dev/null
docker run -d --name "gonka-router-fleet-probe-$suffix" \
    --network "$front" curlimages/curl:8.12.1 sleep 300 >/dev/null

probe=(docker exec "gonka-router-fleet-probe-$suffix" curl -fsS \
    --connect-timeout 2 --max-time 10)
for _ in $(seq 60); do
    docker exec "gonka-router-fleet-proxy-$suffix" /bin/busybox wget \
        -q -O /dev/null 'http://127.0.0.1:8404/readyz?version=v4' \
        >/dev/null 2>&1 && break
    sleep 0.25
done
docker exec "gonka-router-fleet-proxy-$suffix" /bin/busybox wget \
    -q -O /dev/null 'http://127.0.0.1:8404/readyz?version=v4' \
    >/dev/null \
    || fail "top HAProxy did not observe the router fleet"
"${fleet[@]}" verify-admission v4 >/dev/null || fail \
    "strict admission rejected the complete live v4 fleet"

# The commit gate receives the migration singleton's live-route baseline. A
# coarse-healthy fleet that cannot serve one of those routes must not be allowed
# to replace the reversible singleton-backed path.
sed -i 's/^VERSIOND_VERSIONS=v4$/VERSIOND_VERSIONS="v4 v5"/' \
    "$tmpdir/config.env"
if VERSIOND_ROUTER_START_TIMEOUT_SECONDS=2 \
    "${fleet[@]}" verify-admission v5 \
    >"$tmpdir/admission-missing-route.out" 2>&1; then
    fail "strict admission accepted a route-dead fleet"
fi
grep -q 'cannot serve required route v5' \
    "$tmpdir/admission-missing-route.out" || fail \
    "route-dead admission failure did not identify the missing baseline route"
sed -i 's/^VERSIOND_VERSIONS="v4 v5"$/VERSIOND_VERSIONS=v4/' \
    "$tmpdir/config.env"

# The same command used by the standard updater must notice a rendered Compose
# contract change and roll every slot through the reserve-protected path.
declare -A before_apply=()
for slot in "${slots[@]}"; do
    before_apply[$slot]=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
done
printf 'VERSIOND_ROUTER_ALLOW_COARSE_READINESS=true\n' >>"$tmpdir/config.env"
"${fleet[@]}" apply >/dev/null
for slot in "${slots[@]}"; do
    current_id=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    [[ -n $current_id && $current_id != "${before_apply[$slot]}" ]] || fail \
        "fleet apply did not replace changed slot $slot"
done

# Start a response that remains attached to one inner router. Its response
# header identifies the selected slot before the body completes.
docker exec "gonka-router-fleet-probe-$suffix" sh -c \
    'curl -sS --max-time 15 -D /tmp/stream.headers \
        http://proxy-router:18081/v4/sessions/fleet-test/slow \
        >/tmp/stream.body' &
stream_pid=$!
selected_ip=
for _ in $(seq 40); do
    selected_ip=$(docker exec "gonka-router-fleet-probe-$suffix" sh -c \
        "tr '[:upper:]' '[:lower:]' 2>/dev/null </tmp/stream.headers | sed -n 's/^x-versiond-router-addr: \\([^:]*\\).*/\\1/p'" || true)
    [[ -n $selected_ip ]] && break
    sleep 0.1
done
[[ -n $selected_ip ]] || fail "stream did not expose its selected router"
selected_slot=
for slot in "${slots[@]}"; do
    id=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    ip=$(docker inspect --format \
        "{{with index .NetworkSettings.Networks \"$front\"}}{{.IPAddress}}{{end}}" \
        "$id")
    [[ $ip != "$selected_ip" ]] || selected_slot=$slot
done
[[ -n $selected_slot ]] || fail "selected router address $selected_ip owns no slot"

"${fleet[@]}" stop "$selected_slot" >/dev/null &
stop_pid=$!
for _ in $(seq 20); do
    "${probe[@]}" -X POST -d request=continuity \
        http://proxy-router:18081/v4/sessions/fleet-test/chat >/dev/null \
        || fail "new request failed while slot $selected_slot drained"
    sleep 0.1
done
wait "$stream_pid" || fail "accepted stream was cut by router soft-stop"
wait "$stop_pid" || fail "router slot did not complete its graceful stop"
stream_body=$(docker exec "gonka-router-fleet-probe-$suffix" cat /tmp/stream.body)
[[ $stream_body == $'start\ndone' ]] || fail \
    "accepted stream returned unexpected body: $stream_body"
"${fleet[@]}" start "$selected_slot" >/dev/null

declare -A before=()
for slot in "${slots[@]}"; do
    before[$slot]=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    [[ -n ${before[$slot]} ]] || fail "slot $slot was not created"
done

# `up` is safe to use for bootstrap and recovery, not as an unguarded config
# rollout. A changed declaration must leave every running container untouched.
sed -i 's/^VERSIOND_VERSIONS=v4$/VERSIOND_VERSIONS="v4 v5"/' "$tmpdir/config.env"
if "${fleet[@]}" up >"$tmpdir/config-drift.out" 2>&1; then
    fail "fleet up accepted existing slots without the newly declared route"
fi
grep -q 'does not declare expected route v5; run rollout' \
    "$tmpdir/config-drift.out" || fail \
    "fleet up did not explain that route declarations require rollout"
if "${fleet[@]}" status >"$tmpdir/config-drift-status.out" 2>&1; then
    fail "fleet status accepted slots with stale route declarations"
fi
grep -q 'does not declare expected route v5; run rollout' \
    "$tmpdir/config-drift-status.out" || fail \
    "fleet status did not identify stale route declarations"
for slot in "${slots[@]}"; do
    after=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    [[ $after == "${before[$slot]}" ]] \
        || fail "fleet up recreated slot $slot instead of requiring rollout"
done
sed -i 's/^VERSIOND_VERSIONS="v4 v5"$/VERSIOND_VERSIONS=v4/' "$tmpdir/config.env"

# Main-stack convergence is intentionally a different Compose project. It must
# not own or recreate any router slot.
docker compose -p "gonka-router-fleet-main-$suffix" \
    -f "$tmpdir/main.yml" up -d --force-recreate >/dev/null
for slot in "${slots[@]}"; do
    after=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    [[ $after == "${before[$slot]}" ]] \
        || fail "main Compose convergence recreated router slot $slot"
done
docker compose -p "gonka-router-fleet-main-$suffix" \
    -f "$tmpdir/main.yml" down --timeout 1 >/dev/null
docker network inspect "$front" >/dev/null || fail \
    "main Compose down deleted the external router-fleet network"

"${fleet[@]}" stop 0 >/dev/null
if "${fleet[@]}" status >"$tmpdir/stopped-status.out" 2>&1; then
    fail "fleet status accepted a stopped expected slot"
fi
if "${fleet[@]}" stop 1 >"$tmpdir/unsafe.out" 2>&1; then
    fail "the fleet allowed two of three slots to stop"
fi
grep -q 'only 1 other routers are ready, need 2' "$tmpdir/unsafe.out" \
    || fail "unsafe stop did not explain the violated reserve"
"${fleet[@]}" start 0 >/dev/null

# A candidate can pass the coarse Compose healthcheck while lacking a live
# version route. The rollout must reject it and prove the exact old image's
# route view after automatic rollback.
sed -i "s|^VERSIOND_ROUTER_IMAGE=.*|VERSIOND_ROUTER_IMAGE=$bad_image|" \
    "$tmpdir/config.env"
sed -i 's/^VERSIOND_VERSIONS=v4$/VERSIOND_VERSIONS="v4 v5"/' \
    "$tmpdir/config.env"
sed -i 's/^VERSIOND_ROUTER_START_TIMEOUT_SECONDS=30$/VERSIOND_ROUTER_START_TIMEOUT_SECONDS=10/' \
    "$tmpdir/config.env"
if "${fleet[@]}" rollout >"$tmpdir/rollback.out" 2>&1; then
    fail "route-incomplete router candidate was accepted"
fi
if ! grep -q 'slot 0 restored; rollout stopped' "$tmpdir/rollback.out"; then
    cat "$tmpdir/rollback.out" >&2
    fail "failed candidate did not complete route-aware automatic rollback"
fi
sed -i 's/^VERSIOND_VERSIONS="v4 v5"$/VERSIOND_VERSIONS=v4/' \
    "$tmpdir/config.env"
slot_zero=$(docker ps -q \
    --filter label=ai.gonka.component=versiond-router \
    --filter "label=ai.gonka.fleet=$fleet_id" \
    --filter label=ai.gonka.slot=0)
restored_versions=$(docker inspect --format \
    '{{range .Config.Env}}{{println .}}{{end}}' "$slot_zero" | \
    sed -n 's/^VERSIOND_VERSIONS=//p')
[[ $restored_versions == v4 ]] || fail \
    "ordinary rollback restored the old image with candidate env '$restored_versions'"
for slot in "${slots[@]}"; do
    id=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    docker exec "$id" /bin/busybox wget -q -O /dev/null \
        'http://127.0.0.1:8404/readyz?version=v4' \
        || fail "rollback left slot $slot without its v4 route"
done
sed -i "s|^VERSIOND_ROUTER_IMAGE=.*|VERSIOND_ROUTER_IMAGE=$image|" \
    "$tmpdir/config.env"
sed -i 's/^VERSIOND_ROUTER_START_TIMEOUT_SECONDS=10$/VERSIOND_ROUTER_START_TIMEOUT_SECONDS=30/' \
    "$tmpdir/config.env"

# A coarse-healthy router can still lack one version. Generic reserve alone
# would allow the only safe peer for that route to stop.
VERSIOND_ROUTER_SLOT=0 \
    VERSIOND_ROUTER_FLEET_ID=$fleet_id \
    VERSIOND_ROUTER_FRONT_NETWORK=$front \
    VERSIOND_ROUTER_BACK_NETWORK=$back \
    VERSIOND_ROUTER_IMAGE=$image \
    VERSIOND_NON_HA_VERSIONS='' \
    VERSIOND_VERSIONS=v5 \
    docker compose --project-directory "$script_dir" \
        --project-name "$prefix-0" \
        -f "$script_dir/versiond-router-slot/docker-compose.yml" \
        up -d --force-recreate --wait --wait-timeout 30 router >/dev/null
if "${fleet[@]}" stop 1 >"$tmpdir/route-reserve.out" 2>&1; then
    fail "the fleet allowed the v4 route reserve to fall below two"
fi
grep -q 'version v4 has only 1 other ready routers, need 2' \
    "$tmpdir/route-reserve.out" || fail \
    "route-specific reserve failure did not identify v4"
# This test deliberately manufactured config drift outside the fleet API.
# Restore the fixture explicitly; production callers must use guarded rollout.
VERSIOND_ROUTER_SLOT=0 \
    VERSIOND_ROUTER_FLEET_ID=$fleet_id \
    VERSIOND_ROUTER_FRONT_NETWORK=$front \
    VERSIOND_ROUTER_BACK_NETWORK=$back \
    VERSIOND_ROUTER_IMAGE=$image \
    VERSIOND_NON_HA_VERSIONS='' \
    VERSIOND_VERSIONS=v4 \
    docker compose --project-directory "$script_dir" \
        --project-name "$prefix-0" \
        -f "$script_dir/versiond-router-slot/docker-compose.yml" \
        up -d --force-recreate --wait --wait-timeout 30 router >/dev/null

"${fleet[@]}" rollout >/dev/null
"${fleet[@]}" status >/dev/null

# Another installation on the same Docker daemon has its own inventory even
# when it uses the same slot names.
docker run -d --name "gonka-router-fleet-foreign-$suffix" \
    --label ai.gonka.component=versiond-router \
    --label ai.gonka.fleet=another-fleet \
    --label ai.gonka.slot=0 alpine:3.21 sleep 300 >/dev/null
"${fleet[@]}" status >/dev/null

docker run -d --name "gonka-router-fleet-duplicate-$suffix" \
    --label ai.gonka.component=versiond-router \
    --label "ai.gonka.fleet=$fleet_id" \
    --label ai.gonka.slot=0 alpine:3.21 sleep 300 >/dev/null
if "${fleet[@]}" status >"$tmpdir/duplicate.out" 2>&1; then
    fail "duplicate slot ownership was accepted"
fi
grep -q "duplicate containers claim router slot '0'" "$tmpdir/duplicate.out" \
    || fail "duplicate slot failure did not identify the slot"

# A removed slot and a renamed network remain discoverable through fleet
# ownership labels even though neither appears in today's desired slot list.
docker run -d --name "gonka-router-fleet-orphan-$suffix" \
    --label ai.gonka.component=versiond-router \
    --label "ai.gonka.fleet=$fleet_id" \
    --label ai.gonka.slot=retired alpine:3.21 sleep 300 >/dev/null
docker network create \
    --label ai.gonka.component=versiond-router-network \
    --label "ai.gonka.fleet=$fleet_id" \
    --label ai.gonka.network-role=retired \
    "gonka-versiond-router-orphan-$suffix" >/dev/null

if "${fleet[@]}" stop-all >"$tmpdir/stop-all-unacked.out" 2>&1; then
    fail "fleet-wide stop did not require explicit maintenance acknowledgement"
fi
grep -q 'stop-all requires the explicit --maintenance acknowledgement' \
    "$tmpdir/stop-all-unacked.out" || fail \
    "unacknowledged fleet stop did not explain the safety gate"
if "${fleet[@]}" down >"$tmpdir/down-unacked.out" 2>&1; then
    fail "fleet down did not require explicit maintenance acknowledgement"
fi
grep -q 'down requires the explicit --maintenance acknowledgement' \
    "$tmpdir/down-unacked.out" || fail \
    "unacknowledged fleet down did not explain the safety gate"
if "${fleet[@]}" down --maintenance >"$tmpdir/down-attached.out" 2>&1; then
    fail "fleet down removed networks still used by the main topology"
fi
grep -q 'run the main Compose down before fleet down' \
    "$tmpdir/down-attached.out" || fail \
    "attached-network rejection did not explain the required shutdown order"
slot_zero=$(docker ps -q \
    --filter label=ai.gonka.component=versiond-router \
    --filter "label=ai.gonka.fleet=$fleet_id" \
    --filter label=ai.gonka.slot=0 | head -n1)
[[ $(docker inspect --format '{{.State.Status}}' "$slot_zero") == running ]] || fail \
    "failed fleet down mutated routers before its network preflight committed"

"${fleet[@]}" stop-all --maintenance >/dev/null
while IFS= read -r id; do
    [[ -n $id ]] || continue
    [[ $(docker inspect --format '{{.State.Status}}' "$id") == exited ]] || fail \
        "fleet-wide maintenance stop left router $id running"
done < <(docker ps -aq --no-trunc \
    --filter label=ai.gonka.component=versiond-router \
    --filter "label=ai.gonka.fleet=$fleet_id")

# `down` is intentionally ordered after the main Compose project: it refuses
# to delete shared external networks while any non-fleet endpoint remains.
docker rm -f "gonka-router-fleet-backend-$suffix" \
    "gonka-router-fleet-proxy-$suffix" \
    "gonka-router-fleet-probe-$suffix" >/dev/null
"${fleet[@]}" down --maintenance >/dev/null
if docker ps -aq --filter label=ai.gonka.component=versiond-router \
    --filter "label=ai.gonka.fleet=$fleet_id" | grep -q .; then
    fail "fleet down left expected, duplicate, or orphan router containers"
fi
for network in "$front" "$back" "gonka-versiond-router-orphan-$suffix"; do
    if docker network inspect "$network" >/dev/null 2>&1; then
        fail "fleet down left owned network $network"
    fi
done
docker inspect "gonka-router-fleet-foreign-$suffix" >/dev/null || fail \
    "fleet down removed a router owned by another installation"

# A later cold start has an explicit way to recreate the external substrate
# before the main Compose project is brought back.
"${fleet[@]}" prepare-networks
docker network inspect "$front" "$back" >/dev/null

echo "versiond-router-fleet_test: ok"
