# High-availability Devshard Host Setup

**Audience:** hosts that serve inference over **devshard** (`/devshard/...` → `versiond` → `devshardd`).  
**Status:** draft for host operators - edit before wider distribution.  
**Goal:** keep devshard inference available when one replica fails.

**Release:** `devshard-0.2.15-v5`. Core checked at `39240311fb` with gateway and storage fixes from [PR #1730](https://github.com/gonka-ai/gonka/pull/1730) through `bf4de2d21`; versiond fleet and updater checked in the integration from [PR #1611](https://github.com/gonka-ai/gonka/pull/1611) through `ab2bb5171` on 2026-09-09.

---

## Choose your procedure

- **Fresh installation, one host:** complete Step 1 and §2.1; add §2.2 for external PostgreSQL, then run Step 3 and Step 4.
- **Add a second machine:** complete §2.3 after the local installation works.
- **Upgrade an existing deployment:** prepare the files from §2.1, then follow §2.6 before starting or recreating services.
- **Operate an installed deployment:** use §2.4–2.5 and Step 5.

Use shared PostgreSQL for HA replicas; never share SQLite between them. Use replicated PostgreSQL and multiple machines to tolerate host failure. Keep one dapi per participant; replacing the single public proxy can interrupt connections.

<details>
<summary>Deployment layout (reference)</summary>

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

</details>

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

Leave explicit `PGSSL*` settings unset (`PGSSLMODE=disable` is allowed). If your provider requires explicit TLS settings, stop here and use a procedure supporting them; do not disable required TLS.

Connect directly or use **session pooling**. Transaction pooling and read-only replicas are unsupported; local Compose connects directly.

**Option B - Self-managed Postgres** — install PostgreSQL on a dedicated host or cluster.

Run this SQL on your self-managed PostgreSQL server, replacing the password:

```sql
CREATE USER devshardd WITH PASSWORD '...';
CREATE DATABASE devshardd OWNER devshardd;
```

This creates the application role and database; configure replication separately.

**Option C - Local Compose Postgres** — use `docker-compose.versiond.yml` on the join host. Losing that host also loses access to the database.

Install `devshard-postgres-entrypoint.sh` with the Compose files. For an existing database, complete §2.6 before recreating PostgreSQL.

### Configure the selected database

Save the credentials once in §2.1’s `config.env` block, then choose:

| Database | Required configuration |
| --- | --- |
| Local Compose | Use `docker-compose.versiond.yml`; it already sets `GONKA_HA=true`, `DEVSHARD_STORAGE_MODE=postgres` and the local `PGHOST` on both replicas. |
| External/managed | Add §2.2’s override to set the shared endpoint on every replica and disable the local database and its dependencies. |

Set connection variables in each replica’s **container environment**; setting `PGHOST` only in `config.env` does not override the local endpoint. Apply the same HA/storage settings to every extra replica. Do not use SQLite/hybrid storage for HA.

If local startup reports a missing database, restore it. Use `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true` once only for confirmed first-time HA enablement, then unset it. If `.pg-bound` exists, restore the database; do not use the flag.

---

## Step 2 - Configure the deployment

<details>
<summary>Files used by this procedure</summary>

| File                                                           | Role                                                                                                  |
| -------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `deploy/join/docker-compose.yml`                               | Base join (`versiond`, `proxy`, …)                                                                    |
| `deploy/join/docker-compose.versiond.yml`                      | HA overlay: Postgres + additional replica (`versiond2`) + shared networks for the router fleet               |
| `deploy/join/docker-compose.devshard-v5.override.yml`     | v5 images + the **optional** `oracle-filter` used in this example (you create this; see §2.1) |
| `deploy/join/docker-compose.devshard-pg-external.override.yml` | Optional: point **every** `versiond`* replica at managed Postgres (see §2.2)                          |
| `deploy/join/versiond-router-slot/` | Router slot Compose files; managed by `versiond-router-fleet.sh` in separate projects |
| `deploy/join/update-devshard.sh` | Updates the local deployment in order, including the versiond router fleet |

</details>

Use the example’s optional `oracle-filter` to select approved HA protocols. To use the API directly, first complete [Running without the filter](#running-without-the-filter).

Do not rely on `VERSIOND_NON_HA_VERSIONS` to exclude binaries from HA replicas; it does not prevent them from launching.

### 2.1 Common configuration (local replicas)

Prepare these files on the join host for both local and external PostgreSQL. Start fresh deployments with Step 3; update existing ones with §2.6.

#### 1. Credentials in `config.env`

Run these commands on the join host:

```bash
cd /path/to/gonka/deploy/join

# load existing join secrets (KEY_NAME, KEYRING_PASSWORD, …)
source ./config.env
```

This loads the existing join credentials.

Save the following in `config.env`, replacing placeholders with the existing participant identity, database credentials and published images from the tested candidate. Keep `VERSIOND_VERSIONS="v4"` during a v4 upgrade (§2.6).

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

Use the empty `VERSIOND_NON_HA_VERSIONS` list for all-HA routing; for retained SQLite protocols, use the legacy-owner procedure below. Preserve the complete ordered `COMPOSE_FILE` when adding overrides.

Keep fleet settings in `config.env`; router slots run in separate Compose projects. Keep the default 5-second announcement, 25-minute host budget and 30-minute Compose grace unless following §2.5’s timing rules. Budget PostgreSQL connections for every protocol and overlapping generation: each child uses `PG_POOL_MAX_CONNS` (default 4) plus two health/fence connections.

#### 2. Create `docker-compose.devshard-v5.override.yml`

Run this command in `deploy/join`:

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

This writes the v5 override with the optional protocol filter.

##### Running without the filter

For direct catalog access, make these changes to the example before starting it:

- Remove the `oracle-filter` service from `docker-compose.devshard-v5.override.yml` and its entries in every service's `depends_on`, including any external-PG or extra-replica overrides. Keep the other dependencies and image/storage settings.
- Set `VERSIOND_ORACLE_URL=http://api:9100/versions` on every local `versiond` service. On remote hosts, use the reachable private address of the same API.
- Set `export VERSIOND_ROUTING_CATALOG_URL=http://api:9100/versions` in `config.env` for both routing tiers. Ensure the endpoint is reachable from every router slot.
- Skip commands that start, recreate or inspect `oracle-filter` elsewhere in this guide. Query `api:9100/versions` instead when checking the catalog.

Before using direct access, verify that **every protocol in the catalog supports shared PostgreSQL**. For pre-HA protocols, use the legacy-owner layout below and exclude them from HA peers’ catalogs; `VERSIOND_VERSIONS` does not filter direct API results.

<a id="3-bring-up-the-main-stack-and-router-fleet"></a>
<a id="4-confirm"></a>

After preparing the files, use the common [startup](#step-3---start-the-configured-deployment) and [verification](#step-4---verify-it-works) below. For external PostgreSQL, finish §2.2 first; for an existing deployment, follow §2.6 instead of the fresh-start command.

#### Optional: still serving pre-v4 (v3) on the same host

For retained SQLite protocols, put these settings in `config.env` and select the dedicated legacy owner:

```bash
export VERSIOND_LEGACY_HOST=versiond
export VERSIOND_NON_HA_VERSIONS="v1 v2 v3"
```

These settings pin legacy protocols to their designated supervisor.

Keep the non-HA pin list. Give the legacy owner a legacy-only catalog and its SQLite data; keep HA peers on the filtered PostgreSQL-compatible catalog. Set `VERSIOND_LEGACY_HOST` and `VERSIOND_NON_HA_VERSIONS` in `config.env` for supervisors and both routing tiers. Change an existing fleet’s owner/list only through Step 5’s maintenance procedure.

### 2.2 Using external / managed Postgres with the same overlay

Prepare §2.1’s configuration and v5 override first, then complete the following steps before startup.

Keep the database credentials in `deploy/join/config.env` (`DEVSHARD_POSTGRES_*`).
**Create the external database override.** Save the following in `deploy/join/docker-compose.devshard-pg-external.override.yml`, replacing the database hostname:

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

This override connects the replicas to external PostgreSQL and disables their local-database dependency.

Use Compose with `!override` support. Repeat the environment and dependency overrides for every extra replica, and leave the `local-postgres` profile disabled.

**Save the complete file list.** Put this in `config.env`:

```bash
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5.override.yml:docker-compose.devshard-pg-external.override.yml
```

This saves the complete file list for subsequent operations.

Omit filter dependencies for direct catalog access. Continue with the common [startup](#step-3---start-the-configured-deployment) for a fresh installation, or §2.6 for an upgrade.

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

This writes the private-port override for machine A.

With external PostgreSQL, omit the `devshard-postgres` entry and use the actual
shared database host/port on B. Without the filter, omit `oracle-filter` and use
A’s API catalog on port `9100`, already published by the base Compose file.

Include this override after all existing files in A’s Compose commands and
persist the same complete list in `COMPOSE_FILE` in `config.env`. On a fresh
installation, include it in `COMPOSE_FILE` before Step 3. For an
existing installation, publishing these ports recreates the affected services:
schedule maintenance, stop all database writers before recreating local
PostgreSQL, apply the complete configuration, then wait for readiness and run
`./update-devshard.sh --check` before reopening traffic. Do not restart the
whole live stack merely to add a remote member.

Before starting B, run these checks **on B**, replacing A’s private IP:

```bash
curl -fsS http://<A-private-ip>:19100/versions  # direct catalog: port 9100
docker run --rm --network host --read-only \
  --entrypoint pg_isready "${DEVSHARD_POSTGRES_IMAGE:-postgres:16-alpine}" \
  -h <A-private-ip> -p 5432                   # use the shared DB endpoint
```

These commands check access from B to the catalog and PostgreSQL.

Require reachability from B to PostgreSQL, the catalog, chain and node-manager ports. `pg_isready` checks reachability; the later `--check-storage` verifies that B uses the same database.

#### On machine B (`versiond` only) — one compose file is enough

Configure B as follows:

1. **Same participant identity as A** — same `KEY_NAME`, `ACCOUNT_PUBKEY`, `KEYRING_PASSWORD`, and a copy of A’s `.inference/keyring-file/`. On A, run `docker cp versiond:/root/.inference/keyring-file .`, then transfer the copied `keyring-file/` to B’s `.inference/`. Mount B’s `.inference/` read-only as `/root/.inference`. Do **not** start a second `api` with those keys on B.
2. **Set connection variables in the service’s `environment:`.** Shell exports reach the container only through Compose interpolation.
3. **Own data dir** on B (do not share A’s `./devshards*/data`). Binary cache dir may be local.
4. **Publish** `versiond` **on B’s private IP at port 8080** (recommended) so A’s routers can reach it through the endpoint list or pool DNS configured below. Bind LAN-only, not `0.0.0.0`. Optionally firewall so only A can connect.

Save the following as `deploy/join/docker-compose.versiond-remote.yml` on B, replacing private IPs and database endpoints:

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

This configures B’s replica with A’s identity and services, plus its own data directory.

On B, run:

```bash
mkdir -p devshards-remote/{bin,data}
source ./config.env
docker compose -f docker-compose.versiond-remote.yml up -d --wait --wait-timeout 2100
curl -fsS "http://<B-private-ip>:8080/readyz?version=v5"   # not 127.0.0.1 if bound to LAN IP only
```

This starts the remote replica and checks v5 readiness; require HTTP 200.

##### Check B's database before admitting it to the pool

For `--check-storage`, install the PostgreSQL client (`psql`) on the host
running the check, then prepare a separate reference connection file:

```bash
cd /path/to/gonka/deploy/join
cp pool-postgres.env.template pool-postgres.env
chmod 600 pool-postgres.env
```

This creates a reference connection file readable only by its owner.

Fill `pool-postgres.env` with the existing pool's known working PostgreSQL
host, port, database and credentials. Obtain these from A or the database
administrator, independently of the replica being checked. With the intended
versiond image and configuration running, execute:

```bash
./update-devshard.sh --check-storage --reference-env ./pool-postgres.env
```

This checks the running replica against the independently configured database and writes database probes.

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

This lists the local and remote members available to every router slot.

Put `export VERSIOND_POOL_ENDPOINTS_FILE=./versiond-endpoints.json` in A's `config.env`. For a fresh fleet, `./versiond-router-fleet.sh apply` reads this list. Changing an existing list requires a maintenance window:

During the maintenance window, run:

```bash
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
  ./versiond-router-fleet.sh maintenance-rollout
./versiond-router-fleet.sh verify-admission
```

This applies the new membership and checks admission, with an interruption to new requests.

Preserve member IDs when replacing a host at the same endpoint. Apply membership changes with `maintenance-rollout`; editing the file alone is insufficient.

**For DNS membership:** set `VERSIOND_POOL_HOST` to private DNS resolving all members. Verify resolution of the pool and internal names from every router. Use maintenance when changing the pool name/resolver; ordinary DNS member changes need no rollout. Prefer explicit endpoints for differing ports.

#### Verify cross-machine HA

Adapt the failover check in [Step 4](#step-4---verify-it-works), item 6 of the command block, to a real session: find a sticky session whose `X-Upstream-Addr` is the remote replica, stop **that** machine’s `versiond`, wait for withdrawal and confirm the same session continues on a survivor with its committed state intact. `X-Upstream-Addr` shows the final selected peer, not an nginx retry history.

### 2.4 Adding more replicas

1. Add another `versiond` service (new container name + new data volume). The supplied `docker-compose.versiond3.yml` is an example; copy it with new names/data paths for additional replicas. Include it in every Compose command and the updater's `COMPOSE_FILE`. Its PostgreSQL mount also lets the lost-database guard see that replica's `.pg-bound` marker.
2. Give it the same image, catalog source, identity, HA/Postgres environment and drain settings; extend §2.1's override and §2.2's external-PG override for it. On the router back network add alias `versiond-pool`; across machines follow §2.3.
3. Start it and wait for every required `/readyz?version=...` to return 200. DNS discovery needs no router recreation. An explicit endpoint list needs the maintenance procedure from §2.3; then confirm real inference through the public route.

### 2.5 Operating versiond members

Keep enough ready survivors for the load; include §2.2’s external-PG override when applicable.

To stop and restart one local member, run the following with all active overrides in `COMPOSE_FILE`:

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

This restarts only `versiond2` with its existing data and checks readiness.

**Keep the default stop timings:** 5-second announcement, 25-minute host budget and 30-minute Compose grace. For custom values, use units, keep announcement at least as long as router detection (never `0s` behind HAProxy), and keep announcement plus child termination grace (default 10 minutes) below the host budget. Allow for interruption of requests exceeding that budget.

**Replace an image:** select a compatible image, stop that service and run `up -d --no-deps` for it only. Keep ready survivors yourself; Compose does not enforce a reserve. Verify every required version and inference before replacing another member. On failure, restore its previous image/configuration, recreate it and verify against the current database.

**Replace a remote member:** stop/drain it, then remove its explicit endpoint using §2.3’s maintenance procedure. Start its replacement and pass `--check-storage` on that host before restoring membership. For DNS pools, keep the replacement out of DNS until the check passes. The local updater does not inspect remote containers.

**Remove a member:** stop/drain it, remove its service or set replicas to zero (`VERSIOND2_REPLICAS=0` for `versiond2`), then remove its DNS/explicit membership. Use §2.3 maintenance for explicit lists; DNS changes need no router recreation. Verify remaining sessions and retain data/binary directories until recovery is confirmed.

**Add an approved HA protocol:**

1. Extend `VERSIOND_VERSIONS` in `config.env`, source it and recreate only `oracle-filter` with the complete ordered files.
2. Run `./versiond-router-fleet.sh wait-version <version>` and verify inference before using the route.
3. Preserve `proxy-router-state` and every slot’s `router-state` volume. Remove protocols only during maintenance after their sessions are no longer needed; do not expect catalog removal or an outage to withdraw accepted routes automatically.

### 2.6 Upgrade an existing HA deployment to v5

Before upgrading:

- Back up the database; record images, approved binary URLs/SHA256, Compose project/files, mounts and a working escrow.
- Keep the same PostgreSQL, identity and per-replica mounts. Preserve `.inference`, `devshards*/data`, binary caches and router catalog state.
- Prepare §2.1’s files in the **same Compose project**. Do not run unrestricted `up -d`.

Run these commands from the join checkout:

```bash
cd /path/to/gonka/deploy/join
source ./config.env
# Keep every active override in the ordered COMPOSE_FILE saved in config.env.
: "${COMPOSE_FILE:?set the complete Compose file list in config.env}"
dc=(docker compose)
```

This loads the deployment configuration and requires a complete Compose file list.

#### 1. Keep existing protocols available

1. For a v4-only deployment, keep `VERSIOND_VERSIONS="v4"` and a v4-only catalog during cutover, even if v5 is approved.
2. Before replacing supervisors, check that the approved v4 binary reports `postgres` from `--print-storage-mode` in the HA environment and `v4` from `--print-protocol-version`. Use matching gateway/host artifacts; stop if the storage probe is unsupported.
3. Keep v4 routes and artifacts for existing sessions. Use a new escrow for v5; do not rename binaries or escrows.

Add approved v5 only after retained v4 inference passes through the fleet. Leave binary caches and `<data>/<version>` directories in place without renaming them.

#### 2. Local PostgreSQL only — migrate the existing cluster during maintenance

Managed/external PostgreSQL with unchanged data skips this cluster-copy step. Before stopping anything, record the old local source and run the v5 space preflight.

Before stopping or recreating PostgreSQL, run:

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

This records the source database identity, prepares the target directory and checks available space.

Proceed only after preflight succeeds. Reserve the source cluster’s size plus 10% free space, and keep the existing PostgreSQL major version and Alpine/musl image family (`postgres:16-alpine` by default).

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

This stops local writers and recreates PostgreSQL for migration while preserving its source volume.

Before restarting writers:

1. Wait for PostgreSQL health, repeat the system-identifier query and verify the recorded identifier and committed data.
2. Preserve the source volume. Do not use `down`, `down -v`, `rm -v`, pruning or `--renew-anon-volumes` before migration.
3. If copying fails, resolve the logged error and retry with the source intact; do not start writers against an incomplete target.

Only if the old volume was already detached, run the following with its **recorded exact name**:

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

This checks the recorded source volume and starts PostgreSQL with the recovery overlay.

After recovery, keep writers stopped and recreate PostgreSQL once without the recovery overlay. Keep that image/configuration for the updater. Retain the source volume and backup; never use `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT` to recover a missing database.

#### 3. Run the updater with the complete deployment configuration

1. **Prepare for the cutover.** Work in `deploy/join` with the complete `COMPOSE_FILE` from the start of §2.6. Close public traffic and let accepted work finish; replacing the public proxy can interrupt connections. Keep the catalog limited to v4 until the retained v4 escrow passes the checks below. Leave `UPDATE_SKIP_POSTGRES_PROBE` and `UPDATE_ACCEPT_DATABASE_CHANGE` disabled.

   **External PostgreSQL with pre-v5 supervisors:** stop all database writers, including remote members, and independently verify the target database. Start the v5 supervisors with the complete Compose model and check every member with `--check-storage` as in §2.3 before running the updater. Do not use the legacy-container restart below for this case.

2. **Run the preparation commands.** Skip the filter command when unused and `docker start` when members are already running.

   ```bash
   ./versiond-router-fleet.sh prepare-networks
   # Only when using the optional filter:
   docker compose up -d --no-deps oracle-filter

   # Only after a local database copy: restart the retained old containers.
   # Include every stopped local member; start remote members on their hosts.
   docker start versiond versiond2
   ```

   This prepares the network, starts the optional filter and restarts retained containers after a local database copy.

   Keep already-running members running. For each retained pre-v5 member, verify v4 route health before proceeding:

   ```bash
   # Include every local member; repeat on remote hosts.
   (
     for replica in versiond versiond2; do
       docker exec "$replica" wget -qO- http://127.0.0.1:8080/v4/healthz || exit 1
     done
   )
   ```

   This checks that each retained member can serve the v4 route.

   Require a successful response from every member. For v5 supervisors, also require HTTP 200 from `/readyz?version=v4`; do not ignore a 503. After a local database copy, confirm the recorded database identity and retained session as described above. Keep a ready survivor for every served HA version.

3. **Run the update commands in order.** Continue only if each command succeeds.

   ```bash
   ./update-devshard.sh --check
   ./update-devshard.sh
   ./versiond-router-fleet.sh verify-admission
   ./versiond-router-fleet.sh wait-version v4
   ```

   This checks PostgreSQL, updates local services and verifies v4 routing.

   `--check` writes database probes without replacing services. Optionally run `--dry-run` first to inspect the plan. Update remote members separately using §2.5.

4. **Verify v4, then enable v5.**

   - Run inference with the retained v4 escrow through the public fleet route.
   - Once it works, set `VERSIOND_VERSIONS="v4 v5"` and refresh the optional filter (§2.5), or verify both approved, compatible versions in the direct catalog.
   - Run `./versiond-router-fleet.sh wait-version v5`, test a new v5 escrow and complete Step 4 for every served version before reopening traffic.

5. **If the update fails:**

   - Inspect logs and fix the cause. For a database-history error, verify the selected data directory.
   - Rerun normally with the same complete Compose configuration and persistent `UPDATE_STATE_DIR` (default under `~/.local/state/gonka/updater/`) to recover interrupted replacements.
   - Keep previous join files, backups and the unchanged v4 source volume. Do not bypass migration markers; rollback does not undo file edits or database writes.

#### Reference: state migration and rollback

<details>
<summary>Read before converting SQLite state or rolling back an artifact</summary>

Existing HA PostgreSQL data stays in the same database. v5 applies forward schema migrations under a database advisory lock. For first-time conversion of a single-owner deployment, explicit `postgres` mode also imports supported epoch-layout SQLite sessions and file payloads before serving; stop the old writer and migrate each source directory with one owner. Successful sources are quarantined as `*.migrated.<timestamp>`, and conflicting data aborts startup. Older monolithic layouts need separate verification.

A wire-compatible **same-protocol** artifact update is different from adding v5: PostgreSQL children can overlap while the candidate starts and, when supported, reports `recovery_complete=true` (default `VERSIOND_RECOVERY_TIMEOUT=30m`). Check `recovery_failed` and logs separately: completion does not mean every session recovered successfully. Failed preparation keeps the predecessor serving. SQLite/hybrid replacements drain and stop before starting; older candidates without the recovery field skip that recovery wait. Keep pool capacity for overlapping children. Do not repeatedly change the approved artifact while predecessors are still draining.

Restoring an old image does not undo database migrations or later committed writes. There is no automatic PostgreSQL-to-SQLite or schema downgrade; retain `.pg-bound`, use a binary known to read the current state, or perform a coordinated restore during maintenance. The preserved v4 cluster is a recovery source from the copy time, not a current replica after v5 writes. Validate restart/rollback on a copy of the actual state before relying on it.

</details>

---

<a id="step-3---environment-variables-checklist"></a>

## Step 3 - Start the configured deployment

**Fresh installation only.** Complete §2.1 and any applicable overrides (§2.2 external database, §2.3 private ports, §2.4 extra replicas). Save all files in the correct order in `COMPOSE_FILE` in `config.env`. For an existing deployment, use §2.6.

Run on the join host:

```bash
cd /path/to/gonka/deploy/join
source ./config.env
: "${COMPOSE_FILE:?set the complete Compose file list in config.env}"
./versiond-router-fleet.sh prepare-networks

docker compose up -d --wait --wait-timeout 2100

./versiond-router-fleet.sh apply
./versiond-router-fleet.sh verify-admission
./versiond-router-fleet.sh wait-version v5
```

This starts the selected local/external PostgreSQL layout and verifies v5 routing; complete Step 4 before accepting traffic.

---

## Step 4 - Verify it works

Run the following checks in order, using every local replica and every retained protocol. Continue only after each check succeeds; item 6 deliberately stops and restores the serving replica:

```bash
# 1) Containers (include oracle-filter when using the optional filter)
docker ps | grep -E 'oracle-filter|versiond|devshard-postgres'

# 2) Public proxy points at the router fleet
docker inspect proxy --format '{{range .Config.Env}}{{println .}}{{end}}' | grep VERSIOND_
# expect: VERSIOND_ROUTER_POOL_HOST=versiond-router-fleet
# all-HA: VERSIOND_NON_HA_VERSIONS= (empty)
# filtered layout: VERSIOND_ROUTING_CATALOG_URL=http://oracle-filter:9100/versions

# Check the catalog; without the filter, query api:9100/versions instead.
docker exec oracle-filter wget -qO- http://127.0.0.1:9100/versions
# Require selected approved protocols with their real binary URL/SHA256.

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

This verifies service readiness, database identity and route failover.

Confirm `/healthz` or `desired_versions` logs list the selected approved HA protocols (v5 and retained v4, no v3), with PostgreSQL storage on every replica and sticky routing across the HA pool.

PostgreSQL outages make v5 children unready and fail closed. After a database fence loss, verify that the affected child exits and `versiond` replaces it before it receives traffic again. A child left running and unready fails recovery acceptance.

Then test session continuity:

1. Record inference, serving member, committed nonce and cost for a funded escrow.
2. Stop that member and continue the **same session** on a survivor; verify state and accounting.
3. Repeat for retained v4 and new v5. Complete the [drain, crash and restart test plan](../devshard/docs/devshard-host-ha-test-plan.md).

Allow for interrupted streams after a crash; HAProxy does not replay sent non-idempotent requests or retry application 503s.

---

## Step 5 - `versiond-router` fleet operations

Use `versiond-router-fleet.sh` to stop routers; the main project’s `docker compose down` does not stop them.

Allow up to `VERSIOND_ROUTER_DRAIN_TIMEOUT_SECONDS` (default 1800 seconds) per replaced slot during maintenance; catalog refresh can delay shutdown until the forced-stop timeout.

Run the following to stop/restart slot 0; run `apply` after selecting a compatible router image in `config.env`:

```bash
./versiond-router-fleet.sh status
./versiond-router-fleet.sh stop 0
./versiond-router-fleet.sh start 0
./versiond-router-fleet.sh verify-admission

# After selecting a compatible router image in config.env:
./versiond-router-fleet.sh apply
```

This checks the fleet, restarts one slot and applies the selected router image.

`stop` and rolling replacement enforce the ready reserve; replacement also requires fresh admission checks. Preserve stopped previous containers and catalog volumes; rerun interrupted operations. Use §2.3’s `maintenance-rollout` for membership, resolver or legacy-routing changes. Finish interrupted maintenance cleanup with the same image/configuration before changing either.

For maintenance of the whole machine, drain the fleet before stopping the main stack:

```bash
./versiond-router-fleet.sh stop-all --maintenance
# Stop the main stack using its complete Compose file list.
# Only when decommissioning, after the main stack is down:
# ./versiond-router-fleet.sh down --maintenance
```

This drains the router fleet before the main stack is stopped.

---

## What not to do

1. **Multiple versionds on SQLite** — split-brain / missing leases.
2. **Different keys** on HA replicas of the same participant.
3. **Launch v3 (or other pre-HA binaries) on HA peers with shared Postgres** — use the HA oracle override, or a dedicated non-HA supervisor for legacy versions.
4. **Two dapi processes** with the same warm/cold keys — duplicates PoC / chain txs.
5. **Assume local** `devshard-postgres` **on one VM is “full HA”** — replicate the DB or use managed PG.
6. **Omit the overrides for your chosen layout** when running `docker compose up` — preserve its image, storage and catalog settings. The optional filter is not required when you have configured direct catalog access as described in §2.1.

---

<a id="minimal-recipe-fresh-installation-one-host-v5"></a>

For a fresh installation, follow the procedure at the top of this page. To move an installed local database to managed HA PostgreSQL, migrate its existing data and change `PGHOST` on every replica together during maintenance, using §2.2’s override. An empty target does not contain existing sessions.
