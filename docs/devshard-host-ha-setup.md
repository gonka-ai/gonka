# High-availability Devshard Host Setup

**Audience:** hosts that serve inference over **devshard** (`/devshard/...` → `versiond` → `devshardd`).  
**Status:** draft for host operators - edit before wider distribution.  
**Goal:** run a **high-available (HA)** host stack so a single `versiond` / `devshardd` failure does not take the host offline.

**Release:** `devshard-0.2.15-v5`. Core checked at `39240311fb` with gateway and storage fixes from [PR #1730](https://github.com/gonka-ai/gonka/pull/1730) through `bf4de2d21`; versiond fleet and updater checked in the integration from [PR #1611](https://github.com/gonka-ai/gonka/pull/1611) through `ab2bb5171` on 2026-09-09.

---

## Why this matters

Use multiple `versiond` replicas with shared PostgreSQL to survive a replica failure. The examples use `versiond` and `versiond2`; add replicas with §2.4.

```text
Public HAProxy → nginx policy workers → HAProxy (:18081)
        │
        ▼
 versiond-router fleet   ← sticky routing by session/escrow ID
        │
        ├── versiond  (instance A) ──► devshardd children
        └── versiond2 (instance B) ──► devshardd children
                 │
                 └── shared Postgres  (required)
```

**You must use Postgres** for HA. SQLite is single-writer and **must not** be shared across instances.

---

## Prerequisites

1. Install join files from **`devshard-0.2.15-v5`** with the fleet/updater integration listed above: `update-devshard.sh`, `versiond-router-fleet.sh`, `deployment-lock.sh`, `updater-rollback.sh`, `updater-container-state.py` and `versiond-router-slot/`. In §2.1, select compatible v5 supervisor, HAProxy router/public proxy and nginx policy images from one tested candidate. Use governance-approved `devshardd` artifacts for each served protocol.
2. Working `node` + `api` (dapi) + `proxy` on the host (standard join deployment).
3. Same participant identity on **every** HA `versiond` replica:
   - same `KEY_NAME` / keyring
   - same `ACCOUNT_PUBKEY`
4. Use HA artifacts whose `--print-storage-mode` reports `postgres` and whose protocol matches the approved name. Pin older `v1` / `v2` / `v3` protocols to a dedicated legacy supervisor (§2.1).
5. Verify that `api:9100/versions` lists the required protocol, downloadable binary URL and SHA256. A branch checkout does not publish or approve artifacts; this guide uses `v5`.
6. Docker Compose **2.24.4 or newer**, Bash, Python 3, `curl` **7.71 or newer**, `jq`, `flock`, `sha256sum` and `timeout` on the machine running the fleet/updater scripts.

---

## Step 1 - Install Postgres (preferably HA itself)

Use replicated or managed PostgreSQL for database HA; a single database VM remains a point of failure.

### Choose a database

**Option A - Managed Postgres (recommended)** — AWS RDS Multi-AZ, GCP Cloud SQL HA, Azure Flexible Server HA, and similar.

Create a database and user, for example:

| Setting  | Example              |
| -------- | -------------------- |
| Database | `devshardd`          |
| User     | `devshardd`          |
| Password | strong secret        |

Record the writable endpoint, port (usually `5432`), database, user and password. Allow access from every replica.

The updater supports bundled or external writable PostgreSQL without explicit TLS settings: it rejects `PGSSL*` except `PGSSLMODE=disable`. If your provider requires explicit TLS configuration, use a different procedure; do not disable required TLS.

Connect directly or use **session pooling**. Transaction pooling and read-only replicas are unsupported; local Compose connects directly.

**Option B - Self-managed Postgres** — install Postgres on a dedicated host or cluster, create the role/DB, and configure replication yourself for DB HA:

```sql
CREATE USER devshardd WITH PASSWORD '...';
CREATE DATABASE devshardd OWNER devshardd;
```

**Option C - Local Compose Postgres** — use `docker-compose.versiond.yml` on the join host. Losing that host also loses access to the database.

Install `devshard-postgres-entrypoint.sh` with the Compose files; v5 stores PostgreSQL under `${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}/data`. For an existing deployment, follow §2.6 before recreating PostgreSQL to preserve its old anonymous volume.

### Where to put Postgres settings

Set `PGHOST` in **every replica’s container environment**. A shell export affects containers only when Compose interpolates it into `environment:`.

| What                                                                                                   | File on the host                                                                                         | Who sets it                                                                        |
| ------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| DB name / user / password                                                                              | `deploy/join/config.env` (you edit)                                                                      | You                                                                                |
| `PGHOST`, `PGDATABASE`, `PGUSER`, `PGPASSWORD`, `DEVSHARD_STORAGE_MODE` on every `versiond*` container | `deploy/join/docker-compose.versiond.yml` (already in the overlay) **or** a compose **override** you add | Overlay by default; override only for an external DB; repeat for any extra replica |

#### External or managed Postgres

For Options A/B:

1. Put credentials in `deploy/join/config.env`:
   ```bash
   export DEVSHARD_POSTGRES_DB=devshardd
   export DEVSHARD_POSTGRES_USER=devshardd
   export DEVSHARD_POSTGRES_PASSWORD='<strong-password>'
   ```
2. Add a compose override (for example `deploy/join/docker-compose.devshard-pg-external.override.yml`) that sets `PGHOST` (and related vars) under **every** `versiond`* service in the HA pool and disables the unused local database and its dependencies — see §2.2.
  Needed because the stock overlay hardcodes `PGHOST=devshard-postgres`.
3. Start with **four** `-f` files: base + `versiond` overlay + the v5 override from §2.1 + your external-PG override.

> Do not run multiple `versiond` instances on SQLite.
>
> Keep `GONKA_HA=true`, `DEVSHARD_STORAGE_MODE=postgres` and `PGHOST` on HA `versiond` containers. v5 checks storage before starting HA children as well as when `versiond-router` sends `Devshard-Ha: true`. Hybrid/SQLite fallback is not supported for HA.

#### Local compose Postgres (`devshard-postgres`)

For Option C:

1. Edit `deploy/join/config.env` and add (or uncomment):
   ```bash
   export DEVSHARD_POSTGRES_DB=devshardd
   export DEVSHARD_POSTGRES_USER=devshardd
   export DEVSHARD_POSTGRES_PASSWORD='<strong-password>'
   ```
2. Leave `PGHOST` / `DEVSHARD_STORAGE_MODE` out of `config.env` — they are already set on every `versiond*` service defined in `docker-compose.versiond.yml` (and must be set the same way on any extra replicas you add):
   ```yaml
   - PGHOST=devshard-postgres
   - PGDATABASE=${DEVSHARD_POSTGRES_DB:-devshardd}
   - PGUSER=${DEVSHARD_POSTGRES_USER:-devshardd}
   - PGPASSWORD=${DEVSHARD_POSTGRES_PASSWORD:?DEVSHARD_POSTGRES_PASSWORD is required}
   - DEVSHARD_STORAGE_MODE=postgres
   ```
3. `source ./config.env`, then start with `-f docker-compose.versiond.yml` (see §2.1). A fresh empty deployment initializes its persistent cluster automatically. If binaries already exist but no cluster is attached, startup refuses empty initialization: restore the old database, or set `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true` once only for confirmed first-time HA enablement, then unset it. A `.pg-bound` marker always requires restoring the database; the flag cannot bypass it.

---

## Step 2 - Run multiple `versiond` instances + `versiond-router`

Example files in your setup. Some are present in the release branch:

| File                                                           | Role                                                                                                  |
| -------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `deploy/join/docker-compose.yml`                               | Base join (`versiond`, `proxy`, …)                                                                    |
| `deploy/join/docker-compose.versiond.yml`                      | HA overlay: Postgres + additional replica (`versiond2`) + shared networks for the router fleet               |
| `deploy/join/docker-compose.devshard-v5.override.yml`     | v5 images + the **optional** `oracle-filter` used in this example (you create this; see §2.1) |
| `deploy/join/docker-compose.devshard-pg-external.override.yml` | Optional: point **every** `versiond`* replica at managed Postgres (see §2.2)                          |
| `deploy/join/versiond-router-slot/` | Router slot Compose files; managed by `versiond-router-fleet.sh` in separate projects |
| `deploy/join/update-devshard.sh` | Updates the local deployment in order, including the versiond router fleet |

**Optional `oracle-filter`:** the example service restricts `api:9100/versions` to `VERSIOND_VERSIONS`, giving supervisors and both routing tiers the same selected HA protocols. It does not approve versions or change the chain catalog.

For direct API access, follow [Running without the filter](#running-without-the-filter) and verify catalog compatibility. `VERSIOND_NON_HA_VERSIONS` controls routing and HA preflight, **not which versions versiond launches**. This example uses the filter and an empty non-HA list.

### 2.1 Same machine, multiple instances (fresh v5 installation)

On the join host (for an existing deployment, prepare these files but follow §2.6 before `up -d`):

#### 1. Credentials in `config.env`

```bash
cd /path/to/gonka/deploy/join

# load existing join secrets (KEY_NAME, KEYRING_PASSWORD, …)
source ./config.env
```

Add to `config.env` (password is required; DB/user/`VERSIOND_POOL_HOST` have compose defaults but setting them explicitly is fine):

```bash
export DEVSHARD_POSTGRES_DB=devshardd
export DEVSHARD_POSTGRES_USER=devshardd
export DEVSHARD_POSTGRES_PASSWORD='<strong-password>'

export VERSIOND_IMAGE='<v5-versiond-image:tag-or-digest>'
export VERSIOND_ROUTER_IMAGE='<v5-haproxy-router-image:tag-or-digest>'
export PROXY_ROUTER_IMAGE='<v5-proxy-router-image:tag-or-digest>'
export PROXY_POLICY_IMAGE='<v5-proxy-image:tag-or-digest>'
export VERSIOND_VERSIONS="v5"
export VERSIOND_NON_HA_VERSIONS=""
# Example with the optional filter; without it use http://api:9100/versions.
export VERSIOND_ROUTING_CATALOG_URL=http://oracle-filter:9100/versions
export VERSIOND_ROUTER_FLEET_SLOTS="0 1 2"
export VERSIOND_ROUTER_MIN_READY=2
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5.override.yml
# Append every additional override used by this deployment, in the same order.
# For an upgrade retaining v4 sessions, use "v4 v5" after the checks in §2.6.
# These fleet settings are read from config.env by both deployment scripts.
```

Select published images from the tested candidate. Put fleet settings in `config.env`: its separate Compose projects do not inherit the main override. The default is three router slots with two kept ready during replacement, independent of the replica count.

#### 2. Create `docker-compose.devshard-v5.override.yml`

```bash
cat > docker-compose.devshard-v5.override.yml <<'EOF'
services:
  # Filtered /versions: only selected HA protocols (prevents v3 children on the HA+Postgres pool)
  oracle-filter:
    container_name: oracle-filter
    image: python:3.12-alpine
    environment:
      - ORACLE_UPSTREAM=http://api:9100/versions
      - ORACLE_ALLOW=${VERSIOND_VERSIONS:-v5}
      - LISTEN_PORT=9100
    command:
      - python
      - -c
      - |
        import json, os, urllib.request
        from http.server import BaseHTTPRequestHandler, HTTPServer
        UP = os.environ["ORACLE_UPSTREAM"]
        ALLOW = set(x.strip() for x in os.environ.get("ORACLE_ALLOW", "v5").replace(",", " ").split() if x.strip())
        PORT = int(os.environ.get("LISTEN_PORT", "9100"))
        class H(BaseHTTPRequestHandler):
            def do_GET(self):
                if self.path.split("?",1)[0] not in ("/versions", "/"):
                    self.send_response(404); self.end_headers(); return
                with urllib.request.urlopen(UP, timeout=10) as r:
                    data = json.load(r)
                vers = [v for v in data.get("versions", []) if v.get("name") in ALLOW]
                body = json.dumps({"versions": vers}).encode()
                self.send_response(200)
                self.send_header("Content-Type", "application/json")
                self.send_header("Content-Length", str(len(body)))
                self.end_headers()
                self.wfile.write(body)
            def log_message(self, *args):
                pass
        HTTPServer(("0.0.0.0", PORT), H).serve_forever()
    depends_on:
      api:
        condition: service_started
    networks:
      default: {}
      versiond-router-back: {}
    restart: always

  versiond:
    image: ${VERSIOND_IMAGE:?select the v5 versiond image}
    environment:
      - VERSIOND_ORACLE_URL=http://oracle-filter:9100/versions
      - VERSIOND_NON_HA_VERSIONS=${VERSIOND_NON_HA_VERSIONS-}
    depends_on:
      oracle-filter:
        condition: service_started
      devshard-postgres:
        condition: service_healthy

  versiond2:
    image: ${VERSIOND_IMAGE:?select the v5 versiond image}
    environment:
      - VERSIOND_ORACLE_URL=http://oracle-filter:9100/versions
      - VERSIOND_NON_HA_VERSIONS=${VERSIOND_NON_HA_VERSIONS-}
    depends_on:
      oracle-filter:
        condition: service_started
      devshard-postgres:
        condition: service_healthy

  # If you add versiond3 (or more), give each the same oracle-filter env + depends_on.

  proxy:
    environment:
      - VERSIOND_NON_HA_VERSIONS=${VERSIOND_NON_HA_VERSIONS-}
      - VERSIOND_ROUTING_CATALOG_URL=${VERSIOND_ROUTING_CATALOG_URL:?set the routing catalog URL}
      - VERSIOND_VERSIONS=${VERSIOND_VERSIONS:-v5}
EOF
```

##### Running without the filter

For direct catalog access, make these changes to the example before starting it:

- Remove the `oracle-filter` service from `docker-compose.devshard-v5.override.yml` and its entries in every service's `depends_on`, including any external-PG or extra-replica overrides. Keep the other dependencies and image/storage settings.
- Set `VERSIOND_ORACLE_URL=http://api:9100/versions` on every local `versiond` service. On remote hosts, use the reachable private address of the same API.
- Set `export VERSIOND_ROUTING_CATALOG_URL=http://api:9100/versions` in `config.env` for both routing tiers. Ensure the endpoint is reachable from every router slot.
- Skip commands that start, recreate or inspect `oracle-filter` elsewhere in this guide. Query `api:9100/versions` instead when checking the catalog.

With direct access, `VERSIOND_VERSIONS` only bootstraps routers; it **does not filter the catalog**. All protocols visible to HA supervisors must support shared PostgreSQL. For pre-HA protocols, use the legacy-owner layout below and exclude them from HA peers’ catalogs. Compatible newly approved versions are discovered automatically.

#### 3. Bring up the main stack and router fleet

The command below uses local PostgreSQL. For an external database, first create the override in §2.2 and use that section’s startup command instead.

```bash
source ./config.env
./versiond-router-fleet.sh prepare-networks

docker compose \
  -f docker-compose.yml \
  -f docker-compose.versiond.yml \
  -f docker-compose.devshard-v5.override.yml \
  up -d --wait --wait-timeout 2100

./versiond-router-fleet.sh apply
./versiond-router-fleet.sh verify-admission
./versiond-router-fleet.sh wait-version v5
```

Local replica data remains separate: `versiond` uses `./devshards/data`, `versiond2` uses `./devshards2/data`.

#### 4. Confirm

```bash
docker ps --format '{{.Names}}\t{{.Status}}' | grep -E 'oracle-filter|versiond|devshard-postgres'

docker inspect proxy --format '{{range .Config.Env}}{{println .}}{{end}}' | grep VERSIOND_
# VERSIOND_ROUTER_POOL_HOST=versiond-router-fleet
# VERSIOND_NON_HA_VERSIONS=   (empty)
# VERSIOND_ROUTING_CATALOG_URL=http://oracle-filter:9100/versions

./versiond-router-fleet.sh status
./versiond-router-fleet.sh verify-admission

# With the optional filter; otherwise query api:9100/versions.
docker exec oracle-filter wget -qO- http://127.0.0.1:9100/versions
# expect the selected approved protocols and their real binary URL/SHA256

# Include every local replica in this list.
for replica in versiond versiond2; do
  docker exec "$replica" wget -qO- http://127.0.0.1:8080/healthz
  docker exec "$replica" wget -qO- "http://127.0.0.1:8080/readyz?version=v5"
done
# expect v5 running and per-version readiness HTTP 200 on every replica
./versiond-router-fleet.sh wait-version v5
# Public proxy health or router liveness alone does not prove that a route is ready
```

#### Optional: still serving pre-v4 (v3) on the same host

For retained SQLite protocols, use a dedicated non-HA supervisor. Stock overlay defaults:

```bash
export VERSIOND_LEGACY_HOST=versiond
export VERSIOND_NON_HA_VERSIONS="v1 v2 v3"
```

Keep the non-HA pin list. Give the legacy owner a legacy-only catalog and its SQLite data; keep HA peers on the filtered PostgreSQL-compatible catalog. Set `VERSIOND_LEGACY_HOST` and `VERSIOND_NON_HA_VERSIONS` in `config.env` for supervisors and both routing tiers. Change an existing fleet’s owner/list only through Step 5’s maintenance procedure.

### 2.2 Using external / managed Postgres with the same overlay

`config.env` alone is **not enough**: stock `docker-compose.versiond.yml` still sets `PGHOST=devshard-postgres`. You must add a **compose override file on the host**.

1. Keep credentials in `deploy/join/config.env` (`DEVSHARD_POSTGRES_*` as above).
2. Create `deploy/join/docker-compose.devshard-pg-external.override.yml` (name is yours; keep it in `deploy/join/`):

```yaml
services:
  devshard-postgres:
    profiles: [local-postgres]  # Do not enable this profile for external PG.

  # Repeat this environment block for every HA replica (versiond, versiond2, versiond3, …).
  versiond:
    environment:
      - PGHOST=your-managed-pg.example.com
      - PGPORT=5432
      - PGDATABASE=${DEVSHARD_POSTGRES_DB:-devshardd}
      - PGUSER=${DEVSHARD_POSTGRES_USER:-devshardd}
      - PGPASSWORD=${DEVSHARD_POSTGRES_PASSWORD}
      - DEVSHARD_STORAGE_MODE=postgres
    depends_on: !override
      api:
        condition: service_started
      oracle-filter:
        condition: service_started

  versiond2:
    environment:
      - PGHOST=your-managed-pg.example.com
      - PGPORT=5432
      - PGDATABASE=${DEVSHARD_POSTGRES_DB:-devshardd}
      - PGUSER=${DEVSHARD_POSTGRES_USER:-devshardd}
      - PGPASSWORD=${DEVSHARD_POSTGRES_PASSWORD}
      - DEVSHARD_STORAGE_MODE=postgres
    depends_on: !override
      api:
        condition: service_started
      oracle-filter:
        condition: service_started
```

Compose must support `!override`. Repeat the dependency override for every extra replica so the unused local database is excluded from startup and cannot trigger its lost-database guard.

3. Set the complete file list in `config.env`, including the external-PG override:

```bash
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5.override.yml:docker-compose.devshard-pg-external.override.yml
```

Then start (omit filter dependencies for direct catalog access):

```bash
cd /path/to/gonka/deploy/join
source ./config.env
./versiond-router-fleet.sh prepare-networks

docker compose \
  -f docker-compose.yml \
  -f docker-compose.versiond.yml \
  -f docker-compose.devshard-v5.override.yml \
  -f docker-compose.devshard-pg-external.override.yml \
  up -d --wait --wait-timeout 2100

./versiond-router-fleet.sh apply
./versiond-router-fleet.sh verify-admission
./versiond-router-fleet.sh wait-version v5
```

### 2.3 Multiple machines (recommended, true host HA)

Keep local replicas and the router fleet on A; add a replica on B over a private network. Bind additional listeners to private IPs.

Example extending the two local replicas from §2.1:

| Role      | Runs                                                            |
| --------- | --------------------------------------------------------------- |
| Machine A | `versiond` + `versiond2`, usual node/api/proxy and the `versiond-router` fleet |
| Machine B | `versiond` only — **no** second dapi with the same keys         |
| Shared    | Postgres reachable from every `versiond` (managed HA preferred) |

> Run only one `decentralized-api` (dapi) with the participant’s keys. This procedure makes devshard traffic HA.

#### On machine A (dapi / Postgres / router side) — publish for B on the private network

1. Postgres (`5432`) and node-manager gRPC (`9400`) — required.
2. Chain RPC/gRPC (`26657`, `9090`) — required for remote `devshardd` (same as local `NODE_HOST=node`).
3. Oracle URL — use the **same catalog source** as local HA: the optional `oracle-filter` on a private port, or direct API access when running without it. If A uses a filter, bypassing it on B can launch versions excluded from A’s HA pool.
4. Confirm `PGPASSWORD` / `KEYRING_PASSWORD` in `config.env` match what **running** local `versiond`* containers use.

For the local PostgreSQL and optional-filter example, put A’s private IP in
`config.env` as `export GONKA_PRIVATE_BIND_IP=<A-private-ip>`, then create
`docker-compose.devshard-private.override.yml` in `deploy/join`:

```bash
cat > docker-compose.devshard-private.override.yml <<'EOF'
services:
  node:
    ports:
      - "${GONKA_PRIVATE_BIND_IP:?set A's private IP}:26657:26657"
      - "${GONKA_PRIVATE_BIND_IP:?set A's private IP}:9090:9090"
  api:
    ports:
      - "${GONKA_PRIVATE_BIND_IP:?set A's private IP}:${NODE_MANAGER_GRPC_PORT:-9400}:${NODE_MANAGER_GRPC_PORT:-9400}"
  devshard-postgres:
    ports:
      - "${GONKA_PRIVATE_BIND_IP:?set A's private IP}:5432:5432"
  oracle-filter:
    ports:
      - "${GONKA_PRIVATE_BIND_IP:?set A's private IP}:19100:9100"
EOF
```

With external PostgreSQL, omit the `devshard-postgres` entry and use the actual
shared database host/port on B. Without the filter, omit `oracle-filter` and use
A’s API catalog on port `9100`, already published by the base Compose file.

Include this override after all existing files in A’s Compose commands and
persist the same complete list in `COMPOSE_FILE` in `config.env`. On a fresh
installation, include it in the startup command from §2.1 or §2.2. For an
existing installation, publishing these ports recreates the affected services:
schedule maintenance, stop all database writers before recreating local
PostgreSQL, apply the complete configuration, then wait for readiness and run
`./update-devshard.sh --check` before reopening traffic. Do not restart the
whole live stack merely to add a remote member.

Before starting B, check these endpoints **from B** (replace A’s private IP):

```bash
curl -fsS http://<A-private-ip>:19100/versions  # direct catalog: port 9100
docker run --rm --network host --read-only \
  --entrypoint pg_isready "${DEVSHARD_POSTGRES_IMAGE:-postgres:16-alpine}" \
  -h <A-private-ip> -p 5432                   # use the shared DB endpoint
```

Require reachability from B to PostgreSQL, the catalog, chain and node-manager ports. `pg_isready` checks reachability; the later `--check-storage` verifies that B uses the same database.

#### On machine B (`versiond` only) — one compose file is enough

Configure B as follows:

1. **Same participant identity as A** — same `KEY_NAME`, `ACCOUNT_PUBKEY`, `KEYRING_PASSWORD`, and a copy of A’s `.inference/keyring-file/`. On A, run `docker cp versiond:/root/.inference/keyring-file .`, then transfer the copied `keyring-file/` to B’s `.inference/`. Mount B’s `.inference/` read-only as `/root/.inference`. Do **not** start a second `api` with those keys on B.
2. **Set connection variables in the service’s `environment:`.** Shell exports reach the container only through Compose interpolation.
3. **Own data dir** on B (do not share A’s `./devshards*/data`). Binary cache dir may be local.
4. **Publish** `versiond` **on B’s private IP at port 8080** (recommended) so A’s routers can reach it through the endpoint list or pool DNS configured below. Bind LAN-only, not `0.0.0.0`. Optionally firewall so only A can connect.

Example file on B: `docker-compose.versiond-remote.yml` (replace private IPs; `source ./config.env` before `docker compose up`):

```yaml
services:
  versiond:
    image: ${VERSIOND_IMAGE:?select the same v5 versiond image as A}
    container_name: versiond
    environment:
      # Same catalog source as on A (optional filter or direct API)
      - VERSIOND_ORACLE_URL=http://<A-private-ip>:19100/versions
      - GONKA_HA=true
      - VERSIOND_NON_HA_VERSIONS=${VERSIOND_NON_HA_VERSIONS-}
      - VERSIOND_HOST_SHUTDOWN_BUDGET=25m
      - VERSIOND_DRAIN_ANNOUNCE=5s
      - VERSIOND_BINARY_NAME=devshardd
      - NODE_MANAGER_ADDR=<A-private-ip>:9400
      - NODE_HOST=<A-private-ip>
      - KEY_NAME=${KEY_NAME}
      - ACCOUNT_PUBKEY=${ACCOUNT_PUBKEY}
      - KEYRING_BACKEND=${KEYRING_BACKEND:-file}
      - KEYRING_PASSWORD=${KEYRING_PASSWORD}
      - KEYRING_DIR=/root/.inference
      - PGHOST=<A-private-ip>  # external PG: use the shared database host
      - PGPORT=5432            # external PG: use its actual port
      - PG_POOL_MAX_CONNS=${DEVSHARD_POSTGRES_POOL_MAX_CONNS:-4}
      - PGDATABASE=${DEVSHARD_POSTGRES_DB:-devshardd}
      - PGUSER=${DEVSHARD_POSTGRES_USER:-devshardd}
      - PGPASSWORD=${DEVSHARD_POSTGRES_PASSWORD:?DEVSHARD_POSTGRES_PASSWORD is required}
      - DEVSHARD_STORAGE_MODE=postgres
    volumes:
      - .inference:/root/.inference:ro
      - ./devshards-remote/bin:/opt/versiond/bin
      - ./devshards-remote/data:/opt/versiond/data
    ports:
      - "<B-private-ip>:8080:8080"   # LAN only — not 0.0.0.0
    healthcheck:
      test: ["CMD", "/bin/busybox", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/readyz?version=v5"]
      interval: 2s
      timeout: 3s
      retries: 3
      start_period: 30m
    stop_grace_period: 30m
    restart: always
```

```bash
mkdir -p devshards-remote/{bin,data}
source ./config.env
docker compose -f docker-compose.versiond-remote.yml up -d --wait --wait-timeout 2100
curl -fsS "http://<B-private-ip>:8080/readyz?version=v5"   # not 127.0.0.1 if bound to LAN IP only
```

##### Check B's database before admitting it to the pool

For `--check-storage`, install the PostgreSQL client (`psql`) on the host
running the check, then prepare a separate reference connection file:

```bash
cd /path/to/gonka/deploy/join
cp pool-postgres.env.template pool-postgres.env
chmod 600 pool-postgres.env
```

Fill `pool-postgres.env` with the existing pool's known working PostgreSQL
host, port, database and credentials. Obtain these from A or the database
administrator, independently of the replica being checked. With the intended
versiond image and configuration running, execute:

```bash
./update-devshard.sh --check-storage --reference-env ./pool-postgres.env
```

Require `Storage check passed` and exit code 0 before admission. The check writes through each child’s database connection and verifies against the independent reference. It defaults to container `versiond`; repeat `--container NAME` for other local members. No network-node Compose services are needed. Check one host at a time, with no updater running elsewhere.

#### On the machine that runs the router fleet (usually A)

List every local and remote member in `versiond-endpoints.json`. Local service names resolve on the shared router back network; remote addresses must be reachable from every router slot:

```json
[
  {"id": "local-a", "host": "versiond", "port": 8080},
  {"id": "local-a-2", "host": "versiond2", "port": 8080},
  {"id": "remote-b", "host": "10.0.0.12", "port": 8080}
]
```

Put `export VERSIOND_POOL_ENDPOINTS_FILE=./versiond-endpoints.json` in A's `config.env`. For a fresh fleet, `./versiond-router-fleet.sh apply` reads this list. Changing an existing list requires a maintenance window:

```bash
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
  ./versiond-router-fleet.sh maintenance-rollout
./versiond-router-fleet.sh verify-admission
```

Use `maintenance-rollout` for membership changes; editing the file alone has no effect and ordinary `apply` refuses them. Expect an interruption to new requests. Preserve member IDs when replacing a host at the same endpoint.

Alternatively, set `VERSIOND_POOL_HOST` to private DNS resolving all reachable members; Docker’s local alias cannot discover remote hosts. Every router must resolve both the pool and internal names. Membership updates are automatic, but changing the pool name/resolver requires maintenance. Use an explicit list for differing ports.

#### Verify cross-machine HA

Adapt the failover check in [Step 4](#step-4---verify-it-works), item 6 of the command block, to a real session: find a sticky session whose `X-Upstream-Addr` is the remote replica, stop **that** machine’s `versiond`, wait for withdrawal and confirm the same session continues on a survivor with its committed state intact. `X-Upstream-Addr` shows the final selected peer, not an nginx retry history.

### 2.4 Adding more replicas

1. Add another `versiond` service (new container name + new data volume). The supplied `docker-compose.versiond3.yml` is an example; copy it with new names/data paths for additional replicas. Include it in every Compose command and the updater's `COMPOSE_FILE`. Its PostgreSQL mount also lets the lost-database guard see that replica's `.pg-bound` marker.
2. Give it the same image, catalog source, identity, HA/Postgres environment and drain settings; extend §2.1's override and §2.2's external-PG override for it. On the router back network add alias `versiond-pool`; across machines follow §2.3.
3. Start it and wait for every required `/readyz?version=...` to return 200. DNS discovery needs no router recreation. An explicit endpoint list needs the maintenance procedure from §2.3; then confirm real inference through the public route.

### 2.5 Operating versiond members

Use the same ordered compose files for every operation (append §2.2's external-PG override when applicable):

```bash
cd /path/to/gonka/deploy/join
source ./config.env
# COMPOSE_FILE must include every active override, including external PG,
# private ports and additional replicas; keep this list in config.env.
: "${COMPOSE_FILE:?set the complete Compose file list in config.env}"
dc=(docker compose)

# Stop only one member, keeping enough ready survivors for the load.
"${dc[@]}" stop versiond2
# Restart the same member with its existing data:
"${dc[@]}" up -d --no-deps --wait --wait-timeout 2100 versiond2
docker exec versiond2 wget -qO- "http://127.0.0.1:8080/readyz?version=v5"
```

**Stop timing:** v5 withdraws readiness and drains accepted work on stop. Retain the 5-second announcement, 25-minute host budget and 30-minute Compose grace. Announcement must cover router detection and must not be `0s` behind HAProxy. With custom durations, include units (`30s`, `5m`) and keep announcement plus child termination grace (default 10 minutes) below the host budget. Requests exceeding the budget may be forcibly interrupted.

**Replace an image:** select a compatible image, stop that service and run `up -d --no-deps` for it only. Keep ready survivors yourself; Compose does not enforce a reserve. Verify every required version and inference before replacing another member. On failure, restore its previous image/configuration, recreate it and verify against the current database.

**Replace a remote member:** stop/drain it, then remove its explicit endpoint using §2.3’s maintenance procedure. Start its replacement and pass `--check-storage` on that host before restoring membership. For DNS pools, keep the replacement out of DNS until the check passes. The local updater does not inspect remote containers.

**Remove a member:** stop/drain it, remove its service or set replicas to zero (`VERSIOND2_REPLICAS=0` for `versiond2`), then remove its DNS/explicit membership. Use §2.3 maintenance for explicit lists; DNS changes need no router recreation. Verify remaining sessions and retain data/binary directories until recovery is confirmed.

**Add an approved HA protocol:** extend `VERSIOND_VERSIONS` in `config.env`, source it and recreate only `oracle-filter` with the complete ordered files. Run `./versiond-router-fleet.sh wait-version <version>` and verify inference before use; routers discover it automatically. Preserve `proxy-router-state` and each slot’s `router-state`. Accepted routes survive catalog outages and are not removed automatically by default; remove a protocol only during supervised maintenance after its sessions are no longer needed.

### 2.6 Upgrade an existing HA deployment to v5

Use the same Postgres, identity and per-replica mounts as the existing deployment. Take a database backup and record the current images, approved binary URLs/SHA256, compose project/files, mounts and a working escrow. Preserve `.inference`, each `devshards*/data` directory, the binary cache and router catalog state. Prepare the v5 files from §2.1 in the **same compose project**, but do not run an unrestricted `up -d` yet.

```bash
cd /path/to/gonka/deploy/join
source ./config.env
# Keep every active override in the ordered COMPOSE_FILE saved in config.env.
: "${COMPOSE_FILE:?set the complete Compose file list in config.env}"
dc=(docker compose)
```

#### 1. Keep existing protocols available

For a v4-only deployment, keep `VERSIOND_VERSIONS="v4"` during cutover, even if v5 is approved: the fleet must admit currently serving versions before supervisors are replaced. Use the optional filter or a direct catalog satisfying this constraint. Check the approved v4 binary reports `postgres` from `--print-storage-mode` in the HA environment and `v4` from `--print-protocol-version`; artifacts without the storage probe cannot join. Match gateway/host artifacts. Retain v4 routes and compatible artifacts for existing sessions; do not rename binaries or escrows to v5. Use a new escrow for v5.

Add approved v5 only after retained v4 inference passes through the fleet. Supervisors migrate verified binary-cache entries automatically or download them again. **Do not delete or rename** the cache or `<data>/<version>` directories.

#### 2. Local PostgreSQL only — migrate the existing cluster during maintenance

Managed/external PostgreSQL with unchanged data skips this cluster-copy step. Before stopping anything, record the old local source and run the v5 space preflight.

Prepare the target directory with Docker, including under root-owned `devshards/`, then require preflight success before migration.

```bash
# Run in deploy/join, before removing/recreating the old container.
docker inspect devshard-postgres --format '{{json .Mounts}}'
docker exec devshard-postgres sh -c \
  'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT system_identifier FROM pg_control_system();"'
# Record the system identifier, source volume at /var/lib/postgresql/data, and backup.
pg_dir="${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}"
[[ "$pg_dir" = /* ]] || pg_dir="$PWD/$pg_dir"
# Create the target directory so preflight's space check works under a root-owned parent.
docker run --rm --network none --read-only \
  --security-opt label=disable \
  --volume "$pg_dir:/target:ro" \
  --entrypoint /bin/true \
  "${POSTGRES_MIGRATION_HELPER_IMAGE:-${DEVSHARD_POSTGRES_IMAGE:-postgres:16-alpine}}" &&
bash ./devshard-postgres-migration-preflight.sh \
  --source-container devshard-postgres \
  --target-dir "${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}"
```

The copy needs the source cluster's size plus 10% free space. Keep the existing PostgreSQL major version and Alpine/musl image family (`postgres:16-alpine` by default); this is a data-directory copy, not `pg_upgrade` or conversion between image variants.

Enter a maintenance window, stop new devshard traffic, let accepted work finish, and stop **all** database-writing HA members, including remote ones:

```bash
# Include every local member; stop remote members on their own machines too.
docker stop --time 1800 versiond versiond2
# Refresh the database backup now that application writes have stopped.
docker stop --time 300 devshard-postgres
./versiond-router-fleet.sh prepare-networks
"${dc[@]}" up -d --no-deps devshard-postgres
"${dc[@]}" logs --tail=100 devshard-postgres
```

Recreate the database **in place** so Compose retains its old anonymous volume. Do **not** use `down`, `down -v`, `rm -v`, volume pruning, or `--renew-anon-volumes` before migration. The entrypoint copies the old cluster into staging, publishes it atomically under `devshards/postgres/data`, and preserves the source. Wait for PostgreSQL health, repeat the system-identifier query above and verify the recorded identifier and committed data before starting writers. If a copy fails, preserve the source, resolve the logged error and retry; an incomplete target is not a usable database.

If the old volume was already detached, use its **recorded exact name**, not an empty replacement:

```bash
export DEVSHARD_POSTGRES_LEGACY_VOLUME='<recorded-old-volume-name>'
# Command-line -f replaces COMPOSE_FILE, so pass the complete list explicitly.
files=()
IFS=':' read -ra parts <<<"$COMPOSE_FILE"
for f in "${parts[@]}"; do files+=(-f "$f"); done
pg_dir="${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}"
[[ "$pg_dir" = /* ]] || pg_dir="$PWD/$pg_dir"
# Create the target directory so preflight's space check works under a root-owned parent.
docker run --rm --network none --read-only \
  --security-opt label=disable \
  --volume "$pg_dir:/target:ro" \
  --entrypoint /bin/true \
  "${POSTGRES_MIGRATION_HELPER_IMAGE:-${DEVSHARD_POSTGRES_IMAGE:-postgres:16-alpine}}" &&
bash ./devshard-postgres-migration-preflight.sh \
  --source-volume "$DEVSHARD_POSTGRES_LEGACY_VOLUME" \
  --target-dir "${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}" &&
docker compose "${files[@]}" -f docker-compose.versiond-postgres-recovery.yml \
  up -d --no-deps devshard-postgres
```

After verifying migration, recreate PostgreSQL once without the recovery overlay, while writers remain stopped. Keep this same PostgreSQL image and Compose configuration for the updater so it does not recreate the database again after writers restart. Keep the source volume and backup. Never use `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT` to recover a deployment whose database is missing.

#### 3. Run the updater with the complete deployment configuration

1. **Prepare for the cutover.** Work in `deploy/join` with the complete `COMPOSE_FILE` from the start of §2.6. Close public traffic and let accepted work finish; replacing the public proxy can interrupt connections. Keep the catalog limited to v4 until the retained v4 escrow passes the checks below. Leave `UPDATE_SKIP_POSTGRES_PROBE` and `UPDATE_ACCEPT_DATABASE_CHANGE` disabled.

   **External PostgreSQL with pre-v5 supervisors:** stop all database writers, including remote members, and independently verify the target database. Start the v5 supervisors with the complete Compose model and check every member with `--check-storage` as in §2.3 before running the updater. Do not use the legacy-container restart below for this case.

2. **Prepare the network and running members.**

   ```bash
   ./versiond-router-fleet.sh prepare-networks
   # Only when using the optional filter:
   docker compose up -d --no-deps oracle-filter

   # Only after a local database copy: restart the retained old containers.
   # Include every stopped local member; start remote members on their hosts.
   docker start versiond versiond2
   ```

   Keep already-running members running. For each retained pre-v5 member, verify v4 route health before proceeding:

   ```bash
   # Include every local member; repeat on remote hosts.
   (
     for replica in versiond versiond2; do
       docker exec "$replica" wget -qO- http://127.0.0.1:8080/v4/healthz || exit 1
     done
   )
   ```

   Require a successful response from every member. For v5 supervisors, also require HTTP 200 from `/readyz?version=v4`; do not ignore a 503. After a local database copy, confirm the recorded database identity and retained session as described above. Keep a ready survivor for every served HA version.

3. **Check and update this host.** Continue only if each command succeeds.

   ```bash
   ./update-devshard.sh --check
   ./update-devshard.sh
   ./versiond-router-fleet.sh verify-admission
   ./versiond-router-fleet.sh wait-version v4
   ```

   The updater checks PostgreSQL, updates routing and replaces local replicas one at a time. `--check` writes database probes but does not replace services. Optionally run `--dry-run` before updating to inspect the plan. Update remote members separately using §2.5.

4. **Verify v4, then enable v5.** Run inference with the retained v4 escrow through the public fleet route. Once it works, set `VERSIOND_VERSIONS="v4 v5"` and refresh the optional filter as in §2.5, or verify that the direct catalog exposes both approved, compatible versions. Run `./versiond-router-fleet.sh wait-version v5`, then verify a new v5 escrow and complete Step 4's checks for every served version before reopening traffic.

5. **If the update fails, fix the reported cause before retrying.** Inspect logs and rerun normally with the same complete Compose configuration and persistent `UPDATE_STATE_DIR` (default under `~/.local/state/gonka/updater/`). The updater attempts to restore failed replacements; a normal rerun recovers interrupted replacements. Keep previous join files and backups: rollback does not undo file edits or database writes. Keep the saved v4 volume unchanged and do not bypass migration-marker checks. For a database-history error, verify the selected data directory before retrying.

#### Reference: state migration and rollback

Existing HA PostgreSQL data stays in the same database. v5 applies forward schema migrations under a database advisory lock. For first-time conversion of a single-owner deployment, explicit `postgres` mode also imports supported epoch-layout SQLite sessions and file payloads before serving; stop the old writer and migrate each source directory with one owner. Successful sources are quarantined as `*.migrated.<timestamp>`, and conflicting data aborts startup. Older monolithic layouts need separate verification.

A wire-compatible **same-protocol** artifact update is different from adding v5: PostgreSQL children can overlap while the candidate starts and, when supported, reports `recovery_complete=true` (default `VERSIOND_RECOVERY_TIMEOUT=30m`). Check `recovery_failed` and logs separately: completion does not mean every session recovered successfully. Failed preparation keeps the predecessor serving. SQLite/hybrid replacements drain and stop before starting; older candidates without the recovery field skip that recovery wait. Keep pool capacity for overlapping children. Do not repeatedly change the approved artifact while predecessors are still draining.

Restoring an old image does not undo database migrations or later committed writes. There is no automatic PostgreSQL-to-SQLite or schema downgrade; retain `.pg-bound`, use a binary known to read the current state, or perform a coordinated restore during maintenance. The preserved v4 cluster is a recovery source from the copy time, not a current replica after v5 writes. Validate restart/rollback on a copy of the actual state before relying on it.

---

## Step 3 - Environment variables checklist

### Put in `deploy/join/config.env` (and `source` it before compose)

```bash
# Identity (already required for join; must match on every HA replica)
export KEY_NAME=...
export ACCOUNT_PUBKEY=...
export KEYRING_BACKEND=file
export KEYRING_PASSWORD=...

# Devshard HA Postgres — password required; DB/user default to devshardd if omitted
export DEVSHARD_POSTGRES_DB=devshardd
export DEVSHARD_POSTGRES_USER=devshardd
export DEVSHARD_POSTGRES_PASSWORD='...'

# v5 deployment selection (main Compose project and independent router slots)
export VERSIOND_IMAGE='<v5-versiond-image:tag-or-digest>'
export VERSIOND_ROUTER_IMAGE='<v5-haproxy-router-image:tag-or-digest>'
export PROXY_ROUTER_IMAGE='<v5-proxy-router-image:tag-or-digest>'
export PROXY_POLICY_IMAGE='<v5-proxy-image:tag-or-digest>'
export VERSIOND_VERSIONS="v5"
export VERSIOND_NON_HA_VERSIONS=""
# Example with the optional filter; without it use http://api:9100/versions.
export VERSIOND_ROUTING_CATALOG_URL=http://oracle-filter:9100/versions
export VERSIOND_ROUTER_FLEET_SLOTS="0 1 2"
export VERSIOND_ROUTER_MIN_READY=2
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5.override.yml
# Append every additional override used by this deployment, in the same order.
# Optional — same as compose default on one machine
export VERSIOND_POOL_HOST=versiond-pool
```

For all-HA routing, explicitly set `VERSIOND_NON_HA_VERSIONS=""` for supervisors and both routing tiers. Unset means the overlay defaults to `v1 v2 v3`.

### Already set by `docker-compose.versiond.yml`

You normally **do not edit these by hand** when using the local `devshard-postgres` service:

- `PGHOST=devshard-postgres`
- `PGDATABASE` / `PGUSER` / `PGPASSWORD` (from `DEVSHARD_POSTGRES_*`)
- `DEVSHARD_STORAGE_MODE=postgres` and `GONKA_HA=true`
- `PG_POOL_MAX_CONNS=${DEVSHARD_POSTGRES_POOL_MAX_CONNS:-4}` per child; budget connections for every version and overlapping generation, plus two dedicated health/fence connections per child
- `VERSIOND_DRAIN_ANNOUNCE=5s`, `VERSIOND_HOST_SHUTDOWN_BUDGET=25m`, `stop_grace_period: 30m`
- policy workers: `VERSIOND_SERVICE_NAME=proxy-policy-ingress`, `VERSIOND_PORT=18081`; public proxy: `VERSIOND_ROUTER_POOL_HOST=versiond-router-fleet`

### Put in compose overrides

| File                                               | Purpose                                                                                     |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `docker-compose.devshard-v5.override.yml`     | v5 supervisor images + optional `oracle-filter` for every peer and the router catalog; `VERSIOND_NON_HA_VERSIONS=` empty |
| `docker-compose.devshard-pg-external.override.yml` | Managed DB: set `PGHOST=...` under **every** `versiond`* service (see §2.2)                 |

---

## Step 4 - Verify it works

```bash
# 1) Containers (include oracle-filter when using the optional filter)
docker ps | grep -E 'oracle-filter|versiond|devshard-postgres'

# 2) Public proxy points at the router fleet
docker inspect proxy --format '{{range .Config.Env}}{{println .}}{{end}}' | grep VERSIOND_ROUTER_POOL_HOST
# expect: VERSIOND_ROUTER_POOL_HOST=versiond-router-fleet

# 3) Every router slot is admitted through the public proxy
./versiond-router-fleet.sh status
./versiond-router-fleet.sh verify-admission

# 4) Required children running and ready on every member
# Include every local replica in this list.
for replica in versiond versiond2; do
  docker exec "$replica" wget -qO- http://127.0.0.1:8080/healthz
  docker exec "$replica" wget -qO- "http://127.0.0.1:8080/readyz?version=v5"
done
./versiond-router-fleet.sh wait-version v5
# Repeat with version=v4 if retained; /healthz or router /livez alone is insufficient.

# The gateway probes this public path before starting inference.
curl -fsS "http://127.0.0.1:${API_PORT:-8000}/devshard/v5/healthz"
# The public proxy strips /devshard/ before forwarding to the router.

# 5) Postgres mode and storage proof on every HA child (after binary download)
# From versiond / devshardd logs: storage mode postgres / PG connected.
for replica in versiond versiond2; do
  docker exec "$replica" wget -qO- http://127.0.0.1:8080/internal/storage-identity
done
# Require a nonempty identity and generation targets from every member.
# A 503 here blocks the updater even when normal readiness passes.

# 6) Stop the local replica that served the probe and confirm route failover.
# Include every local replica in this list. Remote peers are checked on their hosts.
(
  set -euo pipefail
  replicas=(versiond versiond2)
  health_url="http://127.0.0.1:${API_PORT:-8000}/devshard/v5/healthz"
  route_peer() {
    curl -fsS --max-time 5 --retry 30 --retry-delay 1 --retry-max-time 60 \
      --retry-connrefused --retry-all-errors -D - -o /dev/null "$health_url" |
      awk 'tolower($1) == "x-upstream-addr:" {gsub("\r", "", $2); peer=$2} END {print peer}'
  }
  before=$(route_peer)
  test -n "$before"
  # Match the selected IP against each replica's networks, without joining IPs.
  serving_replica=$(docker inspect "${replicas[@]}" | jq -er --arg ip "${before%:*}" '
    [.[] | select(any(.NetworkSettings.Networks[]; .IPAddress == $ip)) |
      .Name | ltrimstr("/")] |
    if length == 1 then .[0] else error("selected peer is not one unique local replica") end')
  printf 'Probe served by %s (%s)\n' "$serving_replica" "$before"
  # Restore this replica even if the failover check fails.
  trap 'docker start "$serving_replica" >/dev/null' EXIT
  docker stop -t 1800 "$serving_replica"
  after=$(route_peer)
  test -n "$after" && test "$after" != "$before"
  printf 'Probe now served by %s\n' "$after"
  docker start "$serving_replica"
  trap - EXIT
  timeout 2100 bash -c '
    until docker exec "$1" wget -qO- "http://127.0.0.1:8080/readyz?version=v5"; do
      sleep 2
    done' _ "$serving_replica"
)
# X-Upstream-Addr identifies the final serving peer, not a retry list.
# With a remote selected peer, stop/restore that peer on its own host (§2.3).
```

Confirm `/healthz` or `desired_versions` logs list the selected approved HA protocols (v5 and retained v4, no v3), with PostgreSQL storage on every replica and sticky routing across the HA pool.

PostgreSQL outages make v5 children unready and fail closed. After a database fence loss, verify that the affected child exits and `versiond` replaces it before it receives traffic again. A child left running and unready fails recovery acceptance.

Also test a **real funded escrow**: record inference, serving member, committed nonce and cost; stop that member and continue the same session on a survivor. Verify state/accounting for retained v4 and new v5 separately. Health probes cannot establish session continuity. HAProxy does not replay sent non-idempotent requests or retry application 503s; crashes may interrupt streams. Follow the [test plan](../devshard/docs/devshard-host-ha-test-plan.md) for drain, crash and restart checks.

---

## Step 5 - `versiond-router` fleet operations

| Component                | HA status                                                                                                                                                                                   |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `versiond` / `devshardd` | **Yes today** (N replicas + shared Postgres)                                                                                                                                                |
| Postgres                 | **Your choice of Options A/B/C** (prefer managed/replicated)                                                                                                                                |
| `versiond-router`        | Independent router slots; default three with two kept ready during replacement. |
| `proxy` (public HAProxy) | One public listener; replacing it or losing its machine can interrupt connections                                                                                                                                      |
| nginx policy workers    | Replicated behind the public listener; verify accepted-request continuity during replacement |
| `decentralized-api`      | Still single-instance                                                                                                                                                                       |

Use the fleet script for router lifecycle; slots are separate Compose projects and are not stopped by the main project's `docker compose down`.

**Known shutdown issue:** with catalog refresh enabled, a router slot can remain running after its connections have drained. Stopping or replacing it can then wait for Docker's forced-stop timeout (`VERSIOND_ROUTER_DRAIN_TIMEOUT_SECONDS`, 1800 seconds by default). Allow for this delay for each replaced slot when scheduling maintenance.

```bash
./versiond-router-fleet.sh status
./versiond-router-fleet.sh stop 0
./versiond-router-fleet.sh start 0
./versiond-router-fleet.sh verify-admission

# After selecting a compatible router image in config.env:
./versiond-router-fleet.sh apply
```

`stop` and rolling replacement enforce the ready reserve; replacement also requires fresh admission checks. Preserve stopped previous containers and catalog volumes; rerun interrupted operations. Use §2.3’s `maintenance-rollout` for membership, resolver or legacy-routing changes. Finish interrupted maintenance cleanup with the same image/configuration before changing either.

For maintenance of the whole machine, drain the fleet before stopping the main stack:

```bash
./versiond-router-fleet.sh stop-all --maintenance
# Stop the main stack using its complete Compose file list.
# Only when decommissioning, after the main stack is down:
# ./versiond-router-fleet.sh down --maintenance
```

---

## What not to do

1. **Multiple versionds on SQLite** — split-brain / missing leases.
2. **Different keys** on HA replicas of the same participant.
3. **Launch v3 (or other pre-HA binaries) on HA peers with shared Postgres** — use the HA oracle override, or a dedicated non-HA supervisor for legacy versions.
4. **Two dapi processes** with the same warm/cold keys — duplicates PoC / chain txs.
5. **Assume local** `devshard-postgres` **on one VM is “full HA”** — replicate the DB or use managed PG.
6. **Omit the overrides for your chosen layout** when running `docker compose up` — preserve its image, storage and catalog settings. The optional filter is not required when you have configured direct catalog access as described in §2.1.

---

## Minimal recipe (fresh installation, one host, v5)

```bash
cd /path/to/gonka/deploy/join
source ./config.env

# Persist in config.env:
#   DEVSHARD_POSTGRES_PASSWORD=...
#   VERSIOND_IMAGE / VERSIOND_ROUTER_IMAGE / PROXY_ROUTER_IMAGE / PROXY_POLICY_IMAGE
#   VERSIOND_VERSIONS=v5 / VERSIOND_NON_HA_VERSIONS=""
#   VERSIOND_ROUTING_CATALOG_URL=http://oracle-filter:9100/versions
#   COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5.override.yml
#   (optional) DEVSHARD_POSTGRES_DB / USER / VERSIOND_POOL_HOST

# Create docker-compose.devshard-v5.override.yml as in §2.1
./versiond-router-fleet.sh prepare-networks

docker compose \
  -f docker-compose.yml \
  -f docker-compose.versiond.yml \
  -f docker-compose.devshard-v5.override.yml \
  up -d --wait --wait-timeout 2100

./versiond-router-fleet.sh apply
./versiond-router-fleet.sh verify-admission
./versiond-router-fleet.sh wait-version v5
docker ps | grep -E 'oracle-filter|versiond|postgres'
docker exec versiond wget -qO- http://127.0.0.1:8080/healthz
```

Then migrate the existing database to a **managed HA Postgres** when you are ready for real durability, and change `PGHOST` on every replica together during that maintenance (add §2.2 override to the same `docker compose` command). Pointing at a new empty database does not transfer existing sessions.
