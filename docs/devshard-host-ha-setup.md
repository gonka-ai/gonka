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

**For `devshard-0.2.15-v5`:** the original v4 installation examples below remain applicable to that layout. Use §2.6 for an initial v5 installation, §2.7 for upgrading an existing installation, and §2.5 for operating v5 members. The optional fleet/updater procedure in Step 5 is explicitly conditional on the open PRs being included in the selected release.

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

### 2.3 Multiple machines (recommended, true host HA)

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

**On machine B (**`versiond` **only) — one compose file is enough**

B does not run `api` / `node`. It runs a single `versiond` that uses **A’s** Postgres, oracle, node-manager, and chain endpoints over the private network.

1. **Same participant identity as A** — same `KEY_NAME`, `ACCOUNT_PUBKEY`, `KEYRING_PASSWORD`, and a copy of A’s `.inference/keyring-file/` (often root-owned; copy with `sudo`). Mount it read-only as `/root/.inference`. Do **not** start a second `api` with those keys on B.
2. **Put all connection settings in the** `versiond` **service** `environment:` (compose file). Shell `export`s in `config.env` only help if compose interpolates them into that block — the container must see the vars.
3. **Own data dir** on B (do not share A’s `./devshards*/data`). Binary cache dir may be local.
4. **Publish** `versiond` **on B’s private IP at port 8080** (recommended) so A’s router can use `VERSIOND_PORT=8080` with B’s IP in `VERSIOND_HOSTS`. Bind LAN-only, not `0.0.0.0`. Optionally firewall so only A can connect.

Example file on B: `docker-compose.versiond-remote.yml` (replace private IPs; `source ./config.env` before `docker compose up`):

```yaml
services:
  versiond:
    image: ghcr.io/product-science/versiond:0.2.15
    container_name: versiond
    environment:
      # Filtered v4 oracle on A (not raw api:9100 — that returns v3+v4)
      - VERSIOND_ORACLE_URL=http://<A-private-ip>:19100/versions
      - VERSIOND_BINARY_NAME=devshardd
      - NODE_MANAGER_ADDR=<A-private-ip>:9400
      - NODE_HOST=<A-private-ip>
      - KEY_NAME=${KEY_NAME}
      - ACCOUNT_PUBKEY=${ACCOUNT_PUBKEY}
      - KEYRING_BACKEND=${KEYRING_BACKEND:-file}
      - KEYRING_PASSWORD=${KEYRING_PASSWORD}
      - KEYRING_DIR=/root/.inference
      - PGHOST=<A-private-ip>
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
    restart: always
```

```bash
mkdir -p devshards-remote/{bin,data}
source ./config.env
docker compose -f docker-compose.versiond-remote.yml up -d
curl -sS http://<B-private-ip>:8080/healthz   # not 127.0.0.1 if bound to LAN IP only
```

**On the machine that runs** `versiond-router` **(usually A):** list every replica. `VERSIOND_PORT` stays **8080**:

```bash
export VERSIOND_HOSTS="versiond versiond2 <B-private-ip>"
# or DNS names: versiond-a.internal versiond-b.internal
export VERSIOND_PORT=8080
# v4-only: clear NON_HA via override (empty); do not leave stock "v1 v2 v3"
```

Recreate `versiond-router` after changing hosts. 

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



### 2.5 Operating `versiond` members

With the v5 HAProxy router, local members join through the shared `versiond-pool` DNS alias (`VERSIOND_POOL_HOST`), not `VERSIOND_HOSTS`. Give an additional service the same alias, participant keys, oracle and Postgres settings, and its own data directory. Start only that service; the router admits each version after fresh readiness and route-health checks. Local DNS membership changes do not require recreating the router.

For remote members (§2.3), use one private DNS name that resolves to **all** local and remote upstream addresses reachable from the router, and set `VERSIOND_POOL_HOST` to that name. Keep port `8080` and use a resolver reachable by HAProxy (`HAPROXY_DNS_RESOLVER`) if Docker DNS cannot resolve it. Adding a remote IP to `VERSIOND_HOSTS` alone does not configure the new DNS router. The optional fleet's explicit endpoint-file alternative is described in Step 5.

On **every** HA replica, including a remote service created from §2.3, set `GONKA_HA=true`, `DEVSHARD_STORAGE_MODE=postgres` and `PGHOST` in the container environment. Keep `VERSIOND_NON_HA_VERSIONS` consistent with the router (empty for the filtered HA-only pool). The v5 join overlay already sets the HA storage guard; repeat it in custom remote Compose files. Hybrid storage is not an HA option.

Use the complete Compose file list for the installation (including any external-PG override) when stopping or replacing a member:

```bash
# From deploy/join, after source ./config.env; set the full ordered file list.
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5.override.yml
docker compose stop versiond2
# Start the stopped member, or recreate only it after changing its image:
docker compose up -d --no-deps versiond2
docker exec versiond2 wget -qO- 'http://127.0.0.1:8080/readyz?version=v5'
```

Use the actual approved protocol name in the readiness URL. Verify real inference on the existing escrow before replacing the next member. Permanent removal also means deleting that service from the desired deployment; stopping it alone is temporary. Keep enough surviving capacity and retain any legacy SQLite owner.

The first stop signal makes readiness fail, allows routers to withdraw the member, then drains accepted requests and children. Keep `VERSIOND_DRAIN_ANNOUNCE=5s`, `VERSIOND_HOST_SHUTDOWN_BUDGET=25m` and Compose `stop_grace_period: ${VERSIOND_STOP_GRACE_PERIOD:-30m}` unless the workload requires different budgets; add the same stop grace to a custom remote service. With the default child termination grace of `10m`, the announce window plus child grace must remain below the host budget, and the Docker stop grace must exceed it. Versiond duration overrides now require explicit units (`30s`, `5m`). A deadline or abrupt host loss can still interrupt an active stream; do not use a short Docker stop timeout for normal maintenance.

### 2.6 Initial installation with v5

Use this section for a new HA installation. For an existing HA installation, use §2.7 **before** recreating PostgreSQL or replacing serving containers. These steps use the single-router HA overlay; Step 5 covers the optional router fleet and updater from the open PRs.

**1. Choose the release files, images and approved versions.** Use the v5 release's Compose overlay and PostgreSQL helper scripts from `deploy/join`, a `versiond` image containing the v5 lifecycle/storage changes, and a `versiond-router` image containing HAProxy and catalog reconciliation. The overlay's transitional `0.2.15` image defaults do not select these capabilities automatically. Use the release-provided tags or digests, and confirm the approved `devshardd` name, artifact URL and checksum in `/versions`; an image update alone does not approve a new protocol.

**2. Prepare Postgres and the filtered oracle as in Steps 1 and 2.1.** Keep the original v4-only override for v4 installations. For v5, copy it to `docker-compose.devshard-v5.override.yml` and make only these changes in the copy:

- Set `oracle-v4`'s `ORACLE_ALLOW` to the approved HA versions you will serve, for example `v4 v5`. The service may keep its existing name. Do not remove v4 while its sessions still need that route; keep pre-v4 versions on their separate legacy owner.
- Add `image: ${VERSIOND_IMAGE:?VERSIOND_IMAGE is required}` under **both** `versiond` and `versiond2`; keep their filtered `VERSIOND_ORACLE_URL` and dependencies.
- Under both replicas' `environment:`, add `VERSIOND_NON_HA_VERSIONS=` for this HA-only pool. The router's empty value is already in the copied override.

Add to `config.env`, using the selected release images and the same version list as the filter:

```bash
export VERSIOND_IMAGE='<release-versiond-image>'
export VERSIOND_ROUTER_IMAGE='<release-haproxy-router-image>'
export VERSIOND_VERSIONS='v4 v5'
export VERSIOND_ROUTING_CATALOG_URL=http://oracle-v4:9100/versions
export DEVSHARD_POSTGRES_DATA_DIR=./devshards/postgres
```

The catalog-capable router image and non-empty catalog URL must be supplied together. Point the router at the **same filtered oracle** as the supervisors, including on a multi-machine deployment. Preserve the overlay's `versiond-router-state` volume; it stores accepted routes for router restart during an oracle outage. Keep `VERSIOND_ROUTING_CATALOG_ALLOW_REMOVALS=false` in normal operation. The two-replica overlay requires two ready upstreams before publishing a newly learned route.

**3. Start with the new override.** Use the §2.1 command with `docker-compose.devshard-v5.override.yml` **in place of** the v4-only override; append the external-PG override from §2.2 if required. Source `config.env` first. Local PostgreSQL now keeps the cluster under `${DEVSHARD_POSTGRES_DATA_DIR}/data` (default `./devshards/postgres/data`). Keep this directory across container replacement.

A truly empty installation can initialize PostgreSQL. If downloaded artifacts already exist, startup asks the operator to distinguish first-time HA enablement from a lost database. Use `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true docker compose up -d devshard-postgres` with the full Compose file list only for confirmed first-time HA enablement with no prior PostgreSQL state; do not persist the opt-in in `config.env`. A `.pg-bound` marker with no database requires restoring the original cluster; the empty-init option does not bypass that guard.

For managed PostgreSQL, use a direct or session-preserving endpoint: v5 uses session advisory locks. Budget the application pool **plus two dedicated connections per child** (including overlapping generations), with headroom for supervisors and DB administration; the join overlay defaults `PG_POOL_MAX_CONNS` to `4` through `DEVSHARD_POSTGRES_POOL_MAX_CONNS`.

**4. Confirm per-version readiness and inference.** On each replica, check `/readyz?version=<approved-name>` and `/<approved-name>/healthz`, then the public `/devshard/<approved-name>/...` route with real inference. An unrelated healthy version or a healthy public proxy is insufficient. For the HAProxy router, use:

```bash
docker exec versiond-router wget -qO- 'http://127.0.0.1:8404/readyz?version=v5'
docker exec versiond-router /usr/local/lib/versiond-router/pool-status
```

### 2.7 Upgrade an existing installation to v5

**1. Record the current deployment before changing it.** Keep the complete Compose file list, image identifiers, participant keys, each replica's data directory, and a working escrow/inference result. Prepare the v5 image and oracle settings from §2.6 while retaining the approved versions still needed by existing sessions. Back up PostgreSQL and keep the original database/volume until inference through the upgraded public route has been verified. Keep the same database for all HA replicas.

**2. Migrate bundled v4 PostgreSQL before the application rollout.** This step applies only to the old local anonymous-volume layout, not to an unchanged external database. Record the source volume and cluster identity while the old container still exists:

```bash
docker inspect devshard-postgres --format '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{println .Name}}{{end}}{{end}}'
docker exec devshard-postgres sh -c 'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT system_identifier FROM pg_control_system()"'

./devshard-postgres-migration-preflight.sh \
  --source-container devshard-postgres \
  --target-dir "${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}"
```

Allow a maintenance window for replacing this single database. Stop **all** versiond writers using their existing Compose configuration, then stop PostgreSQL cleanly. With the new release files and full Compose list selected, recreate **only** `devshard-postgres` in place (`docker compose up -d --no-deps devshard-postgres`). Do not use `docker compose down`, `--renew-anon-volumes` / `-V`, or delete the old container/volume first: the replacement must retain the attached migration source. The wrapper copies the PostgreSQL 16 Alpine cluster into the persistent directory, using staging and a 10% free-space reserve, and leaves the source unchanged. This does not upgrade the PostgreSQL major version.

If the source is already detached, set `DEVSHARD_POSTGRES_LEGACY_VOLUME` to the recorded volume, rerun the preflight with `--source-volume "$DEVSHARD_POSTGRES_LEGACY_VOLUME"` instead of `--source-container`, and append `docker-compose.versiond-postgres-recovery.yml` to the Compose list for migration. Never initialize an empty replacement to work around a missing source. After PostgreSQL is healthy, compare its `system_identifier` and committed session data with the recorded values before restarting the writers. Once migration is verified, recreate PostgreSQL without the temporary recovery overlay. Retain the backup and old volume; after new writes, that old copy is no longer a current rollback database.

**3. Apply the oracle and application changes.** With the full new Compose list selected, apply the extended filter using `docker compose up -d --no-deps oracle-v4`. For an unchanged external database, use §2.5 to replace one member at a time with surviving capacity. After local PostgreSQL migration, all writers were stopped: first start one member with the selected image and verify its recorded session, then start the remaining members. A pre-v5 supervisor does not implement graceful evacuation; its active streams may be interrupted during the first replacement.

Once the required versions are ready on the members, apply the selected HAProxy image and catalog settings with `docker compose up -d --no-deps versiond-router`. Replacing this single router has a routing interruption. The optional fleet/updater procedure in Step 5 handles its own service order and admission checks; do not run this manual cutover first when using that updater.

For a compatible **same-name artifact replacement** with PostgreSQL overlap inside v5 `versiond`, the candidate's HTTP readiness can pass while session recovery is still running. If the candidate exposes `recovery_complete`, the supervisor keeps a healthy predecessor serving until that field is true, bounded by `VERSIOND_RECOVERY_TIMEOUT` (default `30m`); a recovery timeout rejects the candidate. A legacy candidate without that field or loss of the predecessor skips this warm wait. Set a larger timeout in each replica's container environment if measured recovery needs it. Inspect recovery failures and test the recorded sessions: completion of the recovery pass alone does not prove every session recovered. This wait does not apply to a whole-container restart, and reusing a protocol name does not make incompatible artifacts compatible.

**4. Verify both retained and new protocol routes.** Use §2.6's per-version checks and real inference, including an existing v4 session restored from its snapshot/journal. Use matching gateway/host protocol versions for new v5 sessions; do not relabel an old session as v5. Run the [installation and upgrade checks](devshard-host-ha-test-plan.md#installation-and-upgrade-checks) before discarding recovery material.

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

The commands below describe the original v4/nginx path. For v5, also use the per-version readiness checks in §2.6 and real inference on the same escrow. HAProxy reports the final selected peer in `X-Upstream-Addr`, not nginx’s dead-then-live retry list; compare that address before and after withdrawal and inspect `pool-status`. An interrupted POST or SSE is not safely replayed by the router.

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



### v5 router fleet and host updater (pending PRs)

The procedure below requires a release checkout and images containing [#1609](https://github.com/gonka-ai/gonka/pull/1609), [#1610](https://github.com/gonka-ai/gonka/pull/1610) and [#1611](https://github.com/gonka-ai/gonka/pull/1611), which were **open** when checked on 2026-09-08. It replaces the single-router deployment for that release. Do not apply these commands to the original `0.2.15` files. Edge-api routing in [#1657](https://github.com/gonka-ai/gonka/pull/1657) is separate and outside this devshard-only procedure.

**Prepare the configuration.** Use Linux with `jq`, `flock` and Docker Compose 2.24.4 or newer. Copy the §2.6 override to `docker-compose.devshard-v5-fleet.override.yml`; remove its `versiond-router` service block and any custom `proxy.depends_on.versiond-router` entry. Keep the oracle filter, both replica overrides and any external-PG settings. The new main overlay delegates inner routers to separate Compose projects managed by `versiond-router-fleet.sh`.

Persist the following in `config.env` along with the selected `VERSIOND_IMAGE` and `VERSIOND_ROUTER_IMAGE`. Append every other required override to `COMPOSE_FILE` in its original order:

```bash
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5-fleet.override.yml
export PROXY_ROUTER_IMAGE='<release-public-haproxy-image>'
export PROXY_POLICY_IMAGE='<release-nginx-policy-image>'
export VERSIOND_NON_HA_VERSIONS=''
export VERSIOND_HOSTS=''         # use DNS membership, not the old explicit list
export VERSIOND_VERSIONS='v4 v5'   # match the approved versions allowed by the filter
export VERSIOND_ROUTING_CATALOG_URL=http://oracle-v4:9100/versions
```

**Initial installation:**

```bash
source ./config.env
./versiond-router-fleet.sh prepare-networks
docker compose up -d --wait
./versiond-router-fleet.sh apply
```

**Upgrade of an existing installation:** keep the source/backup and prepare the migration checks from §2.7 first, but let the updater perform the service cutover. Do not first run the fresh-install `up` sequence on a pre-v5 deployment. For a v4-only installation, keep `ORACLE_ALLOW=v4` in the copied filter and set `VERSIOND_VERSIONS='v4'` in `config.env` for this first cutover: the fleet must admit the versions the old members already serve.

```bash
source ./config.env
./update-devshard.sh --check
./update-devshard.sh --dry-run
./update-devshard.sh
```

After that cutover succeeds, extend the copied filter to the approved `v4 v5` list and run `docker compose up -d --no-deps oracle-v4`; the updater does not recreate this custom service. The catalog then introduces v5 without requiring a change to the static v4 bootstrap floor. Wait for v5 admission and test inference before using it.

The checks leave serving containers in place, but use transient database writes for the storage challenge. The updater checks the configured writable database against running local proof-capable children, attaches legacy replicas to the fleet network, verifies public admission before removing the old router, and replaces local versiond services one at a time. It restores the previous image and stops on a failed compatible candidate; rerun after fixing the failure. Keep the previous Compose files too: restoring an image alone cannot reverse the first proxy/topology conversion. Bundled PostgreSQL migration and replacement of the single public proxy need a maintenance window. Remote replicas must be updated separately using §2.5.

**Confirm and operate the fleet:**

```bash
./versiond-router-fleet.sh status
./versiond-router-fleet.sh verify-admission
./versiond-router-fleet.sh wait-version v5   # use each required approved protocol name
```

The default fleet has three slots with a ready reserve of two. Use `./versiond-router-fleet.sh apply` for compatible router image changes with unchanged membership. The public `proxy` is now HAProxy with two nginx policy workers (`proxy-policy`, `proxy-policy2`); the original check for `VERSIOND_SERVICE_NAME=versiond-router` on `proxy` applies to the earlier layout. Verify real public inference after admission. Several inner routers do not make a single public ingress machine redundant.

For an explicit multi-host pool, set `VERSIOND_POOL_ENDPOINTS_FILE=./versiond-endpoints.json` in `config.env`. The file is a non-empty JSON array, for example `[{"id":"local-a","host":"versiond","port":8080},{"id":"remote-b","host":"10.0.0.12","port":8080}]`; include **every** intended member with a unique ID and an address reachable from the routers. Editing this file alone does not change running membership. Changes to an existing explicit list (including `VERSIOND_HOSTS` in this fleet's compatibility mode) require the maintenance command below, not ordinary `apply`:

```bash
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true ./versiond-router-fleet.sh maintenance-rollout
```

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