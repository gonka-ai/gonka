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

```bash
docker compose <the same ordered -f options> \
  up -d --force-recreate devshard-postgres
```

Empty initialization is fail-closed. The entrypoint distinguishes these states:

| Host state | Action |
| --- | --- |
| Existing HA PostgreSQL; normal Compose upgrade | Recreate in place and keep the old container until replacement starts. |
| First HA enablement; versiond files exist but no `.pg-bound` marker exists | Pass `DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true` to the first `docker compose up` command only, then verify PostgreSQL. |
| `.pg-bound` exists under either versiond data root | Restore the legacy PostgreSQL volume. Empty initialization is rejected even with the override. |
| The old container was removed before migration | Use the recovery overlay with the exact dangling volume name. |

The `.pg-bound` marker proves that PostgreSQL currently owns devshard sessions;
it is not a permanent “HA was enabled” marker. It can disappear after every
PostgreSQL session has drained, so downloaded versiond artifacts remain a
conservative guard for that ambiguous state.

For a detached legacy volume, attach
`docker-compose.versiond-postgres-recovery.yml` temporarily and set
`DEVSHARD_POSTGRES_LEGACY_VOLUME` to that volume's exact Docker name. Run
`devshard-postgres-migration-preflight.sh` before replacement to verify source,
target, and free-space requirements. Remove the recovery overlay after the bind
copy is healthy; the legacy volume remains available for explicit rollback.

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
