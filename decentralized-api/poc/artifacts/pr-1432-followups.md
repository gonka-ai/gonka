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
| `SMST_COW` | **on** | In-place `Insert`; no retained snapshots |

When COW is off, early (e.g. 1/3) commits stay cached via the existing process snapshot cache (`WarmSnapshot` / `PrebuildSnapshot`), matching upgrade-v0.2.14 behavior.

Added `TestSMSTStoreFlush30Profile`: N leaves, 30 flushes, snapshot at flush #10.

### Measured (30 flushes, snap at 1/3) — illustrative

N=300k (10k/flush, early=100k):

| Mode | Ingest + snap | Early proof |
|------|---------------|-------------|
| Eager, COW off | ~1.4 s (+ ~0.3 s snap) | ~µs (cache) |
| Deferred, COW off | ~1.1 s (+ ~0.5 s snap) | ~µs (cache) |
| Deferred + COW on | ~0.67 s (snap ~0) | ~µs (retained) |

## Commits

1. `fix(poc/artifacts): release write lock before retained proof I/O`
2. `fix(poc/artifacts): persist flush roots independent of distributions.jsonl`
3. `fix(poc/artifacts): fill live-tip hashes under Lock, serve proofs under RLock`
4. `feat(poc/artifacts): env toggles for deferred hashing and COW + 30-flush profiles`

## Test plan

- [ ] `go test ./poc/artifacts/ -count=1`
- [ ] `go test ./poc/artifacts/ -race -run 'TestCOW|TestDeferred|TestFlushedRoots|TestSMSTCOW|TestSMSTDefaults' -count=1`
- [ ] Optional profile matrix:
  ```bash
  SMST_PROF_N=300000 SMST_DEFERRED_HASH=0 SMST_COW=0 go test ./poc/artifacts/ -run TestSMSTStoreFlush30Profile -v
  SMST_PROF_N=300000 SMST_DEFERRED_HASH=1 SMST_COW=0 go test ./poc/artifacts/ -run TestSMSTStoreFlush30Profile -v
  SMST_PROF_N=300000 SMST_DEFERRED_HASH=1 SMST_COW=1 go test ./poc/artifacts/ -run TestSMSTStoreFlush30Profile -v
  ```
- [ ] Confirm production path with env unset: deferred + COW both on (`TestSMSTDefaultsDeferredAndCOW`)

## Notes for #1432 authors / reviewers

These commits are intended to land **on top of** [#1432](https://github.com/gonka-ai/gonka/pull/1432) (or be folded into that PR). Happy to open this as a stacked PR into `feat/smst-tree-cache` or push onto the same branch if preferred.
