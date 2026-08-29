# Persistent devshard PostgreSQL data

The versiond HA overlay stores PostgreSQL `PGDATA` in
`DEVSHARD_POSTGRES_DATA_DIR` (default `./devshards/postgres`). This bind path is
stable across container replacement and `docker compose down`.

On the first start after an older deployment, `devshard-postgres-entrypoint.sh`
detects the stopped PostgreSQL 16 cluster in the image-owned anonymous volume,
checks that the target filesystem has enough free space, and copies it through a
staging directory. It publishes the copy atomically and leaves the source volume
unchanged as the rollback copy.

The supported upgrade recreates `devshard-postgres` in place. Do not run
`docker compose down` before the first new-layout start: Compose must carry the
existing anonymous volume into the replacement container.

The bundled database uses the PostgreSQL Alpine/musl image family. Its
entrypoint rejects a glibc-based replacement before touching `PGDATA`, even
when the PostgreSQL major version matches. Moving the cluster to another libc
family requires a dedicated PostgreSQL migration that rebuilds
collation-dependent database objects.

```bash
docker compose <the same ordered -f options> \
  up -d --force-recreate devshard-postgres
```

Empty initialization is fail-closed. The entrypoint distinguishes these states:

| Host state | Action |
| --- | --- |
| Existing HA PostgreSQL; normal Compose upgrade | Recreate in place and keep the old container until replacement starts. |
| First HA enablement; versiond files exist but no `.pg-bound` marker exists | Pass `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true` inline to the first `docker compose up` command only, then verify PostgreSQL. Do not store the override in `config.env`. |
| `.pg-bound` exists under either versiond data root | Restore the legacy PostgreSQL volume. Empty initialization is rejected even with the override. |
| The old container was removed before migration | Use the recovery overlay with the exact dangling volume name. |

The `.pg-bound` marker proves that PostgreSQL currently owns devshard sessions;
it is not a permanent “HA was enabled” marker. It can disappear after every
PostgreSQL session has drained, so downloaded versiond artifacts remain a
conservative guard for that ambiguous state.

The PostgreSQL gate has two explicit phases. Before replacing services, validate
the rendered target topology without reading runtime state:

```bash
cd deploy/join
source ./config.env
./postgres-deployment-preflight.sh --compose-only -- \
  -f docker-compose.yml -f docker-compose.versiond.yml
```

After proof-capable versiond images and migrated HA devshard children are
running, perform the live gate before admitting the new topology to production:

```bash
marker=${DEVSHARD_V5_UPGRADE_MARKER:-.gonka-devshard-v5-upgrade-complete}
recorded_identity=$(jq -er '.storage.postgres_identity' "$marker")
./postgres-deployment-preflight.sh --expected-identity "$recorded_identity" -- \
  -f docker-compose.yml -f docker-compose.versiond.yml
```

Omit `--expected-identity` only when no storage identity has been committed by
an earlier successful deployment. The release updater stores the successful
identity under `.storage.postgres_identity` in its atomic completion marker and
reuses it on later runs. A standalone deployment must persist the UUID printed
on stdout in root-protected deployment state and supply it on every subsequent
live check. Do not omit the expected identity to accept an unexpected database;
an intentional replacement belongs to a new recovery transaction after the old
database has been restored or its loss has been explicitly accepted.

The live mode requires both versiond replicas and at least one stable HA child.
It verifies the shared lineage UUID and connects every generation's writer and
reader to one anchor generation with short-lived challenges. This linear proof
detects an independent database while reducing the time in which a child restart
can invalidate the snapshot. A final snapshot read rejects replacement during
the proof. It also verifies that every child uses the rendered application-pool
limit and that PostgreSQL has enough non-reserved connections for the current
children, one rolling replacement on each versiond replica, readiness and fence
sessions, versiond lookups, and concurrent schema initializers.

Both commands must receive the exact Compose files, project directory, and
project name used by the host. Preserve an existing `-p`/`--project-name` or
`COMPOSE_PROJECT_NAME`; selecting another project fails because its live
replicas cannot satisfy the gate.

The live endpoints are supplied by the versiond and devshard changes that this
preflight depends on. A legacy versiond image returns 404, and a child that has
not applied the storage-identity migrations returns 503. These responses fail
closed. The release updater therefore runs `--compose-only` before service
replacement, starts compatible versiond/devshard generations behind the
existing traffic path, and runs the live gate before router admission or public
cutover. The Compose-only phase validates structure and connection selectors;
image capabilities, downloaded devshard artifacts, credentials, and live
connectivity are established by the staged candidate and the live phase while
the updater still owns the rollback baseline.

The live result is a point-in-time admission proof, not a PostgreSQL monitor.
Run it immediately before admission and do not replace generations between the
proof and cutover. After admission, the session fence and readiness lifecycle
from the storage implementation withdraw and restart a child that loses its
database fence.

The lineage UUID and challenge do not select a recovery point. A backup or
clone of the same database retains its UUID, and a single shared writable clone
can satisfy the challenge. Backup freshness, restore approval, and fencing the
operator-selected PostgreSQL primary remain properties of the database recovery
procedure. The expected identity prevents an unrelated lineage from being
admitted; it must not be treated as proof that a restored copy contains every
session written after that backup.

Storage proof is one gate in admission, not the final fleet-ready signal. The
release updater separately verifies that every desired route has the required
number of admitted router replicas before committing the cutover. A successful
storage proof therefore cannot make a partial version catalog production-ready.

These commands are deployment gates, not a standalone replacement procedure.
The release updater keeps the old traffic path and rollback baseline until the
live phase succeeds. If PostgreSQL, either versiond replica, or its stable HA
child fails after `--compose-only`, keep HA admission disabled, repair the
candidate or restore its captured generation, and rerun the live phase. There is
no one-replica bypass. A catalog containing only explicitly non-HA children has
no shared HA workload to prove, so HA admission remains disabled until a stable
HA child exists.

Serialize live checks. Concurrent runs share one transient challenge field and
can overwrite each other's nonce, producing a safe failure that must be retried
before diagnosing separate databases. A generation change also fails the
single attempt; wait for the fleet to stabilize and rerun. The updater that
invokes this gate must hold the host deployment lock and apply an outer command
deadline so a stalled Docker daemon cannot suspend the deployment transaction.

Host-side prerequisites are Linux, `jq`, and Docker Compose 2.24.4 or newer.
The live phase uses the versiond image's `/bin/busybox wget` command against its
loopback admin listener. This matches the image healthcheck contract and keeps
the proof API inaccessible from the deployment network.

For a detached legacy volume, attach
`docker-compose.versiond-postgres-recovery.yml` temporarily and set
`DEVSHARD_POSTGRES_LEGACY_VOLUME` to that volume's exact Docker name. Run
`devshard-postgres-migration-preflight.sh` before replacement to verify source,
target, and free-space requirements. Remove the recovery overlay after the bind
copy is healthy; the legacy volume remains available for explicit rollback.

If the old PostgreSQL volume is permanently lost, restoring service requires an
explicit data-loss decision. All PostgreSQL-owned sessions have already been
lost in this state. Stop the versiond services, inspect the markers, and remove
them only after recording that loss:

```bash
find ./devshards/data ./devshards2/data -type f -name .pg-bound -print
# After confirming that the PostgreSQL volume cannot be recovered:
find ./devshards/data ./devshards2/data -type f -name .pg-bound -delete
DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true \
  docker compose <the same ordered -f options> \
  up -d --force-recreate devshard-postgres
```

Do not add the override to `config.env`. Verify the new PostgreSQL instance
before restarting versiond. This procedure restores availability with an empty
database; it does not recover the lost sessions.

The upstream PostgreSQL image declares `/var/lib/postgresql/data` as a volume.
After migration, a later `down`/`up` can therefore create an unused empty
anonymous volume even though live `PGDATA` is the persistent bind directory.
Keep the original migration volume until its rollback window ends. Afterwards,
inspect candidate volumes and remove only the explicitly identified empty or
retired volume; do not use an indiscriminate `docker volume prune` during the
rollback window.

An interrupted copy is restarted from the intact legacy source. This favors a
verifiable copy over a partially resumed one and can extend the maintenance
window for a large database. The 30-minute healthcheck start period suppresses
startup health failures; it does not terminate a copy that takes longer.
