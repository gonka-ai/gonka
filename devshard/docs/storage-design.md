# Devshard Storage Design

This document records the storage decisions for devshard session state. It is
intentionally decision-focused: each section states the invariant, why it exists,
and the operational consequence.

## Goals

1. Persist every devshard session's metadata, diffs, signatures, finalized nonce,
   and settlement status.
2. Prune old local state with N=3 epoch retention without rewriting surviving
   epochs.
3. Use the same Postgres environment and partitioning style as payload storage.
4. Keep routing deterministic after restarts without querying the chain on every
   storage operation.

## Architecture

```
HostManager
  -> ManagedStorage
       -> HybridStorage (per-escrow router)
            -> SQLite only
            -> Postgres only
            -> SQLite + Postgres during legacy drain
```

`NewStorage` in `devshard/storage/factory.go` opens the backend set for the
process. `HybridStorage` then routes each escrow to the backend that owns it.
See [Storage mode selection](#storage-mode-selection) and
[storage-modes-plan.md](./storage-modes-plan.md).

The storage interface lives in `devshard/storage/interface.go`. `CreateSession`
is the only method that introduces an `EpochID`; all later calls use `escrow_id`
and route through a local `escrow_id -> epoch_id` index.

## Decisions

### Epoch ID Is The Partition Key

Decision: `epoch_id` is `DevshardEscrow.epoch_index` from the chain.

Why: The escrow pins the session's epoch once. All diffs and signatures for that
escrow belong to that partition even if settlement happens after an epoch
boundary.

Consequence: If local storage sees the same escrow in two epochs, it is
corruption. The code must return an error rather than choosing a side.

Epoch `0`: the chain can set effective epoch index to `0`, and
`MsgCreateDevshardEscrow` stores that value. Storage therefore treats epoch `0`
as valid and does not use it as a missing-value sentinel.

### Postgres Mirrors Payload Storage Style

Decision: Postgres uses pgx/libpq env vars and declarative range partitions.

Tables:

```sql
devshard_session_index(escrow_id PRIMARY KEY, epoch_id)
devshard_sessions   PARTITION BY RANGE (epoch_id)
devshard_diffs      PARTITION BY RANGE (epoch_id)
devshard_signatures PARTITION BY RANGE (epoch_id)
devshard_snapshots  PARTITION BY RANGE (epoch_id)
devshard_sealed_inferences PARTITION BY RANGE (epoch_id)
```

Why: This matches `decentralized-api/payloadstorage/postgres_storage.go` and
keeps pruning as partition drops.

Consequence: Devshard parent tables are created once at process startup via
`MigratePostgres` in `devshard/storage/postgres_migrate.go` (payload parent
`inferences` uses `ensureSchema` in `payloadstorage/postgres_storage.go`).
Per-epoch child
partitions are created lazily on first write through `ensurePartition` only —
no `CREATE TABLE` on hot paths. `PruneEpoch` drops epoch partitions at runtime;
that is retention, not schema migration (also described in
[Schema Evolution Across Devshard Versions](#schema-evolution-across-devshard-versions)).
Range prune lists existing devshard partitions through `pg_inherits` and drops
only partitions older than the cutoff.

### SQLite Uses One File Per Epoch

Decision: SQLite stores routing in `_meta.db` and session state in
`epoch_<N>.db` files.

```
_meta.db
epoch_<N>.db
epoch_<N>.db-wal
epoch_<N>.db-shm
```

Why: Removing a whole epoch is a file delete, not a row scan or VACUUM.

Consequence: SQLite pruning closes the epoch pool, deletes the epoch DB and WAL
sidecars, then removes `_meta.db` rows for that epoch. Schema for `_meta.db` and
each `epoch_<N>.db` is applied at first open via `MigrateMeta` /
`MigrateEpochPool` (see
[Schema Evolution Across Devshard Versions](#schema-evolution-across-devshard-versions)).

### SQLite Reconciles Eagerly On Startup

Decision: `NewSQLite` reads `_meta.db` and then scans existing `epoch_*.db`
files to verify and repair the index.

Why: `_meta.db` is only a routing index. A crash can leave a session row without
a meta row, or a stale meta row without a session. Eager reconciliation keeps the
runtime path simple and makes corruption visible at boot.

Consequence: SQLite startup is not fully lazy. It opens epoch files during
reconciliation. With N=3 retention this is bounded by the intended operating
window; if old files accumulate, startup work grows until pruning catches up.

### Storage Mode Selection

Decision: session storage (`NewStorage`) and payload storage
(`common/storage/payloads.Open`) share one mode knob, resolved by
`common/storage/mode`:

```text
DEVSHARD_STORAGE_MODE = sqlite | hybrid | postgres | auto   # default: auto
```

`PGHOST` / `PG*` are **connection** settings, not the mode bit (except under
`auto`, where a non-empty `PGHOST` selects hybrid). Multi-versiond deployments
behind `versiond-router` must use **`postgres`** (compose overlays / gencompose
set `DEVSHARD_STORAGE_MODE=postgres`). `VERSIOND_FORCE` alone never selects
postgres.

| Mode | Meaning |
| --- | --- |
| `sqlite` | Local only (SQLite sessions / file payloads). `PGHOST` is ignored. |
| `hybrid` | Requires `PGHOST`. Postgres primary with local fallback / reconnect. |
| `postgres` | Requires `PGHOST`. Fail-closed Postgres-only (multi-instance / HA). |
| `auto` (default) | Legacy-compatible derivation (below). |

**Auto resolution** (also when the env is unset):

1. if `PGHOST` is set → `hybrid`
2. else → `sqlite`

Fail-closed multi-instance deployments must set `DEVSHARD_STORAGE_MODE=postgres`
explicitly (compose overlays / gencompose do).

#### `sqlite` and `hybrid` (flexible)

`NewStorage` returns a per-session router (`HybridStorage`). The backend for a
**new** escrow is chosen at `CreateSession` time — Postgres in hybrid mode,
SQLite in sqlite mode. An **existing** escrow is always served by whichever
backend physically holds it, so a store can serve legacy SQLite escrows and new
Postgres escrows at the same time (drain-in-place).

| Condition | New escrows | Existing escrows |
| --- | --- | --- |
| mode `sqlite`, no `.pg-bound` | SQLite | SQLite |
| mode `sqlite`, `.pg-bound` present, no SQLite sessions | Boot fails (would orphan PG sessions) | — |
| mode `sqlite`, `.pg-bound` present, SQLite sessions exist | Rejected (WARN: degraded mode) | SQLite-owned only |
| mode `hybrid`, Postgres reachable, no local SQLite sessions | Postgres | Postgres |
| mode `hybrid`, Postgres reachable, local SQLite sessions exist | Postgres | SQLite drains in place; Postgres for the rest |
| mode `hybrid`, Postgres unavailable, local SQLite sessions exist | Rejected while reconnecting (WARN: degraded mode) | SQLite-owned only until PG reconnects |
| mode `hybrid`, Postgres unavailable, no local SQLite sessions | Rejected while reconnecting (WARN: degraded mode) | None until PG reconnects |
| mode `hybrid` or `postgres`, `PGHOST` unset | Boot fails (`ErrHAPostgresRequired` / payloads `ErrSharedPostgresRequired`) | — |

#### `postgres` (fail-closed / HA)

At boot, postgres mode:

1. Requires `PGHOST` (otherwise fails immediately).
2. Connects to Postgres; if unreachable, **boot fails** — no SQLite/file fallback.
3. If local SQLite session artifacts exist, **fully migrates every escrow into
   Postgres** (`MigrateSQLiteSessions`) before serving — partial multi-worker
   progress is not accepted; boot fails until the copy completes — then
   quarantines the SQLite files (`*.migrated.<ts>`).
4. If local file payloads exist under `{data-dir}/payloads/`, copies them into
   Postgres (`MigrateFilePayloadsToPostgres`) and quarantines epoch trees
   (`*.migrated.<ts>`).
5. Serves **Postgres-only** for the rest of the process lifetime.

| Condition | Behavior |
| --- | --- |
| `PGHOST` unset | Boot fails (`ErrHAPostgresRequired` / payloads `ErrSharedPostgresRequired`) |
| Postgres unreachable | Boot fails (`ErrStoragePostgresUnavailable`) — **no** degraded SQLite/file fallback |
| Postgres reachable, local SQLite sessions exist | Full SQLite→PG migrate (journal + sealed/obs), quarantine SQLite artifacts, run Postgres-only |
| Postgres reachable, local file payloads exist | Full file→PG migrate, quarantine epoch dirs, run Postgres-only |
| Postgres reachable, no local SQLite/files | Postgres-only |

Payload storage follows the same mode table: `postgres` is Postgres-only with
no file fallback and migrates on-disk
`payloads/{epoch}/{escrow}/{inference}.json` trees before serving. This
prevents multi-instance split-brain and avoids orphaning payloads that were
written before postgres mode was enabled.

Import the shared resolver from `common/storage/mode` (e.g. in versiond rolling
overlap checks) instead of re-parsing env flags.

SQLite → Postgres migration uses `MigrateSQLiteSessions` (public Storage API)
with a small worker pool (`DEVSHARD_MIGRATE_WORKERS`, default 4). Diffs are
read/written in chunks (`DEVSHARD_MIGRATE_DIFF_CHUNK`, default 5000) and
appended via `AppendDiffs` (Postgres `COPY`) so large sessions do not load the
entire journal into memory. Each escrow copy includes session meta, diffs,
signatures, finalized/settled, snapshot, **sealed inferences**, and
**validation obs** (live + sealed). After a successful full migrate,
`_meta.db` / `epoch_*.db` are renamed to `*.migrated.<ts>` so the next boot
does not re-attach them.

The `.pg-bound` marker tracks whether Postgres currently holds sessions for the
store, not whether it ever did. Its invariant is: **`<storeDir>/.pg-bound`
exists whenever Postgres holds at least one session.** It is written ahead of
each new Postgres `CreateSession` and cleared once a prune drains Postgres to
zero sessions (a boot-time reconcile also aligns it with reality and removes a
stale marker left by a fully-drained previous run). The write is held under the
same lock as the insert so a concurrent prune-driven clear cannot leave a live
Postgres session unmarked across a crash. Prune-time clearing also proves
emptiness against `devshard_session_index` before removing the marker, because a
timed-out create can commit server-side without updating the in-memory index.
Consequently a store whose Postgres sessions have all settled and pruned can
boot SQLite-only again without manually deleting the marker; the marker only
blocks the switch while Postgres sessions still exist.

The router only attaches SQLite when the store has SQLite artifacts and
`NewSQLite` reconciliation finds SQLite-owned escrows, so a store that has
always been Postgres never opens `_meta.db`. When it attaches SQLite to drain
legacy escrows it logs a WARN.

Ownership resolution: the router derives an escrow's backend from each
backend's own persistent index (SQLite `_meta.db` `escrow_epoch`, Postgres
`devshard_session_index`) - cached in memory and rebuilt lazily - rather than a
separate route table. Because `CreateSession` picks exactly one backend and
never falls back, a given escrow lives in only one backend, so append logs
cannot fork across backends. If both backends claim the same escrow, the router
quarantines that escrow with `ErrEscrowBackendConflict` and logs the conflicting
IDs at boot or promotion. Other escrows keep serving. This protects nodes that
carry state from an older dual-routing bug or from a manual `.pg-bound` override.
The earlier design failed because it kept an ephemeral route table that was lost
on reboot and let the same escrow land in both backends when Postgres was
briefly down.

Consequence (non-HA): A Postgres outage while `PGHOST` is set fails new-escrow
creation (and Postgres-owned operations); the router never silently creates a
Postgres-destined escrow in SQLite. Boot still succeeds in WARN-logged degraded
mode, with or without local SQLite sessions. It serves known SQLite escrows,
rejects new/unknown escrows, and runs a background reconnect loop. Once Postgres
reconnects, the router logs an INFO, leaves degraded mode, sends new escrows to
Postgres, and `devshardd` runs another `RecoverSessions()` pass so PG-owned
active sessions are eagerly restored. Legacy SQLite escrows no longer pin the
whole process to SQLite - they drain in place as they settle and prune while new
escrows go straight to Postgres, without waiting for `escrow_epoch` to empty or
for a restart.

Consequence (HA): Postgres is a hard dependency. An unreachable database aborts
boot so sticky multi-versiond never serves divergent local state. Operators must
bring Postgres back before the host rejoins the router pool.

### Managed Pruning Starts After Recovery

Decision: `NewManagedStorage` constructs the wrapper only. Pruning runs on
**epoch change** (runtime-config publish / long-poll) via `PruneOnce`, plus one
catch-up `Start()` after recovery — not on a periodic ticker.

Why: Pruning before recovery can delete old-but-active sessions before the host
has had a chance to replay them. Epoch transitions are already observed on the
dapi event-listener path.

Consequence: dapi and `devshardd` wire storage in this order:

1. Create inner storage.
2. Run legacy migration.
3. Create `ManagedStorage` and register epoch-change → `PruneOnce`.
4. Run `RecoverSessions`.
5. Call `ManagedStorage.Start()` (one-shot catch-up prune).

Tests can call `PruneOnce` directly.

### Prune Cursor Advances Only After Full Success

Decision: `ManagedStorage` advances `prunedUpTo` only when the inner prune call
returns success.

Why: A failed backend must remain retryable.

Consequence: A failed prune leaves `prunedUpTo` unchanged so a later
`PruneOnce` can retry.

### Live Diff Append Is Idempotent for Identical Replay

Decision: `AppendDiff` treats a second write of the **same**
`(escrow_id, nonce)` payload as success (`INSERT … ON CONFLICT DO NOTHING`,
then identity check). A **different** payload at the same nonce returns
`ErrDiffFork` and increments `devshard_diff_fork_detected_total`.

Why: Multi-instance HA + shared Postgres means at-least-once delivery of
gateway-sequenced, byte-identical diffs is normal (stale standby catch-up).
That is not a bug; failing with SQLSTATE 23505 turns a successful durable
write into an HTTP 500. Conflicting bytes remain a hard error (real fork).

See [proposals/ha-diff-persist-consistency.md](./proposals/ha-diff-persist-consistency.md).

### Legacy Migration Is Resumable

Decision: `MigrateLegacySQLite` is idempotent at the migration layer; live
`AppendDiff` is separately idempotent for identical replay (above). Migration
still verifies already-copied rows against the source after a boot failure.

Why: Migration may resume after a partial copy; identity checks prevent silent
forks when a destination row already exists.

Consequence:

- Existing destination session must match the resolved epoch.
- Existing destination diff for a legacy nonce is verified against the legacy
  row.
- Missing signatures are replayed with `AddSignature`.
- Conflicting copied data stops migration with an error.
- The legacy DB is renamed only after all resolved sessions are copied or
  verified.

### Escrow ID Is Pinned To One Version

Decision: `escrow_id` maps to exactly one `(epoch_id, version)` pair.

Why: `versiond` can run multiple `devshardd` versions at the same time, and
Postgres is shared across those processes. A request routed to the wrong version
must not attach to an existing escrow and replay it with different state-machine
rules.

Consequence: `CreateSession` is idempotent only when both epoch and version
match. Same escrow and epoch with a different version returns a version conflict.
Recovery also skips sessions whose stored version does not match the running
binary.

### Duplicate Create Metadata Is Not Rewritten

Decision: `CreateSession` is idempotent for the same `(escrow_id, epoch_id)` and
version and does not update existing metadata.

Why: The chain pins the escrow. Recreating a session should not mutate its local
state after diffs may already exist.

Consequence: Callers that attempt to create the same escrow with different
non-version metadata keep the first row. Conflicting epoch or version creates
return an error.

### Schema Evolution Across Devshard Versions

Decision: **Devshard session storage** uses a **forward-only, append-only**
migration list recorded in `schema_migrations`. Schema changes are applied
**once at startup** (or on first open of a per-epoch SQLite file), not on
request, diff, or payload write paths. Other dapi SQL (`gonka.db` /
`apiconfig`, `inference_stats` / `statsstorage`, off-chain `payloadstorage`)
keeps inline `EnsureSchema` (or equivalent `CREATE TABLE IF NOT EXISTS`) at
boot and is out of scope for this framework.

Why:

1. **`versiond` can run multiple `devshardd` versions in parallel.** Escrow
   routing pins a session to one binary version, but **Postgres is shared**
   across processes — every version in the retention window may read and write
   the same database.
2. **SQLite per-epoch files are shared** when two versions still own escrows in
   the same epoch (`epoch_<N>.db` is not per-binary).
3. While any older binary in the deployed set may still touch a table, schema
   must remain **additive**: new tables, new columns (with defaults), new
   indexes — never in-place drops or renames on live tables.
4. **Destructive shape changes** use a new table (e.g. `*_v2`), dual-write,
   switch reads in the new binary, stop dual-write only after every active
   version has upgraded, and defer physical drop to a separate GC pass.
5. **Migration entries** live only in devshard `*_migrate.go` and
   `devshard/storage/migrate/`. They are append-only ordered steps; CI runs
   `scripts/check-storage-ddl.sh` to block stray `CREATE TABLE` / `CREATE INDEX`
   in store code and destructive keywords inside migration files.

Consequence:

- Implementers add a new `Step` with `id = max(existing) + 1`; never reuse an
  ID. New columns use `ALTER TABLE ... ADD COLUMN` with a default or nullable
  type; new indexes use `CREATE INDEX IF NOT EXISTS`. While an older binary may
  still write the table, do not `DROP`, `RENAME`, or narrow columns; do not add
  `NOT NULL` without a default.
- **`PruneEpoch` is not a migration.** It drops per-epoch partitions (Postgres)
  or deletes per-epoch files (SQLite) that no surviving binary still needs.
  That is bounded retention (N=3), not schema evolution.
- Lazy **`CREATE TABLE ... PARTITION OF`** for a new epoch is allowed only in
  `ensurePartition` (devshard Postgres; dapi payload Postgres uses the same
  pattern inline in `payloadstorage/postgres_storage.go`), not in migrate files
  and not duplicated on individual write methods.

#### Schema migration tooling

We use a small in-repo helper at `devshard/storage/migrate/` (`ApplyPG`,
`ApplySQLite`, `schema_migrations` table). We do **not** use `golang-migrate`
or `goose` for these stores — the schema surface is small and the critical
requirement is a strict forward-only contract across parallel binary versions.
`ApplySQLite` enables `journal_mode=WAL`; still assume **one devshardd process
per store directory** — two processes on the same `_meta.db` can race
`schema_migrations` despite per-step transactions.
Revisit an external tool only if a single store grows past roughly twenty
migration steps.

## Load Readiness

This design is not an early prototype. It is the production storage shape for
devshard session state under the assumption that every escrow lives inside one
epoch. The important production invariant is epoch-bounded lifetime: old shards
are removed by dropping an epoch partition or deleting an epoch file, not by
scanning individual escrows or nonces.

Schema is applied at **process startup** (and on first open of each SQLite epoch
file) through the migration helpers — not during steady-state reads or writes.
That keeps hot paths free of DDL and, together with the forward-only rule in
[Schema Evolution Across Devshard Versions](#schema-evolution-across-devshard-versions),
allows multiple `devshardd` versions to share Postgres and SQLite files safely
while any older version still holds unsettled escrows in the retention window.

For a high-load epoch with 1000 active shards and 100000 nonces per shard:

- Postgres is the intended production backend. The write path targets one
  epoch partition, uses primary keys on `(epoch_id, escrow_id, nonce)`, and
  prunes the full epoch with partition drops.
- SQLite remains a local single-process backend and fallback. It has one writer
  per epoch DB, so it is not the preferred backend for sustained multi-host
  production load, but it is still more stable than the main-branch SQLite
  layout.
- Recovery must be treated as a replay workload. At this scale, callers should
  replay diffs in nonce windows instead of loading a full 100000-diff session
  into memory at once.
- Migration must avoid per-nonce destination probes on clean first migration.
  It should resume from already-copied nonce ranges and verify existing rows
  only on retry.

The SQLite backend is still a concrete improvement over the main-branch
single-file SQLite store:

- Main branch stores all sessions, diffs, and signatures in one SQLite file.
  That file grows across epochs and is not pruned.
- This design stores each epoch in `epoch_<N>.db` and deletes old epochs as
  whole files.
- Main branch has no persistent `escrow_id -> epoch_id` routing key because it
  has no epoch partitions.
- This design has `_meta.db`, startup reconciliation, explicit conflict
  detection, and bounded retention.

So SQLite is not the target for the largest sustained deployment, but it is no
longer an unbounded local database. For local development and draining legacy
SQLite state during a Postgres transition, it remains supported. Data growth is
bounded by retained epochs and pruning is file-level.

Production deployments target Postgres-only mode (`PGHOST` set, empty
`escrow_epoch`). SQLite is not used as a runtime fallback when Postgres goes
down after boot.

## Operational Notes

- Postgres env vars: `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGPASSWORD`.
- Postgres connect deadline at boot: `PG_CONNECT_TIMEOUT` default `2s`.
- Postgres schema migration deadline: `PG_MIGRATION_TIMEOUT` default `2m`.
  It includes time waiting for another devshard migrator to release the
  database-scoped migration lock.
- Migration bootstrap and all pending application steps are serialized by a
  database-scoped advisory lock. Before starting children in an HA deployment,
  versiond runs `devshardd --initialize-postgres-schema` through a current
  downloaded artifact. Children that predate this command wait behind the
  initialization barrier. Concurrent versiond replicas may run the initializer,
  but the database lock serializes them. This gives fresh mixed-version installs
  the same deterministic schema ordering as normal upgrades. Versiond first
  classifies every desired binary; when the entire desired catalog predates the
  initializer command, it preserves the legacy startup behavior instead of
  waiting for a capability that is not present.
- `devshard_storage_identity.identity` is a durable database-lineage marker.
  Copies restored from one backup retain the same marker, so equality alone is
  not proof that two hosts currently share a database.
- Each devshard process also holds a unique session-scoped PostgreSQL advisory
  fence before serving. Every connection subsequently created by its application
  pool must observe that fence in `pg_locks` and report a writable primary before
  pgx admits it. Advisory locks are not WAL-replicated, so a promoted fork does
  not inherit the fence. A DNS or load-balancer endpoint may expose multiple
  addresses for one logical writer, but it cannot silently mix independent
  writable branches within one child pool. Losing the fence session is
  fail-closed for new connections; restoring service on a promoted writer
  requires restarting the child so it establishes a fresh fence.
- The PostgreSQL endpoint remains responsible for cluster-level writer fencing:
  it must expose at most one writable primary for a logical database. The
  advisory fence prevents one child pool from crossing branches, and the live
  challenge detects divergent children when deployment preflight runs; neither
  mechanism elects a PostgreSQL primary or replaces database-layer consensus.
- HA deployment preflight obtains a stable generation snapshot from each
  versiond replica. For every HA-routed child generation across those snapshots,
  it writes a unique random nonce through that one child's application pool and
  requires every other generation to read it. Each operation names its child
  generation and carries the original snapshot token; transitional generations
  and changed snapshots fail closed. Every operation also requires
  `pg_is_in_recovery() = false`. This catches independent clones, read replicas,
  cross-wired protocol versions, and configuration differences between a
  supervisor and its children. A final snapshot read closes the transaction.
  Deployment tooling serializes the exchange; concurrent challenges can cause
  only a safe false negative because each write replaces the previous nonce.
- Live readiness uses one dedicated PostgreSQL connection outside the
  application pool. Two consecutive database failures make `/readyz` return
  `503` without terminating devshardd; two successful probes restore readiness.
  Application-pool saturation is exposed separately and does not by itself
  change readiness.
- Storage mode helpers and `.pg-bound`: `devshard/storage/storage_mode.go`.
- Drain check: `HasSQLiteSessions(storeDir)` or presence of rows in `_meta.db` `escrow_epoch`.
- Production retention is `retain=3`: current epoch plus two previous epochs.
- No SQLite VACUUM is used for pruning.

## Key Files

| Concern | Path |
|---|---|
| Storage interface | `devshard/storage/interface.go` |
| SQLite backend | `devshard/storage/sqlite.go` |
| SQLite meta / epoch schema | `devshard/storage/sqlite_meta_migrate.go`, `sqlite_epoch_migrate.go` |
| Postgres backend | `devshard/storage/postgres.go` |
| Postgres parent schema | `devshard/storage/postgres_migrate.go` |
| Shared migrate framework | `devshard/storage/migrate/` |
| DDL placement CI guard | `scripts/check-storage-ddl.sh` |
| Hybrid backend | `devshard/storage/hybrid.go` |
| Managed pruning | `devshard/storage/managed.go` |
| Legacy data copy | `devshard/storage/migrate.go` |
| Factory / mode selection | `devshard/storage/factory.go`, `storage_mode.go` |
| dapi wiring | `decentralized-api/main.go` |
| devshardd wiring | `decentralized-api/cmd/devshardd/main.go` |
