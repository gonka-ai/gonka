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
       -> SQLite
       -> HybridStorage when PGHOST is set
            -> Postgres
            -> SQLite fallback
```

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
```

Why: This matches `decentralized-api/payloadstorage/postgres_storage.go` and
keeps pruning as partition drops.

Consequence: `PruneEpoch` drops the three epoch partitions. Range prune lists
existing devshard partitions through `pg_inherits` and drops only partitions
older than the cutoff.

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
sidecars, then removes `_meta.db` rows for that epoch.

### SQLite Reconciles Eagerly On Startup

Decision: `NewSQLite` reads `_meta.db` and then scans existing `epoch_*.db`
files to verify and repair the index.

Why: `_meta.db` is only a routing index. A crash can leave a session row without
a meta row, or a stale meta row without a session. Eager reconciliation keeps the
runtime path simple and makes corruption visible at boot.

Consequence: SQLite startup is not fully lazy. It opens epoch files during
reconciliation. With N=3 retention this is bounded by the intended operating
window; if old files accumulate, startup work grows until pruning catches up.

### Hybrid Routing Is Sticky

Decision: When `PGHOST` is set, `HybridStorage` checks Postgres first when it is
available, then SQLite. Once an escrow is found or created in one backend, every
future session-keyed operation for that escrow uses the same backend.

Why: Devshard state is mutable append-log state. Falling back for an existing
Postgres-backed escrow would fork diffs, signatures, and finalized nonce.

Consequence:

- Existing Postgres-backed escrow + Postgres unavailable: fail the operation.
- Existing SQLite-backed escrow + Postgres reconnects: continue using SQLite.
- Same active escrow in both backends during startup scan: log a warning, keep
  the SQLite-routed copy, and continue recovering other sessions.

### SQLite Fallback Is Local-Only

Decision: If Postgres is unreachable and a new escrow is created in SQLite, that
escrow is local-only. It is not migrated or merged into Postgres later.

Why: When Postgres cannot be checked, storage cannot prove the escrow is absent
there. The fallback is an availability tradeoff for new sessions, not a
replication scheme.

Consequence: Operators must not assume SQLite fallback data will appear in
Postgres after reconnect. The session remains SQLite-routed until it settles or
is pruned.

### Managed Pruning Starts After Recovery

Decision: `NewManagedStorage` constructs the wrapper only. Callers start the
background pruner after legacy migration and `HostManager.RecoverSessions`.

Why: Pruning before recovery can delete old-but-active sessions before the host
has had a chance to replay them.

Consequence: dapi and `devshardd` wire storage in this order:

1. Create inner storage.
2. Run legacy migration.
3. Create `ManagedStorage`.
4. Run `RecoverSessions`.
5. Call `ManagedStorage.Start`.

Tests can use `PruneOnce` without starting the background loop.

### Prune Cursor Advances Only After Full Success

Decision: `ManagedStorage` advances `prunedUpTo` only when the inner prune call
returns success.

Why: A failed backend must remain retryable.

Consequence: In hybrid mode, if Postgres is unavailable during prune, SQLite may
delete local old files but the prune returns an error. The managed cursor does
not advance. When Postgres reconnects, a later range prune drops all partitions
older than the current cutoff.

### Legacy Migration Is Resumable

Decision: `MigrateLegacySQLite` is idempotent at the migration layer, not by
weakening normal storage writes.

Why: Live duplicate nonces should still fail. Migration is the only path that
needs to tolerate partially copied rows after a boot failure.

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

## Load Readiness

This design is not an early prototype. It is the production storage shape for
devshard session state under the assumption that every escrow lives inside one
epoch. The important production invariant is epoch-bounded lifetime: old shards
are removed by dropping an epoch partition or deleting an epoch file, not by
scanning individual escrows or nonces.

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
longer an unbounded local database. For local mode and Postgres outage fallback,
it is more ready for high load than the main-branch implementation because data
growth is bounded by retained epochs and pruning is file-level.

## Operational Notes

- Postgres env vars: `PGHOST`, `PGPORT`, `PGDATABASE`, `PGUSER`, `PGPASSWORD`.
- Hybrid retry knobs: `PG_RETRY_INTERVAL` default `240s`,
  `PG_CONNECT_TIMEOUT` default `2s`.
- Production retention is `retain=3`: current epoch plus two previous epochs.
- No SQLite VACUUM is used for pruning.

## Key Files

| Concern | Path |
|---|---|
| Storage interface | `devshard/storage/interface.go` |
| SQLite backend | `devshard/storage/sqlite.go` |
| Postgres backend | `devshard/storage/postgres.go` |
| Hybrid backend | `devshard/storage/hybrid.go` |
| Managed pruning | `devshard/storage/managed.go` |
| Legacy migration | `devshard/storage/migrate.go` |
| Factory | `devshard/storage/factory.go` |
| dapi wiring | `decentralized-api/main.go` |
| devshardd wiring | `decentralized-api/cmd/devshardd/main.go` |
