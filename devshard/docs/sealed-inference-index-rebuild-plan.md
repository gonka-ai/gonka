# Sealed-inference index rebuild: stats loss and startup cost

Companion to [ready-on-boot-warm-cutover-plan.md](ready-on-boot-warm-cutover-plan.md),
which makes this the write storm on the lazy-load request path.

`RebuildSealedInferenceIndex` runs unconditionally on every session recovery. It
destroys the sealed-inference observability columns that the live path wrote, and
it does one delete plus one unbatched insert per sealed inference on the recovery
path (~1.5M inserts for the incident node). This document records the diagnosis
and the plan to fix it.

## Diagnosis

### 1. It wipes, then writes bare rows

`state/seal.go:587` deletes every sealed-inference row for the escrow, then
reinserts one row per sealed id:

```go
if err := sm.inferenceStore.DeleteSealedInferences(sm.state.EscrowID); err != nil {
        return err
}
// for each id in sealedNonces, sorted:
row := storage.InferenceRow{InferenceID: id, SealedNonce: nonce}
if cached, ok := sm.committedEntries[id]; ok {
        if entryID, rec, err := unmarshalInferenceEntry(cached); err == nil && entryID == id {
                row = inferenceObsRow(id, nonce, rec)
        }
}
if err := sm.inferenceStore.InsertSealedInference(sm.state.EscrowID, row); err != nil {
        return err
}
```

`storage.InferenceRow{InferenceID: id, SealedNonce: nonce}` leaves `ObsPresent`
at its zero value, `false`. The rich row is only produced by the
`committedEntries` branch.

### 2. That branch is dead for exactly the ids being iterated

The loop is `for id := range sm.sealedNonces`, and both seal paths remove the
committed entry as they add the id to `sealedNonces`:

- `SealInference` (`state/seal.go:529-539`): `sm.sealedNonces[id] = sealedNonce`,
  then `delete(sm.committedEntries, id)`.
- The auto-seal drain (`state/seal.go:239-240`): identical ordering.

The snapshot cannot reintroduce them either, because it persists
`ExportCommittedEntries()`, which the seal has already pruned. So
`committedEntries[id]` is provably absent for every sealed id, and **every
reinserted row is bare**.

### 3. A bare row reads as missing

```go
func (sm *StateMachine) lookupSealedInferenceLocked(id uint64) (types.InferenceRecord, bool) {
        row, ok, err := sm.inferenceStore.GetSealedInference(sm.state.EscrowID, id)
        if err != nil || !ok || !row.ObsPresent {
                return types.InferenceRecord{}, false
        }
```

`ExportAllInferenceRecords` calls this, gets `false`, falls through to
`hydrateCommittedInferenceLocked`, which also fails because the id is not in
`committedEntries` — so the inference disappears from the export entirely. The
status, votes, model, and token columns that `inferenceObsRow` wrote on the live
path with `ObsPresent: true` are lost on every restart.

### 4. The write volume is real

One `DeleteSealedInferences` plus one unbatched `InsertSealedInference` per
sealed id. The call sits at `cmd/devshardd/session/manager.go:829`, outside the
`if meta.LatestNonce > 0` block, so **the snapshot path pays it too**. The same
call exists on the v3 path at `user/recover.go:335`.

### Why the dead branch matters

It reveals the intent: the author wanted to preserve the rich record and reached
for `committedEntries` as the source, but the seal empties that map by
construction. The rich data still lives in the **diff journal** — the same source
`RebuildValidationObsFromDiffs` already walks. That is where the full-replay
variant should read from.

## Interaction with the ready-on-boot / lazy-load design

Two ways this collides with the design on this branch.

- **It runs on the lazy-load request path.** With `/ready` returning 200 at boot,
  a first request to an unrecovered escrow already blocks through a full replay.
  This adds a wipe plus one insert per sealed inference on that request
  goroutine, which makes the blocking concern materially worse.
- **It defeats the warm cutover gate.** A pre-warmed blue/green child would
  report `recovery_complete: true` having just emptied sealed-inference stats for
  every escrow it touched, trading cold latency for silently missing stats.

## Design

Reuse the split already established for validation obs, keyed on the same
`replayFrom == 1` predicate.

| Path | Behaviour |
| --- | --- |
| Snapshot restored (`replayFrom > 1`) | Gap fill only. Never delete, never downgrade an `ObsPresent = true` row. Normally writes nothing. |
| Full replay (`replayFrom == 1`) | Wipe is defensible, but read rich rows from the diff journal and run it as a background repair behind `ObsRepairGate`. |

Rationale for keeping the wipe on full replay: after a rejected snapshot or a
fresh reconstruction, stored rows may belong to a divergent history and should
go. Rationale for the gap fill on the snapshot path: the stored rows are durable
and the restored state is root-verified, so the only legitimate work is
materialising `id → sealed_nonce` for ids that have no row at all — which is what
bare rows exist for, per the `InferenceRow` comment about pruning.

## Step-by-step plan

### Step 1 — storage: bulk id listing (prerequisite)

The gap fill must not issue 1.5M `GetSealedInference` reads. Today the only bulk
reader is `SQLite.listSealedInferences` (`storage/sqlite_migrate_list.go:21`),
which is unexported and SQLite-only.

1. Add to the `Storage` interface:
   `SealedInferenceIDs(escrowID string) (map[uint64]uint64, error)` — returns
   `inferenceID → sealedNonce` for **every** stored row, including
   `ObsPresent = false` (bare index) rows. After the wipe-rebuild bug has run,
   every row is bare; listing only rich rows would make the snapshot-path gap
   fill rewrite 1.5M existing rows. The caller skips any id already present,
   which is how it tells “already indexed” from “missing”.
   Also add `InsertSealedInferences` so gap fill and from-diffs batch writes
   in chunked transactions instead of one round trip per id, and
   `BulkInsertSealedInferences` for the from-diffs load, which follows a wipe
   and so needs no conflict handling at all.
2. Implement on `SQLite`, `Postgres`, `Memory`, `HybridStorage`, `ManagedStorage`.
   `ObsRepairGate` queues sealed-inference inserts while a repair is in
   progress (reads still pass through).
3. Reuse the existing `sealed_inferences` query shape; add an index on
   `(epoch_id, escrow_id)` only if the plan shows a scan. Postgres already
   keys `(epoch_id, escrow_id, inference_id)`.

Tests: extend `runSealedInferenceLifecycle` in `storage/shared_test.go` so every
backend is covered by one assertion set.

### Step 2 — state: split the rebuild into two entry points

Replace the single `RebuildSealedInferenceIndex` with:

- `FillSealedInferenceIndexGaps() (inserted int, err error)` — reads
  `SealedInferenceIDs`, inserts a bare row for each id in `sealedNonces` that is
  absent and not live, and touches nothing else. No delete.
- `RebuildSealedInferenceIndexFromDiffs(records []types.DiffRecord) error` — the
  full-replay variant: delete for the escrow, then insert rows built from the
  journal so `ObsPresent = true` wherever the journal carries the record, falling
  back to a bare row otherwise.

Both batch their inserts in a single transaction rather than one round trip per
id.

Keep `RebuildSealedInferenceIndex` as a thin wrapper over the gap fill for the v3
`user/recover.go` caller, so that path stops losing stats without restructuring
it.

Tests in `state/seal_test.go`:

- A sealed id with a rich stored row survives the gap fill (`ObsPresent` stays
  true, votes/model/tokens unchanged).
- A sealed id with no row gets a bare row with the correct `sealed_nonce`.
- A live id gets no row.
- Repeated gap fills are idempotent and write nothing on the second run.
- The from-diffs variant produces `ObsPresent = true` rows, and drops rows for
  ids absent from the replayed history.

### Step 3 — recovery: choose per path

In `cmd/devshardd/session/manager.go` `recoverStoredSession`, move the call
inside the recovery branch and pick by `replayFrom`:

- `replayFrom > 1`: call `FillSealedInferenceIndexGaps` inline. It is cheap and
  keeps the published session immediately correct.
- `replayFrom == 1`: attach the journal to the existing `obsRepairJob` and let
  the background repair run the from-diffs rebuild inside the same
  `RepairValidationObs` critical section, so one gate window covers both
  rebuilds.

`obsRepairJob` already carries `records` and `sealed`, so no new plumbing is
needed beyond invoking the second rebuild in `startObsRepair`.

Tests in `manager_snapshot_recovery_test.go`:

- Snapshot path: a pre-existing rich row is intact after recovery, and
  `DeleteSealedInferences` is never called (assert via the store wrapper).
- Full replay: the session is published before the sealed-index rebuild
  finishes, and after `WaitRecoveryRepairs` the rows are rich.
- Rejected-snapshot path (root mismatch) takes the full-replay branch.

### Step 4 — rename the waiter (counters land in Step 8)

- Rename `WaitObsRepairs` to `WaitRecoveryRepairs` (it now waits for both
  rebuilds) and update `app.go` plus the `waitObsRepairsOnCleanup` test helper.
- Do not flip `recovery_complete` on this waiter until Step 8 also counts
  sealed-index repair. Shipping the field early would make a pre-warmed child
  look warm after wiping stats (see companion *defeats the warm cutover gate*).

### Step 5 — verification

- `go test -race ./storage/ ./state/ ./cmd/devshardd/...` from `devshard/`
  (`-skip 'Postgres|MigrateSQLiteToPostgres'` if Docker is unavailable).
- Snapshot path (unit): planted rich rows survive recovery and
  `DeleteSealedInferences` is never called
  (`TestRecoverSessions_SnapshotPathLeavesSealedInferenceRows`). Gap fill on
  an already-indexed set inserts 0
  (`TestFillSealedInferenceIndexGaps_Idempotent`,
  `BenchmarkFillSealedInferenceIndexGaps_NoGaps`).
- Full-replay path (unit): the session is published before the wipe, and after
  `WaitRecoveryRepairs` the rows are rich
  (`TestRecoverSessions_FullReplayRebuildsSealedIndexInBackground`).
- `recovery_complete` still follows the backlog, not the waiter
  (`TestStartRecovery_CompleteBeforeSealedIndexRepair`). Step 8 flips that.
- Operator restart with a long journal and a current snapshot: sealed-inference
  stats are identical before and after, and the recovery log reports
  `filled sealed inference index gaps inserted=0` (or a handful) with a
  duration that does not scale with sealed-row count.
- Restart with no snapshot: stats are briefly incomplete, then match after
  `rebuilt validation obs` (`sealed_inferences` and `duration` on that line).

See [Measuring the 1–3 fix](#measuring-the-1-3-fix) for benches and log fields.

## Measuring the 1–3 fix

The claim is: a snapshot restart no longer does 1.5M writes, and a full replay
still writes but in chunked transactions off the publish path. Measure those
two separately. Do not use `/ready` time — it still waits on the recovery
backlog until Step 6.

### Microbenchmarks (repeatable, no cluster)

From `devshard/`. `-run '^$'` skips unit tests (including Docker Postgres):

```
GOMODCACHE="$HOME/go/pkg/mod" GOCACHE="$HOME/Library/Caches/go-build" \
  go test -run '^$' -bench 'BenchmarkSealedInference|BenchmarkFillSealed|BenchmarkValidationObs' \
  -benchmem -benchtime=1x ./storage/ ./state/
```

The `BenchmarkPostgres*` set is the same shapes against a testcontainers
Postgres, so it needs Docker and is not covered by the command above:

```
GOMODCACHE="$HOME/go/pkg/mod" GOCACHE="$HOME/Library/Caches/go-build" \
  go test -run '^$' -bench 'BenchmarkPostgres' -benchmem -benchtime=1x \
  -timeout 30m ./storage/
```

Use `-count=3` locally when you want variance; CI should stay at `-benchtime=1x` and n≤20k.

| Bench | What it stands in for | Healthy result |
| --- | --- | --- |
| `BenchmarkSealedInferenceIDs` | Snapshot gap fill's only I/O | One `SELECT id, nonce` scan; ~8ms at 20k SQLite on Apple M4 Max |
| `BenchmarkFillSealedInferenceIndexGaps_NoGaps` | Snapshot restart with durable rows | `inserted=0`; ~8ms at 20k, matching the list |
| `BenchmarkFillSealedInferenceIndexGaps_AllMissing` | Cold index (still no delete) | Batched inserts only; ~425ms at 20k SQLite before the prepared-statement fix |
| `BenchmarkSealedInferenceIndex_UnbatchedInsert` | Pre-fix recovery (1 Exec/id) | Baseline; ~650ms at 20k SQLite |
| `BenchmarkSealedInferenceIndex_BatchedInsert` | Gap fill's inserts, where rows may exist | Same 20k in 500-row txs, upsert prepared once per chunk; ~49ms SQLite, ~99ms Postgres (one `unnest` statement per chunk) |
| `BenchmarkPostgresSealedInferenceIndex_BulkInsert` | Full-replay repair's load after the wipe | `COPY`, no per-row conflict probe; ~30ms at 20k, ~3× the upsert. SQLite has no separate path and reuses the batched insert. |
| `BenchmarkValidationObsDrain_PerID` / `_Batched` | The other half of the same repair | ~846ms vs ~65ms at 20k; the batched form is what the rebuild calls |
| `BenchmarkValidationObsRecord_PerDiff` / `_Chunked` | Journal replay into obs | ~320ms vs ~51ms at 20k |
| `BenchmarkPostgres*` | All of the above on Postgres | See [Postgres vs SQLite](#postgres-vs-sqlite) |
| `BenchmarkPostgresSealedInferenceIndex_ChunkedTxPerRow` | The insert between steps 1–3 and the batching fix | ~3058ms at 20k, i.e. no better than unbatched: on Postgres the cost was round trips, not commits |

### Postgres vs SQLite

Both at n=20k, Apple M4 Max, `-benchtime=1x -count=3` (median). Postgres is a
`postgres:18.1` container reached over the Docker bridge, so its round trip is
closer to a same-host TCP hop than to a production network: the per-row rows
below are optimistic, and a real deployment separates them further.

| Per repair, 20k rows | SQLite | Postgres | PG / SQLite |
| --- | --- | --- | --- |
| `SealedInferenceIDs` (the snapshot path's only I/O) | ~7ms | ~4ms | 0.6× |
| Insert, one statement per row, no tx | ~637ms | ~3095ms | 4.9× |
| Insert, chunked tx, still one statement per row | ~431ms | ~2962ms | 6.9× |
| Insert, one `unnest` / prepared upsert per chunk | ~49ms | ~99ms | 2.0× |
| Load into empty space (`COPY` on Postgres) | ~49ms | ~30ms | 0.6× |
| Drain, one transaction per id | ~846ms | ~14023ms | 16.6× |
| Drain, chunked set-at-a-time | ~65ms | ~58ms | 0.9× |
| Record, one write per journal record | ~320ms | ~2951ms | 9.2× |
| Record, 500-entry chunks | ~51ms | ~57ms | 1.1× |

Three things fall out of this.

**Chunking the transaction bought Postgres nothing.** 3095ms unbatched against
2962ms in 500-row transactions is a wash, while the same change on SQLite cut
the insert by a third. SQLite pays per commit, so grouping commits helps;
Postgres pays per round trip, so grouping commits while still sending a
statement per row helps not at all. The per-row cost lands where you would
expect from that: ~155µs per insert, and ~700µs per drain, which is about five
round trips for the drain's begin/select/insert/delete/commit.

**What is left of the batched cost is not the protocol.** A bare round trip to
this container measures ~163µs and a chunk carries 500 rows, so the ~99ms
upsert is not 40 statements of network time — it is the per-row conflict probe.
Dropping it where the caller has just wiped the escrow (`COPY`, no `ON
CONFLICT` possible) gives ~30ms, and the same reasoning applied to the drain's
`INSERT`+`DELETE` pair — one data-modifying CTE instead of a transaction around
two statements — gives ~58ms from ~81ms. Sweeping the chunk size found nothing
past ~500 rows, and `synchronous_commit = off` changed neither, which is the
same conclusion from the other direction: the remaining time is server-side row
work.

**Postgres is now the faster backend for the full-replay repair**, at ~145ms
per 20k against SQLite's ~165ms, where the per-row forms had it 5–17× behind.
Reads were never the problem — the snapshot gap fill's only query is faster on
Postgres too.

Do not run production 1.5M in CI. Linearize from 20k:

- Snapshot path: list 20k ≈ 7ms SQLite / ≈ 4ms Postgres → 1.5M ≈ 0.5s / 0.3s of reads and **zero writes**.
- Pre-fix full replay: ≈ 1.8s per 20k SQLite → 1.5M ≈ 2.3min. Postgres ≈ 20.1s per 20k → 1.5M ≈ **25min**, on top of a full wipe.
- Full replay now: ≈ 0.165s per 20k SQLite → 1.5M ≈ 12s. Postgres ≈ 0.145s per 20k → 1.5M ≈ 11s, **off the publish path**.

The headline win is still snapshot writes going from O(sealed) to 0. Measure that with logs and `pg_stat_statements`, not with `/ready`.

### Restart logs (one escrow, production-shaped)

On a host with a current snapshot:

1. Capture sealed stats before stop: `GET /v1/state` (or `ExportAllInferenceRecords` via admin) for a busy escrow — counts of status/model/tokens.
2. Restart.
3. Grep:
   - `filled sealed inference index gaps` — `inserted` should be 0 or tens, `duration` milliseconds, `sealed_ids` in the millions is fine because it is a map length not a write count.
   - `DeleteSealedInferences` must not appear on the snapshot path (no `rebuilt validation obs` for that escrow).
4. Re-fetch stats; they must match step 1.

On a host with the snapshot removed (full replay):

1. Same pre-capture.
2. Restart.
3. `recovered devshard session` returns while `rebuilt validation obs` is still running (`duration` on that line is the repair, including the sealed-index rebuild; `sealed_inferences` is the seal set size).
4. After that line, stats match step 1 and `ObsPresent` is true.

### Storage counters

Postgres: `pg_stat_statements` for `INSERT INTO devshard_sealed_inferences` — snapshot restart should show **no calls** for recovered escrows, and gap fill ~`ceil(gaps/500)` calls, since a chunk is now one `unnest` statement rather than 500 round trips. Full replay does not appear there at all: it loads with `COPY`, ~`ceil(sealed/50000)` of them, visible as `COPY` in `pg_stat_activity` rather than as an insert. SQLite: WAL bytes during recovery; snapshot restart should not grow WAL by O(sealed).

## Residual performance risks

Found reviewing the Steps 1–5 code. None of these are regressions against the
pre-fix behaviour; they are the costs that survived it, ordered by impact.
Measured on SQLite, Apple M4 Max, n=20k, `-benchtime=1x`.

1. **Whole-set materialisation.** Both rebuilds build the full `[]InferenceRow`
   before writing (`InferenceRow` is ~200B plus three byte slices), and the
   from-diffs fold holds a `*InferenceRecord` per inference in the journal. At
   1.5M that is hundreds of MB of transient heap during recovery. Streaming by
   chunk would cap it.
2. **`ObsRepairGate.queueFor` takes one process-wide mutex on every obs write.**
   It runs on the live seal path for every escrow even when no repair is in
   flight, so all escrows serialize on it. An `atomic` fast path or `RWMutex`
   would remove that.

Fixed while reviewing: **chunking the transaction was not the same as batching
the write.** `InsertSealedInferences` still issued a statement per row inside
each chunk, so Postgres paid a round trip per inference and SQLite re-parsed
the upsert 1.5M times. On Postgres the chunked transaction measured no better
than no batching at all (~2962ms vs ~3095ms at 20k). Postgres now writes one
`unnest` statement per chunk (~99ms) and SQLite prepares the upsert once per
chunk (~431ms → ~49ms), and `pg_stat_statements` shows `ceil(sealed/500)` calls
rather than `sealed`.

Also fixed: **the batched Postgres writes were still paying for conflict
handling neither rebuild needs.** The full-replay path deletes the escrow's
rows and then loads them back, so its inserts can never conflict, but it went
through the same `unnest` upsert as gap fill. `BulkInsertSealedInferences`
loads with `COPY` instead (~99ms → ~30ms at 20k), falling back to the upsert if
a row does collide, so a wrong precondition costs speed rather than the
rebuild. The batch drain likewise wrapped an `INSERT` and a `DELETE` in an
explicit transaction — four round trips per chunk — and is now one
data-modifying CTE with the same atomicity (~81ms → ~58ms). Together these put
the full-replay repair at ~145ms per 20k on Postgres against ~165ms on SQLite,
i.e. Postgres is no longer the slower backend for it. SQLite keeps one path:
its conflict probe is a b-tree lookup in-process, not a round trip, so
`BulkInsertSealedInferences` there is the batched upsert.

Also fixed: **the full-replay repair was dominated by the obs half,
not the sealed index.** `RebuildValidationObsFromDiffs` did one
`RecordValidationsAppliedOnce` per journal record and one
`DrainInferenceValidationObs` per sealed id, the latter its own
begin/select/insert/delete/commit. At 20k that was ~846ms of drain and ~320ms
of record against ~431ms for the sealed insert this plan added. The drain is
now a chunked set-at-a-time move (`DrainInferenceValidationObsBatch`, ~65ms,
13×) and the records accumulate into 500-entry writes (~51ms, 6.3×). On
Postgres the same two changes are worth far more — the drain was ~14s at 20k,
five round trips per inference — and with the insert fixes above the whole
repair goes from ~1.8s to ~0.165s per 20k on SQLite and from ~20.1s to ~0.145s
on Postgres.

Also fixed: the from-diffs fold and the gap fill both used to hold
`sm.mu` across the journal walk and all of their storage I/O, which stalled
`ApplyDiff` on a published session; both now snapshot what they need and work
outside the lock. The gap fill counts its gaps before allocating, so the
no-gap case allocates nothing instead of growing a millions-long
`[]InferenceRow`. The recovery log reads `SealedNonceCount()` rather than
cloning the whole seal-nonce map for its length. `SealedInferenceIDs` builds
its map without a size hint in SQLite and Postgres, which measured negligible
next to the row scan and is left alone.

### What not to use yet

- `/ready` 200 latency — still gated on `recovered` until Step 6.
- `recovery_complete` — Step 8, and it must wait on `WaitRecoveryRepairs` so a full-replay child is not declared warm mid-wipe.
- Request-path latency on first chat after boot — still includes journal replay for unrecovered escrows; 1–3 only removed the extra wipe from that goroutine on the snapshot path.

## Ready-on-boot / warm cutover (after the gap fill)

The companion [ready-on-boot-warm-cutover-plan.md](ready-on-boot-warm-cutover-plan.md)
is not a parallel track. It is the next work on this branch once Steps 1–3's
snapshot half is in: `/ready` can then return 200 during recovery without putting
a wipe-and-1.5M-insert on the first request goroutine, and `recovery_complete`
can wait on both validation-obs and sealed-index repair.

Do **not** drop `recovered` from the 503 condition (companion child item 1)
before Step 3's snapshot half. That is the opposite order from the companion's
own sequencing list, and it is the one that is safe: a 200-during-recovery child
that still runs today's `RebuildSealedInferenceIndex` at
`session/manager.go:829` would make the lazy-load path strictly worse.

Versiond overlap wait (companion flows C/D) is a v5 `versioned` change. It is
listed as Step 9 so the sequence is complete; it is not this v4 restore branch.

### Step 6 — `/ready` 200 as soon as the process can serve

Companion child item 1. `cmd/devshardd/server.go:35-42`: drop `recovered` from
the 503 condition. Keep `storage_ready` and `draining`. Keep
`recovery_complete` on `readyStatus` (`server.go:57-65`).

Status code = can serve. Body field = backlog drained. `waitForChildServingReady`
in versiond already keys on status, so flow A (solo boot) starts working as soon
as this ships.

Tests: `/ready` is 200 while `recoveryComplete()` is still false; 503 while
draining or storage is unready; body still has `recovery_complete: false` until
the waiter from Step 4/8 flips.

### Step 7 — evict and re-recover on `types.ErrInvalidNonce`

Companion child item 3. Required on the same commit as Step 6: a published
child that recovered early will sit behind the old generation's writes to the
shared store. The first request then fails `ErrInvalidNonce` and, today, the
stale `*transport.Server` stays in `m.sessions`.

On nonce mismatch from a live session (apply / gossip / owner chat):

1. Drop it from `HostManager.sessions`, `Close` the host, delete escrow metrics.
2. `recoverStoredSession` again (same `recoveryGate` / singleflight as
   `getOrCreate` at `session/manager.go:379`).
3. Reuse `resolutionFailures` (`manager.go:427-456`) so a client that keeps
   sending a bad nonce cannot spin reload. A genuine catch-up mismatch is not
   a permanent failure — short TTL, not `permanentFailureTTL`.

Tests: a session recovered at nonce N, then a store-ahead apply of N+1, evicts
and succeeds; a bogus nonce is negative-cached and does not re-enter recovery
on every request.

### Step 8 — recovery counters include sealed-index repair

This is Step 4 plus companion child items 2 and 4, done together so
`recovery_complete: true` cannot mean "validation obs rebuilt, sealed index
still wiping."

- Rename `WaitObsRepairs` → `WaitRecoveryRepairs`; `recoveryComplete()` is
  true only after boot recovery **and** that waiter.
- Promote the log-only counters from `completed devshard session recovery`
  (`manager.go:682-689`) onto `readyStatus` and a Prometheus gauge:
  `sessions_total`, `sessions_recovered`, `sessions_failed`,
  `sessions_version_skipped`, `sessions_pending`.
- Pending includes both the recovery queue and in-flight sealed-index /
  validation-obs repair jobs.

Tests: body counters during recovery; `recovery_complete` stays false until
`WaitRecoveryRepairs` returns; a full-replay escrow that is published but still
in `startObsRepair` does not flip the field.

### Step 9 — versiond overlap wait (v5, not this branch)

Companion versiond items 1–6. After `waitForChildReady` at
`versioned/internal/process/manager.go:1020` and **before** the
`m.processes` swap at `1029-1055`, poll admin `/ready` until
`recovery_complete: true`.

- New helper: status plus `recoveryComplete *bool`. Today's `getHTTPStatus`
  (`manager.go:2059`) is status-only and must stay that way for
  `watchChildReadiness` (`manager.go:305`) — pin with a test that the monitor
  never reads the body.
- `VERSIOND_RECOVERY_TIMEOUT`, default 30m. Do not reuse the 60s
  `ReadyTimeout` (`manager.go:169-170`).
- Bail-outs from companion flow D: missing field → cut over cold; old child
  leaves `Running` → publish immediately; `hostDraining` / ctx done / timeout
  → abort, old keeps the route.
- Gate: overlap branch only, `devshardAdminEligible` plus a non-zero admin
  port. Solo start and the stop/start branch never wait.

### Step 10 — update rolling-update docs

After the child `/ready` split (Steps 6–8) and the v5 overlap wait (Step 9),
update [rolling-update.md](rolling-update.md):

- §1.3 probe table: status 200 means the process can serve, not that recovery
  is done. Document `recovery_complete` on the body as the warm signal.
- Swap / flow summary: the overlap rule changes from “new ready before
  traffic” to “new **warm** before traffic, overlap only”. Solo boot and
  stop/start stay on status 200 (flow A/C).
- v4 release notes if they still describe `/ready` 503-until-recovered as the
  versiond publish gate.

Do not edit those docs before Steps 6–9 land; the current text matches the
code until then.

## Sequencing

1. **Steps 1–3 snapshot half** — stop per-restart stats loss and take the
   ~1.5M writes off every recovery, including the soon-to-be-lazy request path.
2. **Steps 6 and 7 together** — `/ready` 200 during recovery, plus nonce
   eviction. Step 6 alone is flow A; without Step 7, flow D is worse than
   today's cold cutover.
3. **Step 3's full-replay-from-diffs half**, then **Step 8** — background
   repair, then `recovery_complete` that actually waits for it.
4. **Step 5** verification against a long journal (snapshot and no-snapshot).
5. **Step 9** on v5 versiond, which is useless until this child ships the
   field and the eviction.
6. **Step 10** docs, once the probe and overlap wait match the new contract.

## Out of scope

- Blocking versus `503 Retry-After` for a first request to an unrecovered
  escrow; that is a client-contract decision.
- Repairing validation obs for an escrow booted from a snapshot whose obs rows
  are empty; pre-existing and tracked separately.
