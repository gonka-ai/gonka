# High-availability Devshard Host Setup

**Audience:** hosts that serve inference over **devshard** (`/devshard/...` → `versiond` → `devshardd`).  
**Status:** draft for host operators - edit before wider distribution.  
**Goal:** run a **high-available (HA)** host stack so a single `versiond` / `devshardd` failure does not take the host offline.

**Release:** `devshard-0.2.15-v5`. Core checked at `39240311fb` with gateway and storage fixes from [PR #1730](https://github.com/gonka-ai/gonka/pull/1730) through `bf4de2d21`; versiond fleet and updater checked in the integration from [PR #1611](https://github.com/gonka-ai/gonka/pull/1611) through `ab2bb5171` on 2026-09-09.

---

## Why this matters

A single `versiond` process is a single point of failure (SPOF): if that machine or container dies, gateways cannot reach your host for that protocol version.

We now support an **HA host layout** with multiple `versiond` replicas. The examples below use `versiond` and `versiond2`; add more replicas as described in §2.4:

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

1. Join files from **`devshard-0.2.15-v5`** including the versiond fleet/updater integration: `update-devshard.sh`, `versiond-router-fleet.sh`, `deployment-lock.sh`, `updater-rollback.sh`, `updater-container-state.py` and `versiond-router-slot/`. Use v5-capable `versiond`, HAProxy `versiond-router` and `proxy-router`, nginx `proxy` policy images, and governance-approved `devshardd` artifacts for the protocols you serve. A tested set from one release candidate is recommended; compatibility depends on the actual component capabilities and protocol artifacts. Select the images explicitly in §2.1. The fleet/updater files are checked from the integration revision above.
2. Working `node` + `api` (dapi) + `proxy` on the host (standard join deployment).
3. Same participant identity on **every** HA `versiond` replica:
   - same `KEY_NAME` / keyring
   - same `ACCOUNT_PUBKEY`
4. Only put **Postgres-capable versions** into the HA pool. With `GONKA_HA=true`, v5 `versiond` requires each HA child to support `--print-storage-mode` and report `postgres`; the child must also match its approved protocol name. Keep older versions (`v1` / `v2` / `v3`) pinned to a **legacy** single host if you still serve them.
5. Confirm the actual `api:9100/versions` response contains the required protocol, downloadable binary URL and SHA256. It follows chain-approved versions; the branch name does not publish a v5 artifact or activate it on-chain. The examples below use protocol `v5`.
6. Docker Compose **2.24.4 or newer**, Bash, Python 3, `curl` **7.71 or newer**, `jq`, `flock`, `sha256sum` and `timeout` on the machine running the fleet/updater scripts.

---



## Step 1 - Install Postgres (preferably HA itself)

HA `versiond` removes dependence on one **app** server, but if Postgres is a single VM, **Postgres becomes your new SPOF**. Prefer a **managed / replicated** database.

### Choose a database

**Option A - Managed Postgres (recommended)** — AWS RDS Multi-AZ, GCP Cloud SQL HA, Azure Flexible Server HA, and similar.

Create a database and user, for example:


| Setting  | Example              |
| -------- | -------------------- |
| Database | `devshardd`          |
| User     | `devshardd`          |
| Password | strong secret        |


Note the primary (or HA) endpoint: host, port (often `5432`), database, user, password. Ensure **all** `versiond` instances can reach it (firewall / VPC / security groups).

This procedure, including `update-devshard.sh`, supports bundled PostgreSQL or an external writable endpoint without custom TLS configuration. The updater rejects explicit `PGSSL*` settings except `PGSSLMODE=disable`. A provider setup requiring explicit TLS configuration is outside this procedure; do not disable a provider’s required TLS to fit this example.

For v5, connect to PostgreSQL directly or through a connection pooler in **session pooling** mode. **Transaction pooling is not supported.** Use an endpoint that accepts writes, not a read-only replica. The local compose setup already connects directly.

**Option B - Self-managed Postgres** — install Postgres on a dedicated host or cluster, create the role/DB, and configure replication yourself for DB HA:

```sql
CREATE USER devshardd WITH PASSWORD '...';
CREATE DATABASE devshardd OWNER devshardd;
```

**Option C - Local compose Postgres** — `docker-compose.versiond.yml` can start `devshard-postgres` on the same join host. Fine for learning HA routing or a single rack; **not** true site HA (if the machine dies, the DB dies with it).

The v5 overlay stores the cluster in `${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}/data`, using `devshard-postgres-entrypoint.sh`. Install that script with the compose files. For an existing HA deployment, follow §2.6 before recreating PostgreSQL: the old cluster may be in an anonymous Docker volume.

### Where to put Postgres settings

`PGHOST` must be set on **every** `versiond`* replica’s **container environment** (via the HA compose overlay or an override). Putting `export PGHOST=...` only in `config.env` does not change those services unless compose reads that variable into their `environment:` block.


| What                                                                                                   | File on the host                                                                                         | Who sets it                                                                        |
| ------------------------------------------------------------------------------------------------------ | -------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------- |
| DB name / user / password                                                                              | `deploy/join/config.env` (you edit)                                                                      | You                                                                                |
| `PGHOST`, `PGDATABASE`, `PGUSER`, `PGPASSWORD`, `DEVSHARD_STORAGE_MODE` on every `versiond*` container | `deploy/join/docker-compose.versiond.yml` (already in the overlay) **or** a compose **override** you add | Overlay by default; override only for an external DB; repeat for any extra replica |




#### External or managed Postgres

Use this when the DB runs outside the join host (managed cloud DB or your own Postgres cluster - Options A and B).

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
> Keep `GONKA_HA=true`, `DEVSHARD_STORAGE_MODE=postgres` and `PGHOST` on HA `versiond` containers. v5 checks storage before starting HA children as well as when `versiond-router` sends `Devshard-Ha: true`. Hybrid/SQLite fallback is not supported for HA.



#### Local compose Postgres (`devshard-postgres`)

Use this when you run the DB from the HA overlay on the join host (Option C).

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

**Optional component: `oracle-filter`.** This guide adds a small Python HTTP service to the example Compose override. It reads `api:9100/versions` and returns only the protocol names selected in `VERSIOND_VERSIONS`. We included it so the example can run only the selected Postgres-capable protocols on HA replicas, while the chain catalog may also contain older protocols, and so supervisors and both routing tiers see the same selected list. It does not approve versions or change the chain catalog.

`oracle-filter` is **not required by versiond or the routers**. They can read `api:9100/versions` directly; see [Running without the filter](#running-without-the-filter). In that case, check the full catalog against your storage and legacy-version layout. `VERSIOND_NON_HA_VERSIONS` controls routing and HA preflight, but does not prevent versiond from launching a version. The examples below use the optional filter and an empty non-HA pin list.



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

Use the published image references for the tested candidate; the repository does not establish their availability. The fleet has three router slots by default and keeps two ready during replacement; this is independent of the number of `versiond` replicas. Put the fleet settings in `config.env`: router slots run in separate Compose projects and do not inherit the main project's override.

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

With direct access, `VERSIOND_VERSIONS` is a router bootstrap list, **not a filter** on the API catalog. Every version visible to the HA supervisors must fit the deployment's storage layout. For this all-HA example, use direct access when the full catalog contains only protocols compatible with the shared PostgreSQL layout. If you also serve pre-HA protocols, see the dedicated legacy-owner layout below; the HA peers still need a catalog that excludes those protocols. Without the optional filter, newly approved compatible versions are learned from the API without editing a filter allowlist.

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

What this starts:


| Container           | Purpose                                       |
| ------------------- | --------------------------------------------- |
| `devshard-postgres` | Shared DB (if not using external PG)          |
| `oracle-filter`         | Optional versions filter included in this example (selected HA protocols)            |
| `versiond`          | Replica A — data dir `./devshards/data`       |
| `versiond2`         | Replica B — data dir `./devshards2/data`      |
| Router slot containers | Independent HAProxy routers in front of the versiond pool |
| `proxy-policy` / `proxy-policy2` | nginx policy workers for public requests |
| `proxy` | Public HAProxy; private `/devshard/` routing to the router fleet |


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

Only if you must keep SQLite versions. Prefer a **dedicated non-HA** supervisor for those; keep them off the HA peers on Postgres. Stock overlay defaults:

```bash
export VERSIOND_LEGACY_HOST=versiond
export VERSIOND_NON_HA_VERSIONS="v1 v2 v3"
```

Do **not** clear the non-HA pin list in that case; use a split layout instead of launching v3 under `DEVSHARD_STORAGE_MODE=postgres` on HA replicas. Keep HA peers on the filtered oracle; give the dedicated legacy owner its own legacy-only oracle and SQLite data. Set `VERSIOND_LEGACY_HOST` to that owner and `VERSIOND_NON_HA_VERSIONS` to the legacy list in `config.env` for supervisors and both routing tiers. Changing an existing fleet's legacy owner/list requires the maintenance procedure in Step 5.



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

Use Docker Compose with `!override` support. This excludes the local database from `up -d` and removes the local-PG dependency on the replicas shown; repeat for every additional replica. Otherwise v5's lost-database guard can reject the unused local database after HA artifacts/state exist.

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

If you fully disable the local `devshard-postgres` service, also remove or override its `depends_on` entries on **every** `versiond`* service so compose does not wait on a container you never start.

### 2.3 Multiple machines (recommended, true host HA)

Keep the local HA replicas on machine A and add a `versiond` replica on machine B. Machine A runs the `versiond-router` fleet. Use a **private network** between machines and bind the additional listeners to private IPs as shown below.

Example extending the two local replicas from §2.1:


| Role      | Runs                                                            |
| --------- | --------------------------------------------------------------- |
| Machine A | `versiond` + `versiond2`, usual node/api/proxy and the `versiond-router` fleet |
| Machine B | `versiond` only — **no** second dapi with the same keys         |
| Shared    | Postgres reachable from every `versiond` (managed HA preferred) |


> **Important:** `decentralized-api` (dapi) is still **single-instance** today. HA here is for **devshard traffic** (`versiond` / `devshardd`), not for running two dapis with one key.

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
pg_isready -h <A-private-ip> -p 5432           # use the shared DB endpoint
```

Install the PostgreSQL client for `pg_isready` and the storage check below.
The readiness probe checks reachability; the later `--check-storage` establishes
that B uses the same database. Ensure B can also reach A’s chain and node-manager
ports listed above.

#### On machine B (`versiond` only) — one compose file is enough

B does not run `api` / `node`. Its `versiond` uses the shared PostgreSQL endpoint and A’s catalog, node-manager and chain endpoints over the private network.

1. **Same participant identity as A** — same `KEY_NAME`, `ACCOUNT_PUBKEY`, `KEYRING_PASSWORD`, and a copy of A’s `.inference/keyring-file/`. On A, run `docker cp versiond:/root/.inference/keyring-file .`, then transfer the copied `keyring-file/` to B’s `.inference/`. Mount B’s `.inference/` read-only as `/root/.inference`. Do **not** start a second `api` with those keys on B.
2. **Put all connection settings in the** `versiond` **service** `environment:` (compose file). Shell `export`s in `config.env` only help if compose interpolates them into that block — the container must see the vars.
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

Install `jq`
and the PostgreSQL client (`psql`), then prepare a separate reference connection
file:

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

Continue only when it prints `Storage check passed` and exits with code 0.
The command checks the running `versiond` container; use `--container NAME`
for another name, repeating it to check several local containers. It requires
no network-node Compose services. Each HA devshardd writes a control value
through its own database connection, and the checker verifies that value in
the reference database. Run these checks one host at a time, with no updater
running elsewhere. Resolve any failure before admitting the replica.

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

Editing the file alone does not change running membership. Ordinary `apply` refuses a changed membership contract; `maintenance-rollout` drains the old fleet before admitting the new list, with an interruption to new requests. Keep the stable member IDs when replacing a host at the same endpoint.

A private DNS pool is also possible: set `VERSIOND_POOL_HOST` to a name resolving all reachable members. Docker's local alias does not discover another machine. Every router must resolve that name and the deployment's internal names; subsequent DNS membership changes are discovered automatically. Changing the pool name or resolver requires the same maintenance procedure. Prefer the explicit list when remote hosts use different ports.

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

A normal stop makes v5 `versiond` unready, announces for 5 seconds while still serving, then closes admission and drains accepted requests/children. Keep the overlay's 30-minute Compose stop grace above the 25-minute host budget. Do not shorten the announce window below the router's detection window; do not set it to `0s` behind HAProxy. Budget exhaustion can force termination, so requests exceeding the budget can be interrupted. Custom duration values need units (`30s`, `5m`); the announce window plus effective child termination grace (10 minutes by default) must remain below the host budget.

For a compatible image replacement, select the new image for that service, stop it, then run `up -d --no-deps` for that service only. Wait for **every required version** to become ready and verify inference before replacing another member. Docker Compose does not enforce a survivor reserve. If the candidate fails, restore that service's prior image/configuration and recreate it; verify it against the current database before proceeding.

For a remote member in an explicit endpoint list, stop it and let it drain,
then remove its entry using §2.3's membership maintenance procedure before
starting the replacement. Repeat `--check-storage` on that remote host before
restoring its entry and admitting it to traffic. The network node's normal
updater does not inspect remote containers. DNS pools discover a ready
replacement automatically; keep it out of pool DNS until the check passes.

For permanent removal, stop/drain the member first, remove its service or set its desired replica count to zero (`VERSIOND2_REPLICAS=0` for `versiond2`), and remove its DNS/explicit membership as applicable. The updater respects zero replica counts. Apply explicit-list changes through §2.3's maintenance procedure and confirm the remaining pool serves its sessions. Keep its data and binary directories until recovery is verified. Router recreation is unnecessary for DNS membership changes.

To add another approved HA protocol in this filtered layout, extend `VERSIOND_VERSIONS` in `config.env`, source it, and recreate only `oracle-filter` with the same ordered files. Supervisors and both routing tiers poll the catalog. Run `./versiond-router-fleet.sh wait-version <version>` and verify real inference before using the new route; the new version can be admitted without replacing routers. Preserve `proxy-router-state` and every slot's `router-state` volume. The catalog retains accepted routes when its source fails and does not automatically remove them by default; removing a protocol requires supervised maintenance after its sessions are no longer needed.

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

For a v4-only deployment, keep `VERSIOND_VERSIONS="v4"` during the fleet cutover, even if v5 is already approved. Fleet admission runs before supervisor replacement and must be able to use the versions already serving. Add v5 after the cutover; the catalog and bootstrap routes must retain v4 for its active sessions. The optional filter provides the v4-only selection used by this upgrade example; direct catalog access requires a catalog compatible with the same cutover constraints. Before replacing supervisors, check the actual approved v4 binary's `--print-storage-mode` in the HA environment (`postgres`) and `--print-protocol-version` (`v4`). An old artifact without the HA storage probe cannot join the new HA supervisor pool. Use matching gateway/host protocol artifacts. Do not rename a v4 binary/escrow to v5 or assume v4 session state is migrated into the v5 protocol. Keep serving retained v4 sessions under their compatible v4 artifact; use a new escrow for v5.

Add v5 only after it is approved and the fleet cutover has passed the retained v4 inference check. New supervisors automatically promote verified old `<bin>/<version>/devshardd` installs into `<bin>/<version>/<archive-sha256>/devshardd`. They verify the old metadata against the current oracle and otherwise download again. **Do not delete or manually rename** the cache or `<data>/<version>` directories.

#### 2. Local PostgreSQL only — migrate the existing cluster during maintenance

Managed/external PostgreSQL with unchanged data skips this cluster-copy step. Before stopping anything, record the old local source and run the v5 space preflight.

The commands below let Docker create the target directory, including under a root-owned `devshards/`, using a temporary container that runs only `true`. Proceed to migration only after preflight succeeds.

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

After a local database copy, restart the retained old member containers with `docker start` and verify their v4 readiness before running the updater (use `/v4/healthz` when an old supervisor returns 404 from `/readyz`). Keep their catalog limited to v4 for this cutover example (using the optional filter if needed), and public traffic closed during the cutover. Fleet admission needs these serving children before it replaces supervisors. For already-migrated PostgreSQL whose members provide v5 storage proofs, keep ready survivors running. Pre-v5 supervisors lack host evacuation: finish their accepted work in the maintenance window before replacement.

```bash
./versiond-router-fleet.sh prepare-networks
# Only when using the optional filter:
docker compose up -d --no-deps oracle-filter

# Include every retained local member stopped for the database copy; start remote ones on their machines.
# Skip this line if they are already running.
docker start versiond versiond2

# Verify the retained v4 children are serving before continuing.
./update-devshard.sh --check
./update-devshard.sh --dry-run
./update-devshard.sh
./versiond-router-fleet.sh verify-admission
./versiond-router-fleet.sh wait-version v4
```

The updater reads `config.env` and the complete Compose model, validates shared writable PostgreSQL and its connection budget, updates local PostgreSQL if used, prepares the router fleet and attaches existing local replicas to its back network. It starts the public proxy, brings up policy workers one at a time, verifies router admission, removes the old singleton router, then replaces local `versiond` replicas one at a time with `VERSIOND_LEGACY_HOST` last (default `versiond`). It does not start custom services such as `oracle-filter`, so start the filter explicitly as above if you use it. Remote replicas are updated on their own machines using §2.5.

The host updater supports bundled PostgreSQL or an external writable endpoint without custom TLS configuration. PostgreSQL TLS setup is outside this procedure: the updater rejects explicit `PGSSL*` settings except `PGSSLMODE=disable`. Leave them unset for the stock deployment.

`--check` does not replace services, but takes the deployment lock and writes PostgreSQL challenges; `--dry-run` also runs preflight. Keep `UPDATE_SKIP_POSTGRES_PROBE` and `UPDATE_ACCEPT_DATABASE_CHANGE` disabled. Legacy members returning 404 are accepted only for the verified bundled PostgreSQL migration; confirm their database and recorded session separately. With an external database, upgrade legacy members during maintenance with all writers stopped and the target database independently verified. Start the v5 supervisors using the complete Compose model, check them with `--check-storage` (§2.3), then run the host updater. A legacy 404 cannot establish continuity with an external target. The new routers accept a pre-v5 supervisor's `/readyz` **404** only together with successful route health; a v5 **503** is never a legacy fallback.

Schedule the first public nginx-to-HAProxy replacement and local PostgreSQL copy as maintenance. Once the retained v4 escrow works through the fleet, extend the optional filter to `VERSIOND_VERSIONS="v4 v5"` as in §2.5, or confirm that the direct catalog exposes both compatible versions, wait for v5 and verify a new v5 escrow. Check per-version readiness on every member and admission through both routing tiers; a healthy public proxy alone is insufficient.

If a versiond replacement or public admission fails, the updater restores the saved container configuration and stops. It restores proxy and policy workers together, including the old nginx on the first cutover. An interrupted replacement is recovered on the next normal run; `--check` reports pending recovery. Keep the same persistent `UPDATE_STATE_DIR` if you override its default under `~/.local/state/gonka/updater/`. Inspect logs, fix the cause and rerun with the same complete file list. Unchanged services and previous-release records are retained. Saved configurations preserve mount sources; keep previous join files because rollback does not undo host-file edits or database writes. The updater does not guarantee uninterrupted policy-worker replacement; validate accepted SSE with the test plan before relying on that behavior.

The updater refuses an unexpected change to the bundled PostgreSQL data directory. Before replacing PostgreSQL, it saves a control value in the database and a recovery record with the chosen image, data directory and retained source volume. After startup it checks that value before proceeding. If interrupted, rerun with the same persistent updater state directory: recovery uses the saved database target before starting a new preflight. A failed history check stops PostgreSQL and retains the record; investigate the selected data directory before retrying.

Keep the saved v4 volume unchanged after copying. The entrypoint verifies it against the migration record; restarting that old copy as a writer creates a separate history and blocks subsequent starts while the volume is attached. Do not replace the migration marker to bypass this check.

Each HA versiond replacement requires a surviving member for every currently served HA version. The candidate must then pass per-version readiness and admission in every router before the updater proceeds to the next member. Pinned versions are checked on their designated owner before its replacement is confirmed. General container health alone does not complete the step.

Replacing the single public proxy can interrupt existing connections, including on later v5 image or configuration updates. Schedule those replacements during maintenance and let accepted work finish first.

The upgrade is complete after the readiness, router admission and inference checks above pass for every version you intend to serve.

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

For **all-HA routing**, keep `VERSIOND_NON_HA_VERSIONS=""` in `config.env` so supervisors, the public proxy and independent router slots all read the same empty list. The overlay defaults to `v1 v2 v3` when this variable is unset.

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

Healthy signs:

- `desired_versions` logs / `/healthz` show the selected approved HA versions (v5, plus v4 if retained; no v3 under HA), and `/readyz?version=...` returns 200 on each member.
- Every HA `versiond*` replica runs its approved `devshardd` artifacts with Postgres storage.
- Router sticky-routes across the HA pool (`VERSIOND_NON_HA_VERSIONS` empty).
- Stopping the replica that served the probe moves subsequent probes to a ready peer. Continuing an actual session is checked separately below.

PostgreSQL outages make v5 children unready and fail closed. After a database fence loss, verify that the affected child exits and `versiond` replaces it before it receives traffic again. A child left running and unready fails recovery acceptance.

The health URL is only a routing smoke check. Also use a real funded escrow: record an inference, serving member, committed nonce and cost, stop that member, and continue the **same session** on a survivor. Verify committed state and accounting; test retained v4 and new v5 escrows separately. HAProxy does not replay non-idempotent requests after sending them and does not retry application 503 responses. A crash can interrupt an in-flight stream; use the [test plan](../devshard/docs/devshard-host-ha-test-plan.md) for graceful-drain, crash and restart checks.

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

`stop` and rolling replacement enforce the configured ready reserve. The fleet drains a slot, waits for fresh checks on its replacement and retains the previous generation until admission succeeds. Leave the stopped previous containers and catalog volumes intact during recovery; rerun the same operation after an interruption. Pool membership, resolver or legacy-routing changes require `maintenance-rollout` as in §2.3. Complete any interrupted maintenance cleanup with the same image and configuration before changing them again.

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
