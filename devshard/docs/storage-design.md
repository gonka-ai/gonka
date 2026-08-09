# Devshard Storage Design

This document records the storage decisions for the four persistence planes in a
gateway deployment: session storage, payload storage, gateway management store,
and epoch accounting. It is intentionally decision-focused: each section states
the invariant, why it exists, and the operational consequence.

Session-storage sections dominate the early Decisions list for historical
reasons; [Gateway store](#gateway-store) and
[Epoch accounting](#epoch-accounting) document the other two gateway-owned
planes.

## Goals

1. Persist every devshard session's metadata, diffs, signatures, finalized nonce,
   and settlement status.
2. Prune old local state with N=3 epoch retention without rewriting surviving
   epochs.
3. Use the same Postgres environment and partitioning style as payload storage.
4. Keep routing deterministic after restarts without querying the chain on every
   storage operation.
5. Persist gateway management state and the epoch accounting ledger with
   fail-closed Postgres when multi-instance HA is selected, without silent
   last-writer-wins loss on counters.

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

There are **four distinct persistence planes** in a gateway deployment, all
switched by the same knob:

| Plane | What it stores | Code | Switch |
| --- | --- | --- | --- |
| **Session storage** | Per-escrow diffs, signatures, snapshots, validation obs (host/runtime) | `devshard/storage.NewStorage` | `DEVSHARD_STORAGE_MODE` |
| **Payload storage** | Inference payloads (dapi) | `common/storage/payloads` | `DEVSHARD_STORAGE_MODE` |
| **Gateway store** | Gateway registry: settings, active escrows, rotation commitments, throttles | `devshardctl` `gateway.db` / Postgres | `DEVSHARD_STORAGE_MODE` |
| **Epoch accounting** | Gateway epoch counters ledger (`accounting.db` / Postgres) | `devshard/accounting` | `DEVSHARD_STORAGE_MODE` |

`DEVSHARD_STORAGE_MODE` is resolved by `common/storage/mode`:

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
| `sqlite` | Local only. `PGHOST` is ignored (warned if set). |
| `hybrid` | Requires `PGHOST`. Postgres primary; session storage may degrade locally while reconnecting. Gateway/accounting are fail-closed. |
| `postgres` | Requires `PGHOST`. Fail-closed Postgres-only (multi-instance / HA). |
| `auto` (default) | Legacy-compatible derivation (below). |

**Auto resolution** (also when the env is unset):

1. if `PGHOST` is set → `hybrid`
2. else → `sqlite`

Fail-closed multi-instance deployments must set `DEVSHARD_STORAGE_MODE=postgres`
explicitly (compose overlays / gencompose do).

#### Uniform wiring

Every Postgres-capable plane calls `mode.Resolve()`:

- `sqlite` → never opens Postgres, even if `PGHOST` is set (logged as ignored)
- `hybrid` / `postgres` → require `PGHOST`; open/boot fails if Postgres is
  unreachable for gateway store and epoch accounting (no SQLite write fallback)
- Session storage under `hybrid` is the exception: it may serve existing
  SQLite-owned escrows and reject new creates while reconnecting (owner-only
  degraded mode). That fallback is intentional and session-only.

**Migration is part of the switch.** Whenever `hybrid` or `postgres` opens
successfully, local SQLite state is imported into Postgres *before* the plane
serves:

| Plane | Import on Postgres open | Runtime SQLite fallback |
| --- | --- | --- |
| Session storage | `MigrateSQLiteSessions` (postgres mode quarantines `*.migrated.<ts>`) | Yes (hybrid degraded / owner-only) |
| Payload storage | `MigrateFilePayloadsToPostgres` (postgres mode quarantines trees) | Hybrid reconnect semantics |
| Gateway store | `MigrateGatewaySQLiteToPostgres` + drain leftover `gateway_sync_journal` | **No** — Postgres-only |
| Epoch accounting | `migrateSQLiteAccountingToPostgres` from `accounting.db` | **No** — Postgres-only |

Imports are idempotent: a marker row (`gateway_migration` / `accounting_migration`)
plus a non-empty-destination guard makes a second boot a no-op, and a Postgres
destination that already holds state is never overwritten by a stale local file.
Accounting carries a second marker, `blob_to_rows`, for the one-shot conversion of
the pre-additive `accounting_escrows` blob table into the row layout in
[Epoch accounting](#epoch-accounting).

Gateway sync-journal drain exists only to absorb rows left by older builds that
still fell back to SQLite; new gateway/accounting processes never write that
journal.

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

### Gateway store

Decision: gateway management state is a relational store behind a `GatewayStore`
interface with two backends (`SQLiteGatewayStore`, `PostgresGatewayStore`). The
factory (`NewGatewayStore`) selects the backend from `DEVSHARD_STORAGE_MODE` /
`PGHOST`. When Postgres is selected the store is **Postgres-only and fail-closed**
— there is no runtime SQLite fallback and nothing writes a sync journal. Local
`{baseStorageDir}/gateway.db` is only a migration source (plus a read-only drain
of leftover hybrid-era journal rows).

Why: gateway settings, active escrows, rotation commitments, and throttle state
must be shared across gateway instances. A hybrid SQLite fallback would let two
instances diverge during a Postgres blip and then race on reconnect. Session
storage keeps its own degraded owner-only SQLite path; gateway management does
not — a missing Postgres is a boot/write failure, not a silent local fork.

Consequence:

```
NewGatewayStore(ctx, baseStorageDir)
  -> mode sqlite / auto-without-PGHOST: SQLiteGatewayStore(gateway.db)
  -> mode hybrid / postgres (PGHOST required):
       PostgresGatewayStore (fail if unreachable)
       importGatewaySQLite: MigrateGatewaySQLiteToPostgres + drain leftover journal
       return Postgres only
```

Tables (same names in SQLite and Postgres):

| Table | Key | Purpose |
| --- | --- | --- |
| `gateway_settings` | singleton `id=1` | gateway config (limits, throttle policy, rotation knobs, …) |
| `gateway_devshards` | `id` | managed escrow / topology rows |
| `participant_throttle_state` | `participant_key` | reactive throttle / quarantine |
| `gateway_rotation_status` | `(model_id, stage, epoch)` | rotation audit trail |
| `gateway_suspicious_hosts` | `participant_key` | operator-flagged hosts |
| `escrow_rotation_commitments` | `tx_hash` | write-ahead intent for escrow creates |
| `gateway_migration` | `name` | idempotent import markers |

**Migration.** `MigrateGatewaySQLiteToPostgres` copies every table in one Postgres
transaction, then writes the `sqlite_import` marker. If Postgres already holds
settings (or the marker already exists), the import is skipped and the marker is
still written so later boots do not re-evaluate a stale local file. Commitments
are part of the copy — they are write-ahead intents and must not be lost. After
import, any leftover `gateway_pg_sync_journal` rows from older hybrid builds are
drained read-only; new processes never write that journal. A malformed journal
row fails boot with a loud log naming `table` / `row_key` / `op` so an operator
can `DELETE` it by hand.

**HA write semantics (gateway store).** Gateway rows are **registry / config**,
not event counters. Concurrent writers merge with SQL upserts
(`ON CONFLICT … DO UPDATE`) that are last-writer-wins **per primary key**:

- Settings: one singleton row; the last `UpdateSettings` wins.
- Devshards / throttles / suspicious hosts / commitments: last upsert or delete
  for that key wins.
- Rotation status: last write for `(model_id, stage, epoch)` wins.
- Devshard upserts preserve an existing `settlement_pending` marker so an
  unrelated topology refresh cannot clear a settlement in flight.

That is the right merge for configuration: two gateways flipping the same
setting should converge to one value, not add. It is **not** safe for counters —
those live in epoch accounting below. Multi-instance gateway HA therefore
assumes operators run one logical control plane (or accept LWW on overlapping
admin writes), while escrow traffic accounting stays additive even if two
instances briefly hold the same escrow.

Env (shared via `common/storage/pgtimeouts`): `PGHOST` / `PGPORT` /
`PGDATABASE` / `PGUSER` / `PGPASSWORD`, `PG_CONNECT_TIMEOUT` (default `2s`),
`PG_OPERATION_TIMEOUT` (default `2s`, per-call deadline derived from the request
context; `0` disables), `PG_IMPORT_TIMEOUT` (default `5m`, boot import + journal
drain). Server-side `statement_timeout=5s` and `lock_timeout=3s` are fixed
package defaults, not env vars. Gateway tables may share the same database as
session/payload tables; names do not collide.

Code: `devshard/cmd/devshardctl/gateway_store*.go`. Historical hybrid design notes
remain in [gateway-postgres-backend-plan.md](./gateway-postgres-backend-plan.md);
the shipped behaviour is this section.

### Epoch accounting

Decision: the gateway epoch ledger (`devshard/accounting`) records, per escrow,
how nonces were assigned and how they resolved — not the inference payloads
themselves. Session storage holds diffs/signatures; accounting holds the
**aggregate view** used by `/accounting/*` and settlement-facing summaries.

#### What is accounted

For each registered escrow the tracker keeps:

| State | Meaning |
| --- | --- |
| Metadata | escrow id, creation epoch, model, slot assignments, timeouts, phase (`active` → `finalizing` → `finalized` → `settled`) |
| `latest` | highest assigned nonce watermark |
| Counters | counts keyed by slot + disposition (+ timeout / quarantine / no-send / failure detail) |
| Host stats | per-slot missed / invalid / cost / required & completed validations (mirrors absolute chain numbers) |
| Protocol-only nonce set | nonces consumed without starting an inference, with the slot each was assigned to |
| Challenge set | challenged nonces, their executor slot, and whether the challenge has been resolved |
| Invalid nonce set | invalidated nonces and their executor slot (idempotent `recordInvalid`) |

Dispositions classify a nonce's outcome (`protocol_only`, `ghost`,
`finished_used` / `unused` / `usage_unknown`, `unfinished_refused` /
`unfinished_execution`, …). Live in-memory nonce state is **not** persisted;
only the folded counters and the compact sets above are. The recorder feeds the
tracker from committed diffs, protocol events, timeouts, and host-stats sync.

Per-slot unresolved-challenge and invalid totals are **derived from the sets**
when a view is built, not stored as counts. Protocol-only nonces likewise fold
into a `protocol_only` counter at read time. The ledger's denominator is
arithmetic on the watermark — `AssignedNoncesForSlot` says how many nonces in
`1..latest` belong to a slot — so every consumed nonce must be explained by
exactly one counter or it surfaces as `Unclassified`.

Challenge lifecycle, why `ChallengeBySlot` was a precomputed report counter, the
legacy carry, and when it can leave the codebase are documented with the gateway
accounting merge framework in
[gateway.md](gateway.md#challenges-and-the-legacy-challengebyslot-carry).

#### How multiple gateways conflict

A single-process SQLite ledger never sees concurrent writers. Under Postgres HA,
two (or more) `devshardctl` instances can observe the **same escrow** — failover
overlap, a stale instance that has not noticed it lost traffic, an operator
running a second gateway, or intentional multi-writer deployments.

The dangerous shape is **last-writer-wins on a whole-escrow blob**:

1. Instance A loads escrow `E`, counts nonces `{1,2}`, flushes blob
   `{counters: 2, …}`.
2. Instance B loads the same row (or an older snapshot), counts nonce `{3}`,
   flushes blob `{counters: 1, …}` (or `{counters: 3}` if it had loaded A's
   state, then A flushes again with only `{1,2}`).
3. Whichever flush lands last **replaces** the payload. The other instance's
   observations disappear with no error.

Every field in the blob was exposed: counters, host stats, challenge/invalid
maps, invalid-nonce set, `latest`, and phase. Dirty-only upserts (write only the
escrows this process touched) stop one instance from wiping a *peer escrow*, but
do not stop two instances from clobbering the **same** escrow.

#### Mitigation: additive / aggregative Postgres ledger

Postgres accounting stores an escrow as **rows with an explicit merge rule per
field**, not as one JSON blob.

Which merge rule is correct depends on **whether two instances can observe the
same event**, so the fields fall into two groups.

*Request-local facts* are produced by the instance that dispatched the nonce.
`counterKey` only yields a disposition when a local signal is present — `Ghost`,
`Sent`, or `Usage`, set by the gateway calling `RecordGhost`, `RecordRealSend`,
or `RecordUsage`. A passive instance sees the start and the finish on chain but
never learns whether the result was used, so it classifies nothing. Writers
therefore hold **disjoint** sets and a reader sums them.

*Replicated facts* are read straight off the committed diff stream, so every
instance attached to the escrow derives them identically. No arithmetic merge
works here: summing turns one chain event into one count per instance, and
taking the max drops what an instance with a stale view never saw. They are
persisted **by identity** — one row per nonce, no `writer_id` — and merged as a
set.

| Field | Table | Merge rule |
| --- | --- | --- |
| Counters (per slot / disposition) | `accounting_escrow_counters` | Request-local: `SUM` across `writer_id` |
| Protocol-only nonces | `accounting_escrow_protocol_nonces` | Replicated: set union (`ON CONFLICT DO NOTHING`) |
| Invalid nonces | `accounting_escrow_invalid_nonces` | Replicated: set union |
| Challenges | `accounting_escrow_challenges` | Replicated: set union, `resolved` merged with monotonic `OR` |
| Host stats | `accounting_escrow_host_stats` | `GREATEST` per column (absolute chain numbers; tracker also merges with max) |
| `latest` nonce watermark | `accounting_escrow_state` | `GREATEST` |
| Escrow phase | `accounting_escrow_state` | Highest rank wins (phases only move forward) |
| Metadata (epoch, model, slots, timeouts) | `accounting_escrow_state` | Identity at registration; `RegisterEscrow` rejects conflicting metadata |
| Flush timestamp, writer error count | `accounting_writers` | Per writer row |
| Pre-set per-slot totals | `accounting_escrow_slot_counts` | Frozen legacy baseline, written only by the `blob_to_rows` conversion |

The summed counters are **partitioned by writer instead of incremented in SQL**.
Each instance owns rows keyed `(escrow_id, writer_id, …)` holding its own
contribution, computed at flush time as
`in-memory total − peer contribution observed at Load` (clamped at zero). Three
operational consequences:

- A flush is **idempotent**. Summed rows are written as absolute values the
  instance alone owns, and set rows are insert-if-absent, so replaying a flush
  whose commit was reported as failed (the classic ambiguous timeout) cannot
  double count — which a bare `count = count + delta` would.
- An instance **never touches a peer's rows**, so the ledger stays correct
  without a lease, fencing token, or exclusive escrow ownership.
- A challenge is never deleted on resolution, only flagged. Deleting it would let
  a repeated verdict from another instance reopen it, and would lose the record a
  restart needs to recognise the nonce.

`Store.Save` holds one mutex across taking the snapshot **and** writing it.
Because `takePersistSnapshot` clears the dirty set and counters are absolute, an
interleaved older write would otherwise be lost permanently rather than corrected
on the next tick — and `Flush` runs from the snapshot ticker as well as from
settle and retire.

`DEVSHARD_ACCOUNTING_WRITER_ID` names the request-local row set. Resolution:
env → hostname → `"default"`.

> **HA requirement.** Multi-instance gateway against shared Postgres **must**
> set a stable, unique `DEVSHARD_ACCOUNTING_WRITER_ID` per replica. Colliding
> ids make two processes rewrite the same `(escrow_id, writer_id, …)` rows and
> lose request-local contributions. An *unstable* unique value (new pod name
> every restart) does not double-count — earlier rows are read as peer
> contributions — but leaves stale writer partitions until retention prunes the
> escrow. Prefer a StatefulSet ordinal / persistent pod name. SQLite ignores
> the variable.

**Factory.** Same mode switch as the gateway store: `sqlite` → local
`accounting.db`; `hybrid` / `postgres` → Postgres-only (fail-closed), with a
one-shot `migrateSQLiteAccountingToPostgres` guarded by the `sqlite_import`
marker. A second marker, `blob_to_rows`, converts any pre-additive
`accounting_escrows` blob table into the row layout under a frozen
`_legacy_blob` writer, then drains the blob table. Leftover blobs written by an
older build after conversion are **not** re-imported (that would double count);
boot logs a warning instead.

**SQLite field evolution (no HA merge).** Staying on SQLite keeps the one-blob
layout. New fields are new JSON keys inside `payload`; there is no SQL column
migration and no `writer_id`. Missing keys decode as zero, and the next flush
rewrites the blob. Representation changes keep old tags as read-only carries.
The operator checklist for classifying fields and for Postgres DDL lives in
[gateway.md](gateway.md#ha-merge-framework); the SQLite-only path is
[gateway.md](gateway.md#staying-on-sqlite-how-new-fields-appear).

**Read model.** In-memory `/accounting/*` stays per instance: an instance sees
peer counts as of its last Load, not live. Postgres holds the merged truth; the
aggregate is what a reload, a settlement job, or an aggregate over the tables
produces.

`Load` reads all tables inside one `REPEATABLE READ` read-only transaction. Read
table by table, a peer committing an escrow mid-load would appear as counter rows
whose state row is not there yet; such an escrow cannot be reconstructed, so it is
logged and skipped rather than failing the load — a failed `Load` disables
accounting for the whole process lifetime, and every later boot would fail on the
same row. The same applies to an escrow whose metadata does not validate. `Load`
runs under the import budget, not the 2 s per-operation timeout, for the same
reason.

**Deliberate limits.** Retention pruning deletes the escrow for *all* writers,
matching the previous blob behavior, and it is computed from the pruning
instance's own epoch horizon. Per-slot totals carried from the pre-set layout
cannot be attributed to nonces, so they stay a frozen baseline the derived counts
add to, and they age out with retention.

Code: `devshard/accounting/store_postgres.go`, `store_postgres_rows.go`,
`factory.go`. SQLite remains a single-writer blob store for local/dev mode.

Adding a field to the ledger means picking a class and a merge rule first; the
decision procedure and the touch-point checklist are in
[gateway.md](gateway.md#ha-merge-framework).

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
- Timeout defaults and env readers live in `common/storage/pgtimeouts` (shared by
  gateway, accounting, session storage, payload reconnect):
  - `PG_CONNECT_TIMEOUT` default `2s` (dial/auth)
  - `PG_OPERATION_TIMEOUT` default `2s` (Go per-call deadline; `0` disables; gateway + accounting)
  - `PG_IMPORT_TIMEOUT` default `5m` (boot SQLite→PG import; gateway + accounting)
  - server-side `statement_timeout=5s` and `lock_timeout=3s` are **not** env-tunable;
    they are applied as connection `RuntimeParams` on every pooled connection
- Gateway env table: [gateway.md](./gateway.md) (devshardctl). Session / `devshardd`
  also honor the same `PG_*` timeout env vars via the shared package.
- Accounting writer identity: `DEVSHARD_ACCOUNTING_WRITER_ID` (default hostname).
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
| Shared mode resolver | `common/storage/mode` |
| Shared Postgres timeouts | `common/storage/pgtimeouts` |
| Gateway store interface / factory | `devshard/cmd/devshardctl/gateway_store.go`, `gateway_store_factory.go` |
| Gateway SQLite / Postgres | `gateway_store_sqlite.go`, `gateway_store_postgres.go` |
| Gateway SQLite→PG migrate + journal drain | `gateway_store_migrate.go`, `gateway_store_sync_journal.go` |
| Epoch accounting tracker | `devshard/accounting/tracker.go` |
| Accounting SQLite / Postgres / additive rows | `store_sqlite.go`, `store_postgres.go`, `store_postgres_rows.go` |
| Accounting factory | `devshard/accounting/factory.go` |
| dapi wiring | `decentralized-api/main.go` |
| devshardd wiring | `decentralized-api/cmd/devshardd/main.go` |
