# High-availability devshard host setup

Run multiple `versiond` replicas against shared PostgreSQL so one replica can fail while another continues serving inference.

| Task | Start here |
| --- | --- |
| Install on a new host | [Before you start](#before-you-start), then [Install](#install-a-new-host) |
| Update an existing HA host | [Upgrade](#upgrade-an-existing-host) |
| Add another machine | [Add a remote replica](#add-a-remote-replica) |
| Restart, replace or remove a member | [Operate the deployment](#operate-the-deployment) |

## Before you start

Use the join files and component images for the [release covered by this guide](#release-reference). Updating these components does not approve new protocols or change which protocols your host must serve.

- Have a working join deployment (`node`, `api`, `proxy`) and the selected release's join files and compatible published images.
- Install Docker Compose **2.24.4+**, Bash, Python 3, `curl` **7.71+**, `jq`, `flock`, `sha256sum` and `timeout`.
- Use the same participant identity on every replica, with separate data directories and one shared writable PostgreSQL database. Run only one dapi with those keys.
- Check the API catalog for each required protocol's approved name, download URL and SHA256. Its artifact must report `postgres` from `--print-storage-mode` in the HA environment and the matching name from `--print-protocol-version`. Use compatible host/gateway artifacts.

**Protocol selection:** save the approved, compatible names in `VERSIOND_VERSIONS`, separated by spaces. Retain every protocol needed by existing sessions. Use this saved list for all checks; add a new protocol separately after the component update succeeds.

**Deployment scope:** run this HA host through the catalog filter below to serve the selected HA protocols. Pre-HA protocols such as `v3` are outside this setup. If you need their existing sessions or any pre-HA process uses this database, complete a separately verified transition before following this procedure.

Use replicated PostgreSQL and replicas on different machines to tolerate a machine failure. Replacing the single public proxy can interrupt connections; schedule it during maintenance and let accepted work finish.

### Select the release images

Set these four entries in `deploy/join/config.env` to the compatible images published for the selected release. Use explicit tags or digests, not `latest`; do not derive image tags from protocol names.

```bash
export VERSIOND_IMAGE='<versiond-image:tag-or-digest>'
export VERSIOND_ROUTER_IMAGE='<haproxy-router-image:tag-or-digest>'
export PROXY_ROUTER_IMAGE='<proxy-router-image:tag-or-digest>'
export PROXY_POLICY_IMAGE='<proxy-image:tag-or-digest>'
```

For an existing host, follow [Upgrade](#upgrade-an-existing-host) before editing these entries. Preserve the rest of `config.env` and the existing overrides; the installation examples below are for new hosts.

<a id="step-1---install-postgres-preferably-ha-itself"></a>
<a id="step-2---configure-the-deployment"></a>
<a id="21-common-configuration-local-replicas"></a>

## Install a new host

Use this section only for a new HA installation. For an existing deployment, follow [Upgrade](#upgrade-an-existing-host).

### 1. Set the deployment configuration

In `deploy/join/config.env`, preserve your existing join settings and fill in the following values. Replace every placeholder; retain all protocols needed by existing sessions.

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

# Deployment settings (retain these across component updates)
export VERSIOND_VERSIONS='<space-separated-approved-HA-protocols>'
export VERSIOND_NON_HA_VERSIONS=""
# Use the same filtered catalog for replicas and both routing tiers.
export VERSIOND_ROUTING_CATALOG_URL=http://oracle-filter:9100/versions
export VERSIOND_ROUTER_FLEET_SLOTS="0 1 2"
export VERSIOND_ROUTER_MIN_READY=2
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5.override.yml
# Append every additional override used by this deployment, in the same order.
# Optional — same as compose default on one machine
export VERSIOND_POOL_HOST=versiond-pool
```

Keep every active override in the ordered `COMPOSE_FILE`. Save fleet settings here too: router slots run in separate Compose projects. Leave `VERSIOND_NON_HA_VERSIONS` empty for this filtered HA layout. Keep existing override filenames across updates, including the `v5` filename used below.

### 2. Create the HA override

Run the following in `deploy/join` to create `docker-compose.devshard-v5.override.yml`:

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
    image: ${VERSIOND_IMAGE:?select the release versiond image}
    environment:
      - VERSIOND_ORACLE_URL=http://oracle-filter:9100/versions
      - VERSIOND_NON_HA_VERSIONS=${VERSIOND_NON_HA_VERSIONS-}
    depends_on:
      oracle-filter:
        condition: service_started
      devshard-postgres:
        condition: service_healthy

  versiond2:
    image: ${VERSIOND_IMAGE:?select the release versiond image}
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

Keep `oracle-filter` running and use its catalog for every replica and both routing tiers.

### 3. Select PostgreSQL

Connect every replica to the **same writable database**, directly or through a pooler in **session mode**. Transaction pooling is unsupported. Leave explicit `PGSSL*` settings unset (`PGSSLMODE=disable` is allowed); if your provider requires explicit TLS settings, use a procedure supporting them instead of disabling TLS.

**Local PostgreSQL:** keep `docker-compose.versiond.yml` and `devshard-postgres-entrypoint.sh` from the release. The database is created at `${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}/data`. Continue to startup.

<a id="22-using-external--managed-postgres-with-the-same-overlay"></a>

<details>
<summary><strong>External or managed PostgreSQL</strong></summary>

Create the database and role through your provider, or run this SQL on your PostgreSQL server after replacing the password:

```sql
CREATE USER devshardd WITH PASSWORD '...';
CREATE DATABASE devshardd OWNER devshardd;
```

Allow access from every replica. Save the credentials in the existing `config.env` block, then create `docker-compose.devshard-pg-external.override.yml` with the actual hostname and port:

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

Repeat the environment and dependency overrides for every extra replica. Leave the `local-postgres` profile disabled. Setting `PGHOST` only in `config.env` does not change the container's endpoint.

Replace `COMPOSE_FILE` in `config.env` with the complete list, appending any further overrides:

```bash
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5.override.yml:docker-compose.devshard-pg-external.override.yml
```

</details>

<a id="step-3---start-the-configured-deployment"></a>
<a id="3-bring-up-the-main-stack-and-router-fleet"></a>

### 4. Start the deployment

Run these commands on the join host. Continue only if each command succeeds:

```bash
cd /path/to/gonka/deploy/join
source ./config.env
: "${COMPOSE_FILE:?set the complete Compose file list in config.env}"
./versiond-router-fleet.sh prepare-networks

docker compose up -d --wait --wait-timeout 2100

./versiond-router-fleet.sh apply
./versiond-router-fleet.sh verify-admission
(
  set -e
  : "${VERSIOND_VERSIONS:?set the approved HA protocol list}"
  for version in $VERSIOND_VERSIONS; do
    ./versiond-router-fleet.sh wait-version "$version"
  done
)
```

The commands start the configured stack and router fleet. Complete [Verify the deployment](#verify-the-deployment) before accepting traffic.

<a id="step-4---verify-it-works"></a>
<a id="4-confirm"></a>

## Verify the deployment

### Check readiness and storage

Run on the join host after installation or an update. Include every local replica in the array; repeat the replica checks on remote hosts.

```bash
cd /path/to/gonka/deploy/join
source ./config.env
(
  set -euo pipefail
  : "${VERSIOND_VERSIONS:?set the approved HA protocol list}"
  replicas=(versiond versiond2)

  docker ps | grep -E 'oracle-filter|versiond|devshard-postgres'
  docker inspect proxy --format '{{range .Config.Env}}{{println .}}{{end}}' | grep VERSIOND_
  # Expect pool=versiond-router-fleet, an empty NON_HA list and the filtered catalog URL.
  docker exec oracle-filter wget -qO- http://127.0.0.1:9100/versions
  # Require every selected protocol with its approved binary URL and SHA256.

  ./versiond-router-fleet.sh status
  ./versiond-router-fleet.sh verify-admission
  for replica in "${replicas[@]}"; do
    docker exec "$replica" wget -qO- http://127.0.0.1:8080/healthz
    docker exec "$replica" wget -qO- http://127.0.0.1:8080/readyz
    docker exec "$replica" wget -qO- http://127.0.0.1:8080/internal/storage-identity
    # Require a nonempty identity and generation targets; check logs for PostgreSQL storage.
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

Require HTTP 200 for readiness and the public route, and only the selected HA protocols in the catalog and running children. A healthy container alone is insufficient. For new or replaced remote members, also complete the [independent database check](#check-the-remote-database) before admission.

### Confirm failover and inference

The following test **stops and restores the local replica serving the probe**. Run it with enough ready survivors and `config.env` loaded. If the selected peer is remote, stop and restore it on its own host instead.

```bash
(
  set -euo pipefail
  replicas=(versiond versiond2)
  read -r probe_version _ <<<"${VERSIOND_VERSIONS:?load config.env first}"
  health_url="http://127.0.0.1:${API_PORT:-8000}/devshard/$probe_version/healthz"
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
    until docker exec "$1" wget -qO- "http://127.0.0.1:8080/readyz?version=$2"; do
      sleep 2
    done' _ "$serving_replica" "$probe_version"
)
# X-Upstream-Addr identifies the final serving peer, not a retry list.
# For a remote selected peer, stop and restore it on its own host.
```

Then test a funded escrow for **each served protocol**: record inference, the serving member, committed nonce and cost; stop that member and continue the **same session** on a survivor. Verify state and accounting. Test a new escrow when adding a new protocol.

Complete the [drain, crash and restart checks](../devshard/docs/devshard-host-ha-test-plan.md). After database fence loss, require the affected child to exit and be replaced before it receives traffic again. A crash can interrupt an in-flight stream; health probes do not establish session continuity.

<a id="26-upgrade-an-existing-ha-deployment-to-v5"></a>

## Upgrade an existing host

### 1. Prepare the release

1. Confirm the host meets the [deployment requirements](#before-you-start). Back up PostgreSQL and save `config.env`, all Compose/endpoint files, current image references and approved artifact URLs/SHA256. Record mounts and a working escrow for every retained protocol.
2. Obtain the target release's join files in the **same deployment directory and Compose project**. Keep your configuration and overrides; review changes against the saved files before applying them. Preserve `.inference`, `devshards*/data`, `.pg-bound`, binary caches, router catalog volumes and `UPDATE_STATE_DIR`.
3. Edit the four [release image entries](#select-the-release-images). Preserve identity, database credentials/endpoint, per-replica mounts, fleet slots/networks/membership, `VERSIOND_VERSIONS` and the full ordered `COMPOSE_FILE`. Keep any custom `COMPOSE_PROJECT_NAME` in `config.env`. Do not copy the new-installation configuration over your existing files.
4. For local PostgreSQL, retain its current compatible image explicitly in `DEVSHARD_POSTGRES_IMAGE`; do not adopt a changed Compose default. A database move, PostgreSQL major upgrade or fleet reconfiguration requires a separate procedure.
5. Verify every retained artifact as described in [Before you start](#before-you-start). Keep the currently serving protocol set throughout the update; add new protocols only after retained inference passes.

If introducing this HA layout for the first time, merge the required [installation settings](#install-a-new-host) into your existing files without running its startup commands. Keep the filter enabled and `VERSIOND_NON_HA_VERSIONS` empty.

After editing, reload the configuration and validate it in the existing join checkout:

```bash
cd /path/to/gonka/deploy/join
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

Before stopping or recreating PostgreSQL, run the following and record the source volume and system identifier:

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

Use its recorded exact name with the complete Compose configuration:

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

After checking the migrated database, keep writers stopped and recreate PostgreSQL once without the recovery overlay. Keep that same image/configuration for the updater. Retain the source volume and backup; never use `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT` to recover a missing database.

</details>

</details>

<a id="3-run-the-updater-with-the-complete-deployment-configuration"></a>

### 3. Run the updater

Close public traffic and let accepted work finish before cutover. Keep it closed until the retained-session tests pass. For a routine update, leave PostgreSQL, the filter, replicas and router fleet running. Leave `UPDATE_SKIP_POSTGRES_PROBE` and `UPDATE_ACCEPT_DATABASE_CHANGE` disabled.

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

Check every retained protocol on every member before continuing. Only for an installed supervisor known to lack readiness (HTTP 404), use `/$version/healthz` instead of `/readyz?version=$version` in this pre-update check; HTTP 503 is a failure. After replacement, all readiness and storage checks in [Verify](#verify-the-deployment) are required.

```bash
# Include every local member; repeat on remote hosts.
(
  set -e
  : "${VERSIOND_VERSIONS:?set the approved HA protocol list}"
  for replica in versiond versiond2; do
    for version in $VERSIOND_VERSIONS; do
      docker exec "$replica" wget -qO- "http://127.0.0.1:8080/readyz?version=$version"
    done
  done
)
```

Require HTTP 200 for every check and keep a ready survivor for each retained protocol. After a database copy, also verify the recorded sessions against the migrated database.

Run the preflight checks. They write database probes without replacing services:

```bash
(
  set -e
  ./update-devshard.sh --check
  ./update-devshard.sh --dry-run
)
```

Review the proposed changes. If either check fails, resolve the cause before continuing; do not reset fleet state or add bypass flags. After both succeed, run the update and admission checks:

```bash
(
  set -e
  ./update-devshard.sh
  ./versiond-router-fleet.sh verify-admission
  : "${VERSIOND_VERSIONS:?set the approved HA protocol list}"
  for version in $VERSIOND_VERSIONS; do
    ./versiond-router-fleet.sh wait-version "$version"
  done
)
```

The updater replaces local services and routing. Update remote members using [Replace a member](#replace-a-member), one at a time, before the final checks.

Run inference with each retained escrow through the public route and complete [Verify](#verify-the-deployment). Reopen traffic only after these checks pass. To enable a newly approved protocol, follow [Add a protocol](#add-a-protocol); use a new escrow, without renaming old binaries or escrows.

If interrupted, follow [Recover a failed update](#recover-a-failed-update).

<a id="23-multiple-machines-recommended-true-host-ha"></a>

## Add a remote replica

Keep the existing join stack and router fleet on **A**. Run only `versiond` on **B**, using the same PostgreSQL and filtered catalog. Keep B out of router membership until its checks pass.

### 1. Prepare machine A

Allow B to reach PostgreSQL (`5432` by default), node-manager (`9400`), chain RPC/gRPC (`26657`, `9090`) and the filtered catalog (`19100` below) over a private network. Confirm credentials match the running local replicas.

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

On a fresh host, include it before [startup](#4-start-the-deployment). On an existing host, schedule maintenance: close traffic, finish work, stop all writers before recreating local PostgreSQL, apply the complete configuration, then check readiness and run `./update-devshard.sh --check` before reopening. Do not restart the whole live stack just to add a member.

Run on B, replacing the private IP and database endpoint:

```bash
curl -fsS http://<A-private-ip>:19100/versions
docker run --rm --network host --read-only \
  --entrypoint pg_isready "${DEVSHARD_POSTGRES_IMAGE:-postgres:16-alpine}" \
  -h <A-private-ip> -p 5432                   # use the shared DB endpoint
```

Require access to all listed ports. `pg_isready` only checks reachability; verify database identity separately below.

### 2. Configure and start machine B

- Copy A's participant identity: `KEY_NAME`, `ACCOUNT_PUBKEY`, `KEYRING_PASSWORD` and keyring. On A, run `docker cp versiond:/root/.inference/keyring-file .`; transfer `keyring-file/` into B's `.inference/`. Keep the read-only mount below; do not run a second dapi.
- Save the same selected image, database credentials and **current `VERSIOND_VERSIONS` list** in B's `config.env`. Put connection endpoints in the container environment as shown below.
- Use B's own data/cache directories and bind port 8080 to its private IP, reachable only by the routers.

Save the following as `deploy/join/docker-compose.versiond-remote.yml` on B, replacing private IPs and PostgreSQL endpoints:

```yaml
services:
  versiond:
    image: ${VERSIOND_IMAGE:?select the same versiond image as A}
    container_name: versiond
    environment:
      # Same filtered catalog as on A
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
      test: ["CMD", "/bin/busybox", "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:8080/readyz"]
      interval: 2s
      timeout: 3s
      retries: 3
      start_period: 30m
    stop_grace_period: 30m
    restart: always
```

Run on B:

```bash
mkdir -p devshards-remote/{bin,data}
source ./config.env
docker compose -f docker-compose.versiond-remote.yml up -d --wait --wait-timeout 2100
(
  set -e
  : "${VERSIOND_VERSIONS:?set the approved HA protocol list}"
  for version in $VERSIOND_VERSIONS; do
    curl -fsS "http://<B-private-ip>:8080/readyz?version=$version"
  done
)
```

Require a healthy container and HTTP 200 for every selected protocol. The Docker healthcheck uses `/readyz`; the separate checks verify the actual protocol set.

<a id="check-bs-database-before-admitting-it-to-the-pool"></a>

### Check the remote database

Install `psql` on the host being checked and run:

```bash
cd /path/to/gonka/deploy/join
cp pool-postgres.env.template pool-postgres.env
chmod 600 pool-postgres.env
```

Fill `pool-postgres.env` with the known working pool's PostgreSQL endpoint and credentials, obtained from A or the database administrator **independently of the candidate replica**. With the intended image running, execute:

```bash
./update-devshard.sh --check-storage --reference-env ./pool-postgres.env
```

Require `Storage check passed` and exit code 0 before admission. This writes control values through the running children and verifies the reference database. Use `--container NAME` for another container, repeating it for several local members. Check one host at a time with no updater running elsewhere.

<a id="on-the-machine-that-runs-the-router-fleet-usually-a"></a>

### 3. Add B to the router pool

On A, list every local and remote member in `versiond-endpoints.json`; ensure each address is reachable from every router slot:

```json
[
  {"id": "local-a", "host": "versiond", "port": 8080},
  {"id": "local-a-2", "host": "versiond2", "port": 8080},
  {"id": "remote-b", "host": "10.0.0.12", "port": 8080}
]
```

Set `export VERSIOND_POOL_ENDPOINTS_FILE=./versiond-endpoints.json` in A's `config.env`. For a fresh fleet, use `./versiond-router-fleet.sh apply`. For an existing fleet, run during maintenance:

```bash
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
  ./versiond-router-fleet.sh maintenance-rollout
./versiond-router-fleet.sh verify-admission
```

This applies membership with an interruption to new requests. Keep member IDs stable when replacing a host at the same endpoint; editing the file alone does not update the running fleet.

For a DNS pool instead, set `VERSIOND_POOL_HOST` to private DNS resolving all reachable members. Every router must resolve the pool and internal names. DNS member changes are discovered automatically; changing the pool name/resolver requires the maintenance command above. Use explicit endpoints for differing ports.

Finally, test a real session served by B: identify it using `X-Upstream-Addr`, stop B's `versiond`, and continue the same session on a survivor with its committed state intact. Restore B and repeat readiness checks before using it again.

## Operate the deployment

<a id="24-adding-more-replicas"></a>

### Add a local replica

1. Copy the supplied `docker-compose.versiond3.yml` with unique container names and data paths. Keep its PostgreSQL mount so the lost-database guard can inspect `.pg-bound`; add the file to the complete `COMPOSE_FILE`.
2. Extend the HA and external-PostgreSQL overrides for this service: use the same image, filtered catalog, identity, HA/database environment and stop timings. Add alias `versiond-pool` on the router back network.
3. Start the member, require readiness for every selected protocol and verify inference. DNS discovery needs no router recreation; use [membership maintenance](#3-add-b-to-the-router-pool) for explicit endpoint lists.

<a id="25-operating-versiond-members"></a>

### Restart a member

Keep enough ready survivors for every served protocol and the load. Run with every active override in `COMPOSE_FILE`:

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

Keep the default 5-second announcement, 25-minute host budget and 30-minute Compose grace. For custom values, use units and keep announcement at least as long as router detection (never `0s` behind HAProxy). Announcement plus child termination grace (default 10 minutes) must stay below the host budget; requests exceeding the budget can be interrupted.

### Replace a member

1. Set the compatible image in that host's existing `config.env`, then run `source ./config.env`. Preserve its database, identity, protocol list and mounts. Keep a ready survivor for every required protocol; Compose does not enforce a reserve.
2. Stop and drain the member. For a remote member, remove its explicit endpoint using [membership maintenance](#3-add-b-to-the-router-pool), or remove it from pool DNS, before starting its replacement.
3. Recreate only that service with `up -d --no-deps`. For a remote member, pass the [database check](#check-the-remote-database) before restoring membership.
4. Verify every required protocol and inference before replacing another member. On failure, restore the prior image/configuration and verify against the current database.

### Remove a member

Stop and drain it, remove its service or set replicas to zero (`VERSIOND2_REPLICAS=0` for `versiond2`), then remove its DNS/explicit membership. Use membership maintenance for explicit lists. Verify remaining sessions and retain data/cache directories until recovery is confirmed.

### Add a protocol

1. Verify approval, artifact compatibility and matching gateway support as in [Before you start](#before-you-start). Add the protocol to `VERSIOND_VERSIONS` in `config.env` on every host; keep existing names needed by retained sessions.
2. On A, run `source ./config.env`, then `docker compose up -d --no-deps oracle-filter` using the complete `COMPOSE_FILE`.
3. Run `./versiond-router-fleet.sh wait-version <new-protocol>`, then complete [Verify](#verify-the-deployment) with the updated list and a new escrow. Router replacement is unnecessary.

Preserve `proxy-router-state` and each slot's `router-state`. Remove protocols only during maintenance after their sessions are no longer needed: changing the filter can stop children, while previously accepted router routes are retained by default.

<a id="step-5---versiond-router-fleet-operations"></a>

### Manage router slots

Use the fleet script; the main project's `docker compose down` does not stop router slots. Allow up to `VERSIOND_ROUTER_DRAIN_TIMEOUT_SECONDS` (default 1800 seconds) per replaced slot because catalog refresh can delay shutdown.

Run to stop/restart slot 0; use `apply` after selecting the new compatible router image in `config.env`:

```bash
source ./config.env
./versiond-router-fleet.sh status
./versiond-router-fleet.sh stop 0
./versiond-router-fleet.sh start 0
./versiond-router-fleet.sh verify-admission

# After selecting a compatible router image in config.env:
source ./config.env
./versiond-router-fleet.sh apply
```

Preserve previous stopped containers and catalog volumes until recovery is complete. Rerun interrupted operations with the same image/configuration. Use [membership maintenance](#3-add-b-to-the-router-pool) for pool, resolver or legacy-routing changes.

For whole-machine maintenance, drain the fleet before stopping the main stack:

```bash
./versiond-router-fleet.sh stop-all --maintenance
# Stop the main stack using its complete Compose file list.
# Only when decommissioning, after the main stack is down:
# ./versiond-router-fleet.sh down --maintenance
```

## Troubleshooting

### Recover a failed update

Inspect logs and fix the cause, then rerun normally with the same complete Compose configuration and persistent `UPDATE_STATE_DIR` (default under `~/.local/state/gonka/updater/`). A normal run recovers interrupted replacements; `--check` reports pending recovery.

For database-history errors, verify the selected data directory before retrying. Keep the preserved source cluster unchanged and do not bypass migration markers. Retain previous join files and backups: restoring a container image does not undo host-file edits, committed writes or schema migrations.

### Resolve a missing database

Restore the recorded database rather than initializing an empty replacement. `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true` is only for confirmed first-time HA enablement: use it once, then unset it. If `.pg-bound` exists, restore the database; the flag is not a recovery procedure.

### Resolve an unready member

Check the selected catalog, binary URL/SHA256, child logs and database access. Require every selected protocol to pass its readiness check. Do not substitute a single-protocol Docker healthcheck for `/readyz`, or enable updater bypass flags to hide a failure.

## Reference

### Release reference

**Release:** `devshard-0.2.15-v5`. Core checked at `39240311fb` with gateway and storage fixes from [PR #1730](https://github.com/gonka-ai/gonka/pull/1730) through `bf4de2d21`; versiond fleet and updater checked in the integration from [PR #1611](https://github.com/gonka-ai/gonka/pull/1611) through `ab2bb5171` on 2026-09-09.

Use join files, updater/fleet scripts and published component images from a compatible release set. For a later release, use its version of this guide and review its upgrade requirements before changing images. Keep site settings and the retained protocol list across releases.

The guide is a draft for host operators. Approval and image availability must be checked for the chosen release; green infrastructure tests do not establish a successful migration of your real sessions. Test the upgrade on a copy of the actual state before production, using the [acceptance plan](../devshard/docs/ha-host-updater-acceptance.md).

<details>
<summary>Deployment layout</summary>

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

<details>
<summary>Database capacity and rollback limits</summary>

Budget connections for every protocol and overlapping generation: `PG_POOL_MAX_CONNS` defaults to 4 per child, plus two health/fence connections. Include all local/remote members and keep spare capacity.

Existing HA PostgreSQL data stays in the same database; new binaries may apply forward schema migrations. Conversion of legacy SQLite state is outside this guide and requires a separate verified procedure.

A wire-compatible **same-protocol** artifact update is different from adding a protocol: PostgreSQL children can overlap while the candidate starts and, when supported, reports `recovery_complete=true` (default `VERSIOND_RECOVERY_TIMEOUT=30m`). Check `recovery_failed` and logs separately: completion does not mean every session recovered successfully. Failed preparation keeps the predecessor serving. SQLite/hybrid replacements drain and stop before starting; older candidates without the recovery field skip that recovery wait. Keep pool capacity for overlapping children. Do not repeatedly change the approved artifact while predecessors are still draining.

Restoring an old image does not undo database migrations or later committed writes. There is no automatic PostgreSQL-to-SQLite or schema downgrade; retain `.pg-bound`, use a binary known to read the current state, or perform a coordinated restore during maintenance. The preserved source cluster is a recovery source from the copy time, not a current replica after writes to the migrated database. Validate restart/rollback on a copy of the actual state before relying on it.

</details>
