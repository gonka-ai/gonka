# High-availability devshard host setup

Devshard inference that stays available, backed by multiple `versiond` instances.

## Contents

- [Why this matters](#why-this-matters)
- [Release and images](#release-and-images)
- [Prerequisites](#prerequisites)
- [Install a new host](#install-a-new-host)
- [Upgrade an existing host](#upgrade-an-existing-host)
- [Add a remote replica](#add-a-remote-replica) — requires storage-proof support; unavailable with published v4/v4.1
- [Add a local replica](#add-a-local-replica)
- [Operate the deployment](#operate-the-deployment)
- [Troubleshooting](#troubleshooting)

## Why this matters

A single `versiond` process is a single point of failure (SPOF): if that machine or container dies, gateways cannot reach your host for that protocol version.

Run multiple `versiond` instances behind the router fleet, with shared PostgreSQL. If one instance fails, the routers send requests to the remaining instances.

```text
Public proxy (/devshard/...)
        │
        ▼
 versiond-router fleet
        │
        ├── versiond  ──► devshardd ──┐
        ├── versiond2 ──► devshardd ──┤
        ├── versiond3 ──► devshardd ──┼── shared PostgreSQL
        │       ⋮                    │
        └── versiondN ──► devshardd ──┘
```

## Release and images

This guide covers [Devshard v5.0.1](https://github.com/gonka-ai/gonka/releases/tag/devshard%2Fv5.0.1).

Edit `deploy/join/config.env`. Set the variables below to the shown values.

```bash
export VERSIOND_IMAGE=ghcr.io/gonka-ai/versiond:0.2.15-devshard-v5
export VERSIOND_ROUTER_IMAGE=ghcr.io/gonka-ai/versiond-router:0.2.15-devshard-v5
export PROXY_ROUTER_IMAGE=ghcr.io/gonka-ai/proxy-router:0.2.15-devshard-v5
export PROXY_POLICY_IMAGE=ghcr.io/gonka-ai/proxy:0.2.15-devshard-v5
export ORACLE_FILTER_IMAGE=python:3.12-alpine
```

## Prerequisites

1. Working `node` + `api` (dapi) + `proxy` on the host (standard join deployment).
2. Join files and host/gateway images from the [same release](#release-and-images).
3. Same participant identity on every HA `versiond` replica:
   - Same `KEY_NAME` and keyring.
   - Same `ACCOUNT_PUBKEY`.
4. Shared PostgreSQL; a separate data directory for each replica. Do not start a second dapi with the same keys.
5. Docker Compose **2.24.4+**, Bash, Python 3, Git, `tar`, `curl`, `jq`, `flock`, `sha256sum`, `timeout`, `xargs`. Remote hosts also need `ssh`, `psql` and SSH access between A and B.

Only put PostgreSQL-capable versions (v4+) into the HA pool. Migration from pre-HA deployments such as `v3` is not covered here.

## Install a new host

### Step 1 - Install PostgreSQL (preferably HA itself)

HA `versiond` removes dependence on one **app** server, but if PostgreSQL is a single VM, **PostgreSQL becomes your new SPOF**. Prefer a managed or replicated database.

#### Choose a database

**Option A — Managed PostgreSQL (recommended).** Select an HA configuration and create a database and user.

**Option B — Self-managed PostgreSQL.** Install PostgreSQL on a dedicated host or cluster, create the role/database, and configure replication and failover for database HA.

For A or B, obtain the primary endpoint, port, database, user and password. Allow connections from every replica and configure [§2.2](#22-external-or-managed-postgresql).

**Option C — Local Compose PostgreSQL.** `docker-compose.versiond.yml` starts `devshard-postgres` on the join host. If the machine dies, the database dies with it.

External PostgreSQL: use a direct connection or session-mode pooling. Transaction pooling and explicit `PGSSL*` settings (except `PGSSLMODE=disable`) are unsupported. Providers requiring explicit TLS settings are outside this procedure; do not disable required TLS.

#### Where to put PostgreSQL settings

Run in `deploy/join`. For a new local database, choose a new password. For an existing database, use its current password from your database administrator:

```bash
(
  set -euo pipefail
  read -r -s -p 'PostgreSQL password: ' DEVSHARD_POSTGRES_PASSWORD
  printf '\n'
  [[ -n "$DEVSHARD_POSTGRES_PASSWORD" ]] || exit 1
  DEVSHARD_POSTGRES_PASSWORD="$DEVSHARD_POSTGRES_PASSWORD" python3 <<'PYTHON'
import os, pathlib, re, shlex
path = pathlib.Path("config.env")
name = "DEVSHARD_POSTGRES_PASSWORD"
lines = path.read_text().splitlines()
lines = [line for line in lines if not re.match(r"^\s*(?:export\s+)?" + name + r"\s*=", line)]
lines.append("export " + name + "=" + shlex.quote(os.environ[name]))
path.chmod(0o600)
path.write_text("\n".join(lines) + "\n")
PYTHON
)
```

Database and user default to `devshardd`; override with `DEVSHARD_POSTGRES_DB` and `DEVSHARD_POSTGRES_USER`.

Local data path: `${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}/data`. For external PostgreSQL, complete [§2.2](#22-external-or-managed-postgresql).

#### Prepare the PostgreSQL directory

Local Compose PostgreSQL only. Run in `deploy/join` before first startup or migration:

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

If the command fails, stop and inspect the directory permissions:

```bash
ls -ld -- "$(dirname "$pg_dir")" "$pg_dir"
```

Do not change database ownership manually.

### Step 2 - Run multiple `versiond` instances + the router fleet

Base installation files in `deploy/join`:

| File | Purpose |
| --- | --- |
| `docker-compose.yml` | Base join deployment, supplied with the release |
| `docker-compose.versiond.yml` | Shared PostgreSQL and HA replica settings, supplied with the release |
| `docker-compose.devshard-v5.override.yml` | Catalog filter; create it below |
| `docker-compose.devshard-pg-external.override.yml` | External database settings; create it only for §2.2 |

#### 2.1 Same machine, two replicas

**1. Save the HA settings in `config.env`.**

Apply [Release and images](#release-and-images) first.

List approved protocol names on the node:

```bash
curl -fsS http://127.0.0.1:9100/versions | jq -er '.versions[].name'
```

For this release, use the listed names `v4`, `v4.1` and, once available, `v5` in `VERSIOND_VERSIONS`. Exclude pre-HA versions such as `v3`. When updating, keep the existing list; use [Add a protocol](#add-a-protocol) for additions.

Edit these variables in `config.env`. Keep the existing identity and database settings.

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

Keep `VERSIOND_NON_HA_VERSIONS` empty. Include every override in `COMPOSE_FILE`, in the same order on every run.

**2. Create `docker-compose.devshard-v5.override.yml` in `deploy/join`.**

```bash
cat > docker-compose.devshard-v5.override.yml <<'EOF'
services:
  # Shared catalog of selected HA protocols for replicas and routers.
  oracle-filter:
    container_name: oracle-filter
    image: ${ORACLE_FILTER_IMAGE:?set the component images in config.env}
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

**Local PostgreSQL:** go to [Step 3](#step-3---start-the-deployment). **External PostgreSQL:** complete §2.2 first.

#### 2.2 External or managed PostgreSQL

Applies to options A and B. Complete §2.1 first.

Create the database and role through the provider, or run:

```sql
CREATE USER devshardd WITH PASSWORD '...';
CREATE DATABASE devshardd OWNER devshardd;
```

Allow access from every replica. Save the credentials as described in [Where to put PostgreSQL settings](#where-to-put-postgresql-settings). Create `docker-compose.devshard-pg-external.override.yml` with the database host and port:

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

Extra replicas: add the service name with `<<: *external-postgres`.

Set `COMPOSE_FILE` in `config.env`, appending any further overrides:

```bash
export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.devshard-v5.override.yml:docker-compose.devshard-pg-external.override.yml
```

### Step 3 - Start the deployment

Run on the join host. Stop at the first failure:

```bash
(
  set -euo pipefail
  cd /path/to/gonka/deploy/join
  source ./config.env
  : "${COMPOSE_FILE:?set the complete Compose file list in config.env}"
  ./versiond-router-fleet.sh prepare-networks

  docker compose up -d --wait --wait-timeout 2100

  ./versiond-router-fleet.sh apply
)
```

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

All commands must succeed; readiness and public health endpoints must return HTTP 200. Keep the supplied `/readyz` Docker healthcheck.

New or replaced remote member: pass the [database check](#check-the-remote-database) before pool admission.

#### 4.2 Check routing failover

Run after installation or a pool change, for each protocol in `VERSIOND_VERSIONS`. The remaining replicas must be able to handle the load.

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

For routine updates, run only the service checks in §4.1.

## Upgrade an existing host

Upgrading a host serving v4/v4.1 requires downtime.

This procedure requires a PostgreSQL-backed host serving only v4 and later protocols, with site settings in `config.env` and separate Compose overrides in a Git checkout. Hosts running v3 require a separate migration procedure.

### Back up PostgreSQL and deployment files

Run in the existing host's `deploy/join` before replacing release files. Leave `versiond` running and save the printed backup directory.

```bash
(
  set -euo pipefail
  source ./config.env
  mkdir -p backups
  BACKUP_DIR=$(mktemp -d "$PWD/backups/pre-update.XXXXXXXX")
  printf 'Backup directory: %s\n' "$BACKUP_DIR"
  git rev-parse HEAD > "$BACKUP_DIR/previous-commit"
  docker inspect versiond > "$BACKUP_DIR/versiond.json"
  python3 - "$BACKUP_DIR" <<'PYTHON'
import json, os, pathlib, shutil, subprocess, sys
backup = pathlib.Path(sys.argv[1])
container = json.loads((backup / "versiond.json").read_text())[0]
labels = container["Config"]["Labels"]
files = [pathlib.Path("config.env").resolve()]
files += [pathlib.Path(p) for p in labels["com.docker.compose.project.config_files"].split(",")]
if os.environ.get("VERSIOND_POOL_ENDPOINTS_FILE"):
    files.append(pathlib.Path(os.environ["VERSIOND_POOL_ENDPOINTS_FILE"]).resolve())
manifest, local_files = [], []
for src in dict.fromkeys(files):
    if not src.is_file():
        sys.exit(f"Missing deployment file: {src}")
    dst = backup / "files" / str(src).lstrip("/")
    dst.parent.mkdir(parents=True, exist_ok=True)
    shutil.copy2(src, dst)
    manifest.append(str(src))
    tracked = subprocess.run(["git", "ls-files", "--error-unmatch", str(src)],
                             stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL).returncode == 0
    if not tracked or src.name == "config.env" or str(src) == str(pathlib.Path(os.environ.get("VERSIOND_POOL_ENDPOINTS_FILE", "")).resolve()):
        local_files.append(str(src))
(backup / "files.json").write_text(json.dumps(manifest, indent=2))
(backup / "local-files.json").write_text(json.dumps(local_files, indent=2))
env = dict(item.split("=", 1) for item in container["Config"]["Env"])
pg = {k: v for k, v in env.items() if k.startswith("PG") and k != "PG_POOL_MAX_CONNS"}
if not pg.get("PGHOST") or any("\n" in v or "\r" in v for v in pg.values()):
    sys.exit("Expected an existing PostgreSQL-backed versiond")
(backup / "postgres.env").write_text("".join(f"{k}={v}\n" for k, v in pg.items()))
(backup / "project").write_text(labels["com.docker.compose.project"])
PYTHON
  project=$(cat "$BACKUP_DIR/project")
  docker ps -aq --filter "label=com.docker.compose.project=$project" |
    xargs -r docker inspect > "$BACKUP_DIR/containers.json"
  if docker inspect devshard-postgres > "$BACKUP_DIR/postgres.json" 2>/dev/null; then
    [[ $(docker inspect devshard-postgres --format '{{index .Config.Labels "com.docker.compose.project"}}') == "$project" ]]
    docker exec devshard-postgres sh -c \
      'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT system_identifier FROM pg_control_system();"' \
      > "$BACKUP_DIR/postgres-system-identifier"
  fi
  docker pull postgres:16-alpine
  pg_args=(--rm --network container:versiond --env-file "$BACKUP_DIR/postgres.env")
  pg_major=$(docker run "${pg_args[@]}" postgres:16-alpine \
    psql -XAtw -v ON_ERROR_STOP=1 -c "SELECT current_setting('server_version_num')::int / 10000")
  [[ "$pg_major" =~ ^[1-9][0-9]*$ ]]
  pg_client="postgres:$pg_major-alpine"
  docker pull "$pg_client"
  docker run "${pg_args[@]}" "$pg_client" pg_dump -w -Fc > "$BACKUP_DIR/database.dump"
  docker run -i --rm "$pg_client" pg_restore --list < "$BACKUP_DIR/database.dump" \
    > "$BACKUP_DIR/database.contents"
  test -s "$BACKUP_DIR/database.contents"
  printf '%s\n' "$pg_client" > "$BACKUP_DIR/pg-client"
  echo 'Backup complete.'
)
```

Continue only after `Backup complete.`. The backup contains passwords; keep it private. The archive check does not test a full restore.

### 1. Prepare the release

Complete the [backup](#back-up-postgresql-and-deployment-files). In `deploy/join`, enter its printed directory. Keep site settings in `config.env` and separate, untracked Compose overrides.

```bash
(
  set -euo pipefail
  read -r -p 'Pre-update backup directory: ' BACKUP_DIR
  test -d "$BACKUP_DIR"
  test -s "$BACKUP_DIR/database.contents"
  test -s "$BACKUP_DIR/previous-commit"
  git diff --binary > "$BACKUP_DIR/tracked.patch"
  git diff --cached --binary > "$BACKUP_DIR/staged.patch"
  git diff --exit-code
  git diff --cached --exit-code
  git fetch https://github.com/gonka-ai/gonka.git refs/tags/devshard/v5.0.1
  git switch --detach FETCH_HEAD
  # Confirm that config.env and the separate site overrides survived unchanged.
  python3 - "$BACKUP_DIR" <<'PYTHON'
import json, pathlib, sys
backup = pathlib.Path(sys.argv[1])
for name in json.loads((backup / "local-files.json").read_text()):
    path = pathlib.Path(name)
    if not path.is_file() or path.read_bytes() != (backup / "files" / name.lstrip("/")).read_bytes():
        sys.exit(f"Local configuration changed: {path}; stop before updating containers")
PYTHON
)
```

If tracked files have local edits, this step stops. Automatic merging is not covered; keep the saved patches and do not run `git reset --hard`.

Apply [Release and images](#release-and-images). Site overrides must use those image variables rather than hard-coded application images.

Do not replace `config.env` with the new-installation example. Update first; add protocols afterwards. Do not combine this update with a database move, PostgreSQL major upgrade or fleet reconfiguration.

<details>
<summary><strong>If the old config.env has no VERSIOND_VERSIONS</strong></summary>

Read the protocols running on the existing `versiond`:

```bash
(
  set -euo pipefail
  versions=$(docker exec versiond /bin/busybox wget -qO- http://127.0.0.1:8080/healthz |
    jq -er 'if length > 0 and all(.[]; .status == "running" and (.name == "v4" or .name == "v4.1" or .name == "v5")) then map(.name) | join(" ") else error("Expected running HA protocols only; stop") end')
  VERSIOND_VERSIONS="$versions" python3 <<'PYTHON'
import os, pathlib, re, shlex
path = pathlib.Path("config.env")
name = "VERSIOND_VERSIONS"
lines = path.read_text().splitlines()
lines = [line for line in lines if not re.match(r"^\s*(?:export\s+)?" + name + r"\s*=", line)]
lines.append("export " + name + "=" + shlex.quote(os.environ[name]))
path.chmod(0o600)
path.write_text("\n".join(lines) + "\n")
PYTHON
)
```

Stop if any protocol is not running or the list includes pre-HA versions.

</details>

First HA setup: complete [§2.1](#21-same-machine-two-replicas) to set the HA variables and create the filter override; retain the current protocol list. For an external database, also complete [§2.2](#22-external-or-managed-postgresql) using the existing database. Then continue below; do not run installation Steps 3–4.

Reload and validate:

```bash
(
  set -euo pipefail
  cd /path/to/gonka/deploy/join
  source ./config.env
  # Keep every active override in the ordered COMPOSE_FILE saved in config.env.
  : "${COMPOSE_FILE:?set the complete Compose file list in config.env}"
  : "${VERSIOND_VERSIONS:?retain the approved HA protocol list in config.env}"
  docker compose config --quiet
)
```

### 2. Check the database layout

External PostgreSQL: leave it running and go to [Update the deployment](#3-update-the-deployment). For local PostgreSQL, inspect the data directory and mounts:

```bash
docker inspect devshard-postgres --format '{{json .Mounts}}'
docker inspect devshard-postgres --format '{{json .Config.Env}}' |
  jq -r '.[] | select(startswith("PGDATA="))'
```

An existing persistent path `${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}/data` needs no copy. A Docker volume at `/var/lib/postgresql/data` requires the procedure below.

<details>
<summary><strong>One-time copy from the old PostgreSQL volume</strong></summary>

For one join host with all writers listed in `replicas`. Use the PostgreSQL image supplied by the release for the existing PostgreSQL 16 Alpine cluster. Complete [directory preparation](#prepare-the-postgresql-directory) first.

Run in `deploy/join`:

```bash
(
  set -euo pipefail
  source ./config.env
  : "${COMPOSE_FILE:?set the complete Compose file list}"
  replicas=(versiond versiond2)
  mkdir -p backups
  backup_dir=$(mktemp -d "$PWD/backups/postgres-copy.XXXXXXXX")
  echo "Backup directory: $backup_dir"
  docker inspect devshard-postgres > "$backup_dir/postgres.json"
  source_id=$(docker exec devshard-postgres sh -c \
    'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT system_identifier FROM pg_control_system();"')
  printf '%s\n' "$source_id" > "$backup_dir/system-identifier"
  bash ./devshard-postgres-migration-preflight.sh \
    --source-container devshard-postgres \
    --target-dir "${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}"

  ./versiond-router-fleet.sh stop-all --maintenance
  docker stop --time 1800 "${replicas[@]}"
  docker exec devshard-postgres sh -c \
    'pg_dump -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Fc' > "$backup_dir/database.dump"
  docker exec -i devshard-postgres pg_restore --list < "$backup_dir/database.dump" >/dev/null
  docker stop --time 300 devshard-postgres
  ./versiond-router-fleet.sh prepare-networks
  # The PostgreSQL entrypoint copies the old volume into the persistent directory.
  docker compose up -d --no-deps --wait --wait-timeout 2100 devshard-postgres
  target_id=$(docker exec devshard-postgres sh -c \
    'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT system_identifier FROM pg_control_system();"')
  [[ -n "$source_id" && "$target_id" == "$source_id" ]]
)
```

On failure, leave replicas stopped and inspect `docker compose logs --tail=100 devshard-postgres`. Preserve the source volume and backup: no `down -v`, `rm -v`, pruning or `--renew-anon-volumes`.

If Compose stops waiting while the copy continues, leave PostgreSQL running. Wait until this command reports `healthy`:

```bash
docker inspect --format '{{.State.Health.Status}}' devshard-postgres
```

Then repeat the identifier check using the backup directory printed above:

```bash
(
  set -euo pipefail
  backup_dir='<printed-backup-directory>'
  [[ $(docker inspect --format '{{.State.Health.Status}}' devshard-postgres) == healthy ]]
  source_id=$(cat "$backup_dir/system-identifier")
  target_id=$(docker exec devshard-postgres sh -c \
    'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT system_identifier FROM pg_control_system();"')
  [[ -n "$source_id" && "$target_id" == "$source_id" ]]
  echo 'System identifiers match.'
)
```

After success, continue with [Update with downtime](#update-with-downtime). Leave the old replicas stopped.

<details>
<summary><strong>If the old volume was already detached</strong></summary>

Keep all replicas stopped. Complete [directory preparation](#prepare-the-postgresql-directory), then enter the pre-update backup directory:

```bash
(
  set -euo pipefail
  source ./config.env || exit 1
  read -r -p 'Pre-update backup directory: ' BACKUP_DIR
  DEVSHARD_POSTGRES_LEGACY_VOLUME=$(jq -er '[.[0].Mounts[] | select(.Type == "volume" and .Destination == "/var/lib/postgresql/data") | .Name] | if length == 1 then .[0] else error("Expected one old PostgreSQL volume") end' "$BACKUP_DIR/postgres.json")
  export DEVSHARD_POSTGRES_LEGACY_VOLUME
  [[ -n "$DEVSHARD_POSTGRES_LEGACY_VOLUME" ]] || exit 1
  test -s "$BACKUP_DIR/postgres-system-identifier" || exit 1
  # Command-line -f replaces COMPOSE_FILE, so pass the complete list explicitly.
  files=()
  IFS=':' read -ra parts <<<"$COMPOSE_FILE"
  for f in "${parts[@]}"; do files+=(-f "$f"); done
  bash ./devshard-postgres-migration-preflight.sh \
  --source-volume "$DEVSHARD_POSTGRES_LEGACY_VOLUME" \
  --target-dir "${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}"
  docker compose "${files[@]}" -f docker-compose.versiond-postgres-recovery.yml \
  up -d --no-deps --wait --wait-timeout 2100 devshard-postgres

  # Compare the system identifier with the backup before proceeding.
  source_id=$(cat "$BACKUP_DIR/postgres-system-identifier")
  target_id=$(docker exec devshard-postgres sh -c \
    'psql -U "$POSTGRES_USER" -d "$POSTGRES_DB" -Atc "SELECT system_identifier FROM pg_control_system();"')
  [[ -n "$source_id" && "$target_id" == "$source_id" ]]
  echo 'System identifiers match.'
)
```

After `System identifiers match.`, continue with [Update with downtime](#update-with-downtime) using the normal `COMPOSE_FILE`, without the recovery overlay. Leave replicas stopped; keep the source volume and backup. Do not enable `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT`.

</details>

</details>

### 3. Update the deployment

Schedule maintenance: replacing the public proxy can interrupt connections.

For published `v4`/`v4.1` binaries, use the downtime procedure below. Rolling updates require storage-proof support from every retained protocol. Leave `UPDATE_SKIP_POSTGRES_PROBE` and `UPDATE_ACCEPT_DATABASE_CHANGE` disabled.

#### Update with downtime

For **one join host**, using the same external database or local PostgreSQL on the persistent path. Copy the [old local volume](#2-check-the-database-layout) first if needed. Keep database settings, participant identity, replica data mounts and protocols unchanged. This procedure does not cover database moves or multi-host updates.

Run in `deploy/join`; include every local replica in `replicas`. The backup contains credentials; keep its directory private.

```bash
(
  set -euo pipefail
  source ./config.env
  : "${COMPOSE_FILE:?set the complete Compose file list}"
  replicas=(versiond versiond2)
  mkdir -p backups
  backup_dir=$(mktemp -d "$PWD/backups/ha-update.XXXXXXXX")
  echo "Backup directory: $backup_dir"

  # Save the old settings and reject changes to PostgreSQL connections.
  docker inspect "${replicas[@]}" > "$backup_dir/containers.json"
  docker compose config --format json > "$backup_dir/compose.json"
  python3 - "$backup_dir" <<'PYTHON'
import json, sys
from pathlib import Path
path = Path(sys.argv[1])
model = json.loads((path / "compose.json").read_text())
reference = None
for container in json.loads((path / "containers.json").read_text()):
    service = container["Config"]["Labels"]["com.docker.compose.service"]
    old = dict(item.split("=", 1) for item in container["Config"]["Env"])
    new = model["services"][service]["environment"]
    old = {key: value for key, value in old.items() if key.startswith("PG") and key != "PG_POOL_MAX_CONNS"}
    new = {key: value for key, value in new.items() if key.startswith("PG") and key != "PG_POOL_MAX_CONNS"}
    old.setdefault("PGPORT", "5432")
    new.setdefault("PGPORT", "5432")
    if old != new or (reference is not None and old != reference):
        sys.exit(f"{service}: PostgreSQL settings differ; keep the existing connection")
    if model["networks"]["default"]["name"] not in container["NetworkSettings"]["Networks"]:
        sys.exit(f"{service}: deployment network changed; stop")
    reference = old
if not reference or any("\n" in value or "\r" in value for value in reference.values()):
    sys.exit("Cannot export PostgreSQL settings to a Docker env file")
(path / "postgres.env").write_text("".join(f"{key}={value}\n" for key, value in reference.items()))
PYTHON
  pg_network=$(jq -er '.networks.default.name' "$backup_dir/compose.json")
  pg_args=(--rm --network "$pg_network" --env-file "$backup_dir/postgres.env")

  # Select pg_dump for the running server's major version; pull before downtime.
  docker pull postgres:16-alpine
  pg_major=$(docker run "${pg_args[@]}" postgres:16-alpine \
    psql -XAtw -v ON_ERROR_STOP=1 -c "SELECT current_setting('server_version_num')::int / 10000")
  [[ "$pg_major" =~ ^[1-9][0-9]*$ ]]
  pg_client="postgres:$pg_major-alpine"
  docker pull "$pg_client"
  docker compose pull "${replicas[@]}" proxy proxy-policy proxy-policy2

  echo "Starting maintenance"
  ./versiond-router-fleet.sh stop-all --maintenance
  docker compose stop "${replicas[@]}"
  docker run "${pg_args[@]}" "$pg_client" pg_dump -w -Fc > "$backup_dir/database.dump"
  docker run -i --rm "$pg_client" pg_restore --list < "$backup_dir/database.dump" >/dev/null
  ./update-devshard.sh --check

  ./versiond-router-fleet.sh prepare-networks
  if [[ $(jq -r '.services.versiond.environment.PGHOST' "$backup_dir/compose.json") == devshard-postgres ]]; then
    docker compose up -d --no-deps --wait --wait-timeout 2100 devshard-postgres
  fi
  docker compose up -d --no-deps oracle-filter
  docker compose up -d --no-deps --wait --wait-timeout 2100 "${replicas[@]}"
  docker compose up -d --no-deps proxy
  docker compose up -d --no-deps --wait --wait-timeout 2100 proxy-policy2 proxy-policy proxy
  ./versiond-router-fleet.sh apply
  ./versiond-router-fleet.sh verify-admission
  project=$(jq -er '.name | select(length > 0)' "$backup_dir/compose.json")
  if legacy_project=$(docker inspect --format '{{index .Config.Labels "com.docker.compose.project"}}' \
      versiond-router 2>/dev/null); then
    if [[ "$legacy_project" == "$project" ]]; then
      docker rm -f versiond-router
    fi
  fi
)
```

Preflight checks database access and capacity, not database identity. If an image download fails before `Starting maintenance`, [cancel the preparation](#cancel-before-maintenance).

Run the [service checks](#41-check-the-running-services), then stop here.

#### Rolling update

Leave PostgreSQL, the filter, replicas and router fleet running.

Prepare the filter before a first rolling update:

```bash
source ./config.env
./versiond-router-fleet.sh prepare-networks
docker compose up -d --no-deps oracle-filter
```

If a protocol lacks storage-proof support, use [Update with downtime](#update-with-downtime). Stop on timeouts, HTTP 503 or invalid proofs.

Check every protocol on every replica:

```bash
(
  set -euo pipefail
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
)
```

Expect HTTP 200 from every check. Update one replica at a time; another replica must serve each protocol throughout the update.

Run preflight and preview the replacements. Preflight writes database probes; it does not replace services:

```bash
./update-devshard.sh --dry-run
```

Review the proposed images and changes. On a directory creation error, run [directory preparation](#prepare-the-postgresql-directory) and retry. Do not proceed while preflight fails. Then run:

```bash
./update-devshard.sh
```

[Replace remote members](#replace-a-member) one at a time before final verification.

Run the [service checks](#41-check-the-running-services). To enable a new protocol, follow [Add a protocol](#add-a-protocol).

## Add a remote replica

Requires storage-proof support from every retained protocol. Published `v4`/`v4.1` binaries do not support this admission procedure.

Use a private network between machines. Do not start a second dapi with the same keys. A's public proxy and node remain single-instance.

New deployment: finish [startup](#step-3---start-the-deployment) and [verification](#step-4---verify-it-works) on A first.

| Machine | Runs |
| --- | --- |
| A | Local `versiond` replicas + node/api/proxy + router fleet |
| B | `versiond` only |
| Shared | PostgreSQL reachable from every `versiond` instance |

Keep B out of the router pool until its checks pass.

### 1. Prepare machine A

On machine A, publish these ports for B on the private network:

- PostgreSQL `5432`, or the external database port.
- Node-manager `9400`.
- Chain RPC/gRPC `26657`, `9090`.
- Filtered catalog `19100`.

Confirm `PGPASSWORD` and `KEYRING_PASSWORD` match the running local `versiond` containers.

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

Schedule maintenance for the port changes. On A and each existing replica host, run in `deploy/join` to stop the replicas:

```bash
source ./config.env &&
mapfile -t replicas < <(docker compose ps --services | grep -E '^versiond[0-9]*$') &&
((${#replicas[@]} > 0)) &&
docker compose stop "${replicas[@]}"
```

On A, apply the port changes:

```bash
(
  set -e
  source ./config.env
  ./versiond-router-fleet.sh stop-all --maintenance
  if docker compose config --services | grep -qx devshard-postgres; then
    docker compose up -d --no-deps --wait --wait-timeout 2100 devshard-postgres
  fi
  docker compose up -d --no-deps --wait --wait-timeout 2100 node api oracle-filter
)
```

On each host, restart the replicas in the same shell used to stop them:

```bash
docker compose up -d --no-deps --wait --wait-timeout 2100 "${replicas[@]}"
```

On A, restore the fleet and run the [service checks](#41-check-the-running-services):

```bash
./versiond-router-fleet.sh apply && ./update-devshard.sh --check
```

### 2. Configure and start machine B

For a new B, run on A in `deploy/join`. Enter B's SSH login as `user@host`. The checkout on B will be created at `~/gonka`, using A's commit.

```bash
(
  set -euo pipefail
  source ./config.env
  read -r -p 'SSH login for machine B (user@host): ' B_SSH
  [[ "${KEYRING_BACKEND:-file}" == file ]] || { echo "This copy procedure requires a file keyring"; exit 1; }
  ssh "$B_SSH" 'test ! -e "$HOME/gonka"'
  ssh "$B_SSH" 'git clone --no-checkout https://github.com/gonka-ai/gonka.git "$HOME/gonka"'
  release_commit=$(git rev-parse HEAD)
  ssh "$B_SSH" "git -C \"\$HOME/gonka\" checkout --detach $release_commit"
  remote_env=$(mktemp)
  trap 'rm -f "$remote_env"' EXIT
  python3 > "$remote_env" <<'PYTHON'
import os, shlex
names = ["KEY_NAME", "ACCOUNT_PUBKEY", "KEYRING_PASSWORD", "VERSIOND_VERSIONS",
         "DEVSHARD_POSTGRES_PASSWORD"]
for name in names:
    value = os.environ[name]
    if not value:
        raise SystemExit(f"Missing {name}")
    print(f"export {name}={shlex.quote(value)}")
for name, default in [("KEYRING_BACKEND", "file"), ("DEVSHARD_POSTGRES_DB", "devshardd"),
                      ("DEVSHARD_POSTGRES_USER", "devshardd")]:
    print(f"export {name}={shlex.quote(os.environ.get(name, default))}")
print('export VERSIOND_NON_HA_VERSIONS=""')
if os.environ.get("VERSIOND_IMAGE"):
    print("export VERSIOND_IMAGE=" + shlex.quote(os.environ["VERSIOND_IMAGE"]))
PYTHON
  ssh "$B_SSH" 'touch "$HOME/gonka/deploy/join/config.env" && chmod 600 "$HOME/gonka/deploy/join/config.env" && cat > "$HOME/gonka/deploy/join/config.env"' < "$remote_env"
  docker exec versiond /bin/busybox tar -C /root/.inference -cf - keyring-file |
    ssh "$B_SSH" 'mkdir -p "$HOME/gonka/deploy/join/.inference"; tar --no-same-owner -xf - -C "$HOME/gonka/deploy/join/.inference"'
)
```

On B, run `cd ~/gonka/deploy/join`. For an existing B, use [Replace a member](#replace-a-member).

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

Check B's database before adding B to the router pool. On B, run:

```bash
(
  set -euo pipefail
  cd /path/to/gonka/deploy/join
  command -v psql
  test ! -e pool-postgres.env || exit 1
  cp pool-postgres.env.template pool-postgres.env
  chmod 600 pool-postgres.env
)
```

Fill in `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER` and `PGPASSWORD` in `pool-postgres.env` using A's database settings or values from the database administrator. Run:

```bash
./update-devshard.sh --check-storage --reference-env ./pool-postgres.env
```

Expect `Storage check passed` and exit code 0. The check writes test data to the database; run it separately from other checks and updates. For additional containers, repeat `--container NAME`.

### 3. Add B to the router pool

On machine A, create `versiond-endpoints.json` in `deploy/join`. Replace `10.0.0.12` with B's private IP and include every replica. All routers must be able to connect to these addresses:

```bash
cat > versiond-endpoints.json <<'EOF'
[
  {"id": "local-a", "host": "versiond", "port": 8080},
  {"id": "local-a-2", "host": "versiond2", "port": 8080},
  {"id": "remote-b", "host": "10.0.0.12", "port": 8080}
]
EOF
```

Set `export VERSIOND_POOL_ENDPOINTS_FILE=./versiond-endpoints.json` in A's `config.env`; `source ./config.env`. Fresh fleet: run `./versiond-router-fleet.sh apply`. Existing fleet: run during maintenance:

```bash
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
  ./versiond-router-fleet.sh maintenance-rollout
./versiond-router-fleet.sh verify-admission
```

The rollout interrupts new requests. Keep member IDs when replacing a host at the same endpoint. Endpoint-file edits take effect only through the rollout.

Run the [service checks](#41-check-the-running-services) and [routing failover check](#42-check-routing-failover) after adding B.

## Add a local replica

After [startup and verification](#step-3---start-the-deployment), add `versiond3`:

1. In `config.env`, insert `docker-compose.versiond3.yml` immediately after `docker-compose.versiond.yml` in `COMPOSE_FILE`. Keep the filter, database and site overrides after it:

   ```bash
   export COMPOSE_FILE=docker-compose.yml:docker-compose.versiond.yml:docker-compose.versiond3.yml:docker-compose.devshard-v5.override.yml
   # External PostgreSQL: append :docker-compose.devshard-pg-external.override.yml
   # Retain every other site override after these files.
   ```

2. Add under `services` in `docker-compose.devshard-v5.override.yml`:

   ```yaml
   versiond3:
     environment:
       VERSIOND_ORACLE_URL: http://oracle-filter:9100/versions
       VERSIOND_NON_HA_VERSIONS: ${VERSIOND_NON_HA_VERSIONS-}
     depends_on:
       oracle-filter:
         condition: service_started
       devshard-postgres:
         condition: service_healthy
   ```

   External PostgreSQL: also add under `services` in `docker-compose.devshard-pg-external.override.yml`, below the existing anchor:

   ```yaml
   versiond3:
     <<: *external-postgres
   ```

3. Local PostgreSQL: run [Update with downtime](#update-with-downtime) for the existing replicas to apply the new PostgreSQL mount. Skip this step for an external database.
4. Start the new replica:

   ```bash
   source ./config.env
   docker compose config --quiet
   docker compose pull versiond3
   docker compose up -d --no-deps --wait --wait-timeout 2100 versiond3
   ```

5. If using `versiond-endpoints.json`, add `versiond3` and [apply the updated list](#3-add-b-to-the-router-pool). Default Docker discovery finds it automatically. Run the [service checks](#41-check-the-running-services), including `versiond3`.

For a fourth replica, copy `docker-compose.versiond3.yml`, replace `3` with `4`, and repeat the overrides above.

## Operate the deployment

### Manage router slots

Use the fleet script to manage routers. `docker compose down` in the join directory does not stop them. The shutdown timeout is 30 minutes per router by default (`VERSIOND_ROUTER_DRAIN_TIMEOUT_SECONDS`).

The fleet script loads `config.env`.

| Task | Command |
| --- | --- |
| View the fleet | `./versiond-router-fleet.sh status` |
| Stop slot 0 | `./versiond-router-fleet.sh stop 0` |
| Restore slot 0 | `./versiond-router-fleet.sh start 0` |
| Verify routing after a change | `./versiond-router-fleet.sh verify-admission` |
| Apply the release's router image | `./versiond-router-fleet.sh apply` |

Full release update: follow [Update the deployment](#3-update-the-deployment).

Keep previous stopped containers and catalog volumes until recovery completes. Rerun interrupted operations with the same image and configuration. Pool, resolver or legacy-routing changes: [membership maintenance](#3-add-b-to-the-router-pool).

Whole-machine maintenance: drain the fleet before stopping the main stack:

```bash
(
  set -euo pipefail
  ./versiond-router-fleet.sh stop-all --maintenance
  # Stop the main stack after the fleet has drained.
  source ./config.env || exit 1
  : "${COMPOSE_FILE:?set the complete Compose file list}"
  docker compose stop
  # Only when decommissioning, after the main stack is down:
  # ./versiond-router-fleet.sh down --maintenance
)
```

### Restart a member

Restart one replica at a time. The remaining replicas must serve all protocols and handle the load. Include every active override in `COMPOSE_FILE`:

```bash
(
  set -euo pipefail
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
)
```

Run the readiness checks above before restarting another replica.

### Replace a member

Keep the same database, participant identity, protocol list and data mounts. Replace one replica at a time; another replica must serve each protocol.

1. Use the target release's files. Apply the variables in [Release and images](#release-and-images).
2. For a remote member, remove its entry from `versiond-endpoints.json` on A and run [membership maintenance](#3-add-b-to-the-router-pool). On the member's machine, run in `deploy/join` to select and replace the replica:

   ```bash
   (
     set -euo pipefail
     source ./config.env
     docker compose ps --services
     read -r -p 'Replica service from the list (for example versiond2): ' service
     [[ "$service" =~ ^versiond[0-9]*$ ]] || exit 1
     docker compose stop "$service"
     docker compose pull "$service" &&
     docker compose up -d --no-deps --wait --wait-timeout 2100 "$service"
   )
   ```

   For a remote member, pass the [database check](#check-the-remote-database) before restoring its entry in A's endpoint file and applying membership maintenance.
3. Pass the [service checks](#41-check-the-running-services) before replacing the next member. If checks fail, stop before replacing another member. Reverting an image does not revert database changes; any rollback version must support the current database.

### Remove a member

On the replica's host, run in `deploy/join`:

```bash
(
  set -euo pipefail
  source ./config.env
  docker compose ps --services
  read -r -p 'Replica service to remove (for example versiond2): ' service
  [[ "$service" =~ ^versiond[0-9]*$ ]] || exit 1
  docker compose stop "$service" && docker compose rm -f "$service"
)
```

Edit `config.env`. To disable `versiond2`, set:

```bash
export VERSIOND2_REPLICAS=0
```

For an extra replica, remove its Compose filename from `COMPOSE_FILE` and its service block from the filter/database overrides. Keep its data directories. If using `versiond-endpoints.json`, remove its entry on A and run [membership maintenance](#3-add-b-to-the-router-pool). Run the [service checks](#41-check-the-running-services) for the remaining replicas.

### Add a protocol

1. Check the [release's binary compatibility requirements](../devshard/docs/release-0.2.15-v5.md#binary-upgrade-compatibility) for the host/gateway versions. Once it appears in the node's [approved protocol list](#21-same-machine-two-replicas), add it to `VERSIOND_VERSIONS` in `config.env` on every host. Keep existing protocols.
2. On A: `source ./config.env`, then `docker compose up -d --no-deps oracle-filter` with the complete `COMPOSE_FILE`.
3. `./versiond-router-fleet.sh wait-version <new-protocol>`, then run the [service checks](#41-check-the-running-services) with the updated list.

No router restart is needed. The next host update applies the saved protocol list to the router fleet and public proxy; schedule it as maintenance.

Keep `proxy-router-state` and each slot's `router-state`. Remove a protocol only during maintenance, after its sessions are no longer needed: a filter change can stop children, while accepted router routes persist by default.

## Troubleshooting

### Cancel before maintenance

Use this only if release preparation or image download failed **before** `Starting maintenance` appeared. It restores files; it does not roll back replaced containers or a migrated database. Run in `deploy/join`; use the `Backup directory` printed by the pre-update backup:

```bash
read -r -p 'Pre-update backup directory: ' BACKUP_DIR
export BACKUP_DIR
(
  set -euo pipefail
  test -s "$BACKUP_DIR/previous-commit"
  git switch --detach "$(cat "$BACKUP_DIR/previous-commit")"
  python3 - "$BACKUP_DIR" <<'PYTHON'
import json, pathlib, shutil, sys
backup = pathlib.Path(sys.argv[1])
for name in json.loads((backup / "files.json").read_text()):
    target = pathlib.Path(name)
    source = backup / "files" / name.lstrip("/")
    if not source.is_file():
        sys.exit(f"Missing backup file: {source}")
    shutil.copy2(source, target)
PYTHON
)
```

After a successful restore, run `source ./config.env`. Keep the backup. For `denied` or `manifest unknown` during image download, wait for access to the release images; do not substitute another tag.


### Resume an interrupted rolling update

If the updater reports pending recovery, rerun it in `deploy/join`. Use the same OS user, release and configuration, including any `UPDATE_STATE_DIR` or `XDG_STATE_HOME` setting. Do not switch between sudo and non-sudo runs. Keep the recovery directory printed by the updater. Run without `--check` or `--dry-run` to recover and continue the update:

```bash
source ./config.env
./update-devshard.sh
```

### Resolve a missing database

Stop before starting replicas against an empty database. For a local database, inspect the container and its mounts:

```bash
docker compose logs --tail=100 devshard-postgres
docker inspect devshard-postgres --format '{{json .Mounts}}' | jq .
```

If the old Docker volume was detached, use the recovery block in [Check the database layout](#2-check-the-database-layout) with the pre-update backup. Missing storage without a preserved source volume, and external database disaster recovery, are outside this guide. Do not use `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT` to bypass recovery.
