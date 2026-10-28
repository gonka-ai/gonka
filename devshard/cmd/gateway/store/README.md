# `store` — the control plane on disk

One SQLite database at `<storageDir>/gateway.db`, holding what must survive a restart.

## What it owns

| File | What it holds |
| --- | --- |
| `devshards.go` | the escrow registry: which escrows this gateway owns and their state |
| `overrides.go` | the operator's runtime configuration overrides |
| `commitments.go`, `rotation_status.go` | rotation intent recorded before the transaction, and where each rotation got to |
| `suspicious.go` | hosts the operator pinned as suspect |
| `model_filters.go` | the operator's per-model filter settings, one row per model, so a settings write never touches them |
| `accounting.go` | the asynchronous per-request ledger, with its own retention |
| `retry.go` | the retry policy for a locked database |

## Boundaries

- **This is control-plane state, not inference data.** Payloads and the nonce ledger live elsewhere.
- **Retention is enforced on both age and row count**, and a zero on either is rejected at construction, so an unbounded ledger cannot be configured.
- **A write that must survive a crash is one statement**, not a read-modify-write.

## Opening the database

`Open` creates the storage dir, opens `gateway.db`, refuses a devshardctl database, and migrates.

The connection pragmas — `journal_mode(WAL)`, `synchronous(NORMAL)`, `busy_timeout(5000)` — travel in the DSN rather than as statements issued after the open. They are per-connection, and a connection the pool recreates would come back without them, silently, with `busy_timeout` at 0 and `synchronous` back at FULL. In the DSN every connection carries them by construction.

The pool is one connection, kept for the life of the process. SQLite serializes writes anyway, and a second connection would only add the lock contention a single connection makes impossible.

Migrations run in order, one transaction per version, and the applied version is recorded in `schema_version` — so `Open` is idempotent and a version that failed part-way is never recorded as applied.

The list is append-only: a version number names whatever statement held that slot when a database recorded it, so replacing an entry leaves every database that already recorded its number without the new statement. The slot that holds `chain_epoch` is one an earlier build spent on `on_hold`, so a migration that only adds a column is matched by name as well (`applyMigration`): it is skipped when the column is already there, and after the numbered pass every such migration below the recorded version is run again and adds whatever column is missing. Any other kind of migration has no such repair, so append, never replace.

### The devshardctl-database guard

A `gateway.db` of a different schema can share two table names with this one at different columns, so migrating it would adopt that shape instead of failing. A database with no `schema_version` that holds any of `gateway_settings`, `gateway_devshards`, `gateway_suspicious_hosts` or `participant_throttle_state` is refused with `ErrLegacyDatabase`: this gateway never creates those tables, so their presence is proof rather than a guess.

The guard is declared apart from the migrations list because it decides whether the storage dir may be migrated at all, and because the migration entries are raw SQL whose indentation is part of the literal.

## Writing under a lock

`WithRetry` runs a write up to `retryAttempts` (10) times, waiting `retryBackoff` between attempts, to ride out a transient write lock. It returns immediately on success, aborts with `ctx.Err()` without sleeping past a cancellation, and otherwise returns the last error once every attempt is exhausted.

Only a locked database is retried. Everything else — a missing row, a constraint — fails the same way on every attempt, so retrying it spends the whole ladder to return the error the first call already had, and the caller waits seconds for an answer it could have had at once.

`isLockedError` matches `SQLITE_BUSY` and `SQLITE_LOCKED`, masked to the primary code: with one connection a lock surfaces in its primary form, but the driver enables extended codes, and a comparison that only matched the primary would stop retrying the moment the connection model changed. The driver's own `busy_timeout` absorbs the short waits, so seeing one of these codes here means the lock outlived it. It is a field on `Store` rather than a direct call because the driver's error type cannot be constructed outside its own package, and the ladder's own mechanics — backoff, cancellation, attempt count — have to be testable without provoking a real cross-process lock.

## The devshard registry

A row names the escrow, the environment variable holding the key that can act on it — never the key itself — its model, whether it is active, its rotation role and epoch, whether a settlement is queued, the hash and time of the settle transaction it last broadcast, the route prefix it was created under, the epoch the chain stamped on the escrow and the amount it locked (both 0 until read), and whether the chain answered that it no longer holds the escrow. An update or delete matching no row returns `ErrDevshardNotFound`, except `ParkForSettlementIfActive` below, which reports a boolean instead.

`UpsertDevshard` replaces every field of an existing row except six:

- `settlement_pending`, so an unrelated upsert never clears a queued settlement — only `SetDevshardSettlementPending` moves it;
- `settle_tx_hash`, which only `SetDevshardSettleTxHash` writes, beside the `settle_tx_at` stamp it is read against, so a re-registration can neither erase a broadcast hash nor leave a stamp for a hash it erased;
- `route_prefix`, because a host binds an escrow to the first version that reaches it and refuses every other one for good, so the version an escrow was created under must never change;
- `chain_epoch` and `amount`, which only `SetDevshardChainFacts` writes, from the chain's own answer; a re-registration cannot clear them, and a zero passed to `SetDevshardChainFacts` leaves its column as it was;
- `gone_from_chain`, which only `MarkDevshardGoneFromChain` sets, in the same statement that clears `active`. Every statement that writes `active = 1` clears it in the same write — `ActivateDevshardUnlessParked`, the operator's activation; `SetDevshardActive(true)`; and an upsert through `UpsertDevshard` or `UpsertDevshardUnlessParked` that writes `active = 1` — so a row an operator reactivates is no longer gone, and a later park settles it like any other row.

Its statement is a named constant so a test can read which columns the update carries: a field present in `DevshardRecord` but absent from that statement is inserted once and never updated again, and no other code in the package fails on it.

`ParkForSettlement` writes `active = 0` and `settlement_pending = 1` in one statement. Written as two, a crash between them leaves the row in a combination no recovery path picks up: the settle sweep looks for pending rows, so the escrow would be out of service and never settled.

`ParkForSettlementIfActive` is the same write restricted to a serving row, and reports whether it moved one — `false` for a row already inactive or gone, never `ErrDevshardNotFound` — so only the call that took a depleted escrow out of service replaces it.

`ActivateDevshardUnlessParked` and `UpsertDevshardUnlessParked` are the operator's activation and registration, written only to a row that is neither settlement-pending nor carrying a settle hash, in the same statement that checks it, and they report whether they wrote. A park that lands before the write wins: the row is left as the park wrote it. One that lands after the write retires the escrow, and the caller publishes through a registry listing taken before the write, so that retirement is not lost while the session opens ([`docs/escrows.md`](../docs/escrows.md), "Settlement and retirement").

Everything else is a targeted single-column update rather than a whole-record upsert, for one reason: a tick loads its escrows once and several steps act on that one slice, so the caller's copy predates whatever an earlier step in the same tick wrote. `SetDevshardRotationRole` moves the role without carrying a stale `active` or `settle_tx_hash` back into the row, and `DevshardSettleTxHash` reads the hash from the row rather than from the record the caller is holding.

`SetDevshardSettleTxHash` stamps `settle_tx_at` alongside the hash, and clears the stamp when the hash is cleared. That stamp is what lets a later tick tell a settle that may still land from one that no longer can, instead of building a second transaction for a settle already on its way. The stamp comes from the process clock, not SQLite's: the tick compares it with its own clock, and the two must be one.

## The commitment row

A `Commitment` is the durable intent for one escrow create, written before the transaction broadcasts so that a crash before the escrow id is known can be recovered by resolving its `TxHash` on chain. It carries what the create would have to be repeated with — model, role, key env, epoch, block height — and the creation time the reconciler measures its grace window from. Commitments load ordered by `(created_at, tx_hash)`, and deleting one that is already gone is a no-op.

`RotationStatus` beside it is the latest observed outcome of one (model, role) rotation stage, kept for admin-debug visibility only.

## The accounting ledger

`NewLedger` registers a single writer with the store, and `Store.Close` drains it before the database handle goes, so no queued row outlives the connection it needs. Only one open ledger is allowed at a time.

The ledger writes from one goroutine behind a queue of `ledgerQueueDepth` (1024) and **sheds rather than blocks**: its caller is on the response path. A row shed under load, or lost to a write failure, is counted in `Stats` and never fails the close. Inserts are `ON CONFLICT(request_id) DO NOTHING`, so a request recorded twice is not a second row. A `RequestRecord` carries no monetary cost: the race outcome that writes it knows only token counts.

### Retention

A sweep runs at most every `retentionSweepEvery` (1 min), and once more when the queue closes.

It runs whatever the insert before it did: a persistently failing insert is exactly when rows are least likely to be deleted and most likely to need it, and gating retention behind a successful write turns a write problem into an unbounded table. The two bounds are attempted independently — the row cap is the disk-fill guard, and a busy lock on the age delete must not be what stops it running. A sweep that cannot delete leaves the ledger growing past both bounds, so each failure is counted rather than dropped.

### Timestamps

`FormatTime` renders every timestamp this package stores, and the accounting API renders with the same function, so the retention cutoff and the rows it is compared against can never drift in precision or zone.

Its layout is fixed-width where `RFC3339Nano` is not. Retention compares and orders timestamps as text, and `RFC3339Nano` trims trailing zeros, so a whole second would sort after the same second plus a tenth and the sweep would prune by the wrong order. `parseTime` is the only reader of what `FormatTime` writes, the empty zero timestamp included.
