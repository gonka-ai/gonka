# High-availability devshard host setup

Devshard inference that stays available, backed by multiple `versiond` instances.

## Why this matters

A single `versiond` process is a single point of failure (SPOF): if that machine or container dies, gateways cannot reach your host for that protocol version.

This HA layout runs multiple `versiond` replicas behind routers, so another ready replica can serve requests when one fails or is stopped for maintenance. The replicas share committed session state in PostgreSQL.

```text
Public proxy (/devshard/...)
        │
        ▼
 versiond-router fleet
        │
        ├── versiond  ──► devshardd ──┐
        └── versiond2 ──► devshardd ──┴── shared PostgreSQL
```

Keep enough ready replicas for every served protocol and the traffic. A request already in progress on a failed replica can still be interrupted.

<a id="before-you-start"></a>

## Prerequisites

1. Working `node` + `api` (dapi) + `proxy` on the host (standard join deployment).
2. Join files and compatible host/gateway images for the [release covered here](#release-reference).
3. Same participant identity on **every** HA `versiond` replica: same `KEY_NAME`, keyring and `ACCOUNT_PUBKEY`.
4. Separate data directories for each replica and one shared PostgreSQL database. Run only one dapi with the participant keys.
5. Docker Compose **2.24.4+**, Bash, Python 3, `curl`, `jq`, `flock`, `sha256sum` and `timeout`.

Use the supplied protocol list for a new host. When updating, retain your current list; follow [Add a protocol](#add-a-protocol) to enable another protocol after its release activation.

Use the catalog filter below for HA protocols. Keep pre-HA protocols such as `v3` in a separate deployment. If their sessions must be retained or a pre-HA process uses this database, complete a separately verified transition first.

**New host:** follow Steps 1–4. **Existing host:** go to [Upgrade](#upgrade-an-existing-host). To extend a running deployment, see [Multiple machines](#add-a-remote-replica) or [Add another local replica](#add-a-local-replica).

<a id="select-the-release-images"></a>

### Release images

Use the component images selected by the release's Compose files and router-fleet script. No manual image settings are required. When adding a machine, use files from the same release as the existing HA deployment.

During [Upgrade](#upgrade-an-existing-host), review any saved `VERSIOND_IMAGE`, `VERSIOND_ROUTER_IMAGE`, `PROXY_ROUTER_IMAGE` and `PROXY_POLICY_IMAGE` overrides. They take precedence over release defaults.

<a id="install-a-new-host"></a>
<a id="step-1---install-postgres-preferably-ha-itself"></a>

<a id="3-select-postgresql"></a>

<a id="step-1---choose-postgresql"></a>

## Step 1 - Install PostgreSQL (preferably HA itself)

HA `versiond` removes dependence on one **app** server, but if PostgreSQL runs on a single VM, **PostgreSQL becomes your new SPOF**. Prefer a **managed / replicated** database.

Connect all `versiond` instances to the same PostgreSQL database.

### Choose a database

**Option A — Managed PostgreSQL (recommended).** Create a database and user through your provider. Select an HA configuration and connect it using [§2.2](#22-using-external--managed-postgres-with-the-same-overlay).

**Option B — Self-managed PostgreSQL.** Install PostgreSQL on a dedicated host or cluster. Create the role/database and configure replication and failover for database HA. Connect it using [§2.2](#22-using-external--managed-postgres-with-the-same-overlay).

For either external option, note the primary endpoint: host, port (usually `5432`), database, user and password. Ensure **all** `versiond` instances can reach it through your firewall or private network.

**Option C — Local Compose PostgreSQL.** `docker-compose.versiond.yml` starts `devshard-postgres` on the join host. If that machine goes down, the database goes down with it.

For an external database, connect directly or through a pooler in **session mode**. Transaction pooling is unsupported. Leave explicit `PGSSL*` settings unset (`PGSSLMODE=disable` is allowed). If your provider requires explicit TLS settings, use a procedure that supports them; do not disable required TLS.

### Where to put PostgreSQL settings

Edit `deploy/join/config.env` and add the database password:

```bash
export DEVSHARD_POSTGRES_PASSWORD='<strong-password>'
```

The database and user default to `devshardd`. Set `DEVSHARD_POSTGRES_DB` and `DEVSHARD_POSTGRES_USER` only if your database uses different names.

For local PostgreSQL, leave `PGHOST` and `DEVSHARD_STORAGE_MODE` out of `config.env`; the HA overlay sets them on the replicas. Data is stored at `${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}/data`. For external PostgreSQL, add the override in §2.2.

<a id="step-2---configure-the-deployment"></a>

<a id="step-2---configure-replicas-and-routing"></a>

## Step 2 - Run multiple `versiond` instances + the router fleet

Keep all deployment files in `deploy/join`:

| File | Purpose |
| --- | --- |
| `docker-compose.yml` | Base join deployment, supplied with the release |
| `docker-compose.versiond.yml` | Shared PostgreSQL and HA replica settings, supplied with the release |
| `docker-compose.devshard-v5.override.yml` | Catalog filter; create it below |
| `docker-compose.devshard-pg-external.override.yml` | External database settings; create it only for §2.2 |

<a id="21-common-configuration-local-replicas"></a>

### 2.1 Same machine, two replicas

On the join host:

<a id="1-set-the-deployment-configuration"></a>

**1. Save the HA settings in `config.env`.**

Keep the existing join identity and PostgreSQL settings. Add to `config.env`:

```bash
# Protocol list for a new host on this release; keep the existing list when updating.
export VERSIOND_VERSIONS="v4 v4.1"
# Deployment settings (retain these across component updates)
export VERSIOND_NON_HA_VERSIONS=""
# Use the same filtered catalog for replicas and both routing tiers.
export VERSIOND_ROUTING_CATALOG_URL=http://oracle-filter:9100/versions
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5.override.yml
# Append every additional override used by this deployment, in the same order.
```

Keep `VERSIOND_NON_HA_VERSIONS` empty. List every active override in `COMPOSE_FILE`. Preserve filenames and file order across updates.

<a id="2-create-the-ha-override"></a>

**2. Create `docker-compose.devshard-v5.override.yml` in `deploy/join`.**

```bash
cat > docker-compose.devshard-v5.override.yml <<'EOF'
services:
  # Shared catalog of selected HA protocols for replicas and routers.
  oracle-filter:
    container_name: oracle-filter
    image: python:3.12-alpine
    environment:
      - ORACLE_UPSTREAM=http://api:9100/versions
      - ORACLE_ALLOW=${VERSIOND_VERSIONS:?set the approved HA protocol list}
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
    depends_on:
      api:
        condition: service_started
    networks:
      default: {}
      versiond-router-back: {}
    restart: always

  versiond:
    environment:
      - VERSIOND_ORACLE_URL=http://oracle-filter:9100/versions
      - VERSIOND_NON_HA_VERSIONS=${VERSIOND_NON_HA_VERSIONS-}
    depends_on:
      oracle-filter:
        condition: service_started
      devshard-postgres:
        condition: service_healthy

  versiond2:
    environment:
      - VERSIOND_ORACLE_URL=http://oracle-filter:9100/versions
      - VERSIOND_NON_HA_VERSIONS=${VERSIOND_NON_HA_VERSIONS-}
    depends_on:
      oracle-filter:
        condition: service_started
      devshard-postgres:
        condition: service_healthy

  # Give every extra replica the same oracle-filter environment and dependencies.

  proxy:
    environment:
      - VERSIOND_NON_HA_VERSIONS=${VERSIOND_NON_HA_VERSIONS-}
      - VERSIOND_ROUTING_CATALOG_URL=${VERSIOND_ROUTING_CATALOG_URL:?set the routing catalog URL}
      - VERSIOND_VERSIONS=${VERSIOND_VERSIONS:?set the approved HA protocol list}
EOF
```

Keep `oracle-filter` running. Use its catalog for every replica and router.

**Local PostgreSQL:** continue to [Step 3](#4-start-the-deployment). **External PostgreSQL:** complete §2.2 first. Add more replicas after startup and verification.

<a id="22-using-external--managed-postgres-with-the-same-overlay"></a>

### 2.2 External or managed PostgreSQL

Use this when the database runs outside the join host (Options A and B). Complete §2.1 first.

`config.env` alone is **not enough**: `docker-compose.versiond.yml` sets `PGHOST=devshard-postgres`. Add the override below to point every replica at the external database.

Create the database and role through your provider, or run this SQL on your PostgreSQL server after replacing the password:

```sql
CREATE USER devshardd WITH PASSWORD '...';
CREATE DATABASE devshardd OWNER devshardd;
```

Allow database access from every replica. Save the credentials in `config.env`. Create `docker-compose.devshard-pg-external.override.yml` with the database hostname and port:

```yaml
services:
  devshard-postgres:
    profiles: [local-postgres]  # Leave this profile disabled.

  versiond: &external-postgres
    environment:
      PGHOST: your-managed-pg.example.com
      PGPORT: "5432"
    depends_on: !override
      api:
        condition: service_started
      oracle-filter:
        condition: service_started

  versiond2:
    <<: *external-postgres
```

For each extra replica, add its service name with `<<: *external-postgres`. Database credentials and storage mode come from the HA overlay.

Replace `COMPOSE_FILE` in `config.env` with the complete list, appending any further overrides:

```bash
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5.override.yml:docker-compose.devshard-pg-external.override.yml
```

<a id="23-multiple-machines-recommended-true-host-ha"></a>

<a id="add-a-remote-replica"></a>

### 2.3 Multiple machines

Use a **private network** between machines. Run the additional `versiond` on B and keep the join stack on A. A's public proxy, node and api remain single-instance.

For a new deployment, finish [Step 3](#4-start-the-deployment) and [Step 4](#verify-the-deployment) on A first, then return here.

| Machine | Runs |
| --- | --- |
| A | Local `versiond` replicas + node/api/proxy + router fleet |
| B | `versiond` only — no second dapi with the same keys |
| Shared | PostgreSQL reachable from every `versiond` instance |

Keep B out of the router pool until its checks pass.

#### 1. Prepare machine A

On A, allow B to reach these ports over the private network:

- PostgreSQL: `5432` (or the external database port).
- Node-manager: `9400`.
- Chain RPC/gRPC: `26657`, `9090`.
- Filtered catalog: `19100`.

Confirm database and keyring credentials match the running local replicas.

Set `export GONKA_PRIVATE_BIND_IP=<A-private-ip>` in A's `config.env`, then run this in `deploy/join`:

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

With external PostgreSQL, omit the `devshard-postgres` entry and use its actual endpoint on B. Append this override to A's complete `COMPOSE_FILE`.

On a fresh host, include the override before [startup](#4-start-the-deployment). On an existing host:

1. Close public traffic and let accepted work finish.
2. Stop all local and remote writers before recreating local PostgreSQL.
3. Apply the complete Compose configuration.
4. Check readiness and run `./update-devshard.sh --check` before reopening traffic.

Do not restart the whole live stack just to add a member.

#### 2. Configure and start machine B

B does not run `api` or `node`. It uses A's filtered catalog, node-manager and chain endpoints, and the shared PostgreSQL database.

For a new B, use the same release's `deploy/join` files. For an existing B, use [Replace a member](#replace-a-member) and retain its current data mounts.

Copy these settings from A into B's `config.env`:

- Identity: `KEY_NAME`, `ACCOUNT_PUBKEY`, `KEYRING_BACKEND`, `KEYRING_PASSWORD`.
- Database credentials: `DEVSHARD_POSTGRES_*`.
- Protocols: `VERSIOND_VERSIONS` and the empty `VERSIOND_NON_HA_VERSIONS`.
- `VERSIOND_IMAGE`, only if A uses a custom image.

On A, run `docker cp versiond:/root/.inference/keyring-file .` and transfer `keyring-file/` into B's `.inference/`. Keep the supplied read-only keyring mount. Use B's own data directory; do not share A's replica data directories.

Add these settings to B's `config.env`, replacing the private addresses:

```bash
export NETWORK_NODE_PRIVATE_IP='<A-private-ip>'
export VERSIOND_BIND_IP='<B-private-ip>'
export COMPOSE_FILE=docker-compose.versiond-remote.yml:docker-compose.versiond-remote-filter.yml
```

For external PostgreSQL, also set `DEVSHARD_POSTGRES_HOST` and `DEVSHARD_POSTGRES_PORT` to the shared database endpoint. Restrict B's port 8080 to the routers.

Run in B's `deploy/join` to create the filter override:

```bash
cat > docker-compose.versiond-remote-filter.yml <<'EOF'
services:
  versiond:
    environment:
      VERSIOND_ORACLE_URL: http://${NETWORK_NODE_PRIVATE_IP:?set A's private IP}:19100/versions
EOF
```

Start B with the supplied Compose file and the filter override. Check every selected protocol:

```bash
source ./config.env
docker compose up -d --wait --wait-timeout 2100
(
  set -e
  : "${VERSIOND_VERSIONS:?set the approved HA protocol list}"
  for version in $VERSIOND_VERSIONS; do
    curl -fsS "http://${VERSIOND_BIND_IP}:8080/readyz?version=$version"
  done
)
```

Continue when the container is healthy and every check returns HTTP 200.

<a id="check-bs-database-before-admitting-it-to-the-pool"></a>

#### Check the remote database

Before adding B to the pool, verify its database connection. Install `psql` on B and run:

```bash
cd /path/to/gonka/deploy/join
cp pool-postgres.env.template pool-postgres.env
chmod 600 pool-postgres.env
```

Fill `pool-postgres.env` with the known working pool's PostgreSQL endpoint and credentials, obtained from A or the database administrator **independently of the candidate replica**. With the intended image running, execute:

```bash
./update-devshard.sh --check-storage --reference-env ./pool-postgres.env
```

Require `Storage check passed` and exit code 0 before admission. Use `--container NAME` for another container; repeat the option for multiple containers. Check one host at a time, with no updater running elsewhere. The check writes test values to the database.

<a id="on-the-machine-that-runs-the-router-fleet-usually-a"></a>

#### 3. Add B to the router pool

On A, list every local and remote member in `versiond-endpoints.json`; ensure each address is reachable from every router slot:

```json
[
  {"id": "local-a", "host": "versiond", "port": 8080},
  {"id": "local-a-2", "host": "versiond2", "port": 8080},
  {"id": "remote-b", "host": "10.0.0.12", "port": 8080}
]
```

Set `export VERSIOND_POOL_ENDPOINTS_FILE=./versiond-endpoints.json` in A's `config.env`, then run `source ./config.env`. For a fresh fleet, use `./versiond-router-fleet.sh apply`. For an existing fleet, run during maintenance:

```bash
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
  ./versiond-router-fleet.sh maintenance-rollout
./versiond-router-fleet.sh verify-admission
```

Expect an interruption to new requests during membership rollout. Keep member IDs stable when replacing a host at the same endpoint. Apply changes after editing the endpoint file.

For a DNS pool instead, set `VERSIOND_POOL_HOST` to private DNS resolving all reachable members. Every router must resolve the pool and internal names. DNS member changes are discovered automatically; changing the pool name/resolver requires the maintenance command above. Use explicit endpoints for differing ports.

Finally, test a real session served by B: identify it using `X-Upstream-Addr`, stop B's `versiond`, and continue the same session on a survivor with its committed state intact. Restore B and repeat readiness checks before using it again.

<a id="24-adding-more-replicas"></a>

<a id="add-a-local-replica"></a>

### 2.4 Add another local replica

After completing [startup and verification](#4-start-the-deployment):

1. Add `docker-compose.versiond3.yml` to `COMPOSE_FILE`. For further replicas, copy it with a new container name and data directory.
2. Give the new service the same filter and database overrides as `versiond` and `versiond2`. Keep the supplied identity/image settings, shutdown timings, `versiond-pool` alias and PostgreSQL mount for `.pg-bound`.
3. Start the replica and check every selected protocol and inference.
4. For explicit endpoint lists, add the replica using [membership maintenance](#3-add-b-to-the-router-pool). With pool DNS, no router recreation is needed.

<a id="step-3---start-the-configured-deployment"></a>
<a id="3-bring-up-the-main-stack-and-router-fleet"></a>

<a id="4-start-the-deployment"></a>

## Step 3 - Start the deployment

Run these commands on the join host. Continue only if each command succeeds:

```bash
cd /path/to/gonka/deploy/join
source ./config.env
: "${COMPOSE_FILE:?set the complete Compose file list in config.env}"
./versiond-router-fleet.sh prepare-networks

docker compose up -d --wait --wait-timeout 2100

./versiond-router-fleet.sh apply
```

Complete [Verify the deployment](#verify-the-deployment) before accepting traffic.

<a id="4-confirm"></a>

<a id="verify-the-deployment"></a>

## Step 4 - Verify it works

<a id="check-readiness-and-storage"></a>

### 4.1 Check the running services

Run on the join host after installation or an update. Include every local replica in `replicas`; repeat the replica checks on remote hosts.

```bash
cd /path/to/gonka/deploy/join
source ./config.env
(
  set -euo pipefail
  : "${VERSIOND_VERSIONS:?set the approved HA protocol list}"
  replicas=(versiond versiond2)

  ./update-devshard.sh --check
  ./versiond-router-fleet.sh verify-admission
  for replica in "${replicas[@]}"; do
    docker exec "$replica" wget -qO- http://127.0.0.1:8080/readyz
    for version in $VERSIOND_VERSIONS; do
      docker exec "$replica" wget -qO- "http://127.0.0.1:8080/readyz?version=$version"
    done
  done
  for version in $VERSIOND_VERSIONS; do
    ./versiond-router-fleet.sh wait-version "$version"
    curl -fsS "http://127.0.0.1:${API_PORT:-8000}/devshard/$version/healthz"
  done
)
```

Healthy signs:

- Preflight reports `Preflight passed`.
- `verify-admission` and `wait-version` finish successfully.
- Every replica passes readiness for every selected protocol.
- The public `/devshard/<version>/healthz` route returns HTTP 200.

Preflight writes database probes; it does not replace services.

For a new or replaced remote member, also pass the [database check](#check-the-remote-database) before adding it to the pool.

<a id="confirm-failover-and-inference"></a>

### 4.2 Check service continuity

After installation or a pool change, test a funded escrow for **each served protocol**. Keep enough ready replicas for every protocol and the load.

1. Send an inference request and record the session's committed nonce and cost. Identify its serving replica from `X-Upstream-Addr` and the container's address.
2. Stop that replica on its host with `docker stop -t 1800 <serving-container>`.
3. Continue the **same session** through the public endpoint. Confirm inference succeeds and the recorded state and accounting are preserved.
4. Restore the replica with `docker start <serving-container>` and repeat its readiness checks before stopping another member. Restore it even if the test fails.

For an ordinary component update, verify the retained sessions after the update; a second planned stop is unnecessary. Use a new escrow when adding a protocol.

<a id="operate-the-deployment"></a>

## Step 5 - Operate the deployment

Use these procedures when needed; they are not additional installation steps.

<a id="step-5---versiond-router-fleet-operations"></a>

### Manage router slots

Manage router slots with the fleet script. The main project's `docker compose down` does not stop them. Allow up to `VERSIOND_ROUTER_DRAIN_TIMEOUT_SECONDS` (default 1800 seconds) per replaced slot.

Run only the operation you need. The fleet script loads `config.env` itself:

| Task | Command |
| --- | --- |
| View the fleet | `./versiond-router-fleet.sh status` |
| Stop slot 0 | `./versiond-router-fleet.sh stop 0` |
| Restore slot 0 | `./versiond-router-fleet.sh start 0` |
| Verify routing after a change | `./versiond-router-fleet.sh verify-admission` |
| Apply the release's router image | `./versiond-router-fleet.sh apply` |

For a complete release update, use the [updater](#3-run-the-updater).

Preserve previous stopped containers and catalog volumes until recovery is complete. Rerun interrupted operations with the same image/configuration. Use [membership maintenance](#3-add-b-to-the-router-pool) for pool, resolver or legacy-routing changes.

For whole-machine maintenance, drain the fleet before stopping the main stack:

```bash
./versiond-router-fleet.sh stop-all --maintenance
# Stop the main stack using its complete Compose file list.
# Only when decommissioning, after the main stack is down:
# ./versiond-router-fleet.sh down --maintenance
```

<a id="25-operating-versiond-members"></a>

### Restart a member

Restart one member at a time. Keep enough ready replicas for every served protocol and the load. Include every active override in `COMPOSE_FILE`:

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
(
  set -e
  : "${VERSIOND_VERSIONS:?set the approved HA protocol list}"
  for version in $VERSIOND_VERSIONS; do
    docker exec versiond2 wget -qO- "http://127.0.0.1:8080/readyz?version=$version"
  done
)
```

Keep the supplied shutdown timings. Wait for the restarted member to pass its checks before working on another.

### Replace a member

1. Use the target release's files and review any [image overrides](#release-images), then run `unset VERSIOND_IMAGE` followed by `source ./config.env`. Preserve the member's database, identity, protocol list and mounts. Keep a ready survivor for every required protocol; Compose does not enforce a reserve.
2. Stop and drain the member. For a remote member, remove its explicit endpoint using [membership maintenance](#3-add-b-to-the-router-pool), or remove it from pool DNS, before starting its replacement.
3. Run `docker compose pull <service>`, then recreate only that service with `docker compose up -d --no-deps --wait --wait-timeout 2100 <service>`. For a remote member, pass the [database check](#check-the-remote-database) before restoring membership.
4. Verify every required protocol and inference before replacing another member. On failure, restore the prior image/configuration and verify against the current database.

### Remove a member

Stop and drain it, remove its service or set replicas to zero (`VERSIOND2_REPLICAS=0` for `versiond2`), then remove its DNS/explicit membership. Use membership maintenance for explicit lists. Verify remaining sessions and retain data/cache directories until recovery is confirmed.

### Add a protocol

1. Follow the new protocol's release instructions for its name and required host/gateway versions. After the activation specified there, add the name to `VERSIOND_VERSIONS` in `config.env` on every host; keep existing names needed by retained sessions.
2. On A, run `source ./config.env`, then `docker compose up -d --no-deps oracle-filter` using the complete `COMPOSE_FILE`.
3. Run `./versiond-router-fleet.sh wait-version <new-protocol>`, then complete [Verify](#verify-the-deployment) with the updated list and a new escrow.

No router restart is needed to activate the protocol. Schedule the next host update as maintenance: it also applies the saved protocol configuration to the router fleet and public proxy.

Preserve `proxy-router-state` and each slot's `router-state`. Remove protocols only during maintenance after their sessions are no longer needed: changing the filter can stop children, while previously accepted router routes are retained by default.

<a id="26-upgrade-an-existing-ha-deployment-to-v5"></a>

## Upgrade an existing host

### 1. Prepare the release

1. Confirm the [prerequisites](#before-you-start) and back up PostgreSQL.
2. Save `config.env`, Compose files, endpoint files, current image references and database mounts.
3. Record a working escrow for each currently served protocol, including its committed nonce and cost. Use these sessions to verify the update.
4. Put the new release's join files in the **same deployment directory and Compose project**. Review changes before applying them; keep your configuration and overrides.
5. Review [image overrides](#release-images) in `config.env` and Compose overrides. Remove obsolete settings to use the release defaults; retain a custom image only if intended for this release. In older local HA overrides, remove the `image: ${VERSIOND_IMAGE:?...}` lines so the supplied overlay can select the image. Keep the catalog filter and the site settings below.
6. For local PostgreSQL, save its current compatible image digest in `DEVSHARD_POSTGRES_IMAGE` using the command below.

Keep these settings and files unchanged during a routine update:

| Keep | Includes |
| --- | --- |
| Identity and replica data | `.inference`, `devshards*/data`, `.pg-bound`, binary caches and per-replica mounts |
| Database connection | Credentials, endpoint and the existing data directory |
| Routing configuration | Fleet slots, networks, membership and router catalog volumes |
| Saved deployment settings | `VERSIOND_VERSIONS`, the ordered `COMPOSE_FILE`, any `COMPOSE_PROJECT_NAME`, and `UPDATE_STATE_DIR` |

Do not replace your `config.env` with the new-installation example. Add protocols only after verifying the update. Plan database moves, PostgreSQL major upgrades and fleet reconfiguration separately; use a different PostgreSQL digest only after confirming cluster compatibility.

If introducing this HA layout for the first time, merge the required [installation settings](#install-a-new-host) into your existing files without running its startup commands. Keep the filter enabled and `VERSIOND_NON_HA_VERSIONS` empty.

<details>
<summary><strong>Find the current PostgreSQL image digest</strong></summary>

Run on the host with local PostgreSQL:

```bash
docker image inspect \
  "$(docker inspect devshard-postgres --format '{{.Image}}')" \
  --format '{{range .RepoDigests}}{{println .}}{{end}}'
```

Save one printed digest as `DEVSHARD_POSTGRES_IMAGE` in `config.env`. If none is printed, obtain a published digest for the current compatible image before continuing.

</details>

<details>
<summary><strong>If the old config.env has no VERSIOND_VERSIONS</strong></summary>

Read the saved protocol list from the existing filter before changing or recreating it:

```bash
docker inspect oracle-filter --format '{{json .Config.Env}}' |
  jq -er '.[] | select(startswith("ORACLE_ALLOW=")) | ltrimstr("ORACLE_ALLOW=") | gsub(","; " ") | select(length > 0)'
```

Save the printed list as the quoted value of `VERSIOND_VERSIONS` in `config.env`. If the container or value is unavailable, recover the list from the previous configuration backup before continuing. Preserve all existing names; do not substitute the new-installation value.

</details>

After editing, clear previously loaded component image values, reload your configuration and validate it:

```bash
cd /path/to/gonka/deploy/join
unset VERSIOND_IMAGE VERSIOND_ROUTER_IMAGE PROXY_ROUTER_IMAGE PROXY_POLICY_IMAGE
source ./config.env
# Keep every active override in the ordered COMPOSE_FILE saved in config.env.
: "${COMPOSE_FILE:?set the complete Compose file list in config.env}"
: "${VERSIOND_VERSIONS:?retain the approved HA protocol list in config.env}"
dc=(docker compose)
"${dc[@]}" config --quiet
```

<a id="2-migrate-local-postgresql-if-needed"></a>

### 2. Check the database layout

Check the running database's data directory and mounts. **Skip copying** if PostgreSQL is external and unchanged, or already runs from the same persistent directory at `${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}/data`. Keep the database running and continue to [Run the updater](#3-run-the-updater).

Use the following procedure only to move a local cluster from the old Docker volume at `/var/lib/postgresql/data` into the persistent bind at `/var/lib/postgresql/gonka/data`.

<details>
<summary><strong>One-time copy from the old PostgreSQL volume</strong></summary>

Keep the source cluster's PostgreSQL major version and Alpine/musl image family. Use the compatible image retained in `DEVSHARD_POSTGRES_IMAGE`; this copy does not upgrade PostgreSQL.

Before copying, confirm the retained containers already use the selected filtered catalog and unchanged database. `docker start` reuses their saved configuration; it does not apply edited overrides. If they need those changes, plan an offline supervisor transition before proceeding with this copy-and-restart procedure.

Complete [directory preparation](#prepare-the-postgresql-directory), then run the following before stopping or recreating PostgreSQL. Record the source volume and system identifier:

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

Proceed only after preflight succeeds; the copy needs the source size plus 10% free space. Preserve the source: do not use `down`, `down -v`, `rm -v`, pruning or `--renew-anon-volumes` before migration.

Enter maintenance, close new traffic and let accepted work finish. Include **all local writers** below and stop remote writers on their hosts; refresh the backup after application writes stop:

```bash
# Include every local member; stop remote members on their own machines too.
docker stop --time 1800 versiond versiond2
# Refresh the database backup now that application writes have stopped.
docker stop --time 300 devshard-postgres
./versiond-router-fleet.sh prepare-networks
"${dc[@]}" up -d --no-deps devshard-postgres
"${dc[@]}" logs --tail=100 devshard-postgres
```

Wait for PostgreSQL health. Repeat the system-identifier query and verify both the recorded identifier and committed data **before restarting writers**. If copying fails, leave writers stopped, preserve the source and resolve the logged error before retrying.

<details>
<summary><strong>If the old volume was already detached</strong></summary>

Complete [directory preparation](#prepare-the-postgresql-directory) if needed, then use the recorded exact volume name with the complete Compose configuration:

```bash
export DEVSHARD_POSTGRES_LEGACY_VOLUME='<recorded-old-volume-name>'
# Command-line -f replaces COMPOSE_FILE, so pass the complete list explicitly.
files=()
IFS=':' read -ra parts <<<"$COMPOSE_FILE"
for f in "${parts[@]}"; do files+=(-f "$f"); done
bash ./devshard-postgres-migration-preflight.sh \
  --source-volume "$DEVSHARD_POSTGRES_LEGACY_VOLUME" \
  --target-dir "${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}" &&
docker compose "${files[@]}" -f docker-compose.versiond-postgres-recovery.yml \
  up -d --no-deps devshard-postgres
```

After checking the migrated database, keep writers stopped and recreate PostgreSQL once without the recovery overlay. Keep that same image/configuration for the updater. Retain the source volume and backup; never use `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT` to recover a missing database.

</details>

</details>

<a id="3-run-the-updater-with-the-complete-deployment-configuration"></a>

### 3. Run the updater

Schedule maintenance for the update; replacing the public proxy can interrupt connections. Close public traffic and let accepted work finish. Reopen traffic only after the retained-session checks pass.

For a routine update, leave PostgreSQL, the filter, replicas and router fleet running. Leave `UPDATE_SKIP_POSTGRES_PROBE` and `UPDATE_ACCEPT_DATABASE_CHANGE` disabled.

<details>
<summary><strong>First transition: prepare the filter and recover stopped members</strong></summary>

Run these preparation commands if introducing this layout or recovering after the database copy. Preserve the retained protocol list:

```bash
./versiond-router-fleet.sh prepare-networks
docker compose up -d --no-deps oracle-filter
```

**After a local database copy:** restart the retained containers only after verifying the copied database and their saved catalog/database settings as required above. Include every stopped local member and start remote members on their hosts:

```bash
docker start versiond versiond2
```

**External PostgreSQL without storage proof:** if an installed supervisor returns HTTP 404 from `/internal/storage-identity`, stop all writers and independently verify the existing database endpoint, identity and data. Start the selected target supervisors with the complete Compose configuration, then require [the independent database check](#check-the-remote-database) on every member before running the updater. Use repeated `--container NAME` arguments for local members. Do not restart the old containers in this case.

A timeout, HTTP 503 or invalid storage proof is a failure to resolve, not an unsupported API.

</details>

Check every retained protocol on every member before updating. The command uses the older health endpoint only on HTTP 404; resolve HTTP 503 and connection errors before continuing. After replacement, complete [Verify](#verify-the-deployment).

```bash
# Include every local member; repeat on remote hosts.
(
  set -e
  : "${VERSIOND_VERSIONS:?set the approved HA protocol list}"
  for replica in versiond versiond2; do
    for version in $VERSIOND_VERSIONS; do
      if output=$(docker exec "$replica" /bin/busybox wget -S -O /dev/null -T 5 \
        "http://127.0.0.1:8080/readyz?version=$version" 2>&1); then
        continue
      fi
      case "$output" in
        *"HTTP/"*" 404 "*)
          docker exec "$replica" /bin/busybox wget -qO- -T 5 \
            "http://127.0.0.1:8080/$version/healthz" ;;
        *) printf '%s/%s: %s\n' "$replica" "$version" "$output" >&2; exit 1 ;;
      esac
    done
  done
)
```

Require HTTP 200 for every check and keep a ready survivor for each retained protocol. After a database copy, also verify the recorded sessions against the migrated database.

Preview the update. This includes the preflight and writes database probes without replacing services:

```bash
./update-devshard.sh --dry-run
```

Review the proposed images and changes. If preflight cannot create the PostgreSQL target directory, complete [directory preparation](#prepare-the-postgresql-directory) and retry. Resolve other errors without resetting fleet state or adding bypass flags. After a successful preview, run:

```bash
./update-devshard.sh
```

The updater replaces local services and routing. Update remote members using [Replace a member](#replace-a-member), one at a time, before the final checks.

Run inference with each retained escrow through the public route and complete [Verify](#verify-the-deployment). Reopen traffic only after these checks pass. To enable a newly approved protocol, follow [Add a protocol](#add-a-protocol); use a new escrow, without renaming old binaries or escrows.

If interrupted, follow [Recover a failed update](#recover-a-failed-update).

## Troubleshooting

### Recover a failed update

Inspect logs and fix the cause, then rerun normally with the same complete Compose configuration and persistent `UPDATE_STATE_DIR` (default under `~/.local/state/gonka/updater/`). A normal run recovers interrupted replacements; `--check` reports pending recovery.

For database-history errors, verify the selected data directory before retrying. Keep the preserved source cluster unchanged and do not bypass migration markers. Retain previous join files and backups: restoring a container image does not undo host-file edits, committed writes or schema migrations.

### Resolve a missing database

Restore the recorded database rather than initializing an empty replacement. `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true` is only for confirmed first-time HA enablement: use it once, then unset it. If `.pg-bound` exists, restore the database; the flag is not a recovery procedure.

### Prepare the PostgreSQL directory

For local PostgreSQL, run in `deploy/join` to create the target directory through Docker before migration or when preflight cannot create it:

```bash
source ./config.env
pg_dir="${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}"
[[ "$pg_dir" = /* ]] || pg_dir="$PWD/$pg_dir"
docker run --rm --network none --read-only \
  --security-opt label=disable \
  --volume "$pg_dir:/target:ro" \
  --entrypoint /bin/true \
  "${POSTGRES_MIGRATION_HELPER_IMAGE:-${DEVSHARD_POSTGRES_IMAGE:-postgres:16-alpine}}"
```

Continue only after the command succeeds. If access still fails, check the parent directory permissions; leave database ownership unchanged.

### Resolve an unready member

Check the selected catalog, binary URL/SHA256, child logs and database access. Require every selected protocol to pass its readiness check. Do not substitute a single-protocol Docker healthcheck for `/readyz`, or enable updater bypass flags to hide a failure.

## Reference

### Release reference

**Release:** `devshard-0.2.15-v5`.

Use join files, updater/fleet scripts and published component images from a compatible release set. For a later release, use its version of this guide and review its upgrade requirements before changing images. Keep site settings and the retained protocol list across releases.

For nonstandard deployments or database changes, validate the procedure on a copy of your data first. The [acceptance plan](../devshard/docs/ha-host-updater-acceptance.md) and [lifecycle test plan](../devshard/docs/devshard-host-ha-test-plan.md) describe extended testing.

<details>
<summary>Database capacity and rollback limits</summary>

Preflight checks the PostgreSQL connection budget for configured members. For custom DNS membership or other database clients, also allow for connections it cannot count. Each child defaults to four pool connections plus two health/fence connections; old and new generations can overlap.

Existing HA PostgreSQL data stays in the same database; new binaries may apply forward schema migrations. Conversion of legacy SQLite state is outside this guide and requires a separate verified procedure.

Restoring an old image does not undo database migrations or later committed writes. There is no automatic PostgreSQL-to-SQLite or schema downgrade; retain `.pg-bound`, use a binary known to read the current state, or perform a coordinated restore during maintenance. The preserved source cluster is a recovery source from the copy time, not a current replica after writes to the migrated database. Validate restart/rollback on a copy of the actual state before relying on it.

</details>
