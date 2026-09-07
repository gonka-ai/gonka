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
 versiond-router fleet (HAProxy)   ← sticky routing by session/escrow ID
        │
        ├── versiond  (instance A) ──► devshardd children
        └── versiond2 (instance B) ──► devshardd children
                 │
                 └── shared Postgres  (required)
```

**You must use Postgres** for HA. SQLite is single-writer and **must not** be shared across instances.

---



## Prerequisites

1. The `devshard-0.2.15-v5` release checkout, matching container images and approved devshardd artifacts; Docker Compose **2.24.4+** and `jq`.
2. Working `node` + `api` (dapi) + `proxy` on the host (standard join deployment).
3. Same participant identity on **every** HA `versiond` replica:
  - same `KEY_NAME` / keyring
  - same `ACCOUNT_PUBKEY`
4. Only put **Postgres-capable versions** (v4+) into the HA pool. Keep older versions (`v1` / `v2` / `v3`) pinned to a **legacy** single host if you still serve them.

---



These steps describe the **HA overlay**. A single-versiond installation remains
supported without the inner router fleet. Existing edge-api nginx routing is unchanged.
Use the release image references in `config.env` (`VERSIOND_IMAGE`,
`VERSIOND_ROUTER_IMAGE`, `PROXY_ROUTER_IMAGE`, `PROXY_POLICY_IMAGE`); use the
actual approved protocol name wherever the examples say `v5`.
For an existing installation, follow the [release update instructions][v5-release]
before using the day-to-day operations in section 2.5.

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
3. Use base + `versiond` overlay + the shipped external-PG overlay + your connection override (Step 2.2).

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
| `deploy/join/docker-compose.versiond.yml`                      | HA overlay: Postgres + second replica (`versiond2`) + networks for the independent router fleet               |
| `deploy/join/versiond-router-slot/docker-compose.yml` | Router slot, managed separately by `versiond-router-fleet.sh` (§2.1) |
| `deploy/join/docker-compose.devshard-pg-external.override.yml` | Optional: point **every** `versiond`* replica at managed Postgres (see §2.2)                          |


Use the release's catalog defaults so supervisors can download the approved artifacts.
`VERSIOND_VERSIONS` sets bootstrap routes, not a filter on downloaded binaries.
Keep legacy names pinned to their real owner; clearing the non-HA list does not
make old binaries PostgreSQL-capable.



### 2.1 Same machine, two instances

On the join host, for a **fresh HA installation**:

**1. Credentials in** `config.env`

```bash
cd /path/to/gonka/deploy/join

# load existing join secrets (KEY_NAME, KEYRING_PASSWORD, …)
source ./config.env
```

Add to `config.env` (password is required; DB/user have compose defaults):

```bash
export DEVSHARD_POSTGRES_DB=devshardd
export DEVSHARD_POSTGRES_USER=devshardd
export DEVSHARD_POSTGRES_PASSWORD='<strong-password>'

export VERSIOND_POOL_HOST=versiond-pool
export VERSIOND_VERSIONS=v5  # actual approved protocol name
# Only for a catalog containing exclusively HA-capable versions:
export VERSIOND_NON_HA_VERSIONS=''
# If serving legacy versions, keep their real pins/owner instead (below).
```

**2. Select the Compose model and discovery**

Persist the project name and complete ordered file list in `config.env`:
`export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml`.
Append your site overrides; for managed PG use the full list from section 2.2.
Leave `VERSIOND_HOSTS` and `VERSIOND_POOL_ENDPOINTS_FILE` unset for local DNS
discovery. The default fleet has three routers and needs two ready routers
during rollout; newly learned HA versions need two ready versiond members.

**3. Bring up the main stack and router fleet**

```bash
source ./config.env
docker compose config --quiet
./versiond-router-fleet.sh prepare-networks
docker compose up -d --wait --wait-timeout 2100
./versiond-router-fleet.sh apply
```

Router slots are separate Compose projects; do not add the slot Compose file
to the main project's file list.

What this starts:


| Container           | Purpose                                       |
| ------------------- | --------------------------------------------- |
| `devshard-postgres` | Shared DB (if not using external PG)          |
| `versiond`          | Replica A — data dir `./devshards/data`       |
| `versiond2`         | Replica B — data dir `./devshards2/data`      |
| Router fleet | Independent HAProxy slots in front of the HA pool |
| `proxy` | Public HAProxy; nginx policy workers retain TLS and HTTP policy |


**4. Confirm**

```bash
docker compose ps
./versiond-router-fleet.sh status
./versiond-router-fleet.sh wait-version v5  # actual approved name
```

Then run the smoke-check in Step 4.

#### Optional: still serving pre-v4 (v3) on the same host

Only if you must keep SQLite versions. Prefer a **dedicated non-HA** supervisor for those, not both HA peers on Postgres. Stock overlay defaults:

```bash
export VERSIOND_LEGACY_HOST=versiond
export VERSIOND_NON_HA_VERSIONS="v1 v2 v3"
```

Keep those versions on their actual legacy owner with its existing data; do not
launch unsupported legacy binaries under `DEVSHARD_STORAGE_MODE=postgres` on HA peers.
Retain the same pins in both routing tiers.



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

  versiond2:
    environment:
      - PGHOST=your-managed-pg.example.com
      - PGPORT=5432
      - PGDATABASE=${DEVSHARD_POSTGRES_DB:-devshardd}
      - PGUSER=${DEVSHARD_POSTGRES_USER:-devshardd}
      - PGPASSWORD=${DEVSHARD_POSTGRES_PASSWORD}
      - DEVSHARD_STORAGE_MODE=postgres
```

3. Select the full model. The shipped external-PG overlay disables normal bundled-PG startup and makes its dependencies optional:

```bash
cd deploy/join
source ./config.env

export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.versiond-external-postgres.yml:docker-compose.devshard-pg-external.override.yml
docker compose config --quiet
```

Persist that complete list in `config.env`, including other site overrides.
Continue with section 2.1's startup commands. Repeat the dependency overrides
for any extra local replica; configure your provider's PostgreSQL TLS settings
and certificate mounts on every member.

### 2.3 Multiple machines (application-host HA)

Conceptually the same layout, but machines run one or more `versiond` members, and the network node runs the router fleet. Ingress, dapi, chain and PostgreSQL remain separate availability boundaries. Use a **private network** between machines; bind new listeners to private IPs and allow only the required peers.

Minimum for two machines:


| Role      | Runs                                                            |
| --------- | --------------------------------------------------------------- |
| Machine A | `versiond` (+ usual node/api/proxy) + versiond-router fleet |
| Machine B | `versiond` only — **no** second dapi with the same keys         |
| Shared    | Postgres reachable from every `versiond` (managed HA preferred) |


> **Important:** `decentralized-api` (dapi) is still **single-instance** today. HA here is for **devshard traffic** (`versiond` / `devshardd`), not for running two dapis with one key.

**On machine A (dapi / Postgres / router side) — publish for B on the private network:**

1. Postgres (`5432`) and node-manager gRPC (`9400`) — required.
2. Chain RPC/gRPC (`26657`, `9090`) — required for remote `devshardd` (same as local `NODE_HOST=node`).
3. Oracle URL — use A's `/versions` on port `9100`, with the same approved artifacts and legacy pins as the local members.
4. Confirm `PGPASSWORD` / `KEYRING_PASSWORD` in `config.env` match what **running** local `versiond`* containers use.

Use the shipped `docker-compose.private-endpoints.yml` in A's complete Compose
file list and set `GONKA_PRIVATE_BIND_IP` to A's private IP. Port 9100 is already
published by the base model; firewall it too. For an existing installation,
adding port bindings may recreate services: schedule that change separately.

**On machine B (**`versiond` **only) — one compose file is enough**

B does not run `api` / `node`. It runs a single `versiond` that uses **A’s** Postgres, oracle, node-manager, and chain endpoints over the private network.

1. **Same participant identity as A** — same `KEY_NAME`, `ACCOUNT_PUBKEY`, `KEYRING_PASSWORD`, and a copy of A’s `.inference/keyring-file/` (often root-owned; copy with `sudo`). Mount it read-only as `/root/.inference`. Do **not** start a second `api` with those keys on B.
2. **Put all connection settings in the** `versiond` **service** `environment:` (compose file). Shell `export`s in `config.env` only help if compose interpolates them into that block — the container must see the vars.
3. **Own data dir** on B (do not share A’s `./devshards*/data`). Binary cache dir may be local.
4. **Publish** `versiond` **on B's private IP and chosen port** so every router can reach it. Bind LAN-only, not `0.0.0.0`, and firewall access to the router peers.

Use the shipped `docker-compose.versiond-remote.yml` from the same release.
It contains the environment, keyring/data mounts, healthcheck and shutdown
grace. Set in B's own `config.env`, alongside the identity, release image and
database credentials above:

```bash
export NETWORK_NODE_PRIVATE_IP=10.20.0.11
export VERSIOND_BIND_IP=10.20.0.12
export VERSIOND_PUBLISHED_PORT=8080
```

For managed PG, also set `DEVSHARD_POSTGRES_HOST` and
`DEVSHARD_POSTGRES_PORT`, and append a remote TLS override if needed.
Otherwise PG defaults to A. Preserve the same legacy pins as A.
Do not copy A's Compose file list or local bindings to B.

```bash
source ./config.env
docker compose -f docker-compose.versiond-remote.yml config --quiet
docker compose -f docker-compose.versiond-remote.yml up -d --wait --wait-timeout 2100
```

**On machine A:** create `versiond-endpoints-conf.json` with the complete pool:

```json
[
  { "id": "versiond-a", "host": "versiond", "port": 8080 },
  { "id": "versiond-a2", "host": "versiond2", "port": 8080 },
  { "id": "versiond-b", "host": "10.20.0.12", "port": 8080 }
]
```

Persist in A's `config.env`:

```bash
export VERSIOND_POOL_ENDPOINTS_FILE=./versiond-endpoints-conf.json
```

The path is relative to `config.env`. Use unique stable IDs and addresses
reachable from every router. The file replaces DNS membership, not appends to
it. For one versiond on each machine, set `VERSIOND2_REPLICAS=0` on A and omit
`versiond-a2`. For several members on B, give each a separate service, local
data directory, published port and endpoint entry.

For a new fleet, run `./versiond-router-fleet.sh apply`. For an existing fleet,
switching to an endpoint file or changing its membership requires the
maintenance operation in Step 5; editing the file alone does not update routers.
Verify the remote member with Step 4. Operate remote containers on their own
machines; the local updater does not deploy them.

### 2.4 Adding a third replica

1. Add the shipped `docker-compose.versiond3.yml` to the complete Compose file list in `config.env`. It gives the replica its own name and data directory.
2. In DNS mode it joins `versiond-pool` automatically. With an endpoint file, add its entry to the complete list and apply Step 5's membership maintenance.
3. Start only the new member and verify per-version admission before using it:

```bash
source ./config.env
docker compose up -d --no-deps --wait --wait-timeout 2100 versiond3
./versiond-router-fleet.sh status
./versiond-router-fleet.sh wait-version v5  # actual approved name
```

For further members, adapt the third-replica file with distinct service names,
replica settings and data paths. Remote members also need distinct ports.

### 2.5 Operating versiond members

Use these operations after installation, one member at a time. First check
Step 4 and confirm that survivors can serve every affected HA version and the
load. Docker does not prevent stopping the last member. Do not remove a
legacy SQLite owner while its versions are still needed.

**Temporarily stop a member without interrupting accepted inference**

On the member's machine, load its complete Compose model and stop just that
service (example: local `versiond2`):

```bash
source ./config.env
docker compose stop versiond2
docker compose ps -a versiond2
```

Versiond announces unready before closing admission; routers withdraw it while
accepted requests finish. Wait for the command to complete and check the logs.
Keep the supplied `stop_grace_period` (default 30 minutes, longer than the
25-minute host budget). Do not use `docker kill`, `rm -f` or a shortened stop
timeout for graceful work. Requests exceeding the drain budget can still be
terminated. A failed whole machine cannot preserve its active streams.

**Replace or restart the stopped member**

Keep its intended endpoint, data and PostgreSQL settings. For an image update,
set the intended `VERSIOND_IMAGE` in its configuration, then:

```bash
source ./config.env
docker compose up -d --no-deps --wait --wait-timeout 2100 versiond2
```

Observe withdrawal of the old member and fresh readiness/admission of the new
one using Step 4. Confirm real inference before stopping another member.
An endpoint change is also a membership change: use Step 5's maintenance
procedure. To move a member to another machine, prepare the replacement as in
section 2.3; preserve shared storage and identity, drain the old member, then
apply the new endpoint list in that maintenance window.

**Permanently remove a member**

Persist `VERSIOND2_REPLICAS=0` for local `versiond2` in `config.env`, then
gracefully stop it as above. Only after it has stopped:

```bash
docker compose rm -f versiond2
```

Use the corresponding setting for another numbered member. For a remote member,
remove the stopped service from that machine's desired Compose configuration.
Remove its endpoint entry, if present, and apply Step 5's membership maintenance.
A temporary stop alone does not decommission a member. Retain data for recovery.

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
export VERSIOND_POOL_HOST=versiond-pool
```

For an all-HA catalog, set `VERSIOND_NON_HA_VERSIONS=''`; otherwise retain the
actual legacy pins and `VERSIOND_LEGACY_HOST`. For local DNS discovery leave
`VERSIOND_HOSTS` and `VERSIOND_POOL_ENDPOINTS_FILE` unset. For multi-host use
section 2.3's endpoint file. Preserve the project name and complete `COMPOSE_FILE`.

### Already set by `docker-compose.versiond.yml`

You normally **do not edit these by hand** when using the local `devshard-postgres` service:

- `PGHOST=devshard-postgres`
- `PGDATABASE` / `PGUSER` / `PGPASSWORD` (from `DEVSHARD_POSTGRES_*`)
- `DEVSHARD_STORAGE_MODE=postgres`
- `GONKA_HA=true` for HA supervisors by default
- public proxy: `VERSIOND_ROUTER_POOL_HOST=versiond-router-fleet`



### Put in compose overrides


| File                                               | Purpose                                                                                     |
| -------------------------------------------------- | ------------------------------------------------------------------------------------------- |
| `docker-compose.versiond-external-postgres.yml` | Shipped overlay for external PG; combine with your connection override below |
| `docker-compose.devshard-pg-external.override.yml` | Managed DB: set `PGHOST=...` under **every** `versiond`* service (see §2.2)                 |


---



## Step 4 - Verify it works

Run with the selected complete Compose model. Use the actual approved version
and repeat the member check for every local/remote replica:

```bash
source ./config.env
export QA_VERSION=v5
version_uri=$(jq -nr --arg v "$QA_VERSION" '$v | @uri')

docker compose ps
./versiond-router-fleet.sh status
./versiond-router-fleet.sh wait-version "$QA_VERSION"

docker compose exec -T versiond /bin/busybox wget -qO- -T 5 \
  "http://127.0.0.1:8080/readyz?version=$version_uri"

docker compose exec -T proxy /bin/busybox wget -qO- -T 5 \
  "http://127.0.0.1:8404/readyz?version=$version_uri"
```

For a remote member run the member check on its machine using its remote Compose
file. Fleet status describes routers, not admission of each versiond. Inspect
each active router's measured pool before operating on the next member:

```bash
docker ps --filter label=ai.gonka.component=versiond-router \
  --format '{{.ID}} {{.Names}} {{.Label "ai.gonka.fleet"}}'
# Set ROUTER_CONTAINER to a running router ID from the intended fleet above.
timeout 5s docker exec "${ROUTER_CONTAINER:?set a router container ID}" \
  /usr/local/lib/versiond-router/pool-status
```

Match the member's resolved IP to the requested version's backend: it must be
`UP` after joining and cease taking traffic during withdrawal. Repeat for all
active routers; aggregate readiness alone does not prove a member joined.
Keep admin ports private.

Healthy signs:

- The intended protocol children are running with the approved artifacts.
- Every HA member uses the intended shared PostgreSQL; legacy versions remain on their owner.
- The requested version is ready through the fleet and public proxy.
- A real inference through the normal public endpoint succeeds.

For fault injection and session-continuity checks, use the separate
[HA test plan](devshard-host-ha-test-plan.md).

---

## Step 5 - `versiond-router` HA


| Component                | HA status                                                                                                                                                                                   |
| ------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `versiond` / `devshardd` | **Yes today** (N replicas + shared Postgres)                                                                                                                                                |
| Postgres                 | **Your choice of Options A/B/C** (prefer managed/replicated)                                                                                                                                |
| `versiond-router` | Three independent HAProxy slots by default, each with its own Compose project and persistent catalog state |
| `proxy` | Public HAProxy with two nginx policy workers; public HAProxy remains a singleton, so its restart interrupts connections |
| `decentralized-api`      | Still single-instance                                                                                                                                                                       |


---



Operate the fleet from the network node with the same configuration:

```bash
./versiond-router-fleet.sh status
# After selecting a compatible router image; membership/placement unchanged:
./versiond-router-fleet.sh apply
./versiond-router-fleet.sh wait-version v5  # actual approved name
```

Slots roll one at a time. Verify admission and actual inference after the update.
For a changed endpoint list or placement, schedule a maintenance window:

```bash
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true ./versiond-router-fleet.sh maintenance-rollout
./versiond-router-fleet.sh status
./versiond-router-fleet.sh wait-version v5
```

This operation drains the old router generation before admitting the new list;
new inference can be unavailable during the window. It is different from stopping
one versiond at an unchanged endpoint. After interruption, correct the cause and
rerun the intended operation; keep retained generations and data for recovery.
Multiple versiond hosts do not replicate ingress, dapi, chain or PostgreSQL.

## What not to do

1. **Two versionds on SQLite** — split-brain / missing leases.
2. **Different keys** on HA replicas of the same participant.
3. **Launch v3 (or other pre-HA binaries) on HA peers with shared Postgres** — keep a dedicated non-HA owner with its existing data.
4. **Two dapi processes** with the same warm/cold keys — duplicates PoC / chain txs.
5. **Assume local** `devshard-postgres` **on one VM is “full HA”** — replicate the DB or use managed PG.
6. **Update a serving HA installation with whole-project `docker compose up`** — follow the [release updater instructions][v5-release]; section 2.5 covers individual member operations.

---



## Minimal recipe (fresh installation, one host, two versionds)

```bash
cd deploy/join
source ./config.env

# Persist credentials, release images, approved protocol names and full file list (section 2.1).
docker compose config --quiet
./versiond-router-fleet.sh prepare-networks
docker compose up -d --wait --wait-timeout 2100
./versiond-router-fleet.sh apply
./versiond-router-fleet.sh status
./versiond-router-fleet.sh wait-version v5  # actual approved name
```

For managed HA Postgres, use section 2.2. Changing `PGHOST` does not migrate
existing data; move that data separately before switching the connection.

[v5-release]: https://github.com/gonka-ai/gonka/blob/devshard-0.2.15-v5/devshard/docs/release-0.2.15-v5.md
