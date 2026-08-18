# Release guide: `devshard-0.2.15-v5`

Operator-facing contract for the v5 deployment line.

Previous line: [release-0.2.14-v4.md](./release-0.2.14-v4.md).
Host evacuation: [versiond-host-evacuation.md](./versiond-host-evacuation.md).
Rolling updates: [rolling-update.md](./rolling-update.md).
Architecture: [high-availability-architecture.md](./high-availability-architecture.md).

The release is identified by Git tag `release/v0.2.15-devshard-v5`. Images are
published under staging tag `0.2.15-devshard-v5`, then the machine-readable
release contract is pinned to each final `repo@sha256:...` manifest before the
Git tag is created. The release gate and host updater reject mutable image tags
outside explicit unreleased testing. The old `edge-api-router` image is retained
only for the one-time v4 migration bridge. The machine-readable values live in
[`devshard-v5-release.env`](../../deploy/join/devshard-v5-release.env), which is
also consumed by the updater and release-image smoke test.

---

## Overview

This release converts the host-local devshard ingress and service pools to an
actively checked HAProxy topology, adds a three-slot `versiond-router` fleet,
and gives `versiond` and `edge-api` bounded graceful shutdown. It also provides
the one-time, topology-aware migration from existing v0.2.15 join deployments:
the updater preserves the effective Compose model, migrates local HA PostgreSQL
to persistent storage, replaces application replicas behind traffic barriers,
and commits the public-router cutover only after production-route checks pass.

The first cutover is a **devshard maintenance operation**. The chain node and ML
containers stay running, but the shared local PostgreSQL process is restarted
when HA storage is used, and replacing the old public nginx terminates
connections still owned by that container. Later application, policy-worker,
and inner-router updates use the rolling contracts described below.

## Host update contract

The operational authority for the rollout is the dated entry on
[Network Updates](https://gonka.ai/docs/network-updates/). That entry must name
the immutable Git tag above, the UTC start and deadline, and the exact commands
from this section. The publication source is
[`network-update-0.2.15-v5.md`](./network-update-0.2.15-v5.md). Do not announce
the release as available until the tag exists, the registry smoke passes, and
that entry has been published in `gonka-ai/gonka-docs`.

The functional deadline is before governance enables the first protocol route
that requires this v5 per-version router contract. The release coordinator sets
the earlier calendar deadline in Network Updates; an arbitrary date in source
code would not be a network decision and is deliberately not inferred by the
updater.

### Exact command for a stock checkout

`config.env` is ignored by Git and remains in place. From the repository root:

```bash
git fetch origin \
  refs/tags/release/v0.2.15-devshard-v5:refs/tags/release/v0.2.15-devshard-v5
git switch --detach release/v0.2.15-devshard-v5
cd deploy/join

./upgrade-devshard-v5.sh --preflight-only --strict-capacity
./upgrade-devshard-v5.sh --acknowledge-maintenance
```

The first command validates the immutable updater source, Docker/Compose and
tool versions, the actual running Compose topology, CPU/RAM capacity, public
port ownership, and PostgreSQL migration space. It does not run Compose
pull or recreate, start, stop, or remove a deployment service, and it does not
pull release application/router images. The PostgreSQL space probe uses a short
read-only helper container (whose helper image Docker may fetch) and may create
the configured empty target directory. The second command is rejected unless
maintenance is acknowledged explicitly.

### Existing local Compose changes

The Host Quickstart historically asked operators to edit tracked Compose files,
so a dirty `docker-compose.yml` is supported. Do not discard those changes and
do not force-reset the repository. Preserve them on a local branch before
merging the release tag:

```bash
git diff --binary HEAD -- deploy/join >"$HOME/gonka-compose-before-v5.patch"
git switch -c "host-local-v5-$(date +%s)"
git add -u -- deploy/join/docker-compose*.yml
git commit -m "Preserve host-local Compose settings before devshard v5"
git fetch origin \
  refs/tags/release/v0.2.15-devshard-v5:refs/tags/release/v0.2.15-devshard-v5
git merge --no-edit release/v0.2.15-devshard-v5
cd deploy/join

./upgrade-devshard-v5.sh --preflight-only --strict-capacity
./upgrade-devshard-v5.sh --acknowledge-maintenance
```

`config.env` is not committed by this sequence. If Git reports a conflict,
stop there: no deployment mutation has happened. Resolve the Compose merge and
rerun the non-disruptive preflight. The updater requires all release-critical
scripts and migration overlays to match the immutable tag byte-for-byte, but it
does not require operator Compose files to match. It recovers their exact
ordered list from running-container labels, renders the effective model, and
prints the SHA-256 and origin (`release`, `local-override`, or `custom`) of every
file before mutation. Custom override files remain part of replacement and
rollback commands.

### Compatibility matrix

| Existing deployment | Supported | Preflight contract |
| --- | --- | --- |
| Standard v0.2.15 join stack (`versiond=single`, `edge-api=single`) | Yes | Existing `proxy`, `versiond`, and `edge-api` must belong to one valid Compose project |
| v0.2.15 base plus `0.2.14-devshard-v4` HA versiond with local PostgreSQL | Yes | Shared Postgres identity must be unchanged; source volume and persistent target must pass the exact copy-space check |
| v0.2.15 base plus `0.2.14-devshard-v4` HA versiond with managed/external PostgreSQL | Yes | Both replicas must retain identical `PGHOST`, `PGPORT`, `PGDATABASE`, and `PGUSER`; the updater does not manage that database |
| Single or three-replica edge-api | Yes | Existing topology is detected independently from versiond mode |
| Observability and operator override files | Yes | The complete ordered file set must be present and render the required service/container/network contract |
| Renamed core services, split Compose projects, Docker Swarm, or Kubernetes | No | Preflight fails before pull or service mutation; Kubernetes is a separate deployment track |
| Pre-v0.2.15 base deployment, pre-v4 devshard, or custom application images | Not release-qualified | Upgrade to a supported v0.2.15 + devshard-v4 join contract or rehearse and qualify the custom source separately |

The updater verifies its own scripts against the Git tag rather than requiring
`HEAD` to equal the tag. This is what permits a local merge commit containing
host Compose changes without permitting a locally edited migration algorithm.
It resolves and prints the tag's full commit SHA; the production release gate
is stricter and runs only when `HEAD` is that exact commit.

### Maintenance and rollback boundary

Schedule the first run outside PoC/cPoC, with no long inference or SSE request
in flight, and update one host at a time. Budget up to 35 minutes for a slow
`versiond` reconcile plus the local PostgreSQL copy time; normal healthy hosts
usually finish earlier. The public nginx-to-HAProxy cutover is brief but closes
the old container's established client connections.

Application and router replacements retain captured immutable Docker image IDs
until their postconditions pass. A later public-router update also captures its
live routing environment and restores both image and environment if admission
fails. Local PostgreSQL has a stricter boundary:
before the new database starts, the old anonymous volume is the physical
rollback source; after v5 PostgreSQL accepts writes, the updater will not switch
back automatically because doing so could fork database history. Keep that
volume and a normal database backup through the rollback window. A failure past
this boundary is database recovery, not an image rollback.

Before publication, run the **Release image smoke** workflow against the tagged
commit. It validates the exact Compose image contract, steady-state and
migration models, router configurations, and application readiness endpoints.

## What's in this release

- **Whole-`versiond` host evacuation, replacement, addition and decommission**
  (Track B) — Compose owns both the running containers and their persisted
  replica counts; see
  [versiond-host-evacuation.md](./versiond-host-evacuation.md) and
  [rolling-update.md §1.8](./rolling-update.md#18-versiond-router-draining-versiond-hosts-ha).
- **Replicated `versiond-router` tier** behind a stable public HAProxy. Router
  replicas are stateless, use active route-aware checks, and roll one slot at a
  time.
- **Replicated private nginx policy workers** retain TLS and HTTP policy, while
  public and service-pool balancing moves to `proxy-router`. The dedicated
  `edge-api-router` hop is no longer needed in steady state.
- **Graceful `versiond` shutdown budgets** for both the single-instance join
  stack and the HA overlay (below).
- **`edge-api` readiness and graceful shutdown** — an instance that cannot reach
  the chain leaves the round-robin rotation instead of answering with failing
  queries, and a stopping instance drains the same way versiond does (below).

## Breaking / operator-facing changes

### Service pools use HAProxy and DNS membership

`proxy-router` and every `versiond-router` replica are HAProxy. Three
consequences for operators:

1. **The pool is no longer a host list.** `VERSIOND_HOSTS` and `EDGE_API_HOSTS`
   are replaced by `VERSIOND_POOL_HOST` / `EDGE_API_POOL_HOST`: one DNS name that
   resolves to every instance. In Compose that is a network alias
   (`versiond-pool`, `edge-api-pool`), which the shipped overlays set for you.
   Starting or stopping an instance changes the pool; nothing else has to.
2. **Health is measured, not declared.** Each inner router polls `GET /readyz`
   on every versiond once a second. The top HAProxy also polls every router for
   the exact requested version. A starting, draining, or unavailable member
   leaves only the affected pool and rejoins after recovery.
3. **The router is a fleet.** Fixed slots run as independent Compose projects.
   The main node project cannot recreate them all, and the fleet rollout refuses
   to stop a slot unless the coarse and per-version ready reserve remains. The
   topology-aware updater calls the fleet's idempotent `apply`: it bootstraps an
   absent fleet and replaces only slots whose image or rendered Compose contract
   changed.

Removed with the old model:

| Removed | Replacement |
| --- | --- |
| `gonka-routerctl`, `gonka-hostctl` | `docker compose stop` / `start`; a read-only pool diagnostic ships inside the router image |
| Router state volume (`/var/lib/gonka/versiond-router`) | nothing — the router holds no durable state |
| `VERSIOND_HOSTS`, `EDGE_API_HOSTS` | DNS aliases `VERSIOND_POOL_HOST`, `EDGE_API_POOL_HOST` |
| `VERSIOND_ADMIN_LISTEN_ADDR` (loopback readiness listener) | `GET :8080/readyz` on the traffic listener |

Sticky routing keys its hash ring on each versiond's address, so every router
replica computes the same mapping regardless of DNS answer order. HA sessions
recover from shared Postgres if their selected versiond host leaves the pool.

### Topology-aware upgrade

Use the same two-phase command from `deploy/join` for every supported join
topology:

```bash
./upgrade-devshard-v5.sh --preflight-only --strict-capacity
./upgrade-devshard-v5.sh --acknowledge-maintenance
```

The preflight requires Docker Compose 2.24.4 or newer, `jq`, `curl`, `flock`,
`sha256sum`, Python 3, and Git. It checks them before changing the deployment.
The Quickstart's 1 TB NVMe recommendation remains operational guidance, not a
numeric upgrade gate: marketed capacity and formatted filesystem capacity are
not interchangeable, and the checkout may be on a different filesystem from
PostgreSQL. For HA migration the blocking check measures the actual source
cluster and requires its full byte size plus 10% on the actual target mount.

Existing v4 nginx and singleton-router containers remain the
rollback path while application replicas are replaced. At the final step the
script starts the independent router fleet and private policy workers, captures
the exact old public nginx image, and switches the public listener to
`proxy-router`. It verifies component readiness before deleting the migration
singletons. Before mutation, the updater proves that its rendered rollback
generation has the same Compose config hash as the running service. If cutover
fails or is interrupted, resources recorded as touched are compared with their
journaled container generation. Unchanged resources are left running; only an
actually replaced, stopped, or unhealthy resource is restored in reverse order
from the exact saved generation. Legacy journals without generation identity
are recovered conservatively instead of treating two absent IDs as equality.
The script exits non-zero.

The updater, router cutover, and standalone fleet commands all take the same
deployment-wide `.gonka-deployment.lock` next to `config.env`; a concurrent
mutation fails before changing Docker state, while nested updater calls inherit
the same lock. During an upgrade, the atomic
`.gonka-devshard-v5-upgrade-complete.in-progress` journal records the desired
topology and the last verified phase. Rerunning the updater resumes that exact
topology even if a replica or the public proxy disappeared after cutover.
Successful completion atomically renames that verified journal to
`.gonka-devshard-v5-upgrade-complete`; there is no separate marker-write /
journal-delete window. This JSON marker records the release commit, topology
modes, ordered Compose files, project identity, expected image digests
(including PostgreSQL), the verified PostgreSQL UUID, and a fingerprint of the
rendered Compose model. While ingress replacement is active, the mode-`0600`
journal temporarily embeds the exact previous Compose generation, including
its environment, so crash recovery does not depend on changed files. Those
rollback bytes are removed before commit; the final marker contains only
hashes, non-secret identities, and transaction receipts. The
proxy container label alone is not evidence that PostgreSQL, versiond, and
edge-api were migrated; both application and ingress postconditions must pass
before the final marker is committed.

If the process stops during ingress replacement, the next ordinary updater run
restores the journaled ingress generation immediately after resolving the saved
Compose topology. It does this before capacity checks, PostgreSQL preflight,
image pulls, or application startup. The updater also re-renders and hashes the
effective Compose model before every later mutation and before marker commit;
editing an override or relevant environment value during a run therefore
causes rollback/failure instead of mixing two deployment generations.
The outer updater also passes this exact fingerprint into the router cutover,
which checks it before fleet work and again before ingress commit. The
independently owned router fleet has a second canonical fingerprint covering
fleet ID, ordered slots, ready reserve, slot manifest, and every rendered slot
model. It is journaled and checked before and after fleet apply, so a resumed
transaction cannot silently adopt changed fleet settings.
For direct incident recovery, `enable-router-ha.sh --recover-only` reads the
project identity, topology modes, timeout, and immutable rollback Compose model
from the active transaction journal before reading `config.env`; a damaged
forward config therefore cannot prevent restoration of the last committed
ingress generation. Journals from builds predating that embedded recovery
context fail closed and require the originating release files.

On the first migration, the storage UUID becomes available when the first v5
`versiond` has initialized the identity row in the shared database. The updater
persists it immediately, before replacing another supervisor or ingress
resource. Every later `versiond` and every resumed transaction must report that
same UUID.

Rerunning the same upgrade is also the normal reconciliation path for this
release. It restores the committed Compose model from the marker, converges
PostgreSQL and each application replica in HA-safe order, rolls changed router
slots one at a time, reconverges public ingress, verifies every expected image
and health check, and rewrites the marker only after success. Image/config-
current containers remain untouched. A main-project `pull` or `up -d` alone
cannot update slot projects that it does not own.

The one-time public-listener replacement cannot preserve connections owned by
the old nginx container because both implementations own the same host ports.
It is therefore a short, explicit ingress cutover after every application pool
is ready, not a zero-interruption router rollout. Subsequent inner-router and
policy-worker replacements are rolling; the remaining single public HAProxy is
the documented host-level failure domain for this release.

On the first run, the script sources `config.env`, detects two independent axes
from the existing containers, and recovers the actual Compose project from
Docker's `com.docker.compose.project.*` labels. The recovered contract includes
the ordered Compose file list and project working directory; replacement,
rollback, and final router cutover all use that same contract. Successful runs
commit this model to the upgrade marker, which is authoritative on later runs:

| Axis | Standard mode | HA / multi mode |
| --- | --- | --- |
| `versiond` | only `versiond` | `devshard-postgres`, `versiond2`, or `versiond-router` exists |
| `edge-api` | only `edge-api` | `edge-api2`, `edge-api3`, or `edge-api-router` exists |

The standard Host Quickstart therefore selects `versiond=single` and
`edge-api=single` automatically. Its existing ML, observability, and local
override files remain in the model even though only services owned by this
upgrade are targeted. In particular, the observability overlay supplies Jaeger
and Grafana routing variables to both the v4 rollback proxy and the fixed v5
`proxy-policy2` and `proxy-policy` nginx slots. The updater replaces
the reserve slot first, waits for end-to-end admission by the public HAProxy,
and only then replaces the active slot. Both sides declare policy wire-contract
version `1`; a future incompatible contract requires an explicit maintenance
migration instead of silently entering a mixed generation. Until the public
proxy and both slots pass their postconditions, their captured images remain
armed for rollback.

Normally no Compose arguments are needed. For a deliberately changed or
ambiguous deployment, pass the complete model through `COMPOSE_FILE` (and
`COMPOSE_PATH_SEPARATOR` when needed) or repeat `--compose-file` in the original
order. `--compose-project-name` and `--compose-project-directory` are also
available. An explicit list may append an override, but may not omit or reorder
files recorded by running containers. If services record incompatible file
sets, a file is missing, the project identity changes, or required service and
`container_name` contracts are absent, the script exits before pull, stop, or
recreate. The exact resolved model is passed to `enable-router-ha.sh`; the final
cutover cannot silently fall back to stock files.

Explicit `--versiond-mode` and `--edge-mode` overrides exist for recovery
diagnostics; normal upgrades should not need them.

Before pulling, the script records the immutable image ID of every application
or router service it may roll back. For each existing versiond replica, it also
waits for a settled `/healthz` snapshot and records every running child whose
version route is reachable. If an existing supervisor is stopped, the script
records that fact and does not start it merely to manufacture a baseline. Its
replacement is introduced only at the normal isolated step; failed rollback
recreates the captured old image without starting it. The union of running
replica baselines becomes the router baseline. This is the availability
contract that a rollback must fully restore. The v4 nginx routers do not consume
`/readyz`, so before replacing the first replica the script removes that replica
from the rendered upstream, validates the nginx config, and performs a graceful
reload. Existing requests remain on the old nginx workers; the replacement does
not receive new traffic while `up --wait` is establishing readiness. The script
also installs a temporary after-render hook in the v4 router container. If that
container crashes and Docker restarts it, the shortened upstream is rendered
again before nginx starts accepting traffic.

`EXIT`, `INT`, `TERM`, and `HUP` share the same compensation path. If an active
replacement fails or the script is interrupted, it attempts to recreate the
previous image; a newly introduced service is stopped instead. A restored v4
service must pass three consecutive functional probes before rollback is
reported as successful. Rollback uses the same startup budget as forward
replacement: 35 minutes for versiond and 3 minutes for edge-api. A restored
versiond must report and route every version from its captured baseline;
restoring only one of several versions is a failed rollback. Router rollback
must route the union captured from all versiond replicas rather than trusting
its hash-selected `/healthz` response. Once HAProxy is active, supervisor
rollback is also probed through the production router. HAProxy treats a `404`
from `/readyz` as the explicit pre-v5 capability case and then requires the real
version route's `/healthz`; a v5 `503` remains authoritative for starting and
draining hosts. Edge-api must execute the chain-backed `/v1/versions` query. If
a check fails, the service is stopped and the script requires operator recovery.
A replica interrupted after the nginx barrier remains isolated; rerunning the
command first replaces that replica, then restores the complete nginx upstream
and captures the router baseline. Temporary rollback tags are removed only
after the whole upgrade succeeds. After a failed attempt, keep the reported
`gonka-upgrade-rollback/*` tags until recovery is verified; they can then be
removed with `docker image rm`.

For multi-edge, the script replaces `edge-api2` first and waits for its v5
`/readyz`, then switches from nginx to HAProxy. HAProxy excludes old or unready
replicas, so each later replacement either joins after `/readyz` passes or is
stopped without poisoning the live pool.

For versiond HA, the migration order is `versiond2`, a temporary v5 singleton
router, then the legacy owner `versiond`. Once application replacement is
complete, the final cutover starts the replicated router tier and public
distributor. Requests pinned to pre-HA SQLite versions still need the
maintenance window described below because only `versiond` owns that data.

#### HA-only PostgreSQL migration

The v4 HA overlay left `devshard-postgres` on the anonymous
`/var/lib/postgresql/data` volume declared by `postgres:16-alpine`. The v5
overlay stores `PGDATA` in the stable `DEVSHARD_POSTGRES_DATA_DIR` bind
(`./devshards/postgres` by default) and migrates an existing v4 cluster
automatically. Base-only installations skip this entire path.
The v5 Compose model and migration preflight resolve PostgreSQL from the same
immutable multi-architecture digest in `devshard-v5-release.env`; a later move
of the mutable `postgres:16-alpine` tag cannot change the database binary during
retry or rollback.

PostgreSQL is deliberately outside the image rollback contract above. Its v4
source volume is retained and migration publishes an atomic copy into the
persistent target. If migration or startup fails, the script stops PostgreSQL
and preserves both locations for diagnosis or another restart. It does not
automatically switch back to the source volume: the new database may already
have accepted writes, so doing that could fork the storage history.

The local migration runs only when the effective Compose model gives both
`versiond` replicas `PGHOST=devshard-postgres`. A custom model may point both
replicas at the same managed PostgreSQL host; in that case the updater preserves
the override and does not pull, recreate, or preflight the local
`devshard-postgres` service. It automatically adds
`docker-compose.versiond-external-postgres.yml`, which removes the local
dependency and keeps the bundled database out of later Compose operations; the
hoster does not select another upgrade mode. HA rejects `DATABASE_URL` so the
supervisor's session lookup and its children cannot resolve different
databases; it also rejects `PGSERVICE`, `PGSERVICEFILE`, and `PGOPTIONS`, which
can override the checked identity through a libpq service file or session
parameters such as `search_path`. Before any mutation it compares `PGHOST`,
`PGPORT`, `PGDATABASE`, and `PGUSER` with the existing containers. An implicit database
identity change, disagreement between replicas, or non-Postgres HA storage is a
hard failure rather than an attempted migration. Before committing the update,
the script also reads the schema UUID through both replacement supervisors and
requires an exact match; equal connection strings alone are not treated as
proof that both aliases reach the same database.

The first stock local-PostgreSQL HA v4-to-v5 cutover is a **devshard maintenance
operation**, not a rolling update. It restarts the one shared PostgreSQL
instance, replaces the router process, and temporarily takes the only pre-HA
SQLite owner out of service. Schedule it outside PoC/cPoC, make sure no long
inference or SSE request is still in flight, and update multiple network nodes
one at a time. This matches the maintenance guidance in
[Network Updates](https://gonka.ai/docs/network-updates/).

Do not stop the whole node for this cutover. The script names every service it
may recreate and uses `--no-deps`, so `node`, `api`, `tmkms`, `bridge`, `proxy`,
`explorer`, and ML containers remain running.

Before stopping or recreating the shared PostgreSQL container, the script
mounts its v4 data volume read-only, measures the cluster with `du`, and checks
the filesystem behind `DEVSHARD_POSTGRES_DATA_DIR` with `df`. Migration requires
the full source size plus a 10% reserve. If an earlier copy left an incomplete
`.migrating` directory, preflight counts its size as reclaimable because the
entrypoint removes it before copying again. An insufficient effective amount of
space stops the procedure while the existing PostgreSQL process is still
running.

Do not run `docker compose down` or use `up --renew-anon-volumes` before this
first v5 `up`. During an in-place recreation, Compose carries the v4 anonymous
volume into the replacement container. Before PostgreSQL starts, the shipped
entrypoint copies the stopped cluster to a staging directory, validates
`PG_VERSION`, syncs it, and atomically renames it to the new `PGDATA`. The old
volume is not modified and remains the physical rollback copy. Later starts use
the bind-mounted cluster directly, so `down` / `up` no longer risks this database
migration.

The v5 upgrade command is the targeted devshard cutover described here; it does
not stop the whole node. Any later full-stack stop is a separate maintenance
operation and must follow the official
[Host Quickstart stopping procedure](https://gonka.ai/docs/host/quickstart/).

This is fail-closed if an operator removed the old container first. When the old
volume is no longer attached but existing versiond artifacts are present, the
entrypoint refuses to initialize an empty database. It logs a direct recovery
error instead of starting an apparently healthy node with lost HA state.
`DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true` bypasses that guard only for an
intentional new HA database; it is not a recovery mechanism.

After startup, verify both the persistent mount and the database:

```bash
docker inspect devshard-postgres --format \
  '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/gonka"}}{{.Type}} {{.Source}}{{end}}{{end}}'
docker logs devshard-postgres 2>&1 | grep 'gonka-postgres-entrypoint'
source ./config.env
docker exec devshard-postgres \
  pg_isready -U "${DEVSHARD_POSTGRES_USER:-devshardd}" \
  -d "${DEVSHARD_POSTGRES_DB:-devshardd}"
docker exec devshard-postgres \
  psql -U "${DEVSHARD_POSTGRES_USER:-devshardd}" \
  -d "${DEVSHARD_POSTGRES_DB:-devshardd}" -Atc \
  "SELECT count(*) FROM pg_catalog.pg_tables WHERE schemaname = 'public';"
```

The first command must report `bind` and the configured host directory. An
upgraded v4 node also logs `v4 PostgreSQL migration completed`; a fresh node logs
that it is initializing persistent `PGDATA`. Keep the old anonymous volume and a
normal database backup through the rollback window.

#### Recover a detached v4 PostgreSQL volume

If the fail-closed guard reports a detached v4 volume, do not copy its contents
directly into `DEVSHARD_POSTGRES_DATA_DIR`. That directory is the bind-mount
root; the actual `PGDATA` is its `data` subdirectory. Prefer the shipped recovery
overlay instead: it temporarily mounts the selected volume back at the legacy
`/var/lib/postgresql/data` path and lets the same validated, atomic migration
entrypoint populate `DEVSHARD_POSTGRES_DATA_DIR/data`.

First identify the exact detached volume from the deployment inventory or
backup records. `docker volume ls --filter dangling=true` can list candidates,
but do not guess when several PostgreSQL volumes exist. Then run this from
`deploy/join`, replacing the placeholder value:

```bash
export DEVSHARD_POSTGRES_LEGACY_VOLUME=replace-with-exact-volume-name

(
set -e
source ./config.env
: "${DEVSHARD_POSTGRES_LEGACY_VOLUME:?set the detached v4 volume name}"

compose=(docker compose -f docker-compose.yml -f docker-compose.versiond.yml)
recovery_compose=(
  "${compose[@]}"
  -f docker-compose.versiond-postgres-recovery.yml
)

# Fail before Docker can create a new empty volume for a mistyped name.
docker volume inspect "$DEVSHARD_POSTGRES_LEGACY_VOLUME" >/dev/null
mounted_ids=$(docker ps -q \
  --filter "volume=$DEVSHARD_POSTGRES_LEGACY_VOLUME")
for mounted_id in $mounted_ids; do
  mounted_name=$(docker inspect "$mounted_id" --format '{{.Name}}')
  if [ "$mounted_name" != /devshard-postgres ]; then
    echo "legacy volume is mounted by unexpected container $mounted_name" >&2
    exit 1
  fi
  echo "resuming recovery through existing devshard-postgres container"
done

target_root=${DEVSHARD_POSTGRES_DATA_DIR:-./devshards/postgres}
./devshard-postgres-migration-preflight.sh \
  --source-volume "$DEVSHARD_POSTGRES_LEGACY_VOLUME" \
  --target-dir "$target_root"

# Only after every non-disruptive preflight succeeds, stop database clients and
# PostgreSQL without deleting containers or volumes.
"${compose[@]}" stop versiond versiond2 devshard-postgres

"${recovery_compose[@]}" up -d --no-deps --force-recreate \
  --wait --wait-timeout 2100 devshard-postgres
"${recovery_compose[@]}" exec -T devshard-postgres \
  test -s /var/lib/postgresql/gonka/data/PG_VERSION

# Detach the temporary source. The recovered bind is now authoritative, so it
# is safe to replace only the image-declared anonymous legacy mount.
"${compose[@]}" up -d --no-deps --force-recreate --renew-anon-volumes \
  --wait --wait-timeout 2100 devshard-postgres

container_id=$("${compose[@]}" ps -q devshard-postgres)
current_legacy_volume=$(docker inspect "$container_id" --format \
  '{{range .Mounts}}{{if eq .Destination "/var/lib/postgresql/data"}}{{.Name}}{{end}}{{end}}')
if [ -z "$current_legacy_volume" ] || \
   [ "$current_legacy_volume" = "$DEVSHARD_POSTGRES_LEGACY_VOLUME" ]; then
  echo "temporary PostgreSQL recovery volume is still attached" >&2
  exit 1
fi
)
```

The preflight is restart-safe. It mounts both the selected legacy volume and
`target_root` read-only. If the persistent `data/PG_VERSION` is already
published, or `.migrating` contains a validated completion marker, it succeeds
without requiring free space for another full copy. An incomplete `.migrating`
directory is included as reclaimable space because the entrypoint removes it
before retrying the copy. Rerun the same block after an interrupted recovery;
do not delete either directory to make the space check pass.

Repeat the database verification above, then rerun
`./upgrade-devshard-v5.sh`. The script preserves the stopped state of both old
supervisors, introduces `versiond2` behind the legacy-router barrier, and only
restores the complete upstream after that replacement is ready. It then
continues the normal cutover. Keep the detached source volume and a logical or
physical backup through the rollback window. Do not use the empty-init override
to silence a recovery condition. `--renew-anon-volumes` is safe only in the
post-recovery command above, after the bind-mounted `PGDATA` has been verified;
never add it to the initial v4-to-v5 migration command.

### Graceful versiond shutdown (single-instance and HA)

`versiond` now owns one graceful shutdown budget across proxy admission,
accepted requests (including complete SSE streams), child drain, child stop,
and HTTP shutdown. This applies to the base single-instance join stack as well
as the HA overlay. Under HA, a stopping host first spends
`VERSIOND_DRAIN_ANNOUNCE` reporting unready while still serving, which is what
lets the router remove it before it stops accepting; single-instance
`docker compose down`, `stop`, or `restart` has no alternate host, but still lets
work accepted before `SIGTERM` finish.

The join Compose defaults are:

| Setting | Default | Role |
| --- | --- | --- |
| `VERSIOND_DRAIN_ANNOUNCE` | `5s` | Keep serving after `/readyz` starts failing, so the balancer notices first. Counts against the shutdown budget; `0` = no balancer; below `5s` refuses to boot |
| `VERSIOND_HEALTH_START_PERIOD` | `30m` | Compose startup allowance for downloads and first reconcile. A successful `/readyz` check marks the host healthy immediately; ordered upgrades wait at most 35 minutes |
| `VERSIOND_HOST_SHUTDOWN_BUDGET` | `25m` | Internal absolute deadline; expiry forces remaining work and reaps children |
| `VERSIOND_STOP_GRACE_PERIOD` | `30m` | Compose `stop_grace_period`, the outer Docker `SIGKILL` backstop |

Before upgrading, audit custom versiond duration values. Duration settings now
use Go duration syntax and fail startup on malformed or non-positive values
instead of silently falling back to defaults. Use values with units such as
`15m` or `1s`; bare numbers and `VERSIOND_DRAIN_TIMEOUT=0` are invalid. Only
`VERSIOND_DRAIN_ANNOUNCE=0` is accepted, where it explicitly declares that no
balancer announcement window is needed.

These are maximum waits, not fixed delays: an idle versiond exits shortly after
the announce window. A busy or stuck node may make a routine Compose stop wait
longer than the old Docker default of roughly 10 seconds. Keep
`VERSIOND_STOP_GRACE_PERIOD > VERSIOND_HOST_SHUTDOWN_BUDGET` so versiond can
finish its own escalation and child reap. Operators may override these for a
deliberately shorter maintenance window, but doing so can terminate accepted
inference streams; do not shorten only the outer Docker grace.

### edge-api drains instead of cutting queries at 10 seconds

`edge-api` used to answer `SIGTERM` with a fixed 10-second `Shutdown`, which cut
any query still running. It now follows the same contract as versiond:

| Setting | Default | Role |
| --- | --- | --- |
| `EDGE_API_DRAIN_ANNOUNCE` | `5s` | `/readyz` answers 503 while the instance keeps serving, so the router drops it before it stops accepting. `0` declares no balancer; any other value below `5s` refuses to boot |
| `EDGE_API_HEALTH_START_PERIOD` | `2m` | Compose allowance for a replacement to reach the chain and pass `/readyz`; ordered upgrades wait at most three minutes |
| `EDGE_API_SHUTDOWN_BUDGET` | `2m` | How long accepted queries then have to finish; matches the router's default read timeout |
| `EDGE_API_STOP_GRACE_PERIOD` | `3m` | Compose `stop_grace_period`, the outer Docker `SIGKILL` backstop |

A second `SIGTERM`/`SIGINT` cuts the announce window short, and a further one
during the drain itself closes remaining connections immediately — the process
handles the signals itself, so without that an operator watching a stuck drain
would have nothing left but `SIGKILL`. If the budget expires with queries still
running, edge-api closes them and logs why, rather than being `SIGKILL`ed
mid-write.

The shipped Compose files set `stop_grace_period` on every edge-api service,
including the single-instance one: without it Docker's 10-second default would
`SIGKILL` the process during its own drain.

### Routing is per version, not per host

The router asks each host about the version it is about to route to
(`/readyz?version=<v>`) and keeps one pool per protocol name. A host that cannot
run one version leaves that version's pool and keeps serving every other — no
eviction and no reload.

`VERSIOND_VERSIONS` now supplies only a static bootstrap floor. Both the inner
router fleet and the top `proxy-router` continuously consume dapi's existing
read-only governance `GET /versions` endpoint. Each process has 32 inert backend
slots by default. When governance adds a name, the local reconciler assigns a
slot, enables that version's health checks, and publishes the request-map entry
only after the assignment succeeds.

Therefore a normal new version requires **no host-side `config.env` edit and no
router replacement**. Until a host has the child running, it is not in that
version's pool; existing versions continue unchanged. Governance permanently
controls the version feed, while versiond retains its existing same-name
blue/green binary replacement behavior. The router uses only the version name
and does not introduce new artifact-governance rules.

The approved catalog cannot announce a name before governance approves it. A
release coordinator must therefore treat approval as the start of convergence,
not as proof that every host is already ready. The per-host machine-readable
acceptance check is:

```bash
source ./config.env
./versiond-router-fleet.sh wait-version v9
```

It requires every configured inner slot to have learned the name, at least
`VERSIOND_ROUTER_MIN_READY` slots to serve it, and the active parent to admit the
same reserve. Network automation can aggregate this result across hosts before
advertising the new protocol as generally available. A
true pre-approval gate would require a separate signed staged-version feed; it
cannot be inferred from `approved_versions`.

Catalog additions are monotonic across router replacements, and each router tier
atomically persists its last fully projected snapshot. Routers consume DAPI's
existing `{"versions":[...]}` response and retain their last admitted map on
malformed input or removal of an accepted name. This is an HA projection rule,
not a change to governance;
route removal requires a future explicit drain procedure. A replacement validates
and pre-renders a snapshot no older than
`VERSIOND_ROUTING_CATALOG_CACHE_MAX_AGE_SECONDS` (24 hours by default), so a
temporary dapi outage at startup does not erase versions learned after the
image was built. Stale, corrupt, or future-dated cache data is ignored and the
shipped `v4` through `v8` bootstrap floor remains routable. Cache protocol 2
uses `catalog-v2.json`. On the first replacement it validates and atomically
migrates a fresh legacy `catalog.json`; stale or invalid legacy data is ignored
and fetched again. The old file remains untouched for exact-image rollback, so
the fleet can roll from protocol 1 to 2 without an operator migration. Existing
schema-1 cache payloads remain readable as local generation zero. Cached additions
retain dynamic-slot assignments across restarts, so a
restart cannot silently replenish capacity; reducing capacity below a fresh
cache fails startup. The defaults allow 32 additions between router releases;
capacity exhaustion is a persistent degraded projection state and the new name
remains `503` instead of using the coarse pool.

`versiond` continues to consume the existing DAPI version contract. This release
does not change consensus validation or governance mutation rules. Router-side
catalog validation only protects the bounded HAProxy projection from malformed
or stale input.

Coarse mode is an explicit two-part opt-in and changes the placement-readiness
source. Persist both lines in `config.env`, then apply it in a maintenance
window rather than mixing coarse and per-version routers:

```bash
export VERSIOND_VERSIONS=""
export VERSIOND_ROUTING_CATALOG_URL=""
export VERSIOND_ROUTER_ALLOW_COARSE_READINESS=true
VERSIOND_ROUTER_ALLOW_MAINTENANCE_OUTAGE=true \
  ./versiond-router-fleet.sh maintenance-rollout
./enable-router-ha.sh --versiond-mode ha --edge-mode auto
```

An empty value is different from an unset value. Removing the first two exports
restores the join overlay's bootstrap list and governance catalog. Likewise, after migrating
all legacy versions to Postgres, clear their pins with
`export VERSIOND_NON_HA_VERSIONS=""`; removing that export restores the
`v1 v2 v3` default. Legacy pins and the pool hostname define escrow placement
and must not diverge across router replicas. Apply those changes only with the
acknowledged `maintenance-rollout` path from the host-evacuation runbook;
ordinary `rollout` rejects them.

### A failed version poll no longer takes hosts out of rotation

`/readyz` reports whether a host can serve, not whether its last reconcile
succeeded. Previously any reconcile error — an unreachable oracle, an archive
that fails to download — made the host report unready. Because every versiond
reads the same oracle, that failure arrives on all of them at once, so a
control-plane hiccup could empty the pool while every child was still running and
serving normally.

A reconcile failure is still recorded — versiond keeps it in its internal
`Degraded` condition and logs it — it is simply not a routing decision. Note
that `/healthz` is unchanged and does **not** carry it: its JSON array of
per-version child state is the same contract as before, so alerting on reconcile
failures means reading the logs today. A host that fails to install a newly
approved version leaves *that version's* pool through the per-version check
above, and keeps serving the versions it does have. Pinned pre-HA versions use
the same per-version check against their single SQLite owner, so a failed v5
install cannot hide otherwise healthy v1-v3 routes after the restart.

### HA storage is enforced at boot as well as per request

The HA overlay sets `GONKA_HA=true`. A `devshardd` child then refuses to start
unless its storage is fail-closed Postgres, instead of starting and failing every
HA-marked request. The existing per-request `Devshard-Ha` guard stays: a process
that started before the deployment became HA is still caught at request time.
The router also stamps that header when it sees more than one usable host in the
backend selected for a request, so accidentally scaling without the overlay
fails closed at request time. The explicit flag remains required because it
keeps the guard enabled through a partial pool outage.

If a versiond container exits at startup with a storage-mode error after this
upgrade, its `DEVSHARD_STORAGE_MODE`/`PGHOST` did not match the HA overlay — that
node was previously serving HA traffic with unsafe storage.

## High-availability deployment

Run these commands from `deploy/join`. Choose one complete Compose model and use
the same set of `-f` arguments for later service-targeted operations. These
full-model `up` commands are for initial deployment; use the targeted cutover
above when upgrading a running v4 node:

```bash
# HA versiond and shared PostgreSQL, with one edge-api.
source ./config.env && \
./versiond-router-fleet.sh prepare-networks
source ./config.env && \
docker compose -f docker-compose.yml -f docker-compose.versiond.yml up -d
./enable-router-ha.sh --versiond-mode ha --edge-mode single

# HA versiond plus the optional multi-instance edge-api pool.
source ./config.env && \
./versiond-router-fleet.sh prepare-networks
source ./config.env && \
docker compose \
  -f docker-compose.yml \
  -f docker-compose.versiond.yml \
  -f docker-compose.edge-api-multi.yml \
  up -d
./enable-router-ha.sh --versiond-mode ha --edge-mode multi
```

For a fresh HA installation backed by managed PostgreSQL, keep the provider
connection settings in an operator override and include the external-database
overlay in every Compose command:

```yaml
# docker-compose.managed-postgres.yml
services:
  versiond:
    environment: &managed-postgres
      PGHOST: postgres.example.internal
      PGPORT: "5432"
      PGDATABASE: devshardd
      PGUSER: devshardd
      PGPASSWORD: ${DEVSHARD_POSTGRES_PASSWORD:?required}
  versiond2:
    environment: *managed-postgres
```

```bash
source ./config.env
./versiond-router-fleet.sh prepare-networks
docker compose \
  -f docker-compose.yml \
  -f docker-compose.versiond.yml \
  -f docker-compose.versiond-external-postgres.yml \
  -f docker-compose.managed-postgres.yml \
  up -d --wait --wait-timeout 2100
./enable-router-ha.sh \
  --versiond-mode ha --edge-mode single \
  --compose-file docker-compose.yml \
  --compose-file docker-compose.versiond.yml \
  --compose-file docker-compose.versiond-external-postgres.yml \
  --compose-file docker-compose.managed-postgres.yml
```

The external overlay disables the bundled `devshard-postgres` service; it does
not invent provider credentials. Both supervisors must resolve the same
`PGHOST`, `PGPORT`, `PGDATABASE`, and `PGUSER` tuple.
`enable-router-ha.sh` also reads the durable storage UUID through both running
supervisors before it creates the fleet or changes ingress. Equal connection
strings alone are not accepted as proof that both endpoints reach the same
database.

On a cold start, the first `up -d` may expose an unready public proxy while the
application pool is still starting; no existing traffic exists yet. The second
command converges the independently owned router slots and verifies the final
public path. On an existing installation, use `upgrade-devshard-v5.sh`, which
performs the cutover and rollback automatically.

Day-to-day operations must reuse the complete ordered Compose topology,
including the external-PostgreSQL, observability, and operator override files.
Set `COMPOSE_FILE` once in the shell (with the platform's
`COMPOSE_PATH_SEPARATOR` when needed), or repeat the same `--compose-file`
arguments used for deployment. Both Compose and `enable-router-ha.sh` then
preserve those overlays:

| Task | Command |
| --- | --- |
| Take `versiond2` out of service temporarily | `source ./config.env && docker compose stop versiond2` |
| Put it back / replace it | `source ./config.env && docker compose up -d --no-deps --wait --wait-timeout 2100 versiond2` |
| Decommission `versiond2` permanently | persist `VERSIOND2_REPLICAS=0` in `config.env`, then run the `stop` and `rm` commands in the [host evacuation runbook](./versiond-host-evacuation.md#permanent-membership-changes) |
| Inspect the router fleet and parent admission | `source ./config.env && ./versiond-router-fleet.sh status` |
| Roll router image or configuration | persist `config.env`, then run `./enable-router-ha.sh --versiond-mode ha --edge-mode auto`; its fleet `apply` rolls changed slots and refreshes the top map |
| Change legacy pins / placement pool | schedule devshard maintenance and use the runbook's acknowledged `maintenance-rollout`; mixed placement is rejected |

### Full host stop and cleanup

The router slots are independent Compose projects with `restart: always`.
Therefore the main project's `docker compose down` cannot stop or remove them.
For an HA deployment, use this ordered maintenance sequence from `deploy/join`
(add the edge-api multi overlay when that topology is enabled):

```bash
source ./config.env
./versiond-router-fleet.sh stop-all --maintenance
docker compose down
./versiond-router-fleet.sh down --maintenance
```

`stop-all` sends every router its configured soft-stop concurrently and waits
for accepted streams up to the fleet drain timeout. After the main project has
detached, `down` removes all containers carrying this fleet ID, including stale
or duplicate slot records, and removes current or renamed fleet-owned networks.
It refuses before mutation if a non-fleet container is still attached, and it
never touches containers or networks carrying another fleet ID.

After a full cleanup, recreate the external network substrate before the main
Compose model, then reconcile the routers after versiond has started:

```bash
source ./config.env
./versiond-router-fleet.sh prepare-networks
docker compose up -d
./enable-router-ha.sh --versiond-mode ha --edge-mode auto
```

Taking a host out of rotation is stopping it — there is no router-side drain,
because HAProxy reuses server slots and a drain would be inherited by whichever
host lands in that slot next.

Do not stop or decommission `VERSIOND_LEGACY_HOST` while
`VERSIOND_NON_HA_VERSIONS` is non-empty. Those versions have SQLite state on
that host and no failover backend. A plain `stop` is also not a permanent
decommission: `restart: always` can bring the container back after a Docker
daemon restart. Persist the corresponding replica count as `0` first.

## Upgrade / rollout checklist

- [ ] Fetch immutable tag `release/v0.2.15-devshard-v5`; do not run an updater
      copied from a branch or a mutable `main` checkout
- [ ] Confirm the Network Updates entry names the same tag and an exact UTC
      maintenance deadline; update one host at a time within that window
- [ ] Preserve tracked host-local Compose changes on a local branch; keep the
      generated binary patch until the upgrade and rollback window are closed
- [ ] For HA, keep `VERSIOND_ROUTING_CATALOG_URL` enabled on both router tiers and
      size `VERSIOND_ROUTER_VERSION_CAPACITY` / `PROXY_ROUTER_VERSION_CAPACITY`
      for additions between releases; hosters do not edit `VERSIOND_VERSIONS`
      for each governance name
- [ ] For HA or multi-edge, replace `VERSIOND_HOSTS` / `EDGE_API_HOSTS` with
      `VERSIOND_POOL_HOST` / `EDGE_API_POOL_HOST`, or drop them and take the
      shipped defaults
- [ ] Remove `VERSIOND_ADMIN_LISTEN_ADDR` from `config.env`; readiness is now
      `:8080/readyz`
- [ ] Confirm every versiond in the HA overlay has
      `DEVSHARD_STORAGE_MODE=postgres` and a reachable `PGHOST` before enabling
      `GONKA_HA`
- [ ] Run `upgrade-devshard-v5.sh --preflight-only --strict-capacity` and verify
      its topology, Compose file SHA-256 values, source commit, public ports,
      and reported `versiond` / `edge-api` modes
- [ ] For HA, let the script finish its PostgreSQL disk-space preflight before
      stopping any service; migration needs the source size plus 10%. Use the
      in-place migration, not `down` or `--renew-anon-volumes`
- [ ] Confirm `VERSIOND_HOST_SHUTDOWN_BUDGET` and the larger
      `VERSIOND_STOP_GRACE_PERIOD` match the maximum acceptable maintenance
      wait; short values can terminate accepted inference streams
- [ ] For HA, keep `VERSIOND_REPLICAS` and `VERSIOND2_REPLICAS` in `config.env`; use a
      persisted value of `0` for permanent decommission and never decommission
      `VERSIOND_LEGACY_HOST` while `VERSIOND_NON_HA_VERSIONS` is non-empty
- [ ] For HA, remove all legacy pins by persisting
      `VERSIOND_NON_HA_VERSIONS=""`; do not unset it, because unset restores
      the `v1 v2 v3` default
- [ ] Use `upgrade-devshard-v5.sh` so a failed versiond replacement is rolled
      back before the legacy owner or router is touched
- [ ] Start the mutating phase only with `--acknowledge-maintenance`; existing
      public connections can close at the nginx-to-HAProxy cutover
- [ ] For edge-api-multi, confirm auto-detection reports `edge-api=multi`; the
      script replaces every replica behind the migration barrier, then the
      final ingress cutover connects the ready pool directly to `proxy-router`
- [ ] Confirm `EDGE_API_STOP_GRACE_PERIOD` exceeds
      `EDGE_API_DRAIN_ANNOUNCE + EDGE_API_SHUTDOWN_BUDGET` if any of them is
      overridden
- [ ] For HA, use the targeted upgrade script: it migrates PostgreSQL, replaces
      the supervisors behind the compatibility router, starts the independent
      router slots, and commits the public cutover only after component checks
- [ ] For HA, require `versiond-router-fleet.sh status` to report every expected
      slot healthy, no duplicate or orphan owner, and `PARENT_ADMISSION admitted`

## Known follow-ups

- Kubernetes: the application and route-readiness contracts carry over, while
  slot projects and the Compose rollout script do not. Kubernetes deployment is
  not in this release.
- Public ingress: one host-local `proxy-router` remains a failure domain. A
  provider LB, VIP, or Kubernetes Service above multiple hosts is a later layer.
- Storage: the shipped local PostgreSQL is a single host-local failure domain,
  not a database HA cluster. Multi-host deployments require a managed/operator
  PostgreSQL service with synchronous durability and an RPO appropriate for
  acknowledged devshard state.
- Reconcile failures have no machine-readable exposure. They belong in a metric,
  not bolted onto the `/healthz` array that existing clients parse; versiond has
  no metrics endpoint of its own yet.

## Related docs

| Doc | Use |
| --- | --- |
| [versiond-host-evacuation.md](./versiond-host-evacuation.md) | Whole-host evacuation / replacement design and operator contract (Track B) |
| [rolling-update.md](./rolling-update.md) | Child blue/green + drain machinery (Track A) and §1.8 host draining |
| [versiond-router/README.md](../../versiond-router/README.md) | Router routing, per-version health checks, and how to read the pool |
| [release-0.2.14-v4.md](./release-0.2.14-v4.md) | Previous release line |
