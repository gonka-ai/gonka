# High-availability Devshard Host Setup

**Audience:** hosts that serve inference over **devshard** (`/devshard/...` → `versiond` → `devshardd`).\
**Status:** draft for the `devshard-0.2.15-v5` release candidate (see prerequisites).\
**Goal:** run a **high-availability (HA)** host stack so a single `versiond` / `devshardd` failure does not take the host offline.

---

## Why this matters

A single `versiond` process is a single point of failure (SPOF): if that machine or container dies, gateways cannot reach your host for that protocol version.

We now support an **HA host layout**:

```text
Public proxy (HAProxy + nginx policy workers, /devshard/...)
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

1. Join stack, `versiond`, router images and approved `devshardd` artifacts from the **same v5 release candidate**. The old `0.2.15` image tag alone does not provide this layout.
2. Working `node` + `api` (dapi) + `proxy` on the host (standard join deployment), Docker Compose **2.24.4+** and `jq`.
3. Same participant identity on **every** HA `versiond` replica:
  - same `KEY_NAME` / keyring
  - same `ACCOUNT_PUBKEY`
4. Only put **Postgres-capable artifacts** into the HA pool. A version name such as `v4` or `v5` is not proof of capability; the child must pass the storage preflight. Keep older SQLite versions pinned to their **legacy** single owner if you still serve them.

**Release scope checked on 2026-09-08:** HAProxy routing, storage guards, live PostgreSQL readiness, persistent PGDATA, host evacuation, dynamic version routes and shared-storage proof are merged into the release branch ([router](https://github.com/gonka-ai/gonka/pull/1599), [storage proof](https://github.com/gonka-ai/gonka/pull/1607), [PGDATA](https://github.com/gonka-ai/gonka/pull/1603), [evacuation](https://github.com/gonka-ai/gonka/pull/1604), [catalog](https://github.com/gonka-ai/gonka/pull/1606)). The public routing tier, router fleet and updater used below are still open PRs [#1609](https://github.com/gonka-ai/gonka/pull/1609), [#1610](https://github.com/gonka-ai/gonka/pull/1610), [#1611](https://github.com/gonka-ai/gonka/pull/1611). Use these commands with an RC that includes them; check the release's image digests before starting. New edge-api routing in [#1657](https://github.com/gonka-ai/gonka/pull/1657) is outside this release's scope.

For a **fresh HA installation**, follow Steps 1–5. For a host already serving sessions, start with [Updating an existing host](#updating-an-existing-host); do not apply the fresh-install recipe over its running stack.

---



## Installation

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
| SSL      | follow your provider |


Note the primary (or HA) endpoint: host, port (often `5432`), database, user, password. Ensure **all** `versiond` instances can reach it (firewall / VPC / security groups).

**Option B - Self-managed Postgres** — install Postgres on a dedicated host or cluster, create the role/DB, and configure replication yourself for DB HA:

```sql
CREATE USER devshardd WITH PASSWORD '...';
CREATE DATABASE devshardd OWNER devshardd;
```

**Option C - Local compose Postgres** — `docker-compose.versiond.yml` can start `devshard-postgres` on the same join host. Fine for learning HA routing or a single rack; **not** true site HA (if the machine dies, the DB dies with it).

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
2. Add a compose override (for example `deploy/join/docker-compose.devshard-pg-external.override.yml`) that sets `PGHOST` (and related vars) under **every** `versiond`* service in the HA pool — see Step 2.2.
  Needed because the stock overlay hardcodes `PGHOST=devshard-postgres`.
3. Include the external-Postgres overlay and your connection override **after** the HA files, as shown in Step 2.2.

> Do not run two `versiond` instances on SQLite.\
> Keep **both** `GONKA_HA=true` and `DEVSHARD_STORAGE_MODE=postgres` on HA `versiond` containers. The router stamps `Devshard-Ha: true` even when only one survivor is ready; unsafe children fail closed. `PGHOST` with automatic storage selection is not enough.



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
3. `source ./config.env`, then start with the complete Compose file list (see Step 2.1).

The durable cluster is now under `${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}/data`, mounted as `/var/lib/postgresql/gonka/data`. Back up this directory or the database itself. The old `/var/lib/postgresql/data` anonymous volume is a migration source, not the new live PGDATA.

A truly empty installation initializes normally. If downloaded binaries make a first HA conversion ambiguous, the entrypoint requires an explicit `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true` for that one initialization. Use it only after confirming that no PostgreSQL sessions or old DB volume need recovery; it cannot override `.pg-bound` evidence. Do not leave it enabled in `config.env`.

**Connection budget:** `PG_POOL_MAX_CONNS` applies to each child, not the whole host (overlay default: `4`). Allow for all replicas, every served HA version, overlapping old/new generations, health probes and supervisor connections. With the one-predecessor limit from [#1702](https://github.com/gonka-ai/gonka/pull/1702) included, reserve at least `R * (2 * N * (P + 2) + 5)` non-reserved connections for this stack (`R` replicas, `N` HA versions per replica, pool limit `P`). Add headroom for other clients; the updater does not size the database for you.

---



## Step 2 - Run multiple `versiond` instances + `versiond-router`

Example files in your setup. Use the files supplied with the RC:

| File | Role |
| --- | --- |
| `deploy/join/docker-compose.yml` | Base join + public HAProxy and two nginx policy workers |
| `deploy/join/docker-compose.versiond.yml` | HA overlay: Postgres + second replica (`versiond2`) + fleet networks |
| `deploy/join/versiond-router-fleet.sh` | Starts and updates router slots in separate Compose projects |
| `deploy/join/docker-compose.devshard-v5-only.override.yml` | **Recommended for this fresh example:** v5-only oracle filter (you create it below) |
| `deploy/join/docker-compose.versiond-external-postgres.yml` | Optional: disables the bundled DB and makes its dependencies optional |
| `deploy/join/docker-compose.devshard-pg-external.override.yml` | Optional: your managed-Postgres connection settings on **every** replica |

> **Why keep an oracle filter?** `/versions` follows governance and may still list pre-HA artifacts. `VERSIOND_NON_HA_VERSIONS` changes routing and child HA enforcement; it does **not** prevent versiond from launching those children. The example filters to **v5** and clears the non-HA pin list on supervisors and both routing tiers. Use the actual approved RC protocol name; extend the filter only after checking the additional artifacts. This deliberately limits discovery to the allowed names.

### 2.1 Same machine, two instances (fresh v5-only HA)

On the join host:

**1. Credentials in** `config.env`

```bash
cd /path/to/gonka/deploy/join

# load existing join secrets (KEY_NAME, KEYRING_PASSWORD, …)
source ./config.env
```

Add to `config.env` (password is required; DB/user and fleet settings have compose defaults):

```bash
export DEVSHARD_POSTGRES_DB=devshardd
export DEVSHARD_POSTGRES_USER=devshardd
export DEVSHARD_POSTGRES_PASSWORD='<strong-password>'

export GONKA_HA=true
export VERSIOND_VERSIONS="v5"
export VERSIOND_NON_HA_VERSIONS=""
export VERSIOND_ROUTING_CATALOG_URL=http://oracle-v5:9100/versions
export VERSIOND_POOL_HOST=versiond-pool
# Remove an old VERSIOND_HOSTS export; a static list overrides DNS discovery.
unset VERSIOND_HOSTS

# Use the image references / digests supplied with the RC:
# export VERSIOND_IMAGE=...
# export VERSIOND_ROUTER_IMAGE=...
# export PROXY_ROUTER_IMAGE=...
# export PROXY_POLICY_IMAGE=...
```

**2. Create** `docker-compose.devshard-v5-only.override.yml`

```bash
cat > docker-compose.devshard-v5-only.override.yml <<'EOF'
services:
  # Filtered /versions: only the approved HA version in this fresh example
  oracle-v5:
    container_name: oracle-v5
    image: python:3.12-alpine
    environment:
      - ORACLE_UPSTREAM=http://api:9100/versions
      - ORACLE_ALLOW=${VERSIOND_VERSIONS:?set VERSIOND_VERSIONS in config.env}
      - LISTEN_PORT=9100
    command:
      - python
      - -c
      - |
        import json, os, urllib.request
        from http.server import BaseHTTPRequestHandler, HTTPServer
        UP = os.environ["ORACLE_UPSTREAM"]
        ALLOW = set(x.strip() for x in os.environ["ORACLE_ALLOW"].replace(",", " ").split() if x.strip())
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
    networks:
      default: {}
      versiond-router-back: {}
    depends_on:
      api:
        condition: service_started
    restart: always

  versiond:
    environment:
      - VERSIOND_ORACLE_URL=http://oracle-v5:9100/versions
    depends_on:
      oracle-v5:
        condition: service_started
      devshard-postgres:
        condition: service_healthy

  versiond2:
    environment:
      - VERSIOND_ORACLE_URL=http://oracle-v5:9100/versions
    depends_on:
      oracle-v5:
        condition: service_started
      devshard-postgres:
        condition: service_healthy

  # If you add versiond3 (or more), give each the same oracle-v5 env + depends_on.

EOF
```

**3. Bring up HA with all three compose files**

```bash
source ./config.env

# Keep this same ordered list for every later Compose command.
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5-only.override.yml

./versiond-router-fleet.sh prepare-networks
docker compose up -d --wait
./versiond-router-fleet.sh apply
./versiond-router-fleet.sh verify-admission
```

Persist `COMPOSE_FILE` in `config.env` when this is your chosen layout. The fleet reads `config.env` too; exports in a Compose override alone do not configure its separate router projects.

What this starts:

| Container | Purpose |
| --- | --- |
| `devshard-postgres` | Shared DB (if not using external PG) |
| `oracle-v5` | Filtered versions oracle, reachable by supervisors and both routing tiers |
| `versiond` | Replica A — data dir `./devshards/data` |
| `versiond2` | Replica B — data dir `./devshards2/data` |
| Router slots | Three independent HAProxy routers by default (`VERSIOND_ROUTER_FLEET_SLOTS="0 1 2"`) |
| `proxy-policy`, `proxy-policy2` | Private nginx policy workers (TLS, CORS, route policy) |
| `proxy` | Public HAProxy; connects the policy workers and router fleet |

**4. Confirm**

```bash
docker compose ps
./versiond-router-fleet.sh status
./versiond-router-fleet.sh wait-version v5

docker exec versiond wget -qO- http://127.0.0.1:8080/healthz
docker exec versiond wget -qO- 'http://127.0.0.1:8080/readyz?version=v5'
docker exec versiond2 wget -qO- 'http://127.0.0.1:8080/readyz?version=v5'
# Only the allowed child versions; both per-version readiness checks return 200.
```

#### Optional: still serving pre-v4 (v3) on the same host

Only if you must keep SQLite versions. Keep their existing data on a **dedicated non-HA** supervisor, and use separate filtered oracles for that owner and the HA peers. The routing settings are:

```bash
export VERSIOND_LEGACY_HOST='<single-legacy-owner>'
export VERSIOND_NON_HA_VERSIONS="v1 v2 v3"
```

The public and inner catalogs must still include the versions you serve. Do **not** simply remove the v5-only filter and let every Postgres peer launch old binaries. Clearing the pin list does not migrate SQLite sessions. Keep the legacy owner available until its sessions have finished.

### 2.2 Using external / managed Postgres with the same overlay

`config.env` alone is **not enough**: stock `docker-compose.versiond.yml` still sets `PGHOST=devshard-postgres`. You must add a **compose override file on the host**.

1. Keep credentials in `deploy/join/config.env` (`DEVSHARD_POSTGRES_*` as above).
2. Create `deploy/join/docker-compose.devshard-pg-external.override.yml` (name is yours; keep it in `deploy/join/`):

```yaml
services:
  # Repeat this environment block for every HA replica (versiond, versiond2, versiond3, …).
  versiond:
    environment:
      - PGHOST=your-managed-pg.example.com
      - PGPORT=5432
      - PGDATABASE=${DEVSHARD_POSTGRES_DB:-devshardd}
      - PGUSER=${DEVSHARD_POSTGRES_USER:-devshardd}
      - PGPASSWORD=${DEVSHARD_POSTGRES_PASSWORD:?DEVSHARD_POSTGRES_PASSWORD is required}
      - PGSSLMODE=verify-full
      # Mount the provider CA and set PGSSLROOTCERT if required.
      - DEVSHARD_STORAGE_MODE=postgres

  versiond2:
    environment:
      - PGHOST=your-managed-pg.example.com
      - PGPORT=5432
      - PGDATABASE=${DEVSHARD_POSTGRES_DB:-devshardd}
      - PGUSER=${DEVSHARD_POSTGRES_USER:-devshardd}
      - PGPASSWORD=${DEVSHARD_POSTGRES_PASSWORD:?DEVSHARD_POSTGRES_PASSWORD is required}
      - PGSSLMODE=verify-full
      # Mount the provider CA and set PGSSLROOTCERT if required.
      - DEVSHARD_STORAGE_MODE=postgres
```

3. Start with both external-Postgres files after the HA files:

```bash
cd /path/to/gonka/deploy/join
source ./config.env
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5-only.override.yml:docker-compose.versiond-external-postgres.yml:docker-compose.devshard-pg-external.override.yml

./versiond-router-fleet.sh prepare-networks
docker compose up -d --wait
./versiond-router-fleet.sh apply
./versiond-router-fleet.sh verify-admission
```

Persist the complete `COMPOSE_FILE`. Repeat the optional local-DB dependency from `docker-compose.versiond-external-postgres.yml` for any additional replicas; keep other dependencies intact. Do not enable its `gonka-local-postgres-disabled` profile.

Use the same writable database, credentials and TLS/session settings on **every** replica. Avoid `DATABASE_URL`, `PGSERVICE`, `PGSERVICEFILE` and `PGOPTIONS`: the updater rejects these on HA peers because they can redirect storage independently of the `PG*` tuple. Mount certificate files into every container that uses them. The endpoint must preserve session-scoped advisory locks (transaction pooling is unsupported) and expose at most one writable primary; application fencing does not provide database-cluster failover.

### 2.3 Multiple machines (recommended for application-host HA)

Conceptually the same layout, but each machine runs one `versiond`, and the network node runs the router fleet and public proxy. Prefer a **private network** between machines; bind new listeners to private IPs only if you cannot open extra public ports.

Minimum for two machines:


| Role      | Runs                                                            |
| --------- | --------------------------------------------------------------- |
| Machine A | `versiond` (+ usual node/api/proxy) + router fleet         |
| Machine B | `versiond` only — **no** second dapi with the same keys         |
| Shared    | Postgres reachable from every `versiond` (managed HA preferred) |


> **Important:** `decentralized-api` (dapi) is still **single-instance** today. HA here is for **devshard traffic** (`versiond` / `devshardd`), not for running two dapis with one key.

**On machine A (dapi / Postgres / router side) — publish for B on the private network:**

1. Postgres (`5432`) and node-manager gRPC (`9400`) — required.
2. Chain RPC/gRPC (`26657`, `9090`) — required for remote `devshardd` (same as local `NODE_HOST=node`).
3. Oracle URL — publish the **same filtered oracle** as local HA (e.g. `oracle-v5` on a private port `19100:9100`). Pointing remote `versiond` at the raw `/versions` feed can launch versions excluded from the local HA pool.
4. Confirm `PGPASSWORD` / `KEYRING_PASSWORD` in `config.env` match what **running** local `versiond`* containers use.

The RC provides `docker-compose.private-endpoints.yml` for A's chain, node-manager and bundled DB ports (`GONKA_PRIVATE_BIND_IP` selects A's private interface). For the filtered oracle, add this service override to A's existing file list:

```yaml
services:
  oracle-v5:
    ports:
      - "${GONKA_PRIVATE_BIND_IP:?set A private IP}:19100:9100"
```

Apply private-port changes in a maintenance window; recreating `node`, `api` or the shared DB can interrupt both replicas. With managed PG, B connects directly to that DB instead.

**On machine B (`versiond` only) — one compose file is enough**

B does not run `api` / `node`. It runs a single `versiond` that uses **A’s** Postgres, oracle, node-manager, and chain endpoints over the private network.

1. **Same participant identity as A** — same `KEY_NAME`, `ACCOUNT_PUBKEY`, `KEYRING_PASSWORD`, and a copy of A’s `.inference/keyring-file/` (often root-owned; copy with `sudo`). Mount it read-only as `/root/.inference`. Do **not** start a second `api` with those keys on B.
2. **Put all connection settings in the** `versiond` **service** `environment:` (compose file). Shell `export`s in `config.env` only help if compose interpolates them into that block — the container must see the vars.
3. **Own data dir** on B (do not share A’s `./devshards*/data`). Binary cache dir may be local.
4. **Publish** `versiond` **on B’s private IP at port 8080** (recommended) so A’s router fleet can include B in its endpoint file. Bind LAN-only, not `0.0.0.0`. Optionally firewall so only A can connect.

Example file on B: `docker-compose.versiond-remote.yml` (replace private IPs; `source ./config.env` before `docker compose up`):

```yaml
services:
  versiond:
    image: ${VERSIOND_IMAGE:?set the approved RC versiond image}
    container_name: versiond
    environment:
      # Same filtered HA oracle as local replicas
      - VERSIOND_ORACLE_URL=http://<A-private-ip>:19100/versions
      - VERSIOND_BINARY_NAME=devshardd
      - GONKA_HA=true
      - VERSIOND_NON_HA_VERSIONS=
      - VERSIOND_DRAIN_ANNOUNCE=5s
      - VERSIOND_HOST_SHUTDOWN_BUDGET=25m
      - NODE_MANAGER_ADDR=<A-private-ip>:9400
      - NODE_HOST=<A-private-ip>
      - KEY_NAME=${KEY_NAME}
      - ACCOUNT_PUBKEY=${ACCOUNT_PUBKEY}
      - KEYRING_BACKEND=${KEYRING_BACKEND:-file}
      - KEYRING_PASSWORD=${KEYRING_PASSWORD}
      - KEYRING_DIR=/root/.inference
      - PGHOST=<A-private-ip>
      - PGPORT=5432
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
      test: ["CMD", "/bin/busybox", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/readyz"]
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
docker compose -f docker-compose.versiond-remote.yml up -d --wait
curl -fsS 'http://<B-private-ip>:8080/readyz?version=v5'   # not 127.0.0.1 if bound to LAN IP only
```

**On A (router fleet) — include every local and remote member**

Create `versiond-endpoints.json`:

```json
[
  { "id": "versiond", "host": "versiond", "port": 8080 },
  { "id": "versiond2", "host": "versiond2", "port": 8080 },
  { "id": "versiond-b", "host": "10.20.0.12", "port": 8080 }
]
```

Replace B's example address. Set `export VERSIOND_POOL_ENDPOINTS_FILE=./versiond-endpoints.json` in A's `config.env`; the path is relative to that file. Docker DNS only discovers local containers. An explicit file replaces discovery, so include **all** intended members.

For the **first** fleet start, use `apply`. For an **existing** fleet, changing the endpoint list changes session placement and requires a maintenance window:

```bash
source ./config.env
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true ./versiond-router-fleet.sh maintenance-rollout
./versiond-router-fleet.sh verify-admission
```

Editing the file alone does not update running slots. Ordinary `apply` refuses a changed placement contract. Every serving router must move to the same membership generation.

**Verify cross-machine HA** as in Step 4: use real inference on an escrow served by B, stop B's `versiond`, and verify subsequent work and committed state on a survivor. Keep shared dependencies outside B for this test.

### 2.4 Adding a third replica

1. Add `docker-compose.versiond3.yml` to your complete Compose file list. It extends `versiond2` with a new container name, `./devshards3/data` and `VERSIOND3_REPLICAS`.
2. Repeat your oracle / external-PG overrides for `versiond3`. It must join the `versiond-pool` alias on the router back network and use the same identity and storage.
3. Start only the new service: `docker compose up -d --no-deps --wait versiond3`.
4. DNS membership is discovered automatically. If using an endpoint file, append the member and perform the maintenance rollout in §2.3.

### 2.5 Operating versiond members

Use the complete Compose file list on the member's own machine:

| Task | Command / action |
| --- | --- |
| Temporarily stop `versiond2` | `docker compose stop versiond2` |
| Start it again | `docker compose up -d --no-deps --wait versiond2` |
| Replace its image | Set the RC image, stop that service, then `docker compose up -d --no-deps --wait versiond2` |
| Permanently remove it | Persist `VERSIOND2_REPLICAS=0`, then stop and remove its container; also update explicit membership if used |

Stop **one member at a time**, with enough healthy capacity left. Docker does not protect the last survivor. The default announce window is `5s`, host shutdown budget `25m`, and Compose stop grace `30m`; keep the external grace longer than the host budget. All versiond durations require units (`30s`, not `30`).

A graceful stop first makes `/readyz` fail, lets routers withdraw the member, then drains accepted requests. A crash or an expired drain budget can interrupt SSE. Shared Postgres permits later requests to recover committed session state; it does not move an active stream to a new connection. Do not use router-side slot drain as a substitute for stopping the member, and do not decommission a legacy SQLite owner while it still has sessions.

---



## Step 3 - Environment variables checklist

### Put in `deploy/join/config.env` (and `source` it before compose)

```bash
# Identity (already required for join; must match on every HA replica)
export KEY_NAME=...
export ACCOUNT_PUBKEY=...
export KEYRING_BACKEND=file
export KEYRING_PASSWORD=...

# Devshard HA Postgres — password required; DB/user have defaults
export DEVSHARD_POSTGRES_DB=devshardd
export DEVSHARD_POSTGRES_USER=devshardd
export DEVSHARD_POSTGRES_PASSWORD='...'

# Fresh v5-only example (§2.1)
export GONKA_HA=true
export VERSIOND_VERSIONS="v5"
export VERSIOND_NON_HA_VERSIONS=""
export VERSIOND_ROUTING_CATALOG_URL=http://oracle-v5:9100/versions
export VERSIOND_POOL_HOST=versiond-pool
unset VERSIOND_HOSTS
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5-only.override.yml

# Defaults: three router slots, two ready routers retained during ordinary updates
export VERSIOND_ROUTER_FLEET_SLOTS="0 1 2"
export VERSIOND_ROUTER_MIN_READY=2
export VERSIOND_ROUTING_ACTIVATION_MIN_READY=2
export PROXY_ROUTER_ACTIVATION_MIN_READY=2
```

The first activation reserve counts **versiond members**; the public reserve counts **router slots**. Keep the values within the corresponding pool size, and preserve enough capacity when replacing members.

### Already set by `docker-compose.versiond.yml`

You normally **do not edit these by hand** when using the local `devshard-postgres` service:

- `PGHOST=devshard-postgres`
- `PGDATABASE` / `PGUSER` / `PGPASSWORD` (from `DEVSHARD_POSTGRES_*`)
- `PG_POOL_MAX_CONNS` (from `DEVSHARD_POSTGRES_POOL_MAX_CONNS`, default `4`)
- `GONKA_HA=true` and `DEVSHARD_STORAGE_MODE=postgres`
- per-version readiness routing and fleet network connections

### Put in compose overrides

| File | Purpose |
| --- | --- |
| `docker-compose.devshard-v5-only.override.yml` | Filtered oracle + every HA supervisor uses it |
| `docker-compose.devshard-pg-external.override.yml` | Same managed DB and TLS settings on every HA replica (see §2.2) |

Image overrides belong in `config.env` too: `VERSIOND_IMAGE`, `VERSIOND_ROUTER_IMAGE`, `PROXY_ROUTER_IMAGE`, `PROXY_POLICY_IMAGE`. Keep supervisor, router and child capabilities aligned with the chosen RC.

---



## Step 4 - Verify it works

```bash
# 1) Main containers and independent router slots
docker compose ps
./versiond-router-fleet.sh status
./versiond-router-fleet.sh verify-admission
./versiond-router-fleet.sh wait-version v5

# 2) Inspect children, then check readiness on EVERY replica
docker exec versiond wget -qO- http://127.0.0.1:8080/healthz
docker exec versiond wget -qO- 'http://127.0.0.1:8080/readyz?version=v5'
docker exec versiond2 wget -qO- 'http://127.0.0.1:8080/readyz?version=v5'

# 3) Prove local replicas use the same live database (writes diagnostic challenges)
./update-devshard.sh --check

# 4) Public route (use your actual public URL / protocol name)
curl -si http://127.0.0.1:8000/devshard/v5/healthz | grep -iE 'HTTP/|X-Upstream|X-Versiond'
```

`/healthz` shows process state; it is **not** a readiness or failover test. A live child can lose database readiness. The router uses `/readyz?version=<name>` so one unavailable version does not withdraw unrelated healthy versions. New catalog routes wait for their ready reserve; unknown or unready versions remain unavailable.

Healthy signs:

- Only intended versions run; every HA child uses Postgres storage.
- Every intended version is ready on the required number of members and admitted through both routing tiers.
- The same database is used by all replicas. Matching `PGHOST` strings or copied lineage IDs alone are not proof; use the updater's live storage checks for local members, and [verify remote members separately](../devshard/docs/storage-design.md#operational-notes).
- Real inference through the public endpoint succeeds, with correct results and charges.

**Prove failover with a real session.** Start finite SSE inference, identify the serving member from correlated request/session logs (and diagnostic headers where available), and stop **that** service as in §2.5. Confirm accepted work finishes within the drain budget, then continue on the same escrow through a survivor. Check nonces, results and costs; stopping an unused member does not prove HA.

For an abrupt host-loss test, the active stream may fail. Verify that subsequent work recovers committed state without blindly replaying an inference POST. HAProxy does not replay non-idempotent requests after possible execution, and does not retry application `503` responses. `X-Upstream-Addr` identifies the serving upstream; do not expect nginx's old comma-separated retry chain.

See the [HA lifecycle test plan](devshard-host-ha-test-plan.md) for the full acceptance cases.

---



## Step 5 - `versiond-router` HA

| Component | HA status in this RC layout |
| --- | --- |
| `versiond` / `devshardd` | N replicas + shared Postgres; one replica per machine can survive application-host loss |
| Postgres | **Your choice of Options A/B/C** (prefer managed/replicated) |
| `versiond-router` | Independent HAProxy fleet; default three slots, ready reserve two |
| nginx policy workers | Two private workers behind the public proxy |
| Public `proxy` (HAProxy) | Still one process on one Docker host; public ingress HA requires a separate design |
| `decentralized-api` | Still single-instance |

For a compatible router image update, edit `config.env` and run `./versiond-router-fleet.sh apply`, then `status` and `verify-admission`. The fleet replaces slots one at a time and checks admission before continuing. Membership and other session-placement changes use `maintenance-rollout` (§2.3).

The slots belong to separate Compose projects. Before taking the main stack down, run `./versiond-router-fleet.sh stop-all --maintenance`; remove the fleet with `./versiond-router-fleet.sh down --maintenance` after the main stack is stopped. Three slots on one machine do not protect against losing that machine or its Docker daemon.

---



## Updating an existing host

Use the **updater from the chosen RC**, not the fresh-install `up` sequence. Keep existing session versions, legacy pins, data directories and operator overlays in the model. In particular, do not replace an existing v4 catalog with the fresh v5-only example while it still serves v4 escrows.

### 1. Prepare

1. Record the current release, image digests and complete ordered Compose file list. Back up `config.env`, operator overrides and the database.
2. Keep the existing PostgreSQL container and its anonymous volume attached. Do **not** run `docker compose down`, remove the DB container, renew anonymous volumes (`-V`) or prune volumes before migration. The first in-place recreation needs that old volume.
3. Obtain the RC deployment files and select its image digests. Preserve your overrides; for an existing HA host include the HA overlay in `COMPOSE_FILE`. A single-versiond update remains single unless you explicitly change the deployment.
4. Check the approved child artifacts and both catalogs. Use the same HA/legacy version split on every routing tier, and correct old duration values to include units.
5. Schedule a maintenance window for the first cutover: restarting the shared local DB interrupts all its replicas, and replacing the public nginx proxy with HAProxy cuts its existing connections.

### 2. Check and update

```bash
cd /path/to/gonka/deploy/join
source ./config.env

# COMPOSE_FILE must contain this host's complete ordered file list.
# If unset, the updater discovers recorded Compose files from existing containers.
./update-devshard.sh --check
./update-devshard.sh --dry-run
./update-devshard.sh
```

`--check` and `--dry-run` do not replace or restart services. They still run preflight: helper containers and temporary PostgreSQL write/challenge probes can be used, and the migration space check may create the empty target directory. The updater holds the deployment lock during these checks.

Before replacement, the updater checks matching PostgreSQL settings on local HA replicas, a writable primary, available migration space, and the live database identity/challenge through supported running children and generations. Old endpoints returning `404` are a compatibility exception, not proof of shared storage. Leave `UPDATE_SKIP_POSTGRES_PROBE` and `UPDATE_ACCEPT_DATABASE_CHANGE` unset for an ordinary update. **Remote replicas are updated and checked manually.**

The script prints its Compose operations, prepares the router networks, applies the fleet, cuts over the public tier and checks fleet admission before removing the old nginx router or replacing versiond members. It replaces local members one at a time, leaving `versiond` (the default legacy owner) last. It does not update the chain or dapi for you.

The bundled PostgreSQL entrypoint copies the old cluster into persistent PGDATA and leaves the anonymous volume untouched. Allow space for the complete copy. If the source volume is missing or the target is inconsistent, stop and use the release's PostgreSQL recovery procedure; do not enable empty initialization to get past the failure.

### 3. Verify or recover

Run Step 4, then a real inference on a previously used escrow before updating the next host. For remote versiond, use its complete Compose file list, pull the selected image, gracefully stop **one** member, start it with `up -d --no-deps --wait versiond`, and verify admission before continuing.

If a compatible service replacement fails its healthcheck, the updater attempts to restore that service's previous healthy image and stops. Services already updated may remain on the new release. Inspect the failing service's logs, correct the cause and rerun; there is no updater journal to reset. Ordinary fleet candidate failures have their own slot rollback.

An image rollback cannot restore an old service definition. The first nginx-to-HAProxy cutover may require restoring the **previous Compose files and overrides** as well as images. Retain those files until verification is complete.

**Do not switch back to the old PostgreSQL volume after the persistent database has accepted writes.** That volume is now behind the live history. Recover the current database; application image rollback does not roll back PostgreSQL migration or session state.

Protocol-compatible same-name `devshardd` artifact changes are a separate supervisor operation: a ready Postgres replacement takes new requests while the old generation drains. An RC containing [#1702](https://github.com/gonka-ai/gonka/pull/1702) bounds this to one predecessor. Startup/readiness fixes [#1663](https://github.com/gonka-ai/gonka/pull/1663) and [#1706](https://github.com/gonka-ai/gonka/pull/1706) are also still open; verify which ones your RC includes before testing slow or failed child starts. Signature changes in [#1549](https://github.com/gonka-ai/gonka/pull/1549) and [#1705](https://github.com/gonka-ai/gonka/pull/1705) require a **new approved protocol name**; do not publish them as a same-name replacement for live sessions.

---



## What not to do

1. **Two versionds on SQLite** — split-brain / missing leases.
2. **Different keys or different databases** on HA replicas of the same participant.
3. **Launch pre-HA binaries on Postgres peers** — use the filtered oracle, or a dedicated non-HA supervisor for legacy versions.
4. **Two dapi processes** with the same warm/cold keys — duplicates PoC / chain txs.
5. **Assume local** `devshard-postgres` **or one ingress machine is “full HA”** — replicate the DB or use managed PG; plan ingress separately.
6. **Omit an override from later Compose commands**, or edit an endpoint file and assume running routers changed membership.
7. **Use the fresh-install recipe over a running host**, discard its old PG volume before migration, or roll back to that stale volume after new writes.

---



## Minimal recipe (fresh host, two versionds, v5-only)

```bash
cd deploy/join
source ./config.env

# Persist the settings from §2.1, including approved RC image references.
# Create docker-compose.devshard-v5-only.override.yml as in §2.1.
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5-only.override.yml

./versiond-router-fleet.sh prepare-networks
docker compose up -d --wait
./versiond-router-fleet.sh apply
./versiond-router-fleet.sh verify-admission
./versiond-router-fleet.sh wait-version v5
```

For **managed HA Postgres**, include both §2.2 files in the same `COMPOSE_FILE` before starting. For an **existing host**, use the update section instead.
