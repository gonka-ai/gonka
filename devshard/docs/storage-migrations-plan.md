# Storage Schema Migrations Plan

Single-source-of-truth plan for moving every `CREATE TABLE` / `CREATE INDEX` /
partition-DDL statement out of write hot paths and into one explicit migration
step that runs at process startup. Applies to all SQL stores reachable from
`devshardd` and `decentralized-api` (embedded dapi).

## Why

1. **Hot-path DDL is a real bottleneck.** `devshard/storage/postgres.go`
   `SaveSnapshot` issues
   `CREATE TABLE IF NOT EXISTS ... PARTITION OF ...` on every call, completely
   bypassing the in-process `ensurePartition` cache. The same shape exists in
   `decentralized-api/payloadstorage/postgres_storage.go` (cached) but is easy
   to repeat by mistake on any new write path.
2. **No single contract for schema evolution.** DDL strings sit next to write
   code. There is no ordering, no migration table, and no guard that we only
   ever **extend** the schema (add columns, add indexes), never drop or rename.
3. **Versioned devshard binaries share Postgres / SQLite files.** With
   versioned (`gm/microrelease`) and `devshard-0.2.13-v2` running side by side,
   an older binary must be safe to read tables that a newer binary extended,
   and a newer binary must be safe to open a file an older binary created. The
   only safe rule is **forward-only schema** with an explicit ordered list of
   migrations. We must enforce that rule rather than rely on convention.
4. **`ensurePartition` is already the right pattern.** The reviewer's point is
   correct: lazy per-epoch partition creation through `ensurePartition` is
   fine; the problem is hot-path callers that forgot to use it, plus the
   absence of an enforced "schema lives in one place" rule.

## Scope: every `CREATE` statement we own

| Module | File | DDL today | Hot path today? |
| --- | --- | --- | --- |
| devshard postgres parents | `devshard/storage/postgres.go:64-124` | 6 parents + 1 index in `pgCreateParents`, executed once in `NewPostgres` | No (startup only) |
| devshard postgres partitions | `devshard/storage/postgres.go:240-275` | `ensurePartition` creates 5 per-epoch partitions, cached in `knownEpochs` | Per-epoch, cached after first hit |
| devshard postgres `SaveSnapshot` | `devshard/storage/postgres.go:685-692` | `CREATE TABLE IF NOT EXISTS ... PARTITION OF devshard_snapshots` on **every** save, bypassing `ensurePartition` | **Yes — bug** |
| devshard sqlite meta | `devshard/storage/sqlite.go:65-71` | `escrow_epoch` + index, in `openMetaDB` at boot | No (startup only) |
| devshard sqlite per-epoch | `devshard/storage/sqlite.go:315-372` | 5 tables + `ALTER TABLE sessions ADD COLUMN version` in `openEpochPool` | First open per epoch, cached in `pools[epochID]` |
| dapi payload storage parent | `decentralized-api/payloadstorage/postgres_storage.go:17-28` + `ensureSchema` | Single `inferences` parent in `ensureSchema` at boot | No (startup only) |
| dapi payload storage partitions | `decentralized-api/payloadstorage/postgres_storage.go:68-94` | `ensurePartition` with `sync.Map` cache | Per-epoch, cached after first hit |
| dapi stats storage | `decentralized-api/statsstorage/postgres_storage.go:10-39` | 1 table + 4 indexes in `ensureSchema` at boot | No (startup only) |
| dapi config sqlite | `decentralized-api/apiconfig/sqlite_store.go:73-122` | 4 tables + 1 index in `EnsureSchema` at boot | No (startup only) |

Anything not listed above is allowed to run `CREATE` at startup; nothing else is
allowed to run `CREATE` at any other time.

## Does SQLite need migrations too?

Yes, for a slightly different reason than Postgres. The user is **almost**
right but the unit is **per epoch**, not per session:

- `devshard/storage/sqlite.go` opens **one DB file per epoch** under
  `<baseDir>/epoch_<id>.db`. Multiple sessions in the same epoch share one
  file. So a fresh SQLite file is not created "per session". It is created on
  the first session **of a new epoch**.
- The hot-path cost is essentially zero today: `openEpochPool` runs the
  schema once on first open and the pool is cached in `s.pools[epochID]`. So
  unlike Postgres, SQLite has **no hot-path DDL bug** to fix.
- We still need a centralized migration function because of two facts that
  do **not** depend on hot-path cost:
  1. **Forward-only schema rule.** Two devshard binary versions (microrelease
     vs `devshard-0.2.13-v2`, plus future versions) can open the **same**
     `epoch_X.db`. Whichever version touches the file first creates the
     schema. The other version must be safe against missing or extra columns
     and indexes. Today this is enforced ad hoc by the
     `ALTER TABLE sessions ADD COLUMN version TEXT` line that quietly ignores
     `duplicate column name`. That pattern works but is invisible and easy to
     forget. A single ordered migration list makes the contract explicit.
  2. **One migration framework for all stores.** Same set of guarantees
     (atomic, ordered, idempotent, never drop) for SQLite and Postgres
     simplifies review and avoids "this store has migrations, that store
     doesn't" confusion. `apiconfig/sqlite_store.go` and the devshard meta
     DB can share the framework.

So SQLite stays in the plan, but its motivation is contract uniformity and
multi-version safety, not hot-path performance.

## Design principles

1. **One migration entry point per store.** Each store package exports
   `Migrate(ctx, conn)` that idempotently applies every migration. The
   constructor (`NewPostgres`, `NewSQLite`, etc.) calls it exactly once.
2. **Forward-only.** Allowed: `CREATE TABLE IF NOT EXISTS`, `CREATE INDEX
   IF NOT EXISTS`, `ALTER TABLE ... ADD COLUMN`, `CREATE TABLE ... PARTITION
   OF ...` (lazy per-epoch). **Disallowed in production migrations:** `DROP
   TABLE`, `DROP COLUMN`, `DROP INDEX`, `RENAME`, type narrowing. Existing
   `DROP TABLE` calls in `PruneEpoch` paths are not migrations; they are
   runtime pruning of finite per-epoch partitions and stay where they are.
3. **Per-epoch partitions stay lazy.** Creating one partition per known
   epoch at startup is wasteful (retention `N=3`). Partitions still come from
   `ensurePartition`, but `ensurePartition` is the **only** place that does
   it, all call sites go through it, and the cache check is mandatory.
4. **Migrations are ordered and tracked.** A small `schema_migrations` table
   per store records applied IDs. The framework runs migrations whose ID is
   not in the table, in ascending order, in a transaction (Postgres) or
   under a single connection (SQLite WAL). This makes the contract auditable
   without pulling in `golang-migrate`.
5. **No hot-path DDL.** After this lands, no `CREATE TABLE` or `CREATE
   INDEX` string lives in any function called per request, per diff, per
   payload, or per inference write.
6. **Tests assert the contract.** A unit test per store enumerates DDL
   strings via reflection / `rg` and fails if a hot-path file contains a
   `CREATE` keyword outside the migration package.

## Per-store inventory of changes

### `devshard/storage/postgres.go`

1. Move `pgCreateParents` (lines 64-124) and the `schema_migrations` bootstrap
   into a new file `devshard/storage/postgres_migrate.go` with a function
   `Migrate(ctx, *pgxpool.Pool) error`.
2. Replace the inline `pool.Exec(ctx, pgCreateParents)` in `NewPostgres`
   (line 139) with `Migrate(ctx, pool)`.
3. **Fix the bug:** `SaveSnapshot` (lines 685-692) drops its inline
   `CREATE TABLE IF NOT EXISTS ... PARTITION OF` and calls
   `s.ensurePartition(ctx, epochID)` instead. Add a regression test that
   counts DDL queries via a pgx tracer or a wrapper pool.
4. Keep `ensurePartition` (lines 232-276) but make it the **only** site that
   issues `CREATE TABLE ... PARTITION OF`. Add a comment explaining the
   contract.
5. Add a future-migration example: a noop migration `m_002_noop` so the
   ordering and `schema_migrations` table land with a real second entry from
   day one (avoids "first time anyone adds a migration" surprise).

### `devshard/storage/sqlite.go`

1. Extract the meta schema (lines 65-71) into
   `devshard/storage/sqlite_meta_migrate.go` with `MigrateMeta(*sql.DB)`.
2. Extract the per-epoch schema (lines 315-372) **including** the
   `ALTER TABLE sessions ADD COLUMN version` (lines 368-372) into
   `MigrateEpochPool(*sql.DB)`. Express the `ADD COLUMN` as a regular
   migration entry so the "extension over time" intent is explicit.
3. `openMetaDB` calls `MigrateMeta`; `openEpochPool` calls
   `MigrateEpochPool`. Neither contains inline DDL strings.
4. Per-epoch files do not currently track `schema_migrations`. Add it. The
   table is tiny (one row per applied migration) and is the cheapest way to
   keep the forward-only rule auditable across binary versions.
5. `PruneEpoch` (file deletion) stays unchanged.

### `decentralized-api/payloadstorage/postgres_storage.go`

1. Move `createTableSQL` (lines 17-28) and the partition-creation template
   (lines 73-78) into a new `payloadstorage/migrate.go`:
   - `Migrate(ctx, pool)` runs the parent table once at startup.
   - `ensurePartition` is the only caller that issues `CREATE TABLE ...
     PARTITION OF`. Already true; just relocate the constant and confirm
     no other write path issues partition DDL.
2. `NewPostgresStorage` calls `Migrate` instead of `ensureSchema`.
3. Add the partition DDL cache hit/miss test (mirrors devshard
   `SaveSnapshot` regression).

### `decentralized-api/statsstorage/postgres_storage.go`

1. Move `createInferenceStatsTableSQL` (lines 10-39) into
   `statsstorage/migrate.go` `Migrate(ctx, pool)`. No partitioning here.
2. `NewPostgresStorage` calls `Migrate` instead of `ensureSchema`. No other
   changes — this is the simplest store to convert and is a useful template.

### `decentralized-api/apiconfig/sqlite_store.go`

1. Rename `EnsureSchema` (lines 73-122) to `Migrate` and move into
   `apiconfig/sqlite_migrate.go`, splitting the 4-table-1-index block into
   ordered migration entries (one per table) so future `ADD COLUMN`
   additions slot in without rewriting the constant.
2. All call sites that currently call `EnsureSchema` switch to `Migrate`.
3. No partition logic; same simplicity as `statsstorage`.

## Minimal migration framework

One framework per language driver, both ~50 lines. No external dependency:

```go
// devshard/storage/internal/migrate/pg.go
type Step struct {
    ID  int    // strictly increasing
    Name string // human readable
    SQL  string // statements separated by ';' OR a func(ctx, tx) error
}

func ApplyPG(ctx context.Context, pool *pgxpool.Pool, steps []Step) error {
    // 1. CREATE TABLE IF NOT EXISTS schema_migrations(id INT PRIMARY KEY, name TEXT, applied_at TIMESTAMP DEFAULT NOW())
    // 2. SELECT max(id) FROM schema_migrations
    // 3. For each step.ID > current: BEGIN; exec SQL; INSERT schema_migrations; COMMIT
    // 4. Fail if step IDs not strictly increasing (dev-time guard)
}
```

```go
// devshard/storage/internal/migrate/sqlite.go
// Same shape but using *sql.DB and BEGIN IMMEDIATE on SQLite.
```

Both frameworks live under `devshard/storage/internal/migrate/`. The dapi
stores import the same package (Go modules permit; this is internal under
the `devshard` module which is already a dependency of `decentralized-api`
via `go.work`). If the cross-module import is undesirable, we duplicate
the ~50 lines per module — both are acceptable.

## Step-by-step rollout

1. **Add framework + tests.** Land
   `devshard/storage/internal/migrate/{pg,sqlite}.go` + unit tests with a
   throwaway DB. No call sites use it yet.
2. **Convert `statsstorage`** (simplest, no partitions). Land it solo, get
   review on the framework shape. Add an integration test that runs
   `Migrate` twice and asserts the second call is a no-op.
3. **Convert `apiconfig/sqlite_store.go`** (still simple, no partitions).
   Splits the multi-table schema constant into per-table steps.
4. **Convert `decentralized-api/payloadstorage/postgres_storage.go`.**
   Includes the partition template move. Add a test that calls `Store`
   twice for the same epoch and asserts only one `CREATE TABLE ...
   PARTITION OF` query was issued (use a pgxpool tracer or a SQL-text
   recorder fixture).
5. **Convert `devshard/storage/postgres.go`.** Fix the `SaveSnapshot` bug
   in the same commit (so reviewers see the bug fix and the migration
   conversion together). Same partition-cache test as step 4 but for the
   five devshard parents.
6. **Convert `devshard/storage/sqlite.go`.** Meta DB first, then per-epoch
   pool. Add a test that opens an epoch file written by a previous
   "version" of the schema (i.e. without the `version` column) and verifies
   `MigrateEpochPool` adds it via the ordered step.
7. **Lint guard.** Add a CI script under `scripts/` that runs two
   checks over `devshard/storage` and
   `decentralized-api/{payloadstorage,statsstorage,apiconfig}`:
   - Outside `*_migrate.go` / `internal/migrate/`:
     `rg "CREATE TABLE|CREATE INDEX|CREATE UNIQUE INDEX|ALTER TABLE"`
     must produce zero matches (no DDL on hot paths).
   - Inside `*_migrate.go` / `internal/migrate/`:
     `rg "DROP TABLE|DROP COLUMN|DROP INDEX|RENAME TABLE|RENAME COLUMN|ALTER COLUMN"`
     must produce zero matches (no shape-changing DDL even in migrations).
   Documented in the SKILL-style header.
8. **Docs — update `devshard/docs/storage-design.md`.** Two additions and
   one cross-link:
   - **New section "Schema Evolution Across Devshard Versions"** placed
     immediately after §"Escrow ID Is Pinned To One Version". The text
     explicitly states:
     1. `versiond` can run multiple `devshardd` versions in parallel,
        and Postgres is shared across those processes (this is already
        documented for escrow routing; restate the storage consequence).
     2. SQLite per-epoch files are also shared across versions whenever
        two versions own escrows in the same epoch — same constraint.
     3. Therefore the schema is **forward-only and additive**:
        columns and indexes cannot be deleted, dropped, or renamed from
        one version to the next while any older version in the deployed
        set still touches the table.
     4. If a schema change requires dropping a column, narrowing a type,
        renaming a table, or changing a primary key, introduce a **new
        table** instead (`*_v2` naming), dual-write during the
        deprecation window, switch reads, and only schedule the legacy
        drop after every active version has been replaced.
     5. Migration entries live exclusively in `*_migrate.go` /
        `internal/migrate/` and are append-only.
   - **Cross-link** from §"Postgres Mirrors Payload Storage Style",
     §"SQLite Uses One File Per Epoch", and the new section to this plan
     document (`storage-migrations-plan.md`).
   - **Update §"Load Readiness"** to note that schema migrations run
     once at startup (no DDL on hot paths) and that the forward-only
     contract is what makes the multi-version deployment story safe.
   - **Document migration tooling choice** (see §"Note for
     storage-design.md" below): we use a small in-repo framework, not
     `golang-migrate` or `goose`.

## Forward-only schema rule, written down

This rule exists because **`versiond` runs multiple `devshardd` binary
versions in parallel against the same Postgres database and the same SQLite
files** (see `devshard/docs/storage-design.md` §"Escrow ID Is Pinned To One
Version" and the storage layout sections). The active binary set at any
moment is the union of all versions that still own at least one unsettled
escrow in the retention window, which is at least N=3 epochs. Any schema
change visible to one binary must be safe for **every** binary in that set.

The only safe form of change is **additive**:

- Append a new `Step` with an ID equal to `max(existing ID) + 1`. Never
  reuse an ID.
- New columns: `ALTER TABLE ... ADD COLUMN ... [NOT NULL DEFAULT ...]`.
  Old binaries ignore unknown columns on read; their `INSERT` lists must
  not break because the new column has a default. Required: always supply
  a server-side default (`DEFAULT 0`, `DEFAULT ''`, `DEFAULT NOW()`, etc.)
  or make the column nullable. **Never** add a `NOT NULL` column without a
  default — that breaks every older binary mid-flight.
- New indexes: `CREATE INDEX IF NOT EXISTS`. Old binaries are unaffected;
  the planner may pick the new index when the new binary runs, but old
  binaries continue to work.
- Statement uses `IF NOT EXISTS` where the driver supports it; otherwise
  guard with a `SELECT` from `pg_catalog` / `pragma_table_info` so reruns
  are no-ops.

**Hard prohibitions while any older binary in the version set may still
touch the table within the retention window:**

- `DROP TABLE`, `DROP COLUMN`, `DROP INDEX`, `RENAME TABLE`, `RENAME COLUMN`,
  `ALTER COLUMN TYPE` that narrows or reformats, change of `PRIMARY KEY`,
  change of partition strategy.
- Adding a `NOT NULL` column without a default. (Equivalent to a drop from
  the perspective of an older `INSERT` statement.)
- Tightening a `CHECK` constraint such that older binaries' writes start
  failing.

### "If you need to drop something, introduce a new table"

This is the operational rule. Replacing data shape across versions is done
by **adding** a new table and dual-writing, never by mutating the existing
table in place. Concrete recipe:

1. **Add** the new table with the new shape via a forward-only migration
   step. Old binaries do not know about it; that is fine.
2. **Dual-write** in the new binary: every write that touches the legacy
   table also writes to the new table. The legacy column / index continues
   to be populated so old binaries keep working.
3. **Switch reads** in the new binary to the new table. Old binaries still
   read the legacy table.
4. **Wait** until no binary in the active version set still reads the
   legacy table — i.e. until every version owning an unsettled escrow in
   the retention window has been replaced by a version that reads the new
   table.
5. **Stop dual-writing** in a later release. The legacy column / table is
   now dead data; do not drop it. Drop is its own decision, owned by a
   separate "legacy schema GC" pass that runs only after operators confirm
   no binary in the deployment is older than the deprecation point. Until
   that pass, dead-but-present is the correct state.

The same recipe applies to indexes that need different columns or types:
add a new index alongside, switch query plans by adjusting the query, drop
the old index only in the eventual GC pass.

### What this implies for table naming and code review

- New tables follow a naming convention that signals shape: e.g.
  `devshard_signatures_v2`, not `devshard_signatures_new`. The `v` suffix
  matches how `devshardd` itself versions and reads naturally in `\dt`.
- Code review for any migration step rejects the step if it contains
  `DROP`, `RENAME`, or `ALTER COLUMN` keywords against a pre-existing
  table. CI lint guard (step 7) extends to flag these keywords inside
  `*_migrate.go` and `internal/migrate/`, not just outside them.

`PruneEpoch` is **not** a migration. It drops per-epoch partitions or
per-epoch SQLite files, which by construction no surviving binary still
needs. It stays in the store, not in `migrate.go`.

## Test plan

- Unit: `Apply` is idempotent. Apply twice, expect no errors and unchanged
  `schema_migrations`.
- Unit: out-of-order IDs are rejected at startup.
- Unit: missing `IF NOT EXISTS` on a step that fails on rerun is caught by
  the idempotency test.
- Postgres: partition cache test for `devshard/storage/postgres.go` and
  `decentralized-api/payloadstorage/postgres_storage.go`. Call the
  per-epoch write twice; assert only one `CREATE TABLE ... PARTITION OF`
  was issued.
- SQLite: open epoch file with last release's schema fixture, run
  `MigrateEpochPool`, assert new column / index present and existing data
  intact.
- Lint: CI guard described in step 7.

## Out of scope

- Switching to `golang-migrate` or `goose`. Not worth the dependency for
  the volume of schema we own. Revisit when count > ~20 steps per store.
- Splitting devshard SQLite into per-session files. The current per-epoch
  model is already O(1) prune and survives this plan unchanged.
- Cross-store transactions. Each store keeps its own `schema_migrations`
  table and runs its migrations independently at process startup.

## Note for `storage-design.md`

When implementing step 8, add the following to
`devshard/docs/storage-design.md` (in the new schema-evolution / migrations
section, or under a short **"Schema migration tooling"** subsection):

> Switching to `golang-migrate` or `goose` is out of scope for now. It is not
> worth the dependency for the volume of schema we own. Revisit when a store
> has more than ~20 migration steps.

This matches the decision recorded here under §"Out of scope" and gives
operators and reviewers a single place in the design doc to understand why
migrations are a small in-repo `internal/migrate` helper rather than an
external tool.
