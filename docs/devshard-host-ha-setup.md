# High-availability devshard host setup

Devshard inference that stays available, backed by multiple `versiond` instances.

## Why this matters

With a single `versiond`, a failure or maintenance stop makes its protocols unavailable on your host.

HA lets you run multiple `versiond` instances so inference stays available if one instance fails. Routers direct requests to ready instances, which share committed session state in PostgreSQL. You can also restart or replace one instance while the others continue serving.

```text
Public proxy (/devshard/...)
        │
        ▼
 versiond-router fleet
        │
        ├── versiond  ──► devshardd ──┐
        └── versiond2 ──► devshardd ──┴── shared PostgreSQL
```

## Prerequisites

1. Working `node`, `api` (dapi) and `proxy`: the standard join deployment.
2. Files from `deploy/join` and host/gateway images for the [release covered here](#release-reference).
3. Same participant identity on every HA replica: `KEY_NAME`, keyring, `ACCOUNT_PUBKEY`.
4. One data directory per replica, one shared PostgreSQL database, one dapi per participant key.
5. Docker Compose **2.24.4+**, Bash, Python 3, `curl`, `jq`, `flock`, `sha256sum`, `timeout`.

This setup supports HA protocols only. It does not cover migrating pre-HA deployments such as `v3`.

## Contents

- [Install a new host](#install-a-new-host)
- [Upgrade an existing host](#upgrade-an-existing-host)
- [Add a remote replica](#add-a-remote-replica)
- [Add a local replica](#add-a-local-replica)
- [Operate the deployment](#operate-the-deployment)
- [Troubleshooting](#troubleshooting)

## Install a new host

### Step 1 - Install PostgreSQL (preferably HA itself)

HA `versiond` removes dependence on one app server, but if PostgreSQL is a single VM, PostgreSQL becomes your new SPOF. Prefer a managed or replicated database.

All `versiond` instances connect to one PostgreSQL database.

#### Choose a database

**A — Managed PostgreSQL (recommended).** Select an HA configuration. Create the database and user. Configure the connection in [§2.2](#22-external-or-managed-postgresql).

**B — Self-managed PostgreSQL.** Install on a dedicated host or cluster. Create the database and role. Configure replication and failover for database HA. Configure the connection in [§2.2](#22-external-or-managed-postgresql).

A and B: record the primary host, port (usually `5432`), database, user and password. Allow access from every `versiond` instance.

**C — Local Compose PostgreSQL.** `docker-compose.versiond.yml` starts `devshard-postgres` on the join host. Host failure also takes the database offline.

External PostgreSQL requires a direct connection or session-mode pooling. Transaction pooling is unsupported. Explicit `PGSSL*` settings are unsupported; `PGSSLMODE=disable` is allowed. A provider that requires explicit TLS settings needs a separate procedure; never disable required TLS.

#### Where to put PostgreSQL settings

Add the database password to `deploy/join/config.env`:

```bash
export DEVSHARD_POSTGRES_PASSWORD='<strong-password>'
```

Database and user default to `devshardd`; override with `DEVSHARD_POSTGRES_DB` and `DEVSHARD_POSTGRES_USER`.

Local PostgreSQL: the HA overlay sets `PGHOST` and `DEVSHARD_STORAGE_MODE`; leave them out of `config.env`. Data path: `${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}/data`. External PostgreSQL: the §2.2 override is required.

### Step 2 - Run multiple `versiond` instances + the router fleet

All deployment files live in `deploy/join`:

| File | Purpose |
| --- | --- |
| `docker-compose.yml` | Base join deployment, supplied with the release |
| `docker-compose.versiond.yml` | Shared PostgreSQL and HA replica settings, supplied with the release |
| `docker-compose.devshard-v5.override.yml` | Catalog filter; create it below |
| `docker-compose.devshard-pg-external.override.yml` | External database settings; create it only for §2.2 |

#### 2.1 Same machine, two replicas

On the join host:

**1. Save the HA settings in `config.env`.**

List approved protocol names from your node. Run in `deploy/join`:

```bash
source ./config.env
curl -fsS "http://127.0.0.1:${API_PORT:-8000}/chain-api/productscience/inference/inference/params" |
  jq -er '.params.devshard_escrow_params.approved_versions[].name'
```

For this release, use the listed names `v4`, `v4.1` and, once available, `v5` in `VERSIOND_VERSIONS`. Exclude pre-HA versions such as `v3`. When updating, keep the existing list; use [Add a protocol](#add-a-protocol) for additions.

Keep the existing join identity and PostgreSQL settings. Add:

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

`VERSIOND_NON_HA_VERSIONS` stays empty. `COMPOSE_FILE` lists every active override. Preserve override filenames and order across updates.

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

`oracle-filter` must stay running; every replica and router reads its catalog.

**Local PostgreSQL:** go to [Step 3](#step-3---start-the-deployment). **External PostgreSQL:** complete §2.2 first. Add replicas only after startup and verification.

#### 2.2 External or managed PostgreSQL

Applies to options A and B. Complete §2.1 first.

`docker-compose.versiond.yml` sets `PGHOST=devshard-postgres`; override it per replica. `config.env` alone does not change the container endpoint.

Create the database and role through the provider, or run:

```sql
CREATE USER devshardd WITH PASSWORD '...';
CREATE DATABASE devshardd OWNER devshardd;
```

Allow access from every replica. Save the credentials in `config.env`. Create `docker-compose.devshard-pg-external.override.yml` with the database host and port:

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

Extra replicas: add the service name with `<<: *external-postgres`. Credentials and storage mode come from the HA overlay.

Set `COMPOSE_FILE` in `config.env`, appending any further overrides:

```bash
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5.override.yml:docker-compose.devshard-pg-external.override.yml
```

### Step 3 - Start the deployment

Run on the join host. Stop at the first failure:

```bash
cd /path/to/gonka/deploy/join
source ./config.env
: "${COMPOSE_FILE:?set the complete Compose file list in config.env}"
./versiond-router-fleet.sh prepare-networks

docker compose up -d --wait --wait-timeout 2100

./versiond-router-fleet.sh apply
```

Complete [Verify the deployment](#step-4---verify-it-works) after startup.

### Step 4 - Verify it works

#### 4.1 Check the running services

Run on the join host after installation or an update. List every local replica in `replicas`; repeat the replica checks on remote hosts.

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
- `verify-admission` and `wait-version` succeed.
- Every replica passes readiness for every selected protocol.
- Public `/devshard/<version>/healthz` returns HTTP 200.

Preflight writes database probes; it does not replace services.

New or replaced remote member: pass the [database check](#check-the-remote-database) before pool admission.

#### 4.2 Check routing failover

Run after installation or a pool change. Repeat for each protocol in `VERSIOND_VERSIONS`. Keep enough ready replicas for every protocol and the load.

1. On A, request the protocol's health endpoint:

   ```bash
   source ./config.env
   version='<protocol from VERSIOND_VERSIONS>'
   url="http://127.0.0.1:${API_PORT:-8000}/devshard/$version/healthz"
   curl -sS -D - -o /dev/null "$url"
   ```

   Expect HTTP 200. Match `X-Upstream-Addr` to a local container or a remote endpoint. Include every local replica:

   ```bash
   docker inspect -f '{{.Name}} {{range .NetworkSettings.Networks}}{{.IPAddress}} {{end}}' versiond versiond2
   ```

2. Stop the replica shown in `X-Upstream-Addr`, on its host:

   ```bash
   replica='<serving-container>'
   docker stop -t 1800 "$replica"
   ```

3. On A, repeat the same request:

   ```bash
   curl -sS -D - -o /dev/null "$url"
   ```

   Expect HTTP 200 and a different `X-Upstream-Addr`.

4. Restore the stopped replica on its host, even if the check failed:

   ```bash
   docker start "$replica"
   ```

   Pass the [service checks](#41-check-the-running-services) before stopping another replica.

This checks routing failover. Routine updates require only the service checks in §4.1.

## Upgrade an existing host

### 1. Prepare the release

1. Confirm the [prerequisites](#prerequisites). Back up PostgreSQL.
2. Save `config.env`, Compose files, endpoint files, current image references and database mounts.
3. Put the new release's join files in the **same deployment directory and Compose project**. Review changes before applying; keep your configuration and overrides.
4. Review image overrides (`VERSIOND_IMAGE`, `VERSIOND_ROUTER_IMAGE`, `PROXY_ROUTER_IMAGE`, `PROXY_POLICY_IMAGE`) in `config.env` and Compose files; they take precedence over release defaults. Remove obsolete values and legacy local `image: ${VERSIOND_IMAGE:?...}` entries. Keep intentional release-compatible custom images, the catalog filter and site settings.
5. Local PostgreSQL: save its current image digest in `DEVSHARD_POSTGRES_IMAGE` (command below).

Preserve during routine updates:

| Keep | Includes |
| --- | --- |
| Identity and replica data | `.inference`, `devshards*/data`, `.pg-bound`, binary caches and per-replica mounts |
| Database connection | Credentials, endpoint and the existing data directory |
| Routing configuration | Fleet slots, networks, membership and router catalog volumes |
| Saved deployment settings | `VERSIOND_VERSIONS`, the ordered `COMPOSE_FILE`, any `COMPOSE_PROJECT_NAME`, and `UPDATE_STATE_DIR` |

Do not replace `config.env` with the new-installation example. Add protocols only after the update is verified. Database moves, PostgreSQL major upgrades and fleet reconfiguration are separate procedures; change the PostgreSQL digest only after confirming cluster compatibility.

First-time HA layout: merge the required [installation settings](#install-a-new-host) into the existing files without running the startup commands. Keep the filter enabled and `VERSIOND_NON_HA_VERSIONS` empty.

<details>
<summary><strong>Find the current PostgreSQL image digest</strong></summary>

Run on the host with local PostgreSQL:

```bash
docker image inspect \
  "$(docker inspect devshard-postgres --format '{{.Image}}')" \
  --format '{{range .RepoDigests}}{{println .}}{{end}}'
```

Save one printed digest as `DEVSHARD_POSTGRES_IMAGE` in `config.env`. If nothing is printed, obtain a published digest for the current compatible image first.

</details>

<details>
<summary><strong>If the old config.env has no VERSIOND_VERSIONS</strong></summary>

Read the list from the running filter before changing or recreating it:

```bash
docker inspect oracle-filter --format '{{json .Config.Env}}' |
  jq -er '.[] | select(startswith("ORACLE_ALLOW=")) | ltrimstr("ORACLE_ALLOW=") | gsub(","; " ") | select(length > 0)'
```

Save the complete output as a quoted `VERSIOND_VERSIONS` value in `config.env`. If unavailable, recover it from the configuration backup. Never substitute the new-installation list.

</details>

Clear previously loaded image values, reload and validate:

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

### 2. Check the database layout

Inspect the running database's data directory and mounts. **Skip the copy** for unchanged external PostgreSQL or an existing persistent path `${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}/data`: leave PostgreSQL running and go to [Run the updater](#3-run-the-updater).

The procedure below moves a local cluster from the old Docker volume at `/var/lib/postgresql/data` to the persistent bind at `/var/lib/postgresql/gonka/data`.

<details>
<summary><strong>One-time copy from the old PostgreSQL volume</strong></summary>

Keep the source cluster's PostgreSQL major version and Alpine/musl image family. Use the image saved in `DEVSHARD_POSTGRES_IMAGE`; the copy does not upgrade PostgreSQL.

Retained containers must already use the filtered catalog and the unchanged database: `docker start` does not apply edited overrides. Containers that need configuration changes require an offline supervisor transition first.

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

Preflight must pass; free space must be at least the source size plus 10%. Preserve the source: no `down`, `down -v`, `rm -v`, pruning or `--renew-anon-volumes` before migration.

Stop **all writers**: every local member below, remote members on their hosts. Wait for all shutdown commands to finish, then refresh the backup before stopping PostgreSQL:

```bash
# Include every local member; stop remote members on their own machines too.
docker stop --time 1800 versiond versiond2
# Refresh the database backup now that application writes have stopped.
docker stop --time 300 devshard-postgres
./versiond-router-fleet.sh prepare-networks
# Starting PostgreSQL copies the old volume into the persistent data directory.
"${dc[@]}" up -d --no-deps devshard-postgres
"${dc[@]}" logs --tail=100 devshard-postgres
```

Wait for PostgreSQL health. **Before restarting writers**, compare the cluster identifier with the value recorded before the copy:

```bash
docker exec devshard-postgres sh -c \
  'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT system_identifier FROM pg_control_system();"'
```

The identifiers must match. On copy failure, keep writers stopped and preserve the source. Inspect the PostgreSQL logs before retrying.

<details>
<summary><strong>If the old volume was already detached</strong></summary>

Complete [directory preparation](#prepare-the-postgresql-directory) if needed. Use the recorded exact volume name with the complete Compose configuration:

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

After verification, keep writers stopped and recreate PostgreSQL without the recovery overlay. Use the same image and configuration for the updater. Keep the source volume and backup. Do not use `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT` for recovery.

</details>

</details>

### 3. Run the updater

Schedule maintenance: replacing the public proxy can interrupt connections.

Routine update: leave PostgreSQL, the filter, replicas and router fleet running. Leave `UPDATE_SKIP_POSTGRES_PROBE` and `UPDATE_ACCEPT_DATABASE_CHANGE` disabled.

<details>
<summary><strong>First transition: prepare the filter and recover stopped members</strong></summary>

Use this block only for a first-time transition or post-copy recovery. Keep the retained protocol list and run:

```bash
./versiond-router-fleet.sh prepare-networks
docker compose up -d --no-deps oracle-filter
```

**After [copying the local database](#2-check-the-database-layout):** verify the copied database and the retained containers' catalog and database settings, then restart writers. Include every stopped local member; start remote members on their hosts:

```bash
docker start versiond versiond2
```

**External PostgreSQL without storage proof:** an installed supervisor that returns HTTP 404 from `/internal/storage-identity` cannot prove its database. Stop all writers; verify the database endpoint, identity and data independently. Start the target supervisors with the complete Compose configuration; pass the [independent database check](#check-the-remote-database) on every member (repeat `--container NAME` for local members) before running the updater. Do not restart the old containers.

Treat timeouts, HTTP 503 and invalid storage proofs as errors to fix, not as unsupported APIs.

</details>

Check every retained protocol on every member before updating. The command falls back to the older health endpoint only on HTTP 404; HTTP 503 and connection errors must be fixed first. After replacement, complete [Verify](#step-4---verify-it-works).

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

Continue only when every check returns HTTP 200. During each replica replacement, at least one other replica must stay ready for every retained protocol.

Run preflight and preview the replacements. Preflight writes database probes; it does not replace services:

```bash
./update-devshard.sh --dry-run
```

Review the proposed images and changes. On a target-directory creation error, complete [directory preparation](#prepare-the-postgresql-directory) and retry. Fix every other error first; do not reset fleet state or enable bypass flags. Then run:

```bash
./update-devshard.sh
```

The updater replaces local services and routing. [Replace remote members](#replace-a-member) one at a time before final verification.

Run the [service checks](#41-check-the-running-services) after the update. Add new protocols through [Add a protocol](#add-a-protocol). Do not rename existing binaries or escrows.

## Add a remote replica

Use a private network between machines. B runs the extra `versiond`; A keeps the join stack. A's public proxy, node and api stay single-instance.

New deployment: finish [startup](#step-3---start-the-deployment) and [verification](#step-4---verify-it-works) on A first.

| Machine | Runs |
| --- | --- |
| A | Local `versiond` replicas + node/api/proxy + router fleet |
| B | `versiond` only — no second dapi with the same keys |
| Shared | PostgreSQL reachable from every `versiond` instance |

Keep B out of the router pool until its checks pass.

### 1. Prepare machine A

Open these ports on A to B over the private network:

- PostgreSQL `5432`, or the external database port.
- Node-manager `9400`.
- Chain RPC/gRPC `26657`, `9090`.
- Filtered catalog `19100`.

Database and keyring credentials must match the running local replicas.

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

External PostgreSQL: drop the `devshard-postgres` entry; B uses the real endpoint. Append this override to A's `COMPOSE_FILE`.

Fresh host: include the override before [startup](#step-3---start-the-deployment). Existing host:

1. Schedule maintenance for the port changes.
2. Stop all local and remote writers before recreating local PostgreSQL; wait for their shutdown commands to finish.
3. Apply the complete Compose configuration.
4. Check readiness and run `./update-devshard.sh --check`.

Do not restart the whole live stack to add a member.

### 2. Configure and start machine B

B runs no `api` or `node`. It uses A's filtered catalog, node-manager and chain endpoints, and the shared database.

New B: use the same release's `deploy/join` files. Existing B: follow [Replace a member](#replace-a-member) and keep its data mounts.

Copy from A into B's `config.env`:

- Identity: `KEY_NAME`, `ACCOUNT_PUBKEY`, `KEYRING_BACKEND`, `KEYRING_PASSWORD`.
- Database credentials: `DEVSHARD_POSTGRES_*`.
- Protocols: `VERSIOND_VERSIONS` and the empty `VERSIOND_NON_HA_VERSIONS`.
- `VERSIOND_IMAGE`, only if A uses a custom image.

On A: `docker cp versiond:/root/.inference/keyring-file .`; copy `keyring-file/` into B's `.inference/`. Keep the supplied read-only keyring mount. B uses its own data directory; never share A's replica data directories.

Add to B's `config.env`, with the real private addresses:

```bash
export NETWORK_NODE_PRIVATE_IP='<A-private-ip>'
export VERSIOND_BIND_IP='<B-private-ip>'
export COMPOSE_FILE=docker-compose.versiond-remote.yml:docker-compose.versiond-remote-filter.yml
```

External PostgreSQL: also set `DEVSHARD_POSTGRES_HOST` and `DEVSHARD_POSTGRES_PORT`. Restrict B's port 8080 to the routers.

Create the filter override in B's `deploy/join`:

```bash
cat > docker-compose.versiond-remote-filter.yml <<'EOF'
services:
  versiond:
    environment:
      VERSIOND_ORACLE_URL: http://${NETWORK_NODE_PRIVATE_IP:?set A's private IP}:19100/versions
EOF
```

Start B and check every selected protocol:

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

### Check the remote database

Verify B's database before pool admission. Install `psql` on B and run:

```bash
cd /path/to/gonka/deploy/join
cp pool-postgres.env.template pool-postgres.env
chmod 600 pool-postgres.env
```

Fill `pool-postgres.env` with the working pool's endpoint and credentials, taken from A or the database administrator **independently of the candidate replica**. Run with the target image active:

```bash
./update-devshard.sh --check-storage --reference-env ./pool-postgres.env
```

Admission requires `Storage check passed` and exit code 0. The check writes database probes. Run one check at a time, with no concurrent updater runs. For other containers, repeat `--container NAME`.

### 3. Add B to the router pool

On A, list every member in `versiond-endpoints.json`. Every address must be reachable from every router slot:

```json
[
  {"id": "local-a", "host": "versiond", "port": 8080},
  {"id": "local-a-2", "host": "versiond2", "port": 8080},
  {"id": "remote-b", "host": "10.0.0.12", "port": 8080}
]
```

Set `export VERSIOND_POOL_ENDPOINTS_FILE=./versiond-endpoints.json` in A's `config.env`; `source ./config.env`. Fresh fleet: run `./versiond-router-fleet.sh apply`. Existing fleet: run during maintenance:

```bash
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
  ./versiond-router-fleet.sh maintenance-rollout
./versiond-router-fleet.sh verify-admission
```

The rollout interrupts new requests. Keep member IDs when replacing a host at the same endpoint. Endpoint-file edits take effect only through the rollout.

DNS pool: set `VERSIOND_POOL_HOST` to private DNS that resolves all members; every router must resolve pool and internal names. Member changes are automatic; pool-name or resolver changes need the maintenance rollout. Mixed ports need explicit endpoints.

Run the [service checks](#41-check-the-running-services) and [routing failover check](#42-check-routing-failover) after adding B.

## Add a local replica

After [startup and verification](#step-3---start-the-deployment):

1. Add `docker-compose.versiond3.yml` to `COMPOSE_FILE`. Further replicas: copy it with a new container name and data directory.
2. Give the new service the same filter and database overrides as `versiond` and `versiond2`. Keep the supplied identity/image settings, shutdown timings, `versiond-pool` alias and the PostgreSQL mount for `.pg-bound`.
3. Start it; run the [service checks](#41-check-the-running-services), including the new replica.
4. Explicit endpoint lists: add it through [membership maintenance](#3-add-b-to-the-router-pool). With pool DNS, no router recreation is needed.

## Operate the deployment

### Manage router slots

Router slots belong to the fleet script; the main project's `docker compose down` does not stop them. Allow up to `VERSIOND_ROUTER_DRAIN_TIMEOUT_SECONDS` (default 1800 seconds) per replaced slot.

The fleet script loads `config.env`.

| Task | Command |
| --- | --- |
| View the fleet | `./versiond-router-fleet.sh status` |
| Stop slot 0 | `./versiond-router-fleet.sh stop 0` |
| Restore slot 0 | `./versiond-router-fleet.sh start 0` |
| Verify routing after a change | `./versiond-router-fleet.sh verify-admission` |
| Apply the release's router image | `./versiond-router-fleet.sh apply` |

Full release update: use the [updater](#3-run-the-updater).

Keep previous stopped containers and catalog volumes until recovery completes. Rerun interrupted operations with the same image and configuration. Pool, resolver or legacy-routing changes: [membership maintenance](#3-add-b-to-the-router-pool).

Whole-machine maintenance: drain the fleet before stopping the main stack:

```bash
./versiond-router-fleet.sh stop-all --maintenance
# Stop the main stack using its complete Compose file list.
# Only when decommissioning, after the main stack is down:
# ./versiond-router-fleet.sh down --maintenance
```

### Restart a member

Restart one member at a time. Keep enough ready replicas for every served protocol and the load. `COMPOSE_FILE` must include every active override:

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

Keep the supplied shutdown timings. The restarted member must pass its checks before restarting another member.

### Replace a member

Preserve the member's database, identity, protocol list and mounts. Keep at least one other replica ready for each required protocol; Compose does not enforce this.

1. Use the target release's files. Review image overrides as in [Prepare the release](#1-prepare-the-release), then run `unset VERSIOND_IMAGE` and `source ./config.env`.
2. Stop and drain the member. For a remote member, remove its explicit endpoint through [membership maintenance](#3-add-b-to-the-router-pool) or from pool DNS before starting the replacement.
3. Run `docker compose pull <service>`, then `docker compose up -d --no-deps --wait --wait-timeout 2100 <service>`. For a remote member, pass the [database check](#check-the-remote-database) before restoring membership.
4. Pass the [service checks](#41-check-the-running-services) before replacing the next member. On failure, restore the previous image and configuration and verify against the current database.

### Remove a member

Stop and drain it. Remove its service or set replicas to zero (`VERSIOND2_REPLICAS=0` for `versiond2`). Remove its DNS or explicit membership; explicit lists need membership maintenance. Run the [service checks](#41-check-the-running-services) for the remaining replicas. Keep data and cache directories until recovery is confirmed.

### Add a protocol

1. Take the protocol name and required host/gateway versions from its release instructions. Confirm the name appears in your node's [approved protocol list](#21-same-machine-two-replicas), then add it to `VERSIOND_VERSIONS` in `config.env` on every host; keep names needed by retained sessions.
2. On A: `source ./config.env`, then `docker compose up -d --no-deps oracle-filter` with the complete `COMPOSE_FILE`.
3. `./versiond-router-fleet.sh wait-version <new-protocol>`, then run the [service checks](#41-check-the-running-services) with the updated list.

No router restart is needed. The next host update applies the saved protocol list to the router fleet and public proxy; schedule it as maintenance.

Keep `proxy-router-state` and each slot's `router-state`. Remove a protocol only during maintenance, after its sessions are no longer needed: a filter change can stop children, while accepted router routes persist by default.

## Troubleshooting

### Resolve a missing database

Restore the recorded database; do not initialize an empty replacement. `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true` is for confirmed first-time HA enablement only; unset it afterwards. If `.pg-bound` exists, restore the database.

### Prepare the PostgreSQL directory

For local PostgreSQL, run in `deploy/join` before migration or after a preflight directory-creation error:

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

The command must succeed. If access errors persist, check parent-directory permissions. Do not change database ownership.

### Resolve an unready member

Check the selected catalog, binary URL/SHA256, child logs and database access. Every selected protocol must pass its readiness check. Do not replace `/readyz` with a single-protocol Docker healthcheck. Do not enable updater bypass flags to hide a failure.

## Reference

### Release reference

**Release:** `devshard-0.2.15-v5`.

Use join files, updater/fleet scripts and published images from one release set. Later releases: follow their guide and upgrade requirements. Keep site settings and retained protocols.

Validate nonstandard deployments and database changes on a data copy first. Extended checks: [acceptance plan](../devshard/docs/ha-host-updater-acceptance.md), [lifecycle test plan](../devshard/docs/devshard-host-ha-test-plan.md).

<details>
<summary>Database capacity and rollback limits</summary>

Preflight checks PostgreSQL connection capacity for configured members. Budget extra connections for custom DNS membership and other clients. Each child defaults to four pool connections plus two health/fence connections; old and new generations can overlap.

Keep the existing HA PostgreSQL database; new binaries may apply forward schema migrations. Legacy SQLite conversion is a separate verified procedure.

Image rollback does not reverse schema migrations or committed writes. Automatic schema and PostgreSQL-to-SQLite downgrades are unsupported. Keep `.pg-bound`; use a binary compatible with the current state, or do a coordinated restore during maintenance. The preserved source cluster holds data only up to the copy time. Validate restart and rollback on a copy of the current state.

</details>
