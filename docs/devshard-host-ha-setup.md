# High-availability Devshard Host Setup

**Audience:** hosts that serve inference over **devshard** (`/devshard/...` → `versiond` → `devshardd`).  
**Status:** draft for host operators - edit before wider distribution.  
**Goal:** run a **high-available (HA)** host stack so a single `versiond` / `devshardd` failure does not take the host offline.

---

## Why this matters

A single `versiond` process is a single point of failure (SPOF): if that machine or container dies, gateways cannot reach your host for that protocol version.

We now support an **HA host layout**:

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

1. Join stack from a release that includes **devshard v4 HA** (for example `0.2.15` images).
2. Working `node` + `api` (dapi) + `proxy` on the host (standard join deployment).
3. Same participant identity on **every** HA `versiond` replica:
  - same `KEY_NAME` / keyring
  - same `ACCOUNT_PUBKEY`
4. Only put **Postgres-capable versions** (v4+) into the HA pool. Keep older versions (`v1` / `v2` / `v3`) pinned to a **legacy** single host if you still serve them.

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
3. Start with **three** `-f` files: base + `versiond` overlay + your external-PG override.

> Do not run two `versiond` instances on SQLite.  
> Keep `DEVSHARD_STORAGE_MODE=postgres` on HA `versiond` containers — when `versiond-router` sends `Devshard-Ha: true`, children without Postgres mode reject HA traffic.

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
3. `source ./config.env`, then start with `-f docker-compose.versiond.yml` (see Step 2.1).

---

## Step 2 - Run multiple `versiond` instances + `versiond-router`

Example files in your setup. Some are present in the release branch:


| File                                                           | Role                                                                                                  |
| -------------------------------------------------------------- | ----------------------------------------------------------------------------------------------------- |
| `deploy/join/docker-compose.yml`                               | Base join (`versiond`, `proxy`, …)                                                                    |
| `deploy/join/docker-compose.versiond.yml`                      | HA overlay: Postgres + second replica (`versiond2`) + `versiond-router`, proxy → router               |
| `deploy/join/docker-compose.devshard-v4-only.override.yml`     | **Recommended:** v4-only oracle filter + clear `VERSIOND_NON_HA_VERSIONS` (you create this; see §2.1) |
| `deploy/join/docker-compose.devshard-pg-external.override.yml` | Optional: point **every** `versiond`* replica at managed Postgres (see §2.2)                          |


> **Why the v4-only override?** Stock `api:9100/versions` still lists **v3 and v4**. Without a filter, every HA peer starts a **v3** child against shared Postgres, which is unsupported and can race on schema migration. Clearing `VERSIOND_NON_HA_VERSIONS` only changes **routing**; it does **not** stop versiond from launching v3. The override filters the oracle to **v4** and empties the non-HA pin list.

### 2.1 Same machine, two instances (v4-only HA)

On the join host:

**1. Credentials in** `config.env`

```bash
cd /path/to/gonka/deploy/join

# load existing join secrets (KEY_NAME, KEYRING_PASSWORD, …)
source ./config.env
```

Add to `config.env` (password is required; DB/user/`VERSIOND_HOSTS` have compose defaults but setting them explicitly is fine):

```bash
export DEVSHARD_POSTGRES_DB=devshardd
export DEVSHARD_POSTGRES_USER=devshardd
export DEVSHARD_POSTGRES_PASSWORD='<strong-password>'

export VERSIOND_HOSTS="versiond versiond2"
# Do not set VERSIOND_LEGACY_HOST or VERSIOND_NON_HA_VERSIONS for v4-only HA —
# the override below clears NON_HA. Stock overlay defaults NON_HA to "v1 v2 v3".
```

**2. Create** `docker-compose.devshard-v4-only.override.yml`

```bash
cat > docker-compose.devshard-v4-only.override.yml <<'EOF'
services:
  # Filtered /versions: only v4 (prevents v3 children on the HA+Postgres pool)
  oracle-v4:
    container_name: oracle-v4
    image: python:3.12-alpine
    environment:
      - ORACLE_UPSTREAM=http://api:9100/versions
      - ORACLE_ALLOW=v4
      - LISTEN_PORT=9100
    command:
      - python
      - -c
      - |
        import json, os, urllib.request
        from http.server import BaseHTTPRequestHandler, HTTPServer
        UP = os.environ["ORACLE_UPSTREAM"]
        ALLOW = set(x.strip() for x in os.environ.get("ORACLE_ALLOW", "v4").replace(",", " ").split() if x.strip())
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
    environment:
      - VERSIOND_ORACLE_URL=http://oracle-v4:9100/versions
    depends_on:
      oracle-v4:
        condition: service_started
      devshard-postgres:
        condition: service_healthy

  versiond2:
    environment:
      - VERSIOND_ORACLE_URL=http://oracle-v4:9100/versions
    depends_on:
      oracle-v4:
        condition: service_started
      devshard-postgres:
        condition: service_healthy

  # If you add versiond3 (or more), give each the same oracle-v4 env + depends_on.

  versiond-router:
    environment:
      - VERSIOND_NON_HA_VERSIONS=
      - VERSIOND_HOSTS=versiond versiond2
EOF
```

**3. Bring up HA with all three compose files**

```bash
source ./config.env

docker compose \
  -f docker-compose.yml \
  -f docker-compose.versiond.yml \
  -f docker-compose.devshard-v4-only.override.yml \
  up -d
```

What this starts:


| Container           | Purpose                                       |
| ------------------- | --------------------------------------------- |
| `devshard-postgres` | Shared DB (if not using external PG)          |
| `oracle-v4`         | Filtered versions oracle (v4 only)            |
| `versiond`          | Replica A — data dir `./devshards/data`       |
| `versiond2`         | Replica B — data dir `./devshards2/data`      |
| `versiond-router`   | Sticky nginx in front of the HA pool          |
| `proxy`             | Public edge; `/devshard/` → `versiond-router` |


**4. Confirm**

```bash
docker ps --format '{{.Names}}\t{{.Status}}' | grep -E 'oracle-v4|versiond|devshard-postgres'

docker inspect proxy --format '{{range .Config.Env}}{{println .}}{{end}}' | grep VERSIOND_SERVICE_NAME
# VERSIOND_SERVICE_NAME=versiond-router

docker inspect versiond-router --format '{{range .Config.Env}}{{println .}}{{end}}' | grep VERSIOND_
# VERSIOND_HOSTS=versiond versiond2
# VERSIOND_NON_HA_VERSIONS=   (empty)

docker exec versiond wget -qO- http://127.0.0.1:8080/healthz
# expect only v4 running (no v3)
```

#### Optional: still serving pre-v4 (v3) on the same host

Only if you must keep SQLite versions. Prefer a **dedicated non-HA** supervisor for those, not both HA peers on Postgres. Stock overlay defaults:

```bash
export VERSIOND_LEGACY_HOST=versiond
export VERSIOND_NON_HA_VERSIONS="v1 v2 v3"
```

Do **not** use the v4-only override in that case; use a split layout instead of launching v3 under `DEVSHARD_STORAGE_MODE=postgres` on HA replicas.

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
      - PGPASSWORD=${DEVSHARD_POSTGRES_PASSWORD}
      - DEVSHARD_STORAGE_MODE=postgres
    # Optional: stop waiting on local DB if you will not run it
    # depends_on: !reset []

  versiond2:
    environment:
      - PGHOST=your-managed-pg.example.com
      - PGPORT=5432
      - PGDATABASE=${DEVSHARD_POSTGRES_DB:-devshardd}
      - PGUSER=${DEVSHARD_POSTGRES_USER:-devshardd}
      - PGPASSWORD=${DEVSHARD_POSTGRES_PASSWORD}
      - DEVSHARD_STORAGE_MODE=postgres
```

1. Start (include the v4-only override as well if you use the recommended §2.1 layout):

```bash
cd deploy/join
source ./config.env

docker compose \
  -f docker-compose.yml \
  -f docker-compose.versiond.yml \
  -f docker-compose.devshard-v4-only.override.yml \
  -f docker-compose.devshard-pg-external.override.yml \
  up -d
```

If you fully disable the local `devshard-postgres` service, also remove or override its `depends_on` entries on **every** `versiond`* service so compose does not wait on a container you never start.

### 2.3 Multiple machines (true host HA)

Conceptually the same layout, but each machine runs one `versiond`, and one place runs `versiond-router` (or you place the router behind a future load balancer). Prefer a **private network** between machines; bind new listeners to private IPs only if you cannot open extra public ports.

Minimum for two machines:


| Role      | Runs                                                            |
| --------- | --------------------------------------------------------------- |
| Machine A | `versiond` (+ usual node/api/proxy) + `versiond-router`         |
| Machine B | `versiond` only — **no** second dapi with the same keys         |
| Shared    | Postgres reachable from every `versiond` (managed HA preferred) |


> **Important:** `decentralized-api` (dapi) is still **single-instance** today. HA here is for **devshard traffic** (`versiond` / `devshardd`), not for running two dapis with one key.

**On machine A (dapi / Postgres / router side) — publish for B on the private network:**

1. Postgres (`5432`) and node-manager gRPC (`9400`) — required.
2. Chain RPC/gRPC (`26657`, `9090`) — required for remote `devshardd` (same as local `NODE_HOST=node`).
3. Oracle URL — use the **same filtered oracle** as local HA (e.g. `oracle-v4` on a private port). Pointing remote `versiond` at raw `api:9100/versions` will pull **v3+v4** and break the v4-only HA path.
4. Confirm `PGPASSWORD` / `KEYRING_PASSWORD` in `config.env` match what **running** local `versiond`* containers use.

**On every machine that runs** `versiond`**:**

1. Same join identity (`KEY_NAME`, keyring files, `ACCOUNT_PUBKEY`, `KEYRING_PASSWORD`). Copy `keyring-file` with sufficient permissions.
2. Reachable `NODE_MANAGER_ADDR` (A’s private `:9400`). Do **not** start a second `api` with those keys on B.
3. Wire Postgres in the **service environment**: `PGHOST=<A-private-ip>`, `DEVSHARD_STORAGE_MODE=postgres`, same DB user/password as A.
4. Point at shared API/node/oracle, for example:
  - `VERSIOND_ORACLE_URL=http://<A-private>:19100/versions` (filtered v4 oracle — not raw `:9100`)
  - `NODE_MANAGER_ADDR=<A-private>:9400`
  - `NODE_HOST=<A-private>` (RPC/gRPC published on A)
5. **Separate** local data dirs per instance. Binary caches may be shared or per-node.
6. Publish `versiond` on the **private IP at port 8080** (recommended):

```yaml
ports:
  - "<B-private-ip>:8080:8080"   # LAN only — not 0.0.0.0
```

Optionally firewall that port so only machine A can connect. No relay container on A.

**On the machine that runs** `versiond-router`**:** list every replica (local Docker DNS names and/or B’s private IP or DNS name). `VERSIOND_PORT` is shared and should stay **8080**:

```bash
export VERSIOND_HOSTS="versiond versiond2 172.18.114.132"   # example: local + B LAN IP
# or: VERSIOND_HOSTS="versiond-a.internal versiond-b.internal"
export VERSIOND_PORT=8080
# v4-only: clear NON_HA via override (empty); do not leave stock "v1 v2 v3"
```

Recreate `versiond-router` after changing hosts. 

**Fallback only:** if B cannot bind host `:8080`, publish another private port and put a TCP relay on A’s Docker network that listens `:8080` and forwards to B (e.g. socat). Add the relay’s Docker DNS name to `VERSIOND_HOSTS`. 

**On the public** `proxy`**:** confirm after recreate:

```text
VERSIOND_SERVICE_NAME=versiond-router
VERSIOND_PORT=8080
```

**Verify cross-machine HA** the same way as Step 4 §6: find a sticky session whose `X-Upstream-Addr` is the remote replica, stop **that** machine’s `versiond`, confirm the same URL still returns **200** with failover in `X-Upstream-Addr`.

### 2.4 Adding a third replica

1. Add another `versiond` service (new container name + new data volume).
2. Append it to `VERSIOND_HOSTS`, for example `versiond versiond2 versiond3`.
3. Recreate `versiond-router` (and proxy if needed).

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

# Optional — same as compose default
export VERSIOND_HOSTS="versiond versiond2"
```

For **v4-only HA**, do **not** put `VERSIOND_LEGACY_HOST` / `VERSIOND_NON_HA_VERSIONS` in `config.env`. Clear `NON_HA` via `docker-compose.devshard-v4-only.override.yml` (stock overlay otherwise defaults `NON_HA` to `v1 v2 v3`).

### Already set by `docker-compose.versiond.yml`

You normally **do not edit these by hand** when using the local `devshard-postgres` service:

- `PGHOST=devshard-postgres`
- `PGDATABASE` / `PGUSER` / `PGPASSWORD` (from `DEVSHARD_POSTGRES_*`)
- `DEVSHARD_STORAGE_MODE=postgres`
- proxy: `VERSIOND_SERVICE_NAME=versiond-router`

### Put in compose overrides


| File                                               | Purpose                                                                                     |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `docker-compose.devshard-v4-only.override.yml`     | **Recommended:** `oracle-v4` + every `versiond`* uses it; `VERSIOND_NON_HA_VERSIONS=` empty |
| `docker-compose.devshard-pg-external.override.yml` | Managed DB: set `PGHOST=...` under **every** `versiond`* service (see §2.2)                 |


---

## Step 4 - Verify it works

```bash
# 1) Containers (include oracle-v4 when using the recommended override)
docker ps | grep -E 'oracle-v4|versiond|devshard-postgres'

# 2) Proxy points at router
docker inspect proxy --format '{{range .Config.Env}}{{println .}}{{end}}' | grep VERSIOND_SERVICE_NAME
# expect: VERSIOND_SERVICE_NAME=versiond-router

# 3) Router is all-HA (v4-only path)
docker inspect versiond-router --format '{{range .Config.Env}}{{println .}}{{end}}' | grep VERSIOND_

# 4) Only v4 child running
docker exec versiond wget -qO- http://127.0.0.1:8080/healthz

# 5) Postgres mode on v4 (after binary download)
# From versiond / devshardd logs: storage mode postgres / PG connected

# 6) Kill the sticky replica (not a random one) and confirm failover
curl -si http://127.0.0.1:8000/devshard/v4/healthz | grep -iE 'HTTP/|X-Upstream|X-Versiond'
# map X-Upstream-Addr IP → container (docker inspect -f '{{.Name}} {{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' versiond versiond2 versiond3 …)
# stop THAT container, e.g.:
docker stop versiond3
curl -si http://127.0.0.1:8000/devshard/v4/healthz | grep -iE 'HTTP/|X-Upstream|X-Versiond'
# expect: 200; X-Upstream-Addr lists dead peer then live (e.g. 172.19.0.12:8080, 172.19.0.14:8080)
docker start versiond3
```

Healthy signs:

- `desired_versions` / `healthz` show **v4 only** (no v3 under HA).
- Every HA `versiond*` replica runs `devshardd` for v4 with Postgres storage.
- Router sticky-routes across the HA pool (`VERSIOND_NON_HA_VERSIONS` empty).
- Stopping the **sticky** upstream still returns **200** with `X-Upstream-Addr` showing `proxy_next_upstream` to another peer (killing an unused replica does not prove HA).

---

## Step 5 - `versiond-router` HA (in progress)


| Component                | HA status                                                                                                                                                                                   |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `versiond` / `devshardd` | **Yes today** (N replicas + shared Postgres)                                                                                                                                                |
| Postgres                 | **Your choice of Options A/B/C** (prefer managed/replicated)                                                                                                                                |
| `versiond-router`        | **Coming next** - multi-router support is coming soon; when available you will be able to run **several** `versiond-router` **instances** (typically behind the edge proxy / load balancer) |
| `proxy` (nginx edge)     | Usually one per public endpoint (or cloud LB in front)                                                                                                                                      |
| `decentralized-api`      | Still single-instance                                                                                                                                                                       |


---

## What not to do

1. **Two versionds on SQLite** — split-brain / missing leases.
2. **Different keys** on HA replicas of the same participant.
3. **Launch v3 (or other pre-HA binaries) on HA peers with shared Postgres** — use the v4-only oracle override, or a dedicated non-HA supervisor for legacy versions.
4. **Two dapi processes** with the same warm/cold keys — duplicates PoC / chain txs.
5. **Assume local** `devshard-postgres` **on one VM is “full HA”** — replicate the DB or use managed PG.
6. `**docker compose up` without `-f docker-compose.devshard-v4-only.override.yml**` when you intended v4-only — stock oracle still starts v3.

---

## Minimal recipe (one host, two versionds, v4-only)

```bash
cd deploy/join
source ./config.env

# Persist in config.env:
#   DEVSHARD_POSTGRES_PASSWORD=...
#   (optional) DEVSHARD_POSTGRES_DB / USER / VERSIOND_HOSTS

# Create docker-compose.devshard-v4-only.override.yml as in §2.1

docker compose \
  -f docker-compose.yml \
  -f docker-compose.versiond.yml \
  -f docker-compose.devshard-v4-only.override.yml \
  up -d

docker ps | grep -E 'oracle-v4|versiond|postgres'
docker exec versiond wget -qO- http://127.0.0.1:8080/healthz
```

Then move `PGHOST` to a **managed HA Postgres** when you are ready for real durability (add §2.2 override to the same `docker compose` command).