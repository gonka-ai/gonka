#!/usr/bin/env bash

# The v4 -> v5 cutover, for real: a pre-v5 deployment (PostgreSQL in the
# anonymous volume, one nginx versiond-router, versiond replicas that answer
# 404 to every v5 endpoint) is started from Compose files shaped like the
# 0.2.15 release, the files of this directory are copied over it the way a
# release is unpacked on a host, and the real update-devshard.sh runs against
# the real Docker daemon with the router, public proxy and policy images built
# from this repository. versiond itself is a stub: an HTTP server on the
# versiond port that speaks the v4 or the v5 contract and keeps its storage
# lineage in the shared PostgreSQL through psql, as the real one does.
#
# Verified end to end: the database survives the migration, both replicas run
# from the new model on the router back network, the legacy router is gone,
# the fleet is admitted by the new proxy, a request through the published
# port reaches a replica, a second run changes nothing, a run killed halfway
# through the replica step converges on the next run, and the lineage proof
# (wget through the container, jq, psql) works against real binaries.
#
# The release files use fixed container names (versiond, proxy,
# devshard-postgres, ...) and the updater tags gonka-previous/<service>, so
# this test cannot share a host with a deployment. It refuses to start when
# those names exist.

set -Eeuo pipefail

script_dir=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)
repo_dir=$(cd -- "$script_dir/../.." && pwd -P)
suffix=$$
project=gonka-e2e-$suffix
tmpdir=$(mktemp -d)
join=$tmpdir/join
stub_image=gonka-e2e-versiond-stub:$suffix
router_image=gonka-e2e-versiond-router:$suffix
proxy_router_image=gonka-e2e-proxy-router:$suffix
policy_image=gonka-e2e-proxy-policy:$suffix
front=$project-router-front
back=$project-router-back
policy_front=$project-policy-front

fail() {
    echo "update-devshard-e2e_test: $*" >&2
    if [[ -f $tmpdir/update.log ]]; then
        echo "--- updater output (Compose variable warnings dropped) ---" >&2
        grep -v 'level=warning' "$tmpdir/update.log" | tail -n 150 >&2
    fi
    exit 1
}

free_port() {
    python3 -c 'import socket; s = socket.socket(); s.bind(("127.0.0.1", 0)); print(s.getsockname()[1])'
}

for name in versiond versiond2 versiond-router proxy proxy-policy proxy-policy2 devshard-postgres api; do
    ! docker container inspect "$name" >/dev/null 2>&1 || fail \
        "container $name exists; this test needs the fixed names of the release files"
done
previous_tags_before=$(docker image ls --format '{{.Repository}}:{{.Tag}}' 'gonka-previous/*' 2>/dev/null || true)

cleanup() {
    set +e
    docker ps -aq --filter "label=com.docker.compose.project.working_dir=$join" | xargs -r docker rm -f >/dev/null 2>&1
    docker volume ls -q --filter "label=com.docker.compose.project=$project" | xargs -r docker volume rm >/dev/null 2>&1
    docker network ls -q --filter "label=com.docker.compose.project=$project" | xargs -r docker network rm >/dev/null 2>&1
    docker network rm "$front" "$back" "$policy_front" >/dev/null 2>&1
    docker image ls --format '{{.Repository}}:{{.Tag}}' 'gonka-previous/*' 2>/dev/null | while read -r tag; do
        grep -qx "$tag" <<<"$previous_tags_before" || docker image rm "$tag" >/dev/null 2>&1
    done
    docker image ls --format '{{.Repository}}:{{.Tag}}' "gonka/versiond-router-previous" 2>/dev/null | grep -F ":$project-" | \
        xargs -r docker image rm >/dev/null 2>&1
    docker image rm "$stub_image" "$router_image" "$proxy_router_image" "$policy_image" >/dev/null 2>&1
    # PostgreSQL's data directory belongs to the postgres user.
    docker run --rm -v "$tmpdir:/scratch" --entrypoint sh postgres:16-alpine -c 'rm -rf /scratch/join' >/dev/null 2>&1
    rm -rf "$tmpdir"
}
trap cleanup EXIT

api_port=$(free_port)
api_ssl_port=$(free_port)

# --- images ------------------------------------------------------------------
docker build -q -t "$router_image" -f "$repo_dir/versiond-router/Dockerfile" "$repo_dir" >/dev/null || \
    fail "cannot build the versiond-router image"
docker build -q -t "$proxy_router_image" -f "$repo_dir/proxy-router/Dockerfile" "$repo_dir" >/dev/null || \
    fail "cannot build the public proxy image"
docker build -q -t "$policy_image" "$repo_dir/proxy" >/dev/null || fail "cannot build the policy worker image"
mkdir -p "$tmpdir/stub"

# --- the deployment directory, as an operator has it before v5 ---------------
mkdir -p "$join/devshards/bin" "$join/devshards/data" "$join/devshards2/data" \
    "$join/.inference" "$join/secrets/nginx-ssl"

# The versiond stand-in. STUB_API=v4 answers like a 0.2.15 versiond: no
# /readyz, no storage proof. STUB_API=v5 (the default, because the release
# model cannot set it) creates the lineage row once, serves the proof from the
# database and writes challenges into it, so the updater's checks run against
# a real PostgreSQL through the real container tooling.
cat >"$tmpdir/stub/versiond.py" <<'PY'
import http.server
import json
import os
import subprocess
import time
import urllib.parse
import uuid

api = os.environ.get("STUB_API", "v5")
serves = set(os.environ.get("SERVES", "v4 v5").split())
generation = os.environ.get("HOSTNAME", "stub") + "-1"


def sql(statement):
    return subprocess.run(
        ["psql", "-Atq", "-v", "ON_ERROR_STOP=1", "-c", statement],
        capture_output=True, text=True, timeout=15,
    )


if api == "v5":
    deadline = time.time() + 120
    while True:
        result = sql(
            "CREATE TABLE IF NOT EXISTS devshard_storage_identity ("
            " singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton),"
            " identity text NOT NULL, challenge text);"
            " INSERT INTO devshard_storage_identity (singleton, identity)"
            " VALUES (true, 'lineage-' || gen_random_uuid()::text) ON CONFLICT DO NOTHING"
        )
        if result.returncode == 0:
            break
        if time.time() > deadline:
            raise SystemExit("postgres unavailable: " + result.stderr)
        time.sleep(1)


class Handler(http.server.BaseHTTPRequestHandler):
    def reply(self, status, body, content_type="text/plain"):
        data = body.encode()
        self.send_response(status)
        self.send_header("Content-Type", content_type)
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        path, _, query = self.path.partition("?")
        if path == "/healthz":
            return self.reply(200, "ok\n")
        if path == "/readyz":
            if api == "v4":
                return self.reply(404, "not found\n")
            version = urllib.parse.parse_qs(query).get("version", [None])[0]
            if version is None or version in serves:
                return self.reply(200, "ready\n")
            return self.reply(503, "not ready\n")
        if path.count("/") == 2 and path.endswith("/healthz"):
            version = path[1:-len("/healthz")]
            return self.reply(200 if version in serves else 404, "ok\n" if version in serves else "unknown version\n")
        if path == "/internal/storage-identity":
            if api == "v4":
                return self.reply(404, "not found\n")
            result = sql("SELECT identity FROM devshard_storage_identity WHERE singleton")
            if result.returncode != 0:
                return self.reply(503, result.stderr)
            return self.reply(200, json.dumps({
                "identity": result.stdout.strip(),
                "snapshot": "snapshot-1",
                "targets": [{"generation": generation}],
            }), "application/json")
        return self.reply(404, "not found\n")

    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length)
        if self.path != "/internal/storage-challenge" or api == "v4":
            return self.reply(404, "not found\n")
        try:
            nonce = str(uuid.UUID(json.loads(body)["nonce"]))
        except (ValueError, KeyError, TypeError):
            return self.reply(400, "bad challenge\n")
        result = sql("UPDATE devshard_storage_identity SET challenge = '%s' WHERE singleton" % nonce)
        if result.returncode != 0:
            return self.reply(503, result.stderr)
        result = sql("SELECT identity FROM devshard_storage_identity WHERE singleton")
        return self.reply(200, json.dumps({"identity": result.stdout.strip(), "found": True}), "application/json")

    def log_message(self, *_):
        pass


http.server.ThreadingHTTPServer(("", 8080), Handler).serve_forever()
PY

# The versions catalog the public proxy and the router slots read from api.
cat >"$tmpdir/stub/catalog.py" <<'PY'
import http.server
import json


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        body = json.dumps({"versions": [{"name": "v4"}, {"name": "v5"}]}).encode()
        self.send_response(200 if self.path == "/versions" else 404)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, *_):
        pass


http.server.ThreadingHTTPServer(("", 9100), Handler).serve_forever()
PY
# The release model runs the image as it is, so the stub is the image's
# command; the pre-v5 files pick the same image with explicit commands.
cat >"$tmpdir/stub/Dockerfile" <<'EOF'
FROM python:3.12-alpine
RUN apk add --no-cache postgresql16-client
COPY versiond.py catalog.py /stub/
CMD ["python3", "/stub/versiond.py"]
EOF
docker build -q -t "$stub_image" "$tmpdir/stub" >/dev/null || fail "cannot build the versiond stub image"

# Pre-v5 files, shaped like the 0.2.15 release: services the updater relies
# on (names, labels, the anonymous PostgreSQL volume, the router singleton),
# images replaced by the stubs. api is the catalog and is never updated.
cat >"$join/docker-compose.yml" <<'EOF'
services:
  api:
    container_name: api
    image: ${E2E_STUB_IMAGE}
    command: ["python3", "/stub/catalog.py"]
    restart: always
  versiond:
    container_name: versiond
    image: ${E2E_STUB_IMAGE}
    command: ["python3", "/stub/versiond.py"]
    environment:
      - STUB_API=v4
      - VERSIOND_ORACLE_URL=http://api:9100/versions
      - KEY_NAME=${KEY_NAME}
    volumes:
      - .inference:/root/.inference:ro
      - ./devshards/bin:/opt/versiond/bin
      - ./devshards/data:/opt/versiond/data
    healthcheck:
      test: ["CMD", "/bin/busybox", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/healthz"]
      interval: 2s
      timeout: 3s
      retries: 3
    stop_grace_period: 10s
    restart: always
  proxy:
    container_name: proxy
    image: ${E2E_STUB_IMAGE}
    command: ["python3", "-m", "http.server", "80"]
    ports:
      - "${API_PORT:-8000}:80"
    healthcheck:
      test: ["CMD", "/bin/busybox", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:80/"]
      interval: 2s
      timeout: 3s
      retries: 3
    restart: unless-stopped
EOF
cat >"$join/docker-compose.versiond.yml" <<'EOF'
x-versiond: &versiond
  image: ${E2E_STUB_IMAGE}
  command: ["python3", "/stub/versiond.py"]
  environment:
    - STUB_API=v4
    - VERSIOND_ORACLE_URL=http://api:9100/versions
    - GONKA_HA=true
    - KEY_NAME=${KEY_NAME}
    - PGHOST=devshard-postgres
    - PGDATABASE=${DEVSHARD_POSTGRES_DB:-devshardd}
    - PGUSER=${DEVSHARD_POSTGRES_USER:-devshardd}
    - PGPASSWORD=${DEVSHARD_POSTGRES_PASSWORD:?DEVSHARD_POSTGRES_PASSWORD is required}
  networks:
    default:
      aliases:
        - versiond-pool
  healthcheck:
    test: ["CMD", "/bin/busybox", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/healthz"]
    interval: 2s
    timeout: 3s
    retries: 3
  stop_grace_period: 10s
  restart: always
services:
  devshard-postgres:
    container_name: devshard-postgres
    image: postgres:16-alpine
    environment:
      POSTGRES_DB: ${DEVSHARD_POSTGRES_DB:-devshardd}
      POSTGRES_USER: ${DEVSHARD_POSTGRES_USER:-devshardd}
      POSTGRES_PASSWORD: ${DEVSHARD_POSTGRES_PASSWORD:?DEVSHARD_POSTGRES_PASSWORD is required}
    healthcheck:
      test: ["CMD-SHELL", "pg_isready -U $${POSTGRES_USER} -d $${POSTGRES_DB}"]
      interval: 2s
      timeout: 3s
      retries: 30
    restart: always
  versiond:
    <<: *versiond
    container_name: versiond
    volumes:
      - .inference:/root/.inference:ro
      - ./devshards/bin:/opt/versiond/bin
      - ./devshards/data:/opt/versiond/data
  versiond2:
    <<: *versiond
    container_name: versiond2
    volumes:
      - .inference:/root/.inference:ro
      - ./devshards/bin:/opt/versiond/bin
      - ./devshards2/data:/opt/versiond/data
  versiond-router:
    container_name: versiond-router
    image: ${E2E_STUB_IMAGE}
    command: ["python3", "-m", "http.server", "8080"]
    restart: always
EOF

# The operator's configuration: written once, kept across the release.
cat >"$join/config.env" <<EOF
KEY_NAME=e2e
DEVSHARD_POSTGRES_PASSWORD=e2e-secret
API_PORT=$api_port
API_SSL_PORT=$api_ssl_port
E2E_STUB_IMAGE=$stub_image
VERSIOND_IMAGE=$stub_image
PROXY_ROUTER_IMAGE=$proxy_router_image
PROXY_POLICY_IMAGE=$policy_image
VERSIOND_ROUTER_IMAGE=$router_image
VERSIOND_ROUTER_FRONT_NETWORK=$front
VERSIOND_ROUTER_BACK_NETWORK=$back
VERSIOND_ROUTER_METRICS_NETWORK=${project}_default
PROXY_POLICY_FRONT_NETWORK=$policy_front
VERSIOND_ROUTER_PROJECT_PREFIX=$project-router
VERSIOND_ROUTER_FLEET_ID=$project
VERSIOND_ROUTER_FLEET_SLOTS="0 1"
VERSIOND_ROUTER_MIN_READY=1
VERSIOND_ROUTER_START_TIMEOUT_SECONDS=90
VERSIOND_ROUTER_DRAIN_TIMEOUT_SECONDS=10
VERSIOND_VERSIONS="v4 v5"
VERSIOND_NON_HA_VERSIONS=
VERSIOND_HEALTH_START_PERIOD=90s
VERSIOND_STOP_GRACE_PERIOD=10s
DEVSHARD_POSTGRES_START_PERIOD=120s
EOF
set -a
# shellcheck disable=SC1091
source "$join/config.env"
set +a

compose_v4=(docker compose --project-name "$project" --project-directory "$join"
    -f "$join/docker-compose.yml" -f "$join/docker-compose.versiond.yml")
"${compose_v4[@]}" up -d --wait --wait-timeout 180 devshard-postgres api versiond versiond2 versiond-router proxy \
    >"$tmpdir/v4-up.log" 2>&1 || fail "pre-v5 deployment did not start: $(tail -n 20 "$tmpdir/v4-up.log")"

psql_db() {
    docker exec devshard-postgres psql -U devshardd -d devshardd -Atq -v ON_ERROR_STOP=1 -c "$1"
}
psql_db "CREATE TABLE e2e_marker (id int PRIMARY KEY); INSERT INTO e2e_marker VALUES (42)" >/dev/null || \
    fail "cannot write into the pre-v5 database"
v4_postgres_id=$(docker inspect --format '{{.Id}}' devshard-postgres)

# --- the release lands on the host --------------------------------------------
cp -R "$script_dir/." "$join/"
rm -f "$join/config.env.template"

updater=("$join/update-devshard.sh")
"${updater[@]}" --check >"$tmpdir/check.log" 2>&1 || fail "--check failed: $(tail -n 20 "$tmpdir/check.log")"
grep -q 'Topology: ha' "$tmpdir/check.log" || fail "--check did not detect the HA topology: $(cat "$tmpdir/check.log")"
[[ $(docker inspect --format '{{.Id}}' devshard-postgres) == "$v4_postgres_id" ]] || fail "--check changed the deployment"

"${updater[@]}" >"$tmpdir/update.log" 2>&1 || fail "the cutover failed"

fleet=(env GONKA_CONFIG_ENV="$join/config.env" "$join/versiond-router-fleet.sh")
compose_v5=(docker compose --project-name "$project" --project-directory "$join"
    -f "$join/docker-compose.yml" -f "$join/docker-compose.versiond.yml")

# The policy workers carry no fixed container name; every service is looked
# up through Compose.
container_of() {
    local id
    id=$("${compose_v5[@]}" ps -aq "$1") && [[ -n $id ]] || fail "no container for service $1"
    printf '%s\n' "${id%%$'\n'*}"
}

service_ids() {
    local service
    for service in "$@"; do container_of "$service"; done
}

converged() {
    local label=$1 replica identity first_identity='' db_identity
    for replica in versiond versiond2; do
        [[ $(docker inspect --format '{{.State.Health.Status}}' "$replica") == healthy ]] || \
            fail "$label: $replica is not healthy"
        docker inspect --format '{{json .NetworkSettings.Networks}}' "$replica" | jq -e --arg n "$back" 'has($n)' >/dev/null || \
            fail "$label: $replica is not on the router back network"
        docker inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$replica" | grep -q '^DEVSHARD_STORAGE_MODE=postgres$' || \
            fail "$label: $replica does not run from the v5 model"
        identity=$(docker exec "$replica" /bin/busybox wget -qO- -T 5 http://127.0.0.1:8080/internal/storage-identity | jq -er .identity) || \
            fail "$label: $replica serves no storage proof"
        [[ -z $first_identity || $identity == "$first_identity" ]] || fail "$label: replicas disagree on the lineage"
        first_identity=$identity
    done
    db_identity=$(psql_db "SELECT identity FROM devshard_storage_identity WHERE singleton") || \
        fail "$label: the migrated database has no lineage row"
    [[ $db_identity == "$first_identity" ]] || fail "$label: replicas and database disagree on the lineage"
    [[ $(psql_db "SELECT id FROM e2e_marker") == 42 ]] || fail "$label: pre-v5 data did not survive the migration"
    docker exec devshard-postgres test -f /var/lib/postgresql/gonka/data/.migrated-from-v4 || \
        fail "$label: the migration marker is not inside the persistent PGDATA"
    [[ $(docker inspect --format '{{.Id}}' devshard-postgres) != "$v4_postgres_id" ]] || \
        fail "$label: devshard-postgres was not replaced"
    ! docker container inspect versiond-router >/dev/null 2>&1 || fail "$label: the legacy versiond-router still exists"
    for service in proxy proxy-policy proxy-policy2; do
        [[ $(docker inspect --format '{{.State.Health.Status}}' "$(container_of "$service")") == healthy ]] || \
            fail "$label: $service is not healthy"
    done
    "${fleet[@]}" verify-admission >"$tmpdir/admission.log" 2>&1 || \
        fail "$label: the new proxy does not admit the router fleet: $(tail -n 10 "$tmpdir/admission.log")"
    local status version
    for version in v4 v5; do
        for _ in $(seq 30); do
            status=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$api_port/devshard/$version/healthz" || true)
            [[ $status == 200 ]] && break
            sleep 1
        done
        [[ $status == 200 ]] || fail "$label: /devshard/$version/healthz through the published port answered $status"
    done
    echo "update-devshard-e2e_test: $label converged"
}
converged "first cutover"
grep -q 'no replica serves a storage proof\|first cutover\|nothing to compare' "$tmpdir/update.log" || true

# --- a second run changes nothing -----------------------------------------------
ids_before=$(service_ids versiond versiond2 proxy proxy-policy proxy-policy2 devshard-postgres)
"${updater[@]}" >"$tmpdir/update.log" 2>&1 || fail "the rerun on a converged host failed"
[[ $(service_ids versiond versiond2 proxy proxy-policy proxy-policy2 devshard-postgres) == "$ids_before" ]] || \
    fail "the rerun recreated containers on a converged host"
grep -q 'wrote a challenge' "$tmpdir/update.log" || \
    fail "the rerun did not prove the database lineage through the replicas: $(grep -i 'postgres' "$tmpdir/update.log")"
converged "rerun"

# --- a run killed during the replica step converges on the next one -------------
# A configuration change makes the replica step do real work again.
sed -i 's/^VERSIOND_STOP_GRACE_PERIOD=.*/VERSIOND_STOP_GRACE_PERIOD=11s/' "$join/config.env"
"${updater[@]}" >"$tmpdir/update.log" 2>&1 &
updater_pid=$!
for _ in $(seq 600); do
    grep -q 'Step: versiond replicas' "$tmpdir/update.log" 2>/dev/null && break
    kill -0 "$updater_pid" 2>/dev/null || break
    sleep 0.5
done
grep -q 'Step: versiond replicas' "$tmpdir/update.log" || fail "the updater never reached the replica step: $(tail -n 20 "$tmpdir/update.log")"
sleep 2
kill -9 "$updater_pid" 2>/dev/null || true
wait "$updater_pid" 2>/dev/null || true
# Whatever Compose command was in flight finishes or fails on its own; the
# next run must cope with either.
for _ in $(seq 120); do
    pgrep -f "compose.*--project-name $project " >/dev/null 2>&1 || break
    sleep 1
done
"${updater[@]}" >"$tmpdir/update.log" 2>&1 || fail "the run after a killed run failed"
for replica in versiond versiond2; do
    [[ $(docker inspect --format '{{.Config.StopTimeout}}' "$replica") == 11 ]] || \
        fail "$replica does not carry the configuration of the interrupted run after the rerun"
done
converged "after a killed run"

echo "update-devshard-e2e_test: ok"
