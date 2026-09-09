# Follow-ups for [#1432](https://github.com/gonka-ai/gonka/pull/1432)

**Base:** `feat/smst-tree-cache` (or merge into [#1432](https://github.com/gonka-ai/gonka/pull/1432))  
**Branch:** `ak/smst-tree-cache-followups`

## Summary

Review follow-ups on top of [PR #1432](https://github.com/gonka-ai/gonka/pull/1432) (SMST deferred hashing + copy-on-write historical proofs). No consensus / root-byte changes; same Merkle semantics.

### Fixes

1. **Retained historical proofs no longer hold the write lock across artifact I/O**  
   After taking a retained COW view, unlock and re-take `RLock` so ingest and concurrent proofs are not serialized behind `getArtifactByNonce`.

2. **Durable flush boundaries independent of `distributions.jsonl`**  
   Persist `count` + root to `flushed_roots.jsonl` on flush. Recover unions that journal with distribution history so a warn-only dist-append failure cannot make an on-chain flush look non-committed after restart. Backfill for stores that only had dist history.

3. **Live-tip deferred hash fill under `Lock`, proofs under `RLock`**  
   `ensureHashed` runs under write lock only, then retries onto the read-lock fast path. `getRoot` / `GetRootAt` / `GetFlushedRoot` prefer `RLock` when already hashed.

### Profiling / toggles (defaults unchanged for production)

| Env | Default | `0` / `false` |
|-----|---------|----------------|
| `SMST_DEFERRED_HASH` | **on** | Eager per-insert hashing (v0.2.14 baseline) |
| `SMST_COW` | **on** | In-place `Insert` (no path-copy retain at flush) |
| `SMST_SNAPSHOT_IN_MEMORY_CLONE` | **on** | Tip `PrebuildSnapshot` rebuilds from artifacts into the process cache **without** holding the write lock (v0.2.14 `Warm`/`Prebuild` path) |
| `SMST_PARALLEL_HASH` | **on** | Serial `ensureHashed`; default fans out deferred fill across `GOMAXPROCS` |

Tip `PrebuildSnapshot` when count equals the live tip:

| Mode | Behavior |
|------|----------|
| COW on | O(1) retain (usually already done at flush; Prebuild is a no-op) |
| COW off + `SMST_SNAPSHOT_IN_MEMORY_CLONE=1` | Deep in-memory clone under write lock → `retained` |
| COW off + `SMST_SNAPSHOT_IN_MEMORY_CLONE=0` | Unlock, rebuild from artifact log → snapshot cache |

Cold path (tip already past, nothing retained, committed count) still rebuilds into the cache.

`SMST_PARALLEL_HASH` accelerates deferred `ensureHashed` (independent subtrees, min 256 leaves). Eager per-insert path hashing stays serial (parent depends on child).

Profiles: `TestSMSTBuildProfile` (insert vs `GetRoot`) and `TestSMSTStoreFlush30Profile` (N leaves, 30 flushes, snap at flush #10).

### Measured — illustrative (N=300k)

**Build profile** (insert all leaves, then one full `GetRoot`):

| Mode | insert | getroot | total |
|------|--------|---------|-------|
| deferred + multicore | ~114 ms | **~28 ms** | **~141 ms** |
| deferred serial | ~112 ms | **~182 ms** | **~295 ms** |
| eager + multicore | ~681 ms | ~0 | ~681 ms |
| eager serial | ~679 ms | ~0 | ~679 ms |

**Flush30** (COW on; each flush only fills hashes dirtied since last flush):

| Mode | Ingest |
|------|--------|
| deferred + multicore | ~670 ms |
| deferred serial | ~689 ms |
| eager + multicore | ~1.19 s |
| eager serial | ~1.22 s |

Multicore helps the big deferred fill (~6× on full-tree `GetRoot`). Eager is unchanged. Incremental flushes already hash little, so parallel vs serial there is close.

**Snapshot modes** (deferred, COW off, snap at 1/3):

| Mode | Snap | via |
|------|------|-----|
| in-memory clone | ~6.6 ms | retained |
| artifact rebuild | ~95 ms | cache |
| COW on | ~1 µs | retained |

## Commits

1. `fix(poc/artifacts): release write lock before retained proof I/O`
2. `fix(poc/artifacts): persist flush roots independent of distributions.jsonl`
3. `fix(poc/artifacts): fill live-tip hashes under Lock, serve proofs under RLock`
4. `feat(poc/artifacts): env toggles for deferred hashing and COW + 30-flush profiles`
5. `feat(poc/artifacts): tip Prebuild in-memory clone vs artifact rebuild toggle`
6. `feat(poc/artifacts): multicore ensureHashed behind SMST_PARALLEL_HASH`

## Test plan

- [ ] `go test ./poc/artifacts/ -count=1`
- [ ] `go test ./poc/artifacts/ -race -run 'TestCOW|TestDeferred|TestFlushedRoots|TestSMSTCOW|TestSMSTDefaults|TestParallelHash' -count=1`
- [ ] Deferred × parallel profile matrix:
  ```bash
  SMST_PROF_N=300000 SMST_DEFERRED_HASH=1 SMST_PARALLEL_HASH=1 SMST_COW=1 \
    go test ./poc/artifacts/ -run 'TestSMSTBuildProfile|TestSMSTStoreFlush30Profile' -v
  SMST_PROF_N=300000 SMST_DEFERRED_HASH=1 SMST_PARALLEL_HASH=0 SMST_COW=1 \
    go test ./poc/artifacts/ -run 'TestSMSTBuildProfile|TestSMSTStoreFlush30Profile' -v
  SMST_PROF_N=300000 SMST_DEFERRED_HASH=0 SMST_PARALLEL_HASH=1 SMST_COW=1 \
    go test ./poc/artifacts/ -run 'TestSMSTBuildProfile|TestSMSTStoreFlush30Profile' -v
  SMST_PROF_N=300000 SMST_DEFERRED_HASH=0 SMST_PARALLEL_HASH=0 SMST_COW=1 \
    go test ./poc/artifacts/ -run 'TestSMSTBuildProfile|TestSMSTStoreFlush30Profile' -v
  ```
- [ ] Confirm production path with env unset: deferred + COW + in-memory clone + parallel hash all on (`TestSMSTDefaultsDeferredAndCOW`)

## Notes for #1432 authors / reviewers

These commits are intended to land **on top of** [#1432](https://github.com/gonka-ai/gonka/pull/1432) (or be folded into that PR). Happy to open this as a stacked PR into `feat/smst-tree-cache` or push onto the same branch if preferred.
