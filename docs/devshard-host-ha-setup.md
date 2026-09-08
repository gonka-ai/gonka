# High-availability Devshard Host Setup

**Audience:** hosts that serve inference over **devshard** (`/devshard/...` → `versiond` → `devshardd`).  
**Status:** draft for host operators - edit before wider distribution.  
**Goal:** run a **high-available (HA)** host stack so a single `versiond` / `devshardd` failure does not take the host offline.

**Release:** `devshard-0.2.15-v5`, checked against branch commit `39240311fb` on 2026-09-08.

---

## Why this matters

A single `versiond` process is a single point of failure (SPOF): if that machine or container dies, gateways cannot reach your host for that protocol version.

We now support an **HA host layout** with multiple `versiond` replicas. The examples below use `versiond` and `versiond2`; add more replicas as described in §2.4:

```text
Public proxy (/devshard/...)
        │
        ▼
 versiond-router   ← sticky routing by session/escrow ID
        │
        ├── versiond  (instance A) ──► devshardd children
        └── versiond2 (instance B) ──► devshardd children
                 │
                 └── shared Postgres  (required)
```

**You must use Postgres** for HA. SQLite is single-writer and **must not** be shared across instances.

---



## Prerequisites

1. Join files from **`devshard-0.2.15-v5`**, a v5-capable `versiond` image, a catalog-capable HAProxy `versiond-router` image, and governance-approved `devshardd` artifacts for the protocols you serve. A tested set from one release candidate is recommended; compatibility depends on the actual component capabilities and protocol artifacts. Stock join files still pin `versiond:0.2.15` and default to the old nginx `versiond-router:0.2.15`; select the v5 images explicitly in §2.1.
2. Working `node` + `api` (dapi) + `proxy` on the host (standard join deployment).
3. Same participant identity on **every** HA `versiond` replica:
  - same `KEY_NAME` / keyring
  - same `ACCOUNT_PUBKEY`
4. Only put **Postgres-capable versions** into the HA pool. With `GONKA_HA=true`, v5 `versiond` requires each HA child to support `--print-storage-mode` and report `postgres`; the child must also match its approved protocol name. Keep older versions (`v1` / `v2` / `v3`) pinned to a **legacy** single host if you still serve them.
5. Confirm the actual `api:9100/versions` response contains the required protocol, downloadable binary URL and SHA256. It follows chain-approved versions; the branch name does not publish a v5 artifact or activate it on-chain. The examples below use protocol `v5`.

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
| SSL      | follow your provider |


Note the primary (or HA) endpoint: host, port (often `5432`), database, user, password. Ensure **all** `versiond` instances can reach it (firewall / VPC / security groups).

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
2. Add a compose override (for example `deploy/join/docker-compose.devshard-pg-external.override.yml`) that sets `PGHOST` (and related vars) under **every** `versiond`* service in the HA pool and disables the unused local database and its dependencies — see Step 2.2.
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
3. `source ./config.env`, then start with `-f docker-compose.versiond.yml` (see Step 2.1). A fresh empty deployment initializes its persistent cluster automatically. If binaries already exist but no cluster is attached, startup refuses empty initialization: restore the old database, or set `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true` once only for confirmed first-time HA enablement, then unset it. A `.pg-bound` marker always requires restoring the database; the flag cannot bypass it.

---



## Step 2 - Run multiple `versiond` instances + `versiond-router`

Example files in your setup. Some are present in the release branch:


| File                                                           | Role                                                                                                  |
| -------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `deploy/join/docker-compose.yml`                               | Base join (`versiond`, `proxy`, …)                                                                    |
| `deploy/join/docker-compose.versiond.yml`                      | HA overlay: Postgres + additional replica (`versiond2`) + `versiond-router`, proxy → router               |
| `deploy/join/docker-compose.devshard-v5.override.yml`     | **Recommended:** v5 images + HA oracle filter + router catalog (you create this; see §2.1) |
| `deploy/join/docker-compose.devshard-pg-external.override.yml` | Optional: point **every** `versiond`* replica at managed Postgres (see §2.2)                          |


> **Why the HA oracle filter?** `api:9100/versions` follows chain governance and can include pre-HA versions. Without a filter, every HA peer can try to start those children against shared Postgres, which is unsupported and can race on schema migration. `VERSIOND_NON_HA_VERSIONS` controls routing and v5 child HA preflight; it does **not** stop versiond from launching a version. The override gives supervisors and the router the same filtered catalog and empties the non-HA pin list.



### 2.1 Same machine, multiple instances (fresh v5 installation)

On the join host (for an existing deployment, prepare these files but follow §2.6 before `up -d`):

**1. Credentials in** `config.env`

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
export VERSIOND_VERSIONS="v5"
# For an upgrade retaining v4 sessions, use "v4 v5" after the checks in §2.6.
# The override below clears NON_HA on supervisors and router.
```

Use the published image references for the tested candidate; the repository does not specify final v5 tags. `VERSIOND_IMAGE` is consumed by the override below, not by the stock overlay. Selecting a catalog-capable router also requires a non-empty `VERSIOND_ROUTING_CATALOG_URL`; the override supplies it.

**2. Create** `docker-compose.devshard-v5.override.yml`

```bash
cat > docker-compose.devshard-v5.override.yml <<'EOF'
services:
  # Filtered /versions: only selected HA protocols (prevents v3 children on the HA+Postgres pool)
  oracle-v4:
    container_name: oracle-v4
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
    restart: always

  versiond:
    image: ${VERSIOND_IMAGE:?select the v5 versiond image}
    environment:
      - VERSIOND_ORACLE_URL=http://oracle-v4:9100/versions
      - VERSIOND_NON_HA_VERSIONS=
    depends_on:
      oracle-v4:
        condition: service_started
      devshard-postgres:
        condition: service_healthy

  versiond2:
    image: ${VERSIOND_IMAGE:?select the v5 versiond image}
    environment:
      - VERSIOND_ORACLE_URL=http://oracle-v4:9100/versions
      - VERSIOND_NON_HA_VERSIONS=
    depends_on:
      oracle-v4:
        condition: service_started
      devshard-postgres:
        condition: service_healthy

  # If you add versiond3 (or more), give each the same oracle-v4 env + depends_on.

  versiond-router:
    environment:
      - VERSIOND_NON_HA_VERSIONS=
      - VERSIOND_ROUTING_CATALOG_URL=http://oracle-v4:9100/versions
      - VERSIOND_VERSIONS=${VERSIOND_VERSIONS:-v5}
EOF
```

**3. Bring up HA with all three compose files**

```bash
source ./config.env

docker compose \
  -f docker-compose.yml \
  -f docker-compose.versiond.yml \
  -f docker-compose.devshard-v5.override.yml \
  up -d
```

What this starts:


| Container           | Purpose                                       |
| ------------------- | --------------------------------------------- |
| `devshard-postgres` | Shared DB (if not using external PG)          |
| `oracle-v4`         | Filtered versions oracle (selected HA protocols)            |
| `versiond`          | Replica A — data dir `./devshards/data`       |
| `versiond2`         | Replica B — data dir `./devshards2/data`      |
| `versiond-router`   | Sticky HAProxy in front of the HA pool          |
| `proxy`             | Public edge; `/devshard/` → `versiond-router` |


**4. Confirm**

```bash
docker ps --format '{{.Names}}\t{{.Status}}' | grep -E 'oracle-v4|versiond|devshard-postgres'

docker inspect proxy --format '{{range .Config.Env}}{{println .}}{{end}}' | grep VERSIOND_SERVICE_NAME
# VERSIOND_SERVICE_NAME=versiond-router

docker inspect versiond-router --format '{{range .Config.Env}}{{println .}}{{end}}' | grep VERSIOND_
# VERSIOND_POOL_HOST=versiond-pool
# VERSIOND_NON_HA_VERSIONS=   (empty)
# VERSIOND_ROUTING_CATALOG_URL=http://oracle-v4:9100/versions

docker exec oracle-v4 wget -qO- http://127.0.0.1:9100/versions
# expect the selected approved protocols and their real binary URL/SHA256

# Include every local replica in this list.
for replica in versiond versiond2; do
  docker exec "$replica" wget -qO- http://127.0.0.1:8080/healthz
  docker exec "$replica" wget -qO- "http://127.0.0.1:8080/readyz?version=v5"
done
# expect v5 running and per-version readiness HTTP 200 on every replica
docker exec versiond-router wget -qO- "http://127.0.0.1:8404/readyz?version=v5"
# router container health (/livez) alone does not prove that a route is ready
```

#### Optional: still serving pre-v4 (v3) on the same host

Only if you must keep SQLite versions. Prefer a **dedicated non-HA** supervisor for those; keep them off the HA peers on Postgres. Stock overlay defaults:

```bash
export VERSIOND_LEGACY_HOST=versiond
export VERSIOND_NON_HA_VERSIONS="v1 v2 v3"
```

Do **not** clear the non-HA pin list in that case; use a split layout instead of launching v3 under `DEVSHARD_STORAGE_MODE=postgres` on HA replicas. Keep HA peers on the filtered oracle; give the dedicated legacy owner its own legacy-only oracle and SQLite data. Set `VERSIOND_LEGACY_HOST` to that owner and replace the hardcoded empty `VERSIOND_NON_HA_VERSIONS=` entries in the v5 override with the legacy list on supervisors and router; exports alone do not override those entries.



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
      oracle-v4:
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
      oracle-v4:
        condition: service_started
```

Use Docker Compose with `!override` support. This excludes the local database from `up -d` and removes the local-PG dependency on the replicas shown; repeat for every additional replica. Otherwise v5's lost-database guard can reject the unused local database after HA artifacts/state exist.

1. Start (include the v5 override as well if you use the recommended §2.1 layout):

```bash
cd deploy/join
source ./config.env

docker compose \
  -f docker-compose.yml \
  -f docker-compose.versiond.yml \
  -f docker-compose.devshard-v5.override.yml \
  -f docker-compose.devshard-pg-external.override.yml \
  up -d
```

If you fully disable the local `devshard-postgres` service, also remove or override its `depends_on` entries on **every** `versiond`* service so compose does not wait on a container you never start.

### 2.3 Multiple machines (recommended, true host HA)

Conceptually the same layout, but each machine runs one `versiond`, and one place runs `versiond-router` (or you place the router behind a future load balancer). Prefer a **private network** between machines; bind new listeners to private IPs only if you cannot open extra public ports.

Example with one replica per machine:


| Role      | Runs                                                            |
| --------- | --------------------------------------------------------------- |
| Machine A | `versiond` (+ usual node/api/proxy) + `versiond-router`         |
| Machine B | `versiond` only — **no** second dapi with the same keys         |
| Shared    | Postgres reachable from every `versiond` (managed HA preferred) |


> **Important:** `decentralized-api` (dapi) is still **single-instance** today. HA here is for **devshard traffic** (`versiond` / `devshardd`), not for running two dapis with one key.

**On machine A (dapi / Postgres / router side) — publish for B on the private network:**

1. Postgres (`5432`) and node-manager gRPC (`9400`) — required.
2. Chain RPC/gRPC (`26657`, `9090`) — required for remote `devshardd` (same as local `NODE_HOST=node`).
3. Oracle URL — use the **same filtered oracle** as local HA (e.g. `oracle-v4` on a private port). Pointing remote `versiond` at raw `api:9100/versions` can launch versions excluded from the HA pool.
4. Confirm `PGPASSWORD` / `KEYRING_PASSWORD` in `config.env` match what **running** local `versiond`* containers use.

**On machine B (**`versiond` **only) — one compose file is enough**

B does not run `api` / `node`. It runs a single `versiond` that uses **A’s** Postgres, oracle, node-manager, and chain endpoints over the private network.

1. **Same participant identity as A** — same `KEY_NAME`, `ACCOUNT_PUBKEY`, `KEYRING_PASSWORD`, and a copy of A’s `.inference/keyring-file/` (often root-owned; copy with `sudo`). Mount it read-only as `/root/.inference`. Do **not** start a second `api` with those keys on B.
2. **Put all connection settings in the** `versiond` **service** `environment:` (compose file). Shell `export`s in `config.env` only help if compose interpolates them into that block — the container must see the vars.
3. **Own data dir** on B (do not share A’s `./devshards*/data`). Binary cache dir may be local.
4. **Publish** `versiond` **on B’s private IP at port 8080** (recommended) so A’s router can discover B’s IP through the pool DNS name with `VERSIOND_PORT=8080`. Bind LAN-only, not `0.0.0.0`. Optionally firewall so only A can connect.

Example file on B: `docker-compose.versiond-remote.yml` (replace private IPs; `source ./config.env` before `docker compose up`):

```yaml
services:
  versiond:
    image: ${VERSIOND_IMAGE:?select the same v5 versiond image as A}
    container_name: versiond
    environment:
      # Same filtered HA oracle as on A
      - VERSIOND_ORACLE_URL=http://<A-private-ip>:19100/versions
      - GONKA_HA=true
      - VERSIOND_NON_HA_VERSIONS=
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
      - PGHOST=<A-private-ip>
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
    stop_grace_period: 30m
    restart: always
```

```bash
mkdir -p devshards-remote/{bin,data}
source ./config.env
docker compose -f docker-compose.versiond-remote.yml up -d
curl -fsS "http://<B-private-ip>:8080/readyz?version=v5"   # not 127.0.0.1 if bound to LAN IP only
```

**On the machine that runs** `versiond-router` **(usually A):** use a DNS name that returns every replica’s reachable private IPv4 address. Docker’s local `versiond-pool` alias does not discover another machine. `VERSIOND_PORT` stays **8080**:

```bash
export VERSIOND_POOL_HOST=versiond-pool.internal
export HAPROXY_DNS_RESOLVER='<private-dns-ip>:53'
# Clear NON_HA via the v5 override; do not leave stock "v1 v2 v3".
```

Publish local listeners on reachable private addresses as well if that DNS pool includes them. Recreate `versiond-router` when changing the DNS name or resolver settings; subsequent DNS membership changes are discovered automatically. `VERSIOND_HOSTS` is only for the transitional nginx image and does not configure the v5 HAProxy pool.

**On the public** `proxy`**:** confirm after recreate:

```text
VERSIOND_SERVICE_NAME=versiond-router
VERSIOND_PORT=8080
```

**Verify cross-machine HA** the same way as Step 4 §6: find a sticky session whose `X-Upstream-Addr` is the remote replica, stop **that** machine’s `versiond`, wait for withdrawal and confirm the same session continues on a survivor with its committed state intact. `X-Upstream-Addr` shows the final selected peer, not an nginx retry history.

### 2.4 Adding more replicas

1. Add another `versiond` service (new container name + new data volume).
2. Give it the same image, filtered oracle, identity, HA/Postgres environment and drain settings. On the same Docker network add alias `versiond-pool`; across machines add its reachable address to the pool DNS from §2.3.
3. Start it and wait for every required `/readyz?version=...` to return 200. The router discovers it without recreation; confirm real inference through the public route.

### 2.5 Operating versiond members

Use the same ordered compose files for every operation (append §2.2's external-PG override when applicable):

```bash
cd /path/to/gonka/deploy/join
source ./config.env
dc=(docker compose -f docker-compose.yml -f docker-compose.versiond.yml
    -f docker-compose.devshard-v5.override.yml)
# External PG: dc+=(-f docker-compose.devshard-pg-external.override.yml)

# Stop only one member, keeping enough ready survivors for the load.
"${dc[@]}" stop versiond2
# Restart the same member with its existing data:
"${dc[@]}" up -d --no-deps versiond2
docker exec versiond2 wget -qO- "http://127.0.0.1:8080/readyz?version=v5"
```

A normal stop makes v5 `versiond` unready, announces for 5 seconds while still serving, then closes admission and drains accepted requests/children. Keep the overlay's 30-minute Compose stop grace above the 25-minute host budget. Do not shorten the announce window below the router's detection window; do not set it to `0s` behind HAProxy. Budget exhaustion can force termination, so requests exceeding the budget can be interrupted. Custom duration values need units (`30s`, `5m`); the announce window plus effective child termination grace (10 minutes by default) must remain below the host budget.

For a compatible image replacement, select the new image for that service, stop it, then run `up -d --no-deps` for that service only. Wait for **every required version** to become ready and verify inference before replacing another member. Docker Compose does not enforce a survivor reserve. If the candidate fails, restore that service's prior image/configuration and recreate it; verify it against the current database before proceeding.

For permanent removal, stop/drain the member first, remove its service or pool DNS record, and confirm the remaining pool serves its sessions. Keep its data and binary directories until recovery is verified. Router recreation is unnecessary for DNS membership changes.

To add another approved HA protocol in this filtered layout, extend `VERSIOND_VERSIONS` and recreate `oracle-v4` and the router with the same ordered files; supervisors poll the oracle. Preserve the `versiond-router-state` volume. The catalog retains accepted routes when its source fails and does not automatically remove them by default; removing a protocol requires supervised maintenance after its sessions are no longer needed.

### 2.6 Upgrade an existing HA deployment to v5

Use the same Postgres, identity and per-replica mounts as the existing deployment. Take a database backup and record the current images, approved binary URLs/SHA256, compose project/files, mounts and a working escrow. Preserve `.inference`, each `devshards*/data` directory, the binary cache and router catalog state. Prepare the v5 files from §2.1 in the **same compose project**, but do not run an unrestricted `up -d` yet.

**1. Keep existing protocols available.** For active v4 sessions, set `VERSIOND_VERSIONS="v4 v5"` in the new configuration once v5 is approved; the filtered oracle and bootstrap routes must retain v4. Before replacing supervisors, check the actual approved v4 binary's `--print-storage-mode` in the HA environment (`postgres`) and `--print-protocol-version` (`v4`). An old artifact without the HA storage probe cannot join the new HA supervisor pool. Use matching gateway/host protocol artifacts. Do not rename a v4 binary/escrow to v5 or assume v4 session state is migrated into the v5 protocol. Keep serving retained v4 sessions under their compatible v4 artifact; use a new escrow for v5.

If v5 is not approved yet, keep the cutover filter at `v4` and add v5 only after approval. New supervisors automatically promote verified old `<bin>/<version>/devshardd` installs into `<bin>/<version>/<archive-sha256>/devshardd`. They verify the old metadata against the current oracle and otherwise download again. **Do not delete or manually rename** the cache or `<data>/<version>` directories.

**2. Local PostgreSQL only — migrate the existing cluster during maintenance.** Managed/external PostgreSQL with unchanged data skips this cluster-copy step. Before stopping anything, record the old local source and run the v5 space preflight:

```bash
# Run in deploy/join, before removing/recreating the old container.
docker inspect devshard-postgres --format '{{json .Mounts}}'
docker exec devshard-postgres sh -c \
  'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT system_identifier FROM pg_control_system();"'
# Record the system identifier, source volume at /var/lib/postgresql/data, and backup.
bash ./devshard-postgres-migration-preflight.sh \
  --source-container devshard-postgres \
  --target-dir "${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}"
```

The copy needs the source cluster's size plus 10% free space. Keep the existing PostgreSQL major version and Alpine/musl image family (`postgres:16-alpine` by default); this is a data-directory copy, not `pg_upgrade` or conversion between image variants.

Enter a maintenance window, stop new devshard traffic, let accepted work finish, and stop **all** database-writing HA members, including remote ones. With the compose array from §2.5:

```bash
# Include every local member; stop remote members on their own machines too.
docker stop --time 1800 versiond versiond2
# Refresh the database backup now that application writes have stopped.
docker stop --time 300 devshard-postgres
"${dc[@]}" up -d --no-deps devshard-postgres
"${dc[@]}" logs --tail=100 devshard-postgres
```

Recreate the database **in place** so Compose retains its old anonymous volume. Do **not** use `down`, `down -v`, `rm -v`, volume pruning, or `--renew-anon-volumes` before migration. The entrypoint copies the old cluster into staging, publishes it atomically under `devshards/postgres/data`, and preserves the source. Wait for PostgreSQL health, repeat the system-identifier query above and verify the recorded identifier and committed data before starting writers. If a copy fails, preserve the source, resolve the logged error and retry; an incomplete target is not a usable database.

If the old volume was already detached, use its **recorded exact name**, not an empty replacement:

```bash
export DEVSHARD_POSTGRES_LEGACY_VOLUME='<recorded-old-volume-name>'
bash ./devshard-postgres-migration-preflight.sh \
  --source-volume "$DEVSHARD_POSTGRES_LEGACY_VOLUME" \
  --target-dir "${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}"
"${dc[@]}" -f docker-compose.versiond-postgres-recovery.yml \
  up -d --no-deps devshard-postgres
```

After verifying migration, recreate PostgreSQL once without the recovery overlay, while writers remain stopped. Keep the source volume and backup. Never use `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT` to recover a deployment whose database is missing.

**3. Replace the router and supervisors, then verify before reopening traffic.** Start `oracle-v4` with the selected catalog. For an external database or an already-migrated cluster, keep ready survivors running; for the first local cluster migration they remain stopped until the database is verified. Add the `versiond-pool` alias to existing local containers, or configure the DNS pool from §2.3, before using the new HAProxy router. Replace the router before evacuating supervisors: old nginx does not withdraw hosts on `/readyz`. The new router accepts a pre-v5 supervisor's `/readyz` **404** only together with a successful `/<version>/healthz`; a v5 **503** is never treated as a legacy fallback.

```bash
"${dc[@]}" up -d --no-deps oracle-v4
# Before router replacement, ensure its pool DNS resolves the existing members.
"${dc[@]}" up -d --no-deps versiond-router
# Stop and replace one member at a time as in §2.5; repeat for versiond2/remote peers.
"${dc[@]}" stop versiond
"${dc[@]}" up -d --no-deps versiond
# Check /readyz?version=v4 and /readyz?version=v5 for every required route.
```

A one-router replacement has a routing interruption; schedule it in the maintenance window. Pre-v5 supervisors also lack host evacuation, so let their accepted work finish before stopping them; their active streams can otherwise be interrupted. Check per-version readiness on every member and the router, then the public route and recorded v4 escrow, and finally a new v5 escrow as in Step 4. If existing containers lack the pool alias, add it in a controlled network change or recreate them during maintenance; an alias in an unapplied compose file alone does not change the running network.

**State migration and rollback.** Existing HA PostgreSQL data stays in the same database. v5 applies forward schema migrations under a database advisory lock. For first-time conversion of a single-owner deployment, explicit `postgres` mode also imports supported epoch-layout SQLite sessions and file payloads before serving; stop the old writer and migrate each source directory with one owner. Successful sources are quarantined as `*.migrated.<timestamp>`, and conflicting data aborts startup. Older monolithic layouts need separate verification.

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

# v5 deployment selection (the override consumes VERSIOND_IMAGE)
export VERSIOND_IMAGE='<v5-versiond-image:tag-or-digest>'
export VERSIOND_ROUTER_IMAGE='<v5-haproxy-router-image:tag-or-digest>'
export VERSIOND_VERSIONS="v5"
# Optional — same as compose default on one machine
export VERSIOND_POOL_HOST=versiond-pool
```

For **all-HA routing**, do **not** put `VERSIOND_LEGACY_HOST` / `VERSIOND_NON_HA_VERSIONS` in `config.env`. Clear `NON_HA` on supervisors and router via `docker-compose.devshard-v5.override.yml` (stock overlay otherwise defaults `NON_HA` to `v1 v2 v3`).

### Already set by `docker-compose.versiond.yml`

You normally **do not edit these by hand** when using the local `devshard-postgres` service:

- `PGHOST=devshard-postgres`
- `PGDATABASE` / `PGUSER` / `PGPASSWORD` (from `DEVSHARD_POSTGRES_*`)
- `DEVSHARD_STORAGE_MODE=postgres` and `GONKA_HA=true`
- `PG_POOL_MAX_CONNS=${DEVSHARD_POSTGRES_POOL_MAX_CONNS:-4}` per child; budget connections for every version and overlapping generation, plus two dedicated health/fence connections per child
- `VERSIOND_DRAIN_ANNOUNCE=5s`, `VERSIOND_HOST_SHUTDOWN_BUDGET=25m`, `stop_grace_period: 30m`
- proxy: `VERSIOND_SERVICE_NAME=versiond-router`



### Put in compose overrides


| File                                               | Purpose                                                                                     |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `docker-compose.devshard-v5.override.yml`     | **Recommended:** v5 supervisor images + `oracle-v4` for every peer and the router catalog; `VERSIOND_NON_HA_VERSIONS=` empty |
| `docker-compose.devshard-pg-external.override.yml` | Managed DB: set `PGHOST=...` under **every** `versiond`* service (see §2.2)                 |


---



## Step 4 - Verify it works

```bash
# 1) Containers (include oracle-v4 when using the recommended override)
docker ps | grep -E 'oracle-v4|versiond|devshard-postgres'

# 2) Proxy points at router
docker inspect proxy --format '{{range .Config.Env}}{{println .}}{{end}}' | grep VERSIOND_SERVICE_NAME
# expect: VERSIOND_SERVICE_NAME=versiond-router

# 3) Router is all-HA (v5 override)
docker inspect versiond-router --format '{{range .Config.Env}}{{println .}}{{end}}' | grep VERSIOND_

# 4) Required children running and ready on every member
# Include every local replica in this list.
for replica in versiond versiond2; do
  docker exec "$replica" wget -qO- http://127.0.0.1:8080/healthz
  docker exec "$replica" wget -qO- "http://127.0.0.1:8080/readyz?version=v5"
done
docker exec versiond-router wget -qO- "http://127.0.0.1:8404/readyz?version=v5"
# Repeat with version=v4 if retained; /healthz or router /livez alone is insufficient.

# 5) Postgres mode on every HA child (after binary download)
# From versiond / devshardd logs: storage mode postgres / PG connected

# 6) Stop the sticky replica (not a random one) and confirm route failover
curl -si http://127.0.0.1:8000/devshard/v5/healthz | grep -iE 'HTTP/|X-Upstream|X-Versiond'
# map X-Upstream-Addr IP → container (docker inspect -f '{{.Name}} {{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' versiond versiond2 versiond3 …)
# stop THAT container, e.g.:
docker stop --time 1800 versiond3
curl -si http://127.0.0.1:8000/devshard/v5/healthz | grep -iE 'HTTP/|X-Upstream|X-Versiond'
# After active health checks withdraw the stopped peer, expect 200 on a survivor.
# X-Upstream-Addr is the final peer (e.g. 172.19.0.14:8080), not a retry list.
docker start versiond3
```

Healthy signs:

- `desired_versions` logs / `/healthz` show the selected approved HA versions (v5, plus v4 if retained; no v3 under HA), and `/readyz?version=...` returns 200 on each member.
- Every HA `versiond*` replica runs its approved `devshardd` artifacts with Postgres storage.
- Router sticky-routes across the HA pool (`VERSIOND_NON_HA_VERSIONS` empty).
- Stopping the **sticky** upstream moves subsequent traffic to a ready peer (killing an unused replica does not prove HA).

PostgreSQL outages make v5 children unready and fail closed. Losing a database fence terminates the affected child; `versiond` restarts it, and it must become ready again before receiving traffic.

The health URL is only a routing smoke check. Also use a real funded escrow: record an inference, serving member, committed nonce and cost, stop that member, and continue the **same session** on a survivor. Verify committed state and accounting; test retained v4 and new v5 escrows separately. HAProxy does not replay non-idempotent requests after sending them and does not retry application 503 responses. A crash can interrupt an in-flight stream; use the [test plan](devshard-host-ha-test-plan.md) for graceful-drain, crash and restart checks.

---



## Step 5 - `versiond-router` HA (in progress)


| Component                | HA status                                                                                                                                                                                   |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `versiond` / `devshardd` | **Yes today** (N replicas + shared Postgres)                                                                                                                                                |
| Postgres                 | **Your choice of Options A/B/C** (prefer managed/replicated)                                                                                                                                |
| `versiond-router`        | HAProxy supports independent routers, but this branch's join overlay still deploys one. The public HAProxy / router-fleet / updater integration in PRs [#1609](https://github.com/gonka-ai/gonka/pull/1609), [#1610](https://github.com/gonka-ai/gonka/pull/1610), [#1611](https://github.com/gonka-ai/gonka/pull/1611) is still open at the checked revision. |
| `proxy` (nginx edge)     | Usually one per public endpoint (or cloud LB in front)                                                                                                                                      |
| `decentralized-api`      | Still single-instance                                                                                                                                                                       |


---



## What not to do

1. **Multiple versionds on SQLite** — split-brain / missing leases.
2. **Different keys** on HA replicas of the same participant.
3. **Launch v3 (or other pre-HA binaries) on HA peers with shared Postgres** — use the HA oracle override, or a dedicated non-HA supervisor for legacy versions.
4. **Two dapi processes** with the same warm/cold keys — duplicates PoC / chain txs.
5. **Assume local** `devshard-postgres` **on one VM is “full HA”** — replicate the DB or use managed PG.
6. `**docker compose up` without `-f docker-compose.devshard-v5.override.yml**` when you intended v5 HA — stock images are transitional and the raw oracle can include pre-HA children.

---



## Minimal recipe (fresh installation, one host, v5)

```bash
cd deploy/join
source ./config.env

# Persist in config.env:
#   DEVSHARD_POSTGRES_PASSWORD=...
#   VERSIOND_IMAGE / VERSIOND_ROUTER_IMAGE / VERSIOND_VERSIONS=v5
#   (optional) DEVSHARD_POSTGRES_DB / USER / VERSIOND_POOL_HOST

# Create docker-compose.devshard-v5.override.yml as in §2.1

docker compose \
  -f docker-compose.yml \
  -f docker-compose.versiond.yml \
  -f docker-compose.devshard-v5.override.yml \
  up -d

docker ps | grep -E 'oracle-v4|versiond|postgres'
docker exec versiond wget -qO- http://127.0.0.1:8080/healthz
```

Then migrate the existing database to a **managed HA Postgres** when you are ready for real durability, and change `PGHOST` on every replica together during that maintenance (add §2.2 override to the same `docker compose` command). Pointing at a new empty database does not transfer existing sessions.
