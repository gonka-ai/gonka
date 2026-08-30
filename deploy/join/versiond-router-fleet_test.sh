#!/usr/bin/env bash

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
suffix=$$
base_image=gonka-versiond-router-fleet-test:$suffix
updated_image=gonka-versiond-router-fleet-updated-test:$suffix
image=$base_image
bad_image=gonka-versiond-router-fleet-bad-test:$suffix
incompatible_cache_image=gonka-versiond-router-fleet-cache-v1-test:$suffix
proxy_image=gonka-proxy-router-fleet-test:$suffix
front=gonka-versiond-router-front-$suffix
back=gonka-versiond-router-back-$suffix
metrics=gonka-versiond-router-metrics-$suffix
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
            VERSIOND_ROUTER_METRICS_NETWORK=$metrics \
            VERSIOND_ROUTER_IMAGE=$image \
            docker compose --project-directory "$script_dir" \
                --project-name "$prefix-$slot" \
                -f "$script_dir/versiond-router-slot/docker-compose.yml" \
                down --timeout 1 --remove-orphans -v >/dev/null 2>&1 || true
    done
    docker rm -f gonka-router-fleet-backend-$suffix \
        gonka-router-fleet-proxy-$suffix \
        gonka-router-fleet-probe-$suffix \
        gonka-router-fleet-duplicate-$suffix \
        gonka-router-fleet-orphan-$suffix \
        gonka-router-fleet-foreign-$suffix >/dev/null 2>&1 || true
    docker compose -p "gonka-router-fleet-main-$suffix" \
        -f "$tmpdir/main.yml" down --timeout 1 >/dev/null 2>&1 || true
    docker network rm "$front" "$back" "$metrics" \
        "gonka-versiond-router-orphan-$suffix" >/dev/null 2>&1 || true
    docker image rm "$base_image" "$updated_image" "$bad_image" \
        "$incompatible_cache_image" "$proxy_image" >/dev/null 2>&1 || true
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
VERSIOND_ROUTER_IMAGE=$incompatible_cache_image
VERSIOND_ROUTER_FRONT_NETWORK=$front
VERSIOND_ROUTER_BACK_NETWORK=$back
VERSIOND_ROUTER_METRICS_NETWORK=$metrics
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

fleet_spec=$("${fleet[@]}" spec-hash)
[[ $fleet_spec =~ ^[0-9a-f]{64}$ ]] || fail \
    "fleet specification is not represented by a SHA-256"
[[ $("${fleet[@]}" spec-hash) == "$fleet_spec" ]] || fail \
    "unchanged fleet specification produced an unstable hash"
[[ $(VERSIOND_ROUTER_MIN_READY=1 "${fleet[@]}" spec-hash) != "$fleet_spec" ]] || fail \
    "fleet specification hash ignores the ready reserve"
[[ $(VERSIOND_ROUTER_FLEET_SLOTS='2 1 0' "${fleet[@]}" spec-hash) != "$fleet_spec" ]] || fail \
    "fleet specification hash ignores ordered slot membership"
[[ $(VERSIOND_ROUTER_FLEET_ID="$fleet_id-other" "${fleet[@]}" spec-hash) != "$fleet_spec" ]] || fail \
    "fleet specification hash ignores fleet identity"

docker network create --internal \
    --label com.docker.compose.network=default \
    --label com.docker.compose.project="gonka-router-fleet-main-$suffix" \
    "$metrics" >/dev/null

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
cat >"$tmpdir/incompatible-cache.Dockerfile" <<EOF
FROM $image
LABEL ai.gonka.catalog-cache-protocol-version="1"
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
docker build -q -t "$image" -f "$script_dir/../../versiond-router/Dockerfile" \
    "$script_dir/../.." >/dev/null
docker build -q -t "$updated_image" \
    -f "$tmpdir/updated-router.Dockerfile" "$tmpdir" >/dev/null
docker build -q -t "$bad_image" -f "$tmpdir/bad-router.Dockerfile" "$tmpdir" >/dev/null
docker build -q -t "$incompatible_cache_image" \
    -f "$tmpdir/incompatible-cache.Dockerfile" "$tmpdir" >/dev/null
docker build -q -t "$proxy_image" -f "$script_dir/../../proxy-router/Dockerfile" \
    "$script_dir/../.." >/dev/null
docker run -d --name "gonka-router-fleet-backend-$suffix" \
    --network "$back" --network-alias versiond-pool \
    -v "$tmpdir/backend.py:/app.py:ro" \
    python:3.12-alpine python /app.py >/dev/null

"${fleet[@]}" up >/dev/null
bootstrap_status=$("${fleet[@]}" status)
grep -q '^PARENT_ADMISSION not-applicable ' <<<"$bootstrap_status" || fail \
    "fleet status did not distinguish pre-cutover local health from parent admission"
for slot in "${slots[@]}"; do
    id=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$id" | \
        grep -qx 'VERSIOND_ROUTER_TRUST_FORWARDED_HEADERS=true' || fail \
        "slot $slot does not preserve identity from the isolated proxy tier"
done

# `apply` is also the recovery path. It restores absent and non-ready capacity
# before considering any healthy slot for replacement.
healthy_0=$(docker ps -q \
    --filter label=ai.gonka.component=versiond-router \
    --filter "label=ai.gonka.fleet=$fleet_id" \
    --filter label=ai.gonka.slot=0)
healthy_1=$(docker ps -q \
    --filter label=ai.gonka.component=versiond-router \
    --filter "label=ai.gonka.fleet=$fleet_id" \
    --filter label=ai.gonka.slot=1)
missing_id=$(docker ps -q \
    --filter label=ai.gonka.component=versiond-router \
    --filter "label=ai.gonka.fleet=$fleet_id" \
    --filter label=ai.gonka.slot=2)
docker rm -f "$missing_id" >/dev/null
"${fleet[@]}" apply >/dev/null
[[ $(docker ps -q --filter "id=$healthy_0") == "$healthy_0" ]] || fail \
    "repairing an absent slot replaced healthy slot 0"
[[ $(docker ps -q --filter "id=$healthy_1") == "$healthy_1" ]] || fail \
    "repairing an absent slot replaced healthy slot 1"
recovered_2=$(docker ps -q \
    --filter label=ai.gonka.component=versiond-router \
    --filter "label=ai.gonka.fleet=$fleet_id" \
    --filter label=ai.gonka.slot=2)
[[ -n $recovered_2 && $recovered_2 != "$missing_id" ]] || fail \
    "apply did not recreate the absent slot"

docker network disconnect "$back" "$recovered_2"
for _ in $(seq 45); do
    health=$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{end}}' \
        "$recovered_2")
    [[ $health == unhealthy ]] && break
    sleep 1
done
[[ ${health:-} == unhealthy ]] || fail "disconnected slot did not become unhealthy"
"${fleet[@]}" apply >/dev/null
[[ $(docker ps -q --filter "id=$healthy_0") == "$healthy_0" ]] || fail \
    "repairing an unhealthy slot replaced healthy slot 0"
[[ $(docker ps -q --filter "id=$healthy_1") == "$healthy_1" ]] || fail \
    "repairing an unhealthy slot replaced healthy slot 1"
repaired_2=$(docker ps -q \
    --filter label=ai.gonka.component=versiond-router \
    --filter "label=ai.gonka.fleet=$fleet_id" \
    --filter label=ai.gonka.slot=2)
[[ -n $repaired_2 && $repaired_2 != "$recovered_2" ]] || fail \
    "apply did not recreate the unhealthy slot"

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

# A protocol bump uses a distinct cache file and is rolled out without an
# operator migration. The previous file remains available to the captured
# rollback image.
sed -i "s|^VERSIOND_ROUTER_IMAGE=.*|VERSIOND_ROUTER_IMAGE=$image|" \
    "$tmpdir/config.env"
"${fleet[@]}" rollout >/dev/null
for slot in "${slots[@]}"; do
    current_id=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    current_protocol=$(docker inspect --format \
        '{{index .Config.Labels "ai.gonka.catalog-cache-protocol-version"}}' \
        "$current_id")
    [[ $current_protocol == 2 ]] || fail \
        "automatic cache protocol upgrade left slot $slot on $current_protocol"
    initial_ids[$slot]=$current_id
done

# A requested downgrade is ambiguous even though rollback inside the active
# rollout remains safe. Refuse it before stopping the first healthy slot.
sed -i "s|^VERSIOND_ROUTER_IMAGE=.*|VERSIOND_ROUTER_IMAGE=$incompatible_cache_image|" \
    "$tmpdir/config.env"
if "${fleet[@]}" rollout >"$tmpdir/cache-protocol.out" 2>&1; then
    fail "fleet rollout accepted a catalog cache protocol downgrade"
fi
grep -q 'catalog cache protocol mismatch' "$tmpdir/cache-protocol.out" || {
    cat "$tmpdir/cache-protocol.out" >&2
    fail "cache protocol refusal was not actionable"
}
for slot in "${slots[@]}"; do
    current_id=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    [[ $current_id == "${initial_ids[$slot]}" ]] || fail \
        "cache protocol check mutated slot $slot"
done
sed -i "s|^VERSIOND_ROUTER_IMAGE=.*|VERSIOND_ROUTER_IMAGE=$image|" \
    "$tmpdir/config.env"

if VERSIOND_ROUTING_ACTIVATION_TIMEOUT_SECONDS=1 \
    "${fleet[@]}" wait-version v4 >"$tmpdir/no-parent-gate.out" 2>&1; then
    fail "version activation gate succeeded without the parent proxy"
fi
grep -q 'active parent is not proxy-router' "$tmpdir/no-parent-gate.out" || fail \
    "version activation gate did not identify the missing parent"

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
lock_file=$tmpdir/.gonka-deployment.lock
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
grep -q 'another deployment operation holds' "$tmpdir/lock.out" || fail \
    "contended deployment lock did not identify the active operation"

# upgrade -> cutover -> fleet is one operation. Descendants inherit fd 9 and
# must reuse it instead of deadlocking on their parent's lock.
(
    exec 9<"$lock_file"
    flock -n 9
    export GONKA_DEPLOYMENT_LOCK=$lock_file
    export GONKA_DEPLOYMENT_LOCK_HELD=$lock_file
    "${fleet[@]}" status >/dev/null
) || fail "an inherited deployment lock was not re-entrant"

# Legacy pinning changes the escrow placement function. A mixed rolling fleet
# is invalid; the explicit maintenance path drains every old router before any
# router with the new contract becomes visible.
sed -i 's/^VERSIOND_NON_HA_VERSIONS=$/VERSIOND_NON_HA_VERSIONS="v4 v5"/' \
    "$tmpdir/config.env"
sed -i 's/^VERSIOND_VERSIONS=v4$/VERSIOND_VERSIONS=/' "$tmpdir/config.env"
cat >>"$tmpdir/config.env" <<'EOF'
VERSIOND_LEGACY_HOST=versiond-pool
VERSIOND_ROUTING_CATALOG_URL=
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
sed -i "s|^VERSIOND_ROUTER_IMAGE=.*|VERSIOND_ROUTER_IMAGE=$bad_image|" \
    "$tmpdir/config.env"
if VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
    "${fleet[@]}" maintenance-rollout \
    >"$tmpdir/maintenance-rollback.out" 2>&1; then
    fail "route-dead maintenance candidate unexpectedly committed"
fi
grep -q 'the exact previous router fleet was restored' \
    "$tmpdir/maintenance-rollback.out" || {
    cat "$tmpdir/maintenance-rollback.out" >&2
    fail "failed maintenance candidate did not restore the previous fleet"
}
sed -i "s|^VERSIOND_ROUTER_IMAGE=.*|VERSIOND_ROUTER_IMAGE=$image|" \
    "$tmpdir/config.env"
sed -i '/^VERSIOND_ROUTING_CATALOG_URL=$/d' "$tmpdir/config.env"
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
docker network connect "$metrics" "gonka-router-fleet-probe-$suffix"
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

# Maintenance replacement crosses its commit point only after the parent has
# completed fresh L7 admission for every replacement slot.
VERSIOND_ROUTER_ALLOW_COARSE_READINESS=true \
    VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
    "${fleet[@]}" maintenance-rollout >/dev/null
"${fleet[@]}" verify-admission v4 >/dev/null || fail \
    "maintenance rollout committed before parent admission converged"

for slot in "${slots[@]}"; do
    id=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    metrics_ip=$(docker inspect --format \
        "{{with index .NetworkSettings.Networks \"$metrics\"}}{{.IPAddress}}{{end}}" \
        "$id")
    [[ -n $metrics_ip ]] || fail "slot $slot has no metrics-network address"
    "${probe[@]}" "http://$metrics_ip:8405/metrics?scope=frontend" \
        >"$tmpdir/router-$slot.metrics" || fail \
        "Prometheus endpoint is not reachable for router slot $slot"
    grep -q '^haproxy_' "$tmpdir/router-$slot.metrics" || fail \
        "router slot $slot returned no HAProxy metrics"
    if "${probe[@]}" "http://$metrics_ip:8404/livez" >/dev/null 2>&1; then
        fail "router slot $slot exposes its admin listener on the metrics network"
    fi
    if "${probe[@]}" "http://$metrics_ip:8080/v4/sessions/metrics/chat" \
        >/dev/null 2>&1; then
        fail "router slot $slot exposes its data listener on the metrics network"
    fi
done

# The catalog endpoint is part of the routing contract: two routers learning
# approved names from different authorities must never coexist in one ring. The
# rejection happens before any slot mutation, while live traffic remains usable.
declare -A before_catalog_change=()
for slot in "${slots[@]}"; do
    before_catalog_change[$slot]=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
done
printf '%s\n' 'VERSIOND_ROUTING_CATALOG_URL=http://different-catalog:9100/versions' \
    >> "$tmpdir/config.env"
if "${fleet[@]}" rollout >"$tmpdir/catalog-contract.out" 2>&1; then
    fail "ordinary rollout accepted divergent governance catalog authorities"
fi
grep -q 'use maintenance-rollout to avoid mixed escrow placement' \
    "$tmpdir/catalog-contract.out" || fail \
    "catalog authority mismatch was not reported as a placement change"
sed -i '/^VERSIOND_ROUTING_CATALOG_URL=http:\/\/different-catalog:9100\/versions$/d' \
    "$tmpdir/config.env"
for slot in "${slots[@]}"; do
    current_id=$(docker ps -q \
        --filter label=ai.gonka.component=versiond-router \
        --filter "label=ai.gonka.fleet=$fleet_id" \
        --filter "label=ai.gonka.slot=$slot")
    [[ $current_id == "${before_catalog_change[$slot]}" ]] || fail \
        "catalog-authority guard mutated slot $slot before rejecting rollout"
done
for i in $(seq 10); do
    [[ $("${probe[@]}" -X POST -d "request=$i" \
        "http://proxy-router:18081/v4/sessions/catalog-guard-$i/chat") == ok ]] \
        || fail "catalog-authority rejection disrupted live request $i"
done
"${fleet[@]}" verify-admission v4 >/dev/null || fail \
    "strict admission rejected the complete live v4 fleet"
admitted_status=$("${fleet[@]}" status)
grep -qx 'PARENT_ADMISSION admitted' <<<"$admitted_status" || fail \
    "fleet status did not report end-to-end parent admission"

# Container health alone is insufficient: a healthy fleet that the active
# parent cannot reach must make the ordinary status command fail.
docker network disconnect "$front" "gonka-router-fleet-proxy-$suffix"
parent_status_failed=false
for _ in $(seq 20); do
    if ! "${fleet[@]}" status >"$tmpdir/parent-status.out" 2>&1; then
        if grep -q 'parent admission is incomplete' "$tmpdir/parent-status.out"; then
            parent_status_failed=true
            break
        fi
    fi
    sleep 0.5
done
[[ $parent_status_failed == true ]] || fail \
    "fleet status accepted slots that were absent from the active parent"
docker network connect --alias proxy-router "$front" \
    "gonka-router-fleet-proxy-$suffix"
"${fleet[@]}" verify-admission v4 >/dev/null || fail \
    "parent admission did not recover after reconnecting the test proxy"

"${fleet[@]}" wait-version v4 >/dev/null || fail \
    "machine activation gate rejected end-to-end ready v4"
if VERSIOND_ROUTING_ACTIVATION_TIMEOUT_SECONDS=2 \
    "${fleet[@]}" wait-version v5 >"$tmpdir/missing-version-gate.out" 2>&1; then
    fail "version activation gate accepted a version absent from the fleet catalog"
fi
grep -q 'has not learned version v5' "$tmpdir/missing-version-gate.out" || fail \
    "version activation gate did not identify the slot missing v5"

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

selected_slot=${slots[0]}
selected_slot_id=$(docker ps -q \
    --filter label=ai.gonka.component=versiond-router \
    --filter "label=ai.gonka.fleet=$fleet_id" \
    --filter "label=ai.gonka.slot=$selected_slot")
selected_slot_ip=$(docker inspect --format \
    "{{with index .NetworkSettings.Networks \"$front\"}}{{.IPAddress}}{{end}}" \
    "$selected_slot_id")
# Address a known inner router directly so the test does not require a public
# response header that exposes the selected container address.
docker exec "gonka-router-fleet-probe-$suffix" sh -c \
    "curl -sS --max-time 15 \
        http://versiond-router-${selected_slot}-front:8080/v4/sessions/fleet-test/slow \
        >/tmp/stream.body" &
stream_pid=$!
for _ in $(seq 40); do
    docker exec "gonka-router-fleet-probe-$suffix" \
        grep -q '^start$' /tmp/stream.body 2>/dev/null && break
    sleep 0.1
done
docker exec "gonka-router-fleet-probe-$suffix" \
    grep -q '^start$' /tmp/stream.body 2>/dev/null || fail \
    "slow stream did not start on router slot $selected_slot"

"${fleet[@]}" stop "$selected_slot" >/dev/null &
stop_pid=$!
parent_withdrew=false
for _ in $(seq 30); do
    if ! docker exec "gonka-router-fleet-proxy-$suffix" \
        /usr/local/lib/proxy-router/route-status v4 "$selected_slot_ip" \
        >/dev/null 2>&1; then
        parent_withdrew=true
        break
    fi
    sleep 0.1
done
[[ $parent_withdrew == true ]] || fail \
    "parent proxy did not withdraw slot $selected_slot before its stop"
[[ $(docker inspect --format '{{.State.Running}}' "$selected_slot_id") == true ]] \
    || fail "slot $selected_slot exited before parent withdrawal was observable"
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
grep -q 'does not declare expected route v5' \
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
    VERSIOND_ROUTER_METRICS_NETWORK=$metrics \
    VERSIOND_ROUTER_IMAGE=$image \
    VERSIOND_NON_HA_VERSIONS='' \
    VERSIOND_VERSIONS='' \
    VERSIOND_ROUTING_CATALOG_URL='' \
    VERSIOND_ROUTER_ALLOW_COARSE_READINESS=true \
    docker compose --project-directory "$script_dir" \
        --project-name "$prefix-0" \
        -f "$script_dir/versiond-router-slot/docker-compose.yml" \
        up -d --force-recreate --wait --wait-timeout 30 router >/dev/null
if "${fleet[@]}" stop 1 >"$tmpdir/route-reserve.out" 2>&1; then
    fail "the fleet allowed the v4 route reserve to fall below two"
fi
grep -q 'version v4 has only 1 other ready routers, need 2' \
    "$tmpdir/route-reserve.out" || {
    cat "$tmpdir/route-reserve.out" >&2
    fail "route-specific reserve failure did not identify v4"
}
# This test deliberately manufactured config drift outside the fleet API.
# Restore the fixture explicitly; production callers must use guarded rollout.
VERSIOND_ROUTER_SLOT=0 \
    VERSIOND_ROUTER_FLEET_ID=$fleet_id \
    VERSIOND_ROUTER_FRONT_NETWORK=$front \
    VERSIOND_ROUTER_BACK_NETWORK=$back \
    VERSIOND_ROUTER_METRICS_NETWORK=$metrics \
    VERSIOND_ROUTER_IMAGE=$image \
    VERSIOND_NON_HA_VERSIONS='' \
    VERSIOND_VERSIONS=v4 \
    VERSIOND_ROUTER_ALLOW_COARSE_READINESS=true \
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
