# Persistent devshard PostgreSQL data

The versiond HA overlay stores PostgreSQL `PGDATA` in
`DEVSHARD_POSTGRES_DATA_DIR` (default `./devshards/postgres`). This bind path is
stable across container replacement and `docker compose down`.

On the first start after an older deployment, `devshard-postgres-entrypoint.sh`
detects the stopped PostgreSQL 16 cluster in the image-owned anonymous volume,
checks that the target filesystem has enough free space, and copies it through a
staging directory. It publishes the copy atomically and leaves the source volume
unchanged as the rollback copy.

The entrypoint refuses to initialize an empty database when downloaded versiond
artifacts prove this is an existing installation. Set
`DEVSHARD_POSTGRES_ALLOW_EMPTY_INIT=true` only for a deliberate new HA database.

For a detached legacy volume, attach
`docker-compose.versiond-postgres-recovery.yml` temporarily and set
`DEVSHARD_POSTGRES_LEGACY_VOLUME` to that volume's exact Docker name. Run
`devshard-postgres-migration-preflight.sh` before replacement to verify source,
target, and free-space requirements. Remove the recovery overlay after the bind
copy is healthy; the legacy volume remains available for explicit rollback.
