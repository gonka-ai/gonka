# Storage Mode Selection Plan

## TL;DR

Today's `devshard/storage/factory.go:NewStorage` builds a `HybridStorage`
whenever `PGHOST` is set. The hybrid is **always-on dual-backend** (SQLite +
Postgres both live, sticky-routing per escrow held only in memory). That has
two consequences:

1. **Wasted startup work** when the operator intends Postgres-only — SQLite
   schema is run, `_meta.db` opened, `epoch_*.db` files reconciled on every
   boot.
2. **A real fork bug** on reboot (scenario 2 below): the in-memory route
   table is lost, the host can't see Postgres sessions while PG is down, and
   a `CreateSession` for an existing PG-routed escrow lands in SQLite,
   forking the append log permanently.

Fix: **pick one backend at boot for the whole process**, never run both at
once.

- SQLite has any sessions on disk → process is **SQLite-only**.
- Otherwise + `PGHOST` set → process is **Postgres-only**. Boot fails if PG
  is unreachable. **Never** fall back to SQLite mid-run.
- Otherwise → process is **SQLite-only** (fresh).

`HybridStorage` stays as the wrapper through which all calls flow, but it
becomes a thin selector: it picks one backend at boot and forwards every
method to that backend. The dual-backend code paths (sticky routing,
duplicate-active-session detection, dual prune, two-backend `ListActiveSessions`
reconciliation) go away.

## Is the concern valid?

Yes, on two independent axes. The hot-path-cost axis is the smaller one; the
fork bug is the real reason.

### 1. Wasted startup work when `PGHOST` is set

`devshard/storage/factory.go:15-36`:

```go
func NewStorage(ctx context.Context, sqliteDir string) (Storage, error) {
    pgHost := os.Getenv("PGHOST")
    if pgHost == "" {
        ...
        return NewSQLite(sqliteDir)
    }
    sqlite, err := NewSQLite(sqliteDir)   // <-- always opens SQLite
    ...
    return NewHybridStorage(ctx, sqlite, retryInterval, connectTimeout), nil
}
```

When `PGHOST` is set, `NewSQLite` still runs in full:

- `os.MkdirAll(baseDir, 0o755)`.
- `openMetaDB` — opens `_meta.db`, runs `metaSchema` DDL, sets pragmas (WAL,
  synchronous, busy_timeout) — `devshard/storage/sqlite.go:81-130`.
- `loadIndexFromMeta` — scans `escrow_epoch`.
- `reconcileMetaFromEpochFiles` — walks the data dir, opens **every** existing
  `epoch_*.db` file to repair the index. With N=3 retention this is at
  least 3 sqlite files opened on every boot, each running its own pragmas,
  schema, and a `SELECT escrow_id FROM sessions` scan
  (`devshard/storage/sqlite.go:215-237`).

For a deployment that has only ever used Postgres, this is pure waste.

### 2. Reboot-fork bug (the real reason)

Scenario: a session lives in Postgres. Process reboots. Postgres is briefly
unavailable at boot (a restart of the PG container, a network blip, a
managed-service failover). What happens?

Walk-through of `devshard/storage/hybrid.go`:

- `NewHybridStorage` initializes `h.pg = nil` and `h.routes = map{}`. All
  in-memory knowledge of "which backend owned which escrow" is gone (lines
  43-65).
- Recovery calls `ListActiveSessions`. `getOrConnectPostgres()` returns nil,
  so the function returns only SQLite sessions. **Every PG-resident session
  is invisible to recovery** (lines 199-215).
- A client request comes in for a PG-resident escrow. `backendForSession`
  trace: routes empty → PG nil → SQLite probe → `ErrSessionNotFound`. The
  caller sees "not found" instead of "PG unavailable" (lines 141-164).
- A client tries to **create** a session with the same escrow. `backendForCreate`
  trace: `backendForSession` returned `ErrSessionNotFound` → `getOrConnectPostgres`
  is nil → fall through to `return h.sqlite, hybridSQLite` (lines 166-177).
  **The new session lands in SQLite for an escrow whose append log lives in
  Postgres.** This is the fork.
- The cross-backend uniqueness guard at `devshard/storage/postgres.go:312-343`
  (`devshard_session_index`) is physically bypassed — it lives in a database
  we can't currently reach.
- When PG comes back, `ListActiveSessions` sees both copies. The branch at
  `devshard/storage/hybrid.go:221-243` logs *"duplicate active session in
  sqlite and postgres, using sqlite copy"* and silently picks SQLite. The
  PG copy is permanently orphaned for this process.
- `PG_RETRY_INTERVAL` defaults to 240s, so even if PG recovers instantly,
  the host won't re-probe for up to 4 minutes. The fork window is at least
  `connectTimeout` and at most `retryInterval`.

The fork is **silent** — no error to the caller, just a `slog.Warn` mixed
into the rest of the log stream — and **permanent** for the process
lifetime.

## Goal

Eliminate the fork bug. Keep SQLite-when-needed (no PG configured) and the
ability to drain SQLite-resident sessions when an operator transitions to
PG.

Non-goals:

- Migrating SQLite session content into PG automatically. We do not solve
  the cross-backend merge problem; we just stop creating it.
- Removing `HybridStorage`. The wrapper stays; it just collapses to a thin
  selector.

## The rule

At boot, `NewStorage` chooses one backend for the entire process. There is
no per-call backend routing, no fallback during the process lifetime, no
silent flip from PG to SQLite if PG goes down. The choice is:

| Condition | Mode |
| --- | --- |
| `_meta.db` exists AND `escrow_epoch` has rows | **SQLite** |
| Otherwise AND `PGHOST` set AND no `.pg-bound` violation | **Postgres** |
| Otherwise AND `PGHOST` unset | **SQLite** (fresh) |
| `_meta.db` exists with rows but `PGHOST` set | **SQLite** + WARN |
| `_meta.db` absent AND `PGHOST` unset AND `.pg-bound` exists | **Boot fails** |

Once chosen, the mode is fixed for the process. `HybridStorage` holds
exactly one backend and forwards every call.

### What "_meta.db has rows" means precisely

The check is "does SQLite still own active state?" The signal:

- `<storeDir>/_meta.db` exists, and
- `SELECT COUNT(*) FROM escrow_epoch > 0`.

We do **not** check the per-epoch `epoch_*.db` files directly. `_meta.db` is
the authoritative routing index for SQLite, kept consistent by
`reconcileMetaFromEpochFiles`. If `escrow_epoch` is empty, SQLite has
nothing to serve (settled-and-pruned sessions are gone, the meta rows for
them are removed by `PruneEpoch`).

This means an operator's "SQLite → Postgres" migration drains naturally:
keep running with PGHOST set + SQLite data → process stays in SQLite mode
→ sessions settle and get pruned → `escrow_epoch` empties → on the next
reboot, process promotes to Postgres mode.

### The `.pg-bound` marker for the reverse direction

Your three-rule logic handles the SQLite → PG transition well, but the
reverse (operator was running in PG mode, then removes `PGHOST`) silently
orphans PG sessions because no `_meta.db` exists in PG-only deployments.

Mitigation: a one-line marker file `<storeDir>/.pg-bound` written on first
successful Postgres-mode boot. Rules:

- On Postgres-mode boot: if `.pg-bound` is absent, write it.
- On SQLite-mode boot when `PGHOST` is unset: if `.pg-bound` exists, refuse
  boot with a descriptive error: *"this directory was previously bound to
  Postgres; running in SQLite-only mode now would orphan any PG sessions.
  Set `PGHOST` or delete `.pg-bound` to override."*
- On SQLite-mode boot when `PGHOST` is set (`_meta.db` non-empty case): no
  action on `.pg-bound`; we are in transition.

This is symmetric to "SQLite data on disk forces SQLite mode" — it makes
the reverse transition explicit too.

## What changes in `HybridStorage`

The wrapper stays, the dispatcher logic is gutted.

**Before:**

```go
type HybridStorage struct {
    sqlite *SQLite
    mu             sync.Mutex
    pg             *Postgres
    lastRetry      time.Time
    retryInterval  time.Duration
    connectTimeout time.Duration
    routes         map[string]hybridRoute  // in-memory, lost on reboot
}
```

Plus ~200 lines of per-call dispatch, sticky routing, duplicate-detection,
dual prune, two-backend `ListActiveSessions`.

**After:**

```go
type HybridStorage struct {
    backend Storage  // *SQLite or *Postgres, chosen at boot
}

func (h *HybridStorage) CreateSession(p CreateSessionParams) error {
    return h.backend.CreateSession(p)
}
// ... thin forwards for every other Storage method
```

What goes away:

- `mu`, `pg`, `lastRetry`, `retryInterval`, `connectTimeout`, `routes` —
  unused.
- `getOrConnectPostgres`, `shouldAttemptConnect`, `savePostgres`,
  `currentPostgres`, `storeForBackend`, `backendForSession`,
  `backendForCreate`, `remember`, `remembered`, `forgetPruned` — unused.
- The `"duplicate active session in sqlite and postgres, using sqlite copy"`
  warning branch in `ListActiveSessions` — physically impossible now.
- `joinBackendErrors` — only one backend to read from.
- `PG_RETRY_INTERVAL` and `PG_CONNECT_TIMEOUT` semantics change:
  `connectTimeout` becomes a fail-fast boot deadline; `retryInterval` is
  unused (no mid-run reconnect).

Tests that exercise the dual-backend invariants get deleted alongside the
code that supported them. Tests that exercise SQLite-only or PG-only flows
through the wrapper stay.

## Per-file changes (no code yet)

### `devshard/storage/factory.go`

Rewrite `NewStorage` to:

1. Decide the mode using the rule above.
2. Open exactly one backend (`NewSQLite` or `NewPostgres`).
3. Boot-fail if `PG` mode is chosen but `NewPostgres` returns an error
   within `connectTimeout`. Surface the PG connect error to the caller.
4. Write `.pg-bound` if entering PG mode for the first time.
5. Refuse boot in the `.pg-bound`-exists-but-`PGHOST`-unset case.
6. Return `*HybridStorage{backend: chosen}`.

No `retryInterval` parsing. `connectTimeout` parsing stays for the boot
deadline.

### `devshard/storage/hybrid.go`

Rewrite as described in "What changes in `HybridStorage`". The wrapper now:

- Has one field: `backend Storage`.
- Forwards every method directly. ~80 lines including header comments.
- Deletes the dual-backend dispatch helpers.
- `PruneEpoch` and `pruneBefore` become single-backend pass-throughs.
- `Close` forwards to the one backend.

### New `devshard/storage/storage_mode.go`

~60 lines. Pure helpers, no exported types:

- `hasSQLiteSessions(storeDir string) (bool, error)` — checks for
  `_meta.db` presence and runs a `SELECT COUNT(*) FROM escrow_epoch`
  read-only without leaving handles behind. Returns false on missing file;
  returns error on real I/O failure (do not silently mask a corrupt
  `_meta.db`).
- `readPGBound(storeDir string) (bool, error)` — `os.Stat` on
  `<storeDir>/.pg-bound`.
- `writePGBound(storeDir string) error` — atomic write
  (`writefile-tmp + rename`).

### `decentralized-api/cmd/devshardd/main.go` and `decentralized-api/main.go`

No wiring changes — both already call `devshardstorage.NewStorage(ctx, storeDir)`.
Behavior change is contained inside the factory.

### `devshard/storage/managed.go`

`ManagedStorage` wraps whatever `NewStorage` returned. No changes needed;
forwarding through the (now thin) `HybridStorage` works the same way.

### `devshard/docs/storage-design.md`

Remove or rewrite:

- §"Hybrid Routing Is Sticky" — describes per-call routing that no longer
  exists. **Delete** the whole section.
- §"SQLite Fallback Is Local-Only" — describes the dual-backend fallback
  that no longer exists. **Delete** the whole section.
- §"Prune Cursor Advances Only After Full Success", the "In hybrid mode"
  paragraph — **delete** that paragraph; the single-backend semantics make
  the surrounding text correct.
- §"Architecture" diagram — replace with the new diagram showing one
  backend per process.

Add a new §"Storage Mode Selection" describing the boot-time rule, the
`_meta.db`-has-rows check, the `.pg-bound` marker, and the SQLite → PG
transition story. Reference this plan document.

Add a §"Process-Wide Backend Selection" line under §"Load Readiness"
noting that all production deployments target Postgres; SQLite is for
development and for draining legacy state during a transition.

## Tests

1. `NewStorage` with no `_meta.db` + `PGHOST` set + PG reachable → returns
   `*HybridStorage{backend: *Postgres}`; SQLite directory was not opened
   (verify by passing a path without permission to create).
2. `NewStorage` with no `_meta.db` + `PGHOST` set + PG unreachable → returns
   error, no boot.
3. `NewStorage` with `_meta.db` containing rows + `PGHOST` set → returns
   `*HybridStorage{backend: *SQLite}`, logs WARN with the transition
   message; PG is **not** opened.
4. `NewStorage` with `_meta.db` containing rows + `PGHOST` unset → returns
   SQLite, no WARN.
5. `NewStorage` with empty `_meta.db` (zero rows in `escrow_epoch`) +
   `PGHOST` set → returns Postgres; `.pg-bound` gets written.
6. `NewStorage` with no `_meta.db` + `PGHOST` unset + `.pg-bound` exists →
   boot fails with descriptive error.
7. `NewStorage` with no `_meta.db` + `PGHOST` unset + no `.pg-bound` →
   returns SQLite (fresh).
8. `HybridStorage` method dispatch: pass a fake `Storage` as the backend,
   confirm every Storage interface method calls through.
9. Removed-warning regression: a test that previously exercised the
   "duplicate active session" path is deleted, with a note in the commit
   message that the case is now physically impossible.

## Migration / data safety

For deployments that have been running hybrid silently and may have
SQLite-only sessions left over from past PG outages:

1. **One release with WARN** before the factory rewrite. Today's
   `HybridStorage` keeps running, but `NewStorage` logs a WARN at boot
   listing how many active sessions are in SQLite vs PG, and tells the
   operator to run the diagnostic.
2. **`devshardd dump-sqlite-sessions` subcommand** (same idea as in the
   prior draft of this plan): read-only, lists every active session in
   `_meta.db` that is not present in PG; exits non-zero if any are found.
3. **Operator either** waits for SQLite to drain naturally (sessions
   settle, partition prunes empty the meta rows, next reboot promotes to
   PG), **or** accepts data loss, removes the SQLite directory, restarts
   with `PGHOST` set.
4. **Release N+1 ships the factory rewrite.** Operators who did not act
   land in the "SQLite has rows + `PGHOST` set" branch — they keep running
   in SQLite mode until they drain, with the WARN at every boot. No silent
   data loss.

## Step-by-step rollout

1. **Add `storage_mode.go` helpers.** Land them with their unit tests. No
   call sites use them yet.
2. **Add the WARN-only diagnostic.** Today's `NewStorage` keeps building
   `HybridStorage`, but logs the SQLite-vs-PG active-session counts on
   boot, plus the `dump-sqlite-sessions` subcommand. Zero behavior change
   in steady state.
3. **Wait one release.** Operators discover any silent reliance on hybrid
   fallback.
4. **Rewrite the factory** to use the boot-time rule.
5. **Rewrite `HybridStorage` as the thin wrapper.** Delete the dispatch
   helpers, the duplicate-detection branch, the dual prune coordination.
   Update the design doc in the same change.
6. **Delete `PG_RETRY_INTERVAL` env var handling**, document the deletion
   in the release notes.

Steps 4-6 land together as one PR; the WARN/diagnostic in steps 1-2 is the
safety net that lets us do it without a flag day.

## Risks

- **Operators silently reliant on hybrid fallback for new-session-create
  during PG outages.** The one-release WARN window is the mitigation. After
  the rewrite, `CreateSession` returns an error when PG is down. Clients
  must retry. That matches today's behavior for every other operation on
  an existing PG-routed escrow.
- **SQLite drain takes longer than expected.** If a deployment has a
  long-running settled-but-not-yet-pruned session in SQLite, the process
  stays in SQLite mode for longer than the operator expects. Fixable by
  running `MarkSettled` + `PruneEpoch` manually, or by the diagnostic
  exiting non-zero so the operator notices.
- **`.pg-bound` marker accidentally created or deleted by an operator
  manipulating the storage directory.** Document the marker's purpose in
  `storage-design.md` and in the boot error message; treat manual edits as
  operator-owned recovery actions.

## What this plan deliberately does NOT do

- It does not introduce a `DEVSHARD_STORAGE` env var. `PGHOST` presence is
  the only operator-facing signal. (Previous draft of this plan suggested
  an explicit env var. That was over-engineered for the actual decision
  surface.)
- It does not migrate SQLite session content into PG automatically.
  Operators drain naturally or accept loss.
- It does not change SQLite or Postgres internal layouts. Per-epoch SQLite
  files, declarative PG partitions, `escrow_epoch` / `devshard_session_index`
  routing — all unchanged.
- It does not remove `HybridStorage`. The wrapper stays; only its dispatch
  logic is replaced.
