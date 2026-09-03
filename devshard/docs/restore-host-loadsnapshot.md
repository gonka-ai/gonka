## Problem

v4 `devshardd` dropped two pieces of the v3 host recovery path when `HostManager.recoverStoredSession` moved into the standalone binary (`b53fd8fcd`):

1. It no longer called `LoadSnapshot`. Every restart replayed diffs from nonce 1.
2. Recovery ran on a single worker, inline, before the HTTP listener bound.

On a host with a long journal that produced hours of catch-up, `/ready` stayed false, the parent answered 502, and live traffic never reached the child. Logs of the failing window show no snapshot restore lines and a serial `recovered devshard session` trail that lasted for hours.

A third defect sat on the same path: snapshot recovery wiped sealed-inference stats (`RebuildSealedInferenceIndex` deleted every row and reinserted a bare one), so a restart that should have been a tail catch-up also paid one unbatched insert per sealed id and dropped `ObsPresent` columns.

## Decisions

These are the design calls that hold across the fifteen commits. Each later commit is a refinement of one of them, not a reversal — except `/ready`, which commit 3 gated on recovery and commit 15 split into status vs body once the wipe was off the lazy-load path.

| Decision | Choice | Why |
| --- | --- | --- |
| Snapshot load | Restore v3 `LoadSnapshot` on the v4 host recovery path. Decode/load failure falls back to a full journal replay. | A snapshot at nonce N skips diffs `1..N`. That is the difference between a tail catch-up and hours of replay. |
| Snapshot integrity | After restore, verify the snapshot root against the journal diff at nonce N. On mismatch, throw the restored SM away and replay from 1. | Diff replay already checks every nonce it applies. The snapshot path used to trust the blob outright, including on a store v4 can share between hosts. |
| Recovery concurrency | 8 workers, same as v3. | Snapshots alone are not enough on a host with many sessions: serial recovery still blocks listen. |
| Bind vs recover | Bind the listener first. Recover in the background. A session a live caller needs is recovered on demand by `getOrCreate` instead of waiting out the backlog. | Removes the 502 window. |
| `/ready` status vs body | Status 200 = can serve (chain up, storage open, not draining). Body `recovery_complete` = warm (backlog drained **and** background repairs finished). | Commit 3 gated 503 on recovery so versiond would not publish a cold child. Once snapshot recovery stopped wiping stats, 503-until-recovered became the outage: versiond's 60s ready timeout killed the child before it ever served. Status is flow A (solo boot). The body field is flow D (overlap cutover with a healthy old generation). |
| Demand vs cold backlog | Remember every escrow that reaches `getOrCreate` for the rest of the recovery window and dequeue those first. Only workers picking up a **cold** session park. A parked worker resumes as soon as its own escrow is demanded. A fully demanded backlog never pauses. | The first non-blocking version parked **every** worker whenever a request was in flight, including workers already recovering the session the caller wanted. |
| Stale in-memory session | On `types.ErrInvalidNonce` from apply, evict the live session, recover it again, retry the handler once. A mismatch that survives the reload is negative-cached at the short TTL (`resolutionFailureTTL`), not `permanentFailureTTL`. | During overlap the old generation keeps writing the shared store. A session the new child recovered early is behind by publish time. Without eviction, flow D is worse than a cold cutover. |
| Validation obs, snapshot path | Do **not** rebuild. Live apply already wrote durable rows for every nonce the snapshot covers. | Rebuilding on this path re-read the whole journal and paid one write transaction per historical seal to rewrite rows that were already correct. |
| Validation obs, full replay | Must rebuild. `ApplyLocal` records no obs, and the clear-then-replay is the self-heal for batches the live path dropped under backpressure. | This is the only path that needs the rebuild. |
| Incremental / partial-range rebuild | Rejected. | `DrainInferenceValidationObs` deletes the live row `RecordValidationsAppliedOnce` dedups against. Replaying a tail twice double-counts. A storage test pins this. |
| Validation obs, scheduling | End restoration without filling obs. Run the rebuild in the background after the session is published. Queue concurrent obs writes for that escrow and apply them after the rebuild. | Inline rebuild was the expensive half of recovery (a write txn per historical seal) and a cold bind waited it out. Queueing is equivalent to sequential execution because the rebuild covers `1..N` and live diffs during the window are past `N`. |
| Why queueing is safe | Obs writes are already best-effort. Recording is dropped under backpressure. The persist-first path (`ValidateDiff` → `CommitValidated`) already defers drains via `deferredObsWrite` and logs failures rather than failing a commit. | An earlier objection that seal drains were fail-closed was wrong for the production path. |
| Gate bounds | Queue cap 8192 ops (drop newest, report). Flush cap 64 rounds, then close and apply the remainder write-through. Concurrent repairs for one escrow are rejected. | A continuously busy escrow must not hold the gate open forever. Overflow matches the live path, which already drops obs under backpressure. |
| Sealed-inference index, snapshot path | Gap fill only. List existing ids, insert a bare row for each sealed id that is missing, never delete, never downgrade `ObsPresent = true`. | The stored rows are durable and the restored state is root-verified. The wipe-rebuild dropped stats and wrote O(sealed) on every restart, including the path that should write nothing. |
| Sealed-inference index, full replay | Wipe is defensible (divergent history). Rebuild from the journal inside the same `ObsRepairGate` window as validation obs, after the session is published. | Same scheduling argument as obs: the session must be live before the expensive half runs. |
| Batching | Chunk the transaction **and** the statement. Postgres: one `unnest` upsert per 500-row chunk. SQLite: prepare the upsert once per chunk. Drain: set-at-a-time, not one txn per id. Record: 500-entry writes. | Chunking the transaction bought Postgres nothing (~3058ms vs ~3006ms at 20k): it pays per round trip, not per commit. One statement per row was the remaining cost. |
| Post-wipe load | `BulkInsertSealedInferences`: Postgres `COPY`, no per-row conflict probe, fallback to the upsert on unique violation. SQLite keeps the batched upsert (in-process b-tree, not a round trip). Drain: one data-modifying CTE instead of `INSERT`+`DELETE` in an explicit transaction. | The full-replay rebuild has just wiped the escrow, so inserts cannot conflict. The leftover ~99ms/20k on the upsert was the conflict probe, not the protocol. `COPY` is ~30ms; the CTE drain is ~58ms from ~81ms. |
| Shutdown | `WaitRecoveryRepairs` is a closer (renamed from `WaitObsRepairs`). | A rebuild interrupted after its clear leaves the counters empty, and recovery will not retry once a snapshot exists. The waiter now covers both validation obs and the sealed-inference index. |
| Warm signal | `recovery_complete` is true only after the backlog drains **and** `WaitRecoveryRepairs` returns. Promote the log-only counters onto the `/ready` body and `devshardd_session_recovery`. | A full-replay child is published long before its sealed-inference index is rebuilt. Declaring that warm would route overlap traffic at a host mid-wipe. |

## Out of scope (deliberate)

- An escrow recovered from a snapshot whose obs is genuinely empty still never gets repaired. The cheap fix is rebuilding when `GetValidationObservability` comes back empty.
- While a rebuild runs, `/stats` under-reports for that escrow (tables are cleared and refilling). Nothing outside stats reads them.
- Blocking versus `503 Retry-After` for a first request to an unrecovered escrow; that is a client-contract decision.
- Versiond overlap wait on `recovery_complete` (companion plan Step 9). That is a v5 `versiond` change and is useless until this child ships the field and the eviction.
- Rolling-update docs stay on the old probe contract until that versiond wait lands.

---

## Commits

### 1. `3d25e29f5` — `fix(devshard): restore host snapshot load on v4 session recovery`

**Decision.** Put `LoadSnapshot` back on the v4 host recovery path, matching v3 `RecoverSession`. On decode or load failure, fall back to a full journal replay rather than failing the session.

**Changed.**

- `recoverStoredSession` loads a host snapshot when `snapNonce > 0 && snapNonce <= meta.LatestNonce`, restores SM / committed entries / sealed nonces, then replays only `snapNonce+1..latest`.
- After a successful (or full) replay, save a recovery snapshot when the replay was from 1 or the tail was at least `SnapshotInterval`.
- Tests in `manager_snapshot_recovery_test.go`: restore + tail replay, current snapshot skips apply, empty blob and load error fall back to nonce 1.

### 2. `0d4c965c2` — `fix(devshard): restore 8-worker session recovery on v4`

**Decision.** Recovery concurrency is independent of snapshot load. Restore v3's 8-worker pool so a large host does not recover sessions one at a time.

**Changed.**

- `RecoverSessions` fans work across `recoverSessionsConcurrency` (8) workers, capped at the session count.
- Per-session recover/fail/version-skip counters and logs.
- Tests in `manager_recovery_workers_test.go`: parallel recovery, worker cap, version-conflict skip.

### 3. `a13ea92de` — `fix(devshard): unblock bind during recovery and verify restored snapshots`

**Decisions.**

1. Bind first, recover in the background. A live `getOrCreate` recovers the requested session immediately instead of queueing behind the backlog.
2. After restoring a snapshot, verify its root against the journal diff at that nonce. On mismatch, recreate the SM and replay from 1. Diff replay already checks every nonce it applies; the snapshot path must not skip `1..N`.
3. Gate `/ready` 503 on `RecoveryComplete` (revised in commit 15 once the wipe was off the request path).

**Changed.**

- `StartRecovery` / `RecoveryComplete` / `RecoverSessionsContext` (cancellable).
- First-cut `recoveryGate`: park workers while a request is in flight (refined in commit 4).
- `app.go` starts recovery as a closer after `store.Start()`, so the listener binds immediately.
- `buildAdminServer` takes `recoveryComplete`; `/ready` stays 503 until the backlog drains (until commit 15).
- `verifySnapshotRoot` in `recoverStoredSession`.
- Tests: `/ready` reflects recovery progress; snapshot root mismatch replays from 1.

### 4. `7e704795f` — `fix(devshard): order recovery backlog by demand instead of parking all workers`

**Decision.** The gate in commit 3 paused every worker whenever *any* request was in flight, including workers recovering the session the caller wanted. Replace that with a demand-ordered queue:

- Sticky marker: an escrow that hits `getOrCreate` stays prioritized for the rest of the recovery window.
- Workers dequeue demanded sessions first.
- Only a worker about to pick up a **cold** session parks.
- A parked worker whose escrow becomes demanded resumes.
- If every remaining session is demanded, nobody parks.

**Changed.**

- `recoveryGate` + `recoveryQueue` in `manager.go`.
- Priority tests: parking, preemption, promotion, boundedness, sticky marker, mid-drain reordering, end-to-end concurrent demand.
- `countingMetaStore` counters in `stats_test.go` made atomic: concurrent recovery races them once workers are no longer serialized by the old channel handoff.

### 5. `c09f15ec4` — `perf(devshard): skip the obs rebuild when a snapshot covers the history`

**Decisions.**

- Snapshot path: skip obs rebuild. Rows for those nonces are already durable.
- Full replay (`replayFrom == 1`): still rebuild. That is the self-heal.
- Incremental / partial-range top-up: **not valid**. Drain deletes the dedup row, so a tail replayed twice double-counts. `TopUpValidationObsFromDiffs` and `SealedInferenceIDsSortedFrom` were removed after an idempotency test found this.

**Changed.**

- `recoverStoredSession` rebuilds obs only when `replayFrom == 1`.
- `RebuildValidationObsFromDiffs` is explicitly clear-then-replay.
- `TestRecordValidationsAppliedOnce_NotDedupedAfterDrain` pins the hazard.
- Snapshot-path test asserts obs tables are not cleared.

### 6. `3d2fd05d1` — `perf(devshard): rebuild validation obs in the background behind a write gate`

**Decisions.**

- End restoration without filling obs. Publish the session, then start a background repair with the journal already in hand and the seal set as of that journal's last nonce.
- `ObsRepairGate` wraps the store. Idle = pass-through. During a repair for escrow E, `RecordValidationsAppliedOnce` and `DrainInferenceValidationObs` for E are queued in arrival order and applied after the rebuild writes to the inner store (so the rebuild cannot queue behind itself).
- Rebuild covers `1..N`; live diffs during the window are past `N`; queued writes never overlap the rebuild range, so the drain/dedup hazard from commit 5 does not arise.
- Queue 8192, drop newest, report. Flush 64 rounds then close. Concurrent repair for the same escrow rejected. Shutdown waits (`WaitObsRepairs`, renamed in commit 8).

**Changed.**

- New `storage/obs_repair_gate.go` (+ tests: idle transparency, rebuild bypasses queue, queued write survives the clear, record-then-drain order, per-escrow isolation, overflow, flush-after-rebuild-error, concurrent writers).
- `HostManager` always wraps the store in the gate. `startObsRepair` / `WaitObsRepairs`.
- `app.go` registers `WaitObsRepairs` as a closer.
- Full-replay test asserts recovery returns before the rebuild finishes, then waits and checks the rebuild still ran.

### 7. `9af175e43` — `fix(devshard): wait for obs repairs before test TempDir cleanup`

**Decision.** A full-replay recovery returns before `startObsRepair` finishes. Tests that tear down with `t.TempDir` must wait, or the rebuild still holds the SQLite WAL when cleanup removes it.

**Changed.**

- `HostManager` teardown waits for repairs.
- The held-rebuild test unblocks `release` if it fails before closing it.
- CI failure of `TestRecoverSessions_LoadSnapshotErrorReplaysFromOne` was cleanup, not the recovery assertion.

### 8. `b7596d472` — `fix(devshard): stop wiping sealed-inference stats on snapshot recovery`

**Decisions.**

- Snapshot path: gap fill only. Never delete, never downgrade a rich row. Normally writes nothing.
- Full replay: wipe is defensible, but rebuild from the journal in the same background `ObsRepairGate` window as validation obs, in 500-row transactions, after the session is published.
- Rename `WaitObsRepairs` → `WaitRecoveryRepairs`. `recovery_complete` still follows the backlog drain until commit 15.
- Keep `RebuildSealedInferenceIndex` as a gap-fill wrapper so the v3 `user/recover.go` path stops losing stats without restructuring it.

**Changed.**

- `SealedInferenceIDs` / `InsertSealedInferences` on every backend. `ObsRepairGate` queues sealed-inference inserts during a repair.
- `FillSealedInferenceIndexGaps` / `RebuildSealedInferenceIndexFromDiffs`.
- `recoverStoredSession` picks by `replayFrom`.
- Snapshot-path test: planted rich rows survive, `DeleteSealedInferences` is never called. Full-replay test: session published before the wipe, rich rows after `WaitRecoveryRepairs`. `TestStartRecovery_CompleteBeforeSealedIndexRepair` pins that `recovery_complete` still followed the backlog (flipped in commit 15).

### 9. `021e5044b` — `perf(devshard): keep the sealed-index rebuilds off the state machine lock`

**Decision.** The from-diffs fold held `sm.mu.RLock` across the journal walk, and the gap fill held the write lock across listing and every insert. `ApplyDiff` takes the write lock, so a background repair on an already-published session stalled the apply path for the length of the replay.

**Changed.**

- Both rebuilds snapshot escrow id, group, config, seal nonces, live records, then walk and write outside the lock.
- Gap fill counts gaps before allocating, so the common no-gap restart allocates nothing instead of a millions-long `[]InferenceRow`.
- Recovery log reads `SealedNonceCount()` instead of cloning the seal-nonce map for its length.

### 10. `801f87e60` — `perf(devshard): batch the obs half of the full-replay repair`

**Decision.** Profiling showed the sealed-index work was the smaller half. `RebuildValidationObsFromDiffs` issued one `RecordValidationsAppliedOnce` per journal record and one `DrainInferenceValidationObs` per sealed id, each drain its own begin/select/insert/delete/commit.

**Changed.**

- `DrainInferenceValidationObsBatch`: chunked set-at-a-time move plus delete.
- Records accumulate into 500-entry writes.
- Shared backend test pins batch drain against per-id semantics on Memory, SQLite, Postgres.
- At 20k SQLite: drain ~841ms → ~66ms, record ~325ms → ~52ms.

### 11. `4426262cf` — `perf(devshard): batch the sealed-inference upsert, not just its transaction`

**Decision.** `InsertSealedInferences` chunked the transaction but still issued a statement per row, so Postgres paid a round trip per inference and SQLite re-parsed the upsert for every row.

**Changed.**

- Postgres: one `INSERT ... SELECT FROM unnest(...)` per chunk, same upsert clause. Ids repeated inside a chunk collapse to the last value first (`ON CONFLICT DO UPDATE` cannot touch a row twice).
- SQLite: prepare the upsert once per chunk transaction.
- Shared test pins every sealed column, overwrite of an existing row, and in-chunk duplicate.
- At 20k SQLite: ~431ms → ~50ms. `pg_stat_statements` shows `ceil(sealed/500)` calls instead of `sealed`.

### 12. `6481fc2c7` — `test(devshard): benchmark the rebuild against Postgres, not just SQLite`

**Decision.** The batching work was measured on SQLite, where the cost is per commit. On Postgres it is per round trip, and the two disagree about what "batched" means.

**Changed.**

- `BenchmarkPostgres*` counterparts, including the chunked-transaction-per-row shape so the wash stays visible (~3058ms vs ~3006ms unbatched at 20k).
- Comparison recorded in `sealed-inference-index-rebuild-plan.md`.
- Per-row forms had Postgres 5–16× behind SQLite; set-at-a-time forms brought them within ~1.4×.

### 13. `20449866a` — `fix(devshard/host): guard validationQueue reads against concurrent Close`

**Decision.** Backport of #1512. Not caused by this branch's recovery work, but `go test -race` failed `TestHost_ValidationTriggersOnFinishedInference` and `TestSession_Validation_InvalidationConverges` on this tree because `t.Cleanup(h.Close)` races workers still draining the queue.

**Changed.**

- `validateAsync` deferred cleanup and `enqueueValidation` success branch snapshot `h.validationQueue` under `RLock` instead of reading the field after unlock / without the lock.
- `Close()` closes the channel and nils the field under the write lock.

### 14. `45e7ea264` — `perf(devshard/storage): load the rebuilt sealed index with COPY`

**Decisions.**

- The full-replay rebuild wipes then loads, so inserts cannot conflict. The leftover ~99ms/20k on the `unnest` upsert was the per-row conflict probe (~163µs RTT × not the bottleneck; chunk-size sweeps past 500 and `synchronous_commit=off` moved nothing).
- `BulkInsertSealedInferences`: Postgres `COPY`, fallback to the upsert on unique violation so a wrong "no rows yet" precondition costs speed rather than the rebuild.
- Drain: one data-modifying CTE instead of four round trips per chunk (`BEGIN`/`INSERT`/`DELETE`/`COMMIT`).
- SQLite keeps a single path.

**Changed.**

- Interface method on every backend; `ObsRepairGate` queues bulk as an ordinary upsert batch (the "rows do not exist yet" precondition cannot survive being deferred past the rebuild).
- `RebuildSealedInferenceIndexFromDiffs` calls the bulk path.
- At 20k: Postgres load ~99ms → ~30ms, drain ~81ms → ~58ms. Full-replay repair ~145ms Postgres vs ~165ms SQLite, so Postgres is no longer the slower backend for it.

### 15. `4c7022ee3` — `feat(devshard): serve during recovery and wait to call it warm`

**Decisions.** Companion ready-on-boot items 1–4, together, so a 200-during-recovery child cannot look warm mid-wipe and cannot sit stale behind the old generation's writes.

- `/ready` 200 as soon as the process can serve. Drop `recovered` from the 503 condition. Keep `storage_ready` and `draining`.
- `recovery_complete` true only after the backlog drains **and** `WaitRecoveryRepairs` returns. `sessions_pending` includes both the queue and in-flight repairs.
- Promote the log-only counters onto the body and `devshardd_session_recovery`.
- On `ErrInvalidNonce` from apply: evict, recover, retry once. Surviving mismatch → `RememberStaleNonce` at the short TTL.

**Changed.**

- `buildAdminServer` takes `RecoveryProgressSnapshot`. Status 200 while `recovery_complete` is still false; 503 while draining or storage unready.
- `HandleInference` and `ChallengeReceipt` `SetInternal` the apply error so `retryIfStale` can `errors.Is` the nonce mismatch.
- `ReloadStaleSession` / `RememberStaleNonce` / `evictSession`.
- Tests: `/ready` 200 during recovery with body counters; `TestStartRecovery_CompleteAfterSealedIndexRepair` (was "before"); store-ahead reload succeeds; bogus nonce is negative-cached and does not re-enter recovery.

---

## Test plan

- [x] `go test -race ./storage/ ./state/ ./user/ ./cmd/devshardd/...` from `devshard/` (green under `-race` on this branch, including the #1512 backport).
- [x] Snapshot restore + bind-first (unit): tail-only replay, `StartRecovery` reports the backlog drained, listener is not blocked. `/ready` is now **200 during recovery** with `recovery_complete: false` (`TestReadyReflectsSessionRecoveryProgress`). Still open: cold start of a real v4 host with a long journal (`restored devshard snapshot` in logs, listener binds immediately, `/ready` 200).
- [x] Demanded session recovers before cold backlog (unit: `TestRecoverSessions_RunsRequestedSessionsWhileColdOnesWait`). Still open: `prioritized_count` in a live completion log.
- [x] Corrupt / mismatched snapshot blob falls back to full replay (unit: `TestRecoverSessions_CorruptSnapshotReplaysFromOne`, `TestRecoverSessions_SnapshotRootMismatchReplaysFromOne`). Still open: log line `devshard snapshot failed root check` on a real host.
- [x] Full-replay path publishes the session before obs / sealed-index rebuild finishes (unit: `TestRecoverSessions_FullReplayRebuildsValidationObsInBackground`, `TestRecoverSessions_FullReplayRebuildsSealedIndexInBackground`). Still open: stats eventually match after `rebuilt validation obs`.
- [x] Snapshot path leaves sealed-inference stats intact and never calls `DeleteSealedInferences` (`TestRecoverSessions_SnapshotPathLeavesSealedInferenceRows`). Gap fill on an already-indexed set inserts 0 (`TestFillSealedInferenceIndexGaps_Idempotent`).
- [x] `recovery_complete` stays false until `WaitRecoveryRepairs` returns (`TestStartRecovery_CompleteAfterSealedIndexRepair`).
- [x] Store-ahead apply evicts and recovers; a bogus nonce is negative-cached (`TestReloadStaleSession_CatchesUpToStoreAhead`, `TestReloadStaleSession_NegativeCachesBogusNonce`).
- [x] Postgres vs SQLite rebuild benches (`BenchmarkPostgres*`, including `BulkInsert` / CTE drain). Linearized 1.5M: snapshot path ~0.5s reads and zero writes; full replay ~12s SQLite / ~11s Postgres, off the publish path.
- [ ] Shutdown during a full-replay rebuild: process waits for the repair; counters are not left empty. (`WaitRecoveryRepairs` is wired as a closer; no test covers interrupt-after-clear.)
- [ ] Operator restart with a long journal and a current snapshot: sealed-inference stats identical before and after; log `filled sealed inference index gaps inserted=0`.
- [x] Versiond overlap wait on `recovery_complete` (companion plan Step 9). Implemented in `versioned/internal/process/manager.go` (`waitForChildRecoveryComplete` + `getChildRecoveryStatus`), gated by `VERSIOND_RECOVERY_TIMEOUT` (default 30m) in `versioned/internal/config`. Overlap branch of `downloadAndSwap` only; solo start and stop/start never wait. Bail-outs: absent field → cold cutover; old-child death → publish immediately; hostDraining/ctx → abort; timeout → old keeps serving. `watchChildReadiness` stays status-only, pinned by `TestWatchChildReadiness_NeverReadsBody_…`. Docs: `rolling-update.md` §1.3–§1.5, `release-0.2.15-v5.md`. testenv boot tests: `TestVersiondWarmCutoverBoot` (public `/healthz` 200 with `VERSIOND_RECOVERY_TIMEOUT` set + chat serves after boot) and `TestVersiondWarmCutoverOverlapWaitsThenServes` (sha flip produces `running(new)`+`draining(old)` overlap, new traffic serves, old retires) in `devshard/testenv/citest/versiond_warm_cutover_test.go`; `make -C devshard/testenv citest-versiond-warm-cutover`.
