# High-availability devshard host setup

Devshard inference that stays available, backed by multiple `versiond` instances.

## Why this matters

A single `versiond` process is a single point of failure (SPOF): if that machine or container dies, gateways cannot reach your host for that protocol version.

HA routers direct requests to ready `versiond` replicas during failures and maintenance. Replicas share committed session state in PostgreSQL.

```text
Public proxy (/devshard/...)
        │
        ▼
 versiond-router fleet
        │
        ├── versiond  ──► devshardd ──┐
        └── versiond2 ──► devshardd ──┴── shared PostgreSQL
```

Maintain ready capacity for every served protocol. Replica failure can interrupt in-flight requests.

<a id="before-you-start"></a>

## Prerequisites

1. Working `node` + `api` (dapi) + `proxy` on the host (standard join deployment).
2. Join files and compatible host/gateway images for the [release covered here](#release-reference).
3. Same participant identity on every HA `versiond` replica: `KEY_NAME`, keyring and `ACCOUNT_PUBKEY`.
4. Separate data directories for each replica and one shared PostgreSQL database. Run only one dapi with the participant keys.
5. Docker Compose **2.24.4+**, Bash, Python 3, `curl`, `jq`, `flock`, `sha256sum` and `timeout`.

New hosts: use the supplied protocol list. Updates: retain the existing list. Enable new protocols after release activation using [Add a protocol](#add-a-protocol).

Use the catalog filter below for HA protocols. Keep pre-HA protocols such as `v3` in a separate deployment. Retained pre-HA sessions or pre-HA processes using this database require a separately verified transition before this procedure.

**Install:** Steps 1–4. **Upgrade:** [Existing host](#upgrade-an-existing-host). **Extend:** [Remote replica](#add-a-remote-replica) or [local replica](#add-a-local-replica).

<a id="select-the-release-images"></a>

### Release images

Use the default images in the release's Compose files and router-fleet script. Additional machines must use files from the same release as the existing HA deployment.

Saved `VERSIOND_IMAGE`, `VERSIOND_ROUTER_IMAGE`, `PROXY_ROUTER_IMAGE` and `PROXY_POLICY_IMAGE` overrides supersede release defaults. Review them during [Upgrade](#upgrade-an-existing-host).

<a id="install-a-new-host"></a>
<a id="step-1---install-postgres-preferably-ha-itself"></a>

<a id="3-select-postgresql"></a>

<a id="step-1---choose-postgresql"></a>

## Step 1 - Install PostgreSQL (preferably HA itself)

HA `versiond` removes dependence on one **app** server, but if PostgreSQL runs on a single VM, **PostgreSQL becomes your new SPOF**. Prefer a **managed / replicated** database.

Connect all `versiond` instances to the same PostgreSQL database.

### Choose a database

**A — Managed PostgreSQL (recommended).** Select an HA configuration. Create a database and user. Configure the connection in [§2.2](#22-using-external--managed-postgres-with-the-same-overlay).

**B — Self-managed PostgreSQL.** Install PostgreSQL on a dedicated host or cluster. Create the database and role. Configure replication and failover for database HA. Configure the connection in [§2.2](#22-using-external--managed-postgres-with-the-same-overlay).

External databases: record the primary host, port (usually `5432`), database, user and password. Allow access from every `versiond` instance.

**C — Local Compose PostgreSQL.** `docker-compose.versiond.yml` starts `devshard-postgres` on the join host. Host failure also takes the database offline.

External PostgreSQL requires a direct connection or **session-mode pooling**. Transaction pooling and explicit `PGSSL*` settings are unsupported; `PGSSLMODE=disable` is allowed. Providers requiring explicit TLS settings need a separate supported procedure. Do not disable required TLS.

### Where to put PostgreSQL settings

Edit `deploy/join/config.env` and add the database password:

```bash
export DEVSHARD_POSTGRES_PASSWORD='<strong-password>'
```

Database/user default: `devshardd`. Override different names with `DEVSHARD_POSTGRES_DB` and `DEVSHARD_POSTGRES_USER`.

Local PostgreSQL: the HA overlay sets `PGHOST` and `DEVSHARD_STORAGE_MODE`; omit them from `config.env`. Data path: `${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}/data`. External PostgreSQL requires the §2.2 override.

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

Applies to external databases (A and B). Complete §2.1 first.

`docker-compose.versiond.yml` sets `PGHOST=devshard-postgres`. Override it for every replica; changing `config.env` alone is insufficient.

Create the database and role through your provider or run this SQL with your password:

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

Set `COMPOSE_FILE` in `config.env`; append all additional overrides:

```bash
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5.override.yml:docker-compose.devshard-pg-external.override.yml
```

<a id="23-multiple-machines-recommended-true-host-ha"></a>

<a id="add-a-remote-replica"></a>

### 2.3 Multiple machines

Use a **private network** between machines. Run the additional `versiond` on B and keep the join stack on A. A's public proxy, node and api remain single-instance.

Prerequisite for a new deployment: complete [startup](#4-start-the-deployment) and [verification](#verify-the-deployment) on A.

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

Set `export GONKA_PRIVATE_BIND_IP=<A-private-ip>` in A's `config.env`. Run in `deploy/join`:

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

New B: use the same release's `deploy/join` files. Existing B: follow [Replace a member](#replace-a-member); preserve data mounts.

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

Populate `pool-postgres.env` with the working pool's PostgreSQL endpoint and credentials from A or the database administrator. Obtain these **independently of the candidate replica**. Run with the target image active:

```bash
./update-devshard.sh --check-storage --reference-env ./pool-postgres.env
```

Require `Storage check passed` and exit code 0 before admission. The check writes database probes. Run one storage check at a time, with no concurrent updater runs. Select other containers with repeated `--container NAME` options.

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

Membership rollout interrupts new requests. Preserve member IDs when replacing a host at the same endpoint. Apply endpoint-file changes to the running fleet.

DNS pools: set `VERSIOND_POOL_HOST` to private DNS resolving all members. Every router must resolve pool and internal names. Member changes are automatic; pool-name or resolver changes require maintenance rollout. Different ports require explicit endpoints.

Test a session served by B (`X-Upstream-Addr`). Stop B's `versiond`; continue the same session on another replica and verify committed state. Restore B and verify readiness.

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

Run on the join host. Stop on any command failure:

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

Required results:

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

Routine updates: verify retained sessions; no additional planned stop is required. New protocols: use a new escrow.

<a id="operate-the-deployment"></a>

## Step 5 - Operate the deployment

Run the applicable procedure for each maintenance task.

<a id="step-5---versiond-router-fleet-operations"></a>

### Manage router slots

Manage router slots with the fleet script. The main project's `docker compose down` does not stop them. Allow up to `VERSIOND_ROUTER_DRAIN_TIMEOUT_SECONDS` (default 1800 seconds) per replaced slot.

The fleet script loads `config.env`. Select the required operation:

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

# Stop only one member, keeping enough other replicas ready to handle the load.
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

1. Use the target release's files and review any [image overrides](#release-images), then run `unset VERSIOND_IMAGE` followed by `source ./config.env`. Preserve the member's database, identity, protocol list and mounts. Keep at least one other replica ready to serve each required protocol; Compose does not enforce this requirement.
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
3. Record a working escrow, committed nonce and cost for each served protocol. Verify these sessions after updating.
4. Put the new release's join files in the **same deployment directory and Compose project**. Review changes before applying them; keep your configuration and overrides.
5. Review [image overrides](#release-images) in `config.env` and Compose files. Remove obsolete values and legacy local `image: ${VERSIOND_IMAGE:?...}` entries. Retain intentional release-compatible custom images, the catalog filter and site settings.
6. For local PostgreSQL, save its current compatible image digest in `DEVSHARD_POSTGRES_IMAGE` using the command below.

Preserve during routine updates:

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

Save the complete output as a quoted `VERSIOND_VERSIONS` value in `config.env`. If unavailable, recover it from the configuration backup before continuing. Do not substitute the new-installation list.

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

Inspect the running database's data directory and mounts. **Skip copying** for unchanged external PostgreSQL or the existing persistent path `${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}/data`. Leave PostgreSQL running and proceed to [Run the updater](#3-run-the-updater).

The following procedure moves a local cluster from the old Docker volume at `/var/lib/postgresql/data` to the persistent bind at `/var/lib/postgresql/gonka/data`.

<details>
<summary><strong>One-time copy from the old PostgreSQL volume</strong></summary>

Keep the source cluster's PostgreSQL major version and Alpine/musl image family. Use the compatible image retained in `DEVSHARD_POSTGRES_IMAGE`; this copy does not upgrade PostgreSQL.

Before copying, verify the retained containers use the selected filtered catalog and unchanged database. `docker start` does not apply edited overrides. Containers requiring configuration changes need an offline supervisor transition before this procedure.

Complete [directory preparation](#prepare-the-postgresql-directory). Before stopping or recreating PostgreSQL, record the source volume and system identifier and run preflight:

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

Require successful preflight and free space equal to the source size plus 10%. Preserve the source: no `down`, `down -v`, `rm -v`, pruning or `--renew-anon-volumes` before migration.

Close new traffic and wait for accepted work to finish. Stop **all writers**: include every local member below and stop remote members on their hosts. Refresh the backup after writes stop:

```bash
# Include every local member; stop remote members on their own machines too.
docker stop --time 1800 versiond versiond2
# Refresh the database backup now that application writes have stopped.
docker stop --time 300 devshard-postgres
./versiond-router-fleet.sh prepare-networks
"${dc[@]}" up -d --no-deps devshard-postgres
"${dc[@]}" logs --tail=100 devshard-postgres
```

Wait for PostgreSQL health. Verify the recorded system identifier and committed data **before restarting writers**. On copy failure, keep writers stopped, preserve the source and resolve the error before retrying.

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

After database verification, keep writers stopped and recreate PostgreSQL without the recovery overlay. Use the same image/configuration for the updater. Retain the source volume and backup. Do not use `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT` for recovery.

</details>

</details>

<a id="3-run-the-updater-with-the-complete-deployment-configuration"></a>

### 3. Run the updater

Schedule maintenance for the update; replacing the public proxy can interrupt connections. Close public traffic and let accepted work finish. Reopen traffic only after the retained-session checks pass.

For a routine update, leave PostgreSQL, the filter, replicas and router fleet running. Leave `UPDATE_SKIP_POSTGRES_PROBE` and `UPDATE_ACCEPT_DATABASE_CHANGE` disabled.

<details>
<summary><strong>First transition: prepare the filter and recover stopped members</strong></summary>

First-time transition or post-copy recovery only. Preserve the retained protocol list and run:

```bash
./versiond-router-fleet.sh prepare-networks
docker compose up -d --no-deps oracle-filter
```

**After a local database copy:** verify the copied database and retained containers' catalog/database settings before restarting writers. Include all stopped local members below; start remote members on their hosts:

```bash
docker start versiond versiond2
```

**External PostgreSQL without storage proof:** if an installed supervisor returns HTTP 404 from `/internal/storage-identity`, stop all writers and independently verify the existing database endpoint, identity and data. Start the selected target supervisors with the complete Compose configuration, then require [the independent database check](#check-the-remote-database) on every member before running the updater. Use repeated `--container NAME` arguments for local members. Do not restart the old containers in this case.

Timeouts, HTTP 503 and invalid storage proofs require correction. Do not treat them as unsupported APIs.

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

Continue only after every check returns HTTP 200. While updating a replica, keep at least one other replica ready to serve each retained protocol.

Run preflight and preview replacements. This writes database probes but does not replace services:

```bash
./update-devshard.sh --dry-run
```

Review the proposed images and changes. For target-directory creation errors, complete [directory preparation](#prepare-the-postgresql-directory) and retry. Resolve all errors before proceeding; do not reset fleet state or enable bypass flags. After successful preflight, run:

```bash
./update-devshard.sh
```

The updater replaces local services and routing. [Replace remote members](#replace-a-member) one at a time before final verification.

Run inference with every retained escrow through the public route. Complete [Verify](#verify-the-deployment) before reopening traffic. Enable new protocols separately through [Add a protocol](#add-a-protocol); use new escrows. Do not rename existing binaries or escrows.

If interrupted, follow [Recover a failed update](#recover-a-failed-update).

## Troubleshooting

### Recover a failed update

Inspect logs and fix the error. Rerun with the same complete Compose configuration and persistent `UPDATE_STATE_DIR` (default under `~/.local/state/gonka/updater/`). Normal runs recover interrupted replacements; `--check` only reports pending recovery.

For database-history errors, verify the selected data directory before retrying. Keep the preserved source cluster unchanged and do not bypass migration markers. Retain previous join files and backups: restoring a container image does not undo host-file edits, committed writes or schema migrations.

### Resolve a missing database

Restore the recorded database. Do not initialize an empty replacement. Use `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true` only for confirmed first-time HA enablement; unset it afterward. If `.pg-bound` exists, database restoration is required.

### Prepare the PostgreSQL directory

Local PostgreSQL: run in `deploy/join` before migration or after a preflight directory-creation error:

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

Require command success. For persistent access errors, check parent-directory permissions. Preserve database ownership.

### Resolve an unready member

Check the selected catalog, binary URL/SHA256, child logs and database access. Require every selected protocol to pass its readiness check. Do not substitute a single-protocol Docker healthcheck for `/readyz`, or enable updater bypass flags to hide a failure.

## Reference

### Release reference

**Release:** `devshard-0.2.15-v5`.

Use compatible join files, updater/fleet scripts and published images from one release set. For later releases, follow their guide and upgrade requirements. Preserve site settings and retained protocols.

Validate nonstandard deployments and database changes on a data copy first. Extended checks: [acceptance plan](../devshard/docs/ha-host-updater-acceptance.md), [lifecycle test plan](../devshard/docs/devshard-host-ha-test-plan.md).

<details>
<summary>Database capacity and rollback limits</summary>

Preflight checks PostgreSQL connection capacity for configured members. Budget additional connections for custom DNS membership and other clients. Each child defaults to four pool connections plus two health/fence connections. Old and new generations can overlap.

Retain the existing HA PostgreSQL database. New binaries may apply forward schema migrations. Legacy SQLite conversion requires a separate verified procedure.

Image rollback does not reverse schema migrations or committed writes. Automatic schema and PostgreSQL-to-SQLite downgrades are unsupported. Retain `.pg-bound`; use a binary compatible with current state or perform a coordinated restore during maintenance. The preserved source cluster contains data only up to the copy time. Validate restart and rollback on a copy of current state.

</details>
