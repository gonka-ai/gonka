# Two-Level Block/Tx Cache with Conflict Detection for OCC

## Motivation

Cosmos SDK processes transactions sequentially by default. Every read from the
KV store requires protobuf unmarshalling, and every write requires marshalling —
operations that are both CPU-intensive and repeated across transactions within
the same block.

The goal of this proposal is twofold:

1. **Phase 1 (current):** Introduce a two-level block/tx cache implemented by
   `OptimisticStore` (name kept for code compatibility) to eliminate redundant
   marshal/unmarshal within a block while remaining fully deterministic and
   compatible with the existing sequential execution model.

2. **Phase 2 (future):** Reuse the same conflict-tracking infrastructure to
   enable **Optimistic Concurrency Control (OCC)** — parallel execution of
   transactions with automatic conflict detection and rollback.

---

## Problem Statement

The issue is **not** a classical race condition — it is a root of
**non-determinism in optimistic (parallel) behaviour**. When two transactions
execute in parallel and touch overlapping store keys, the final state depends on
execution timing rather than block ordering. This breaks consensus.

The Cosmos SDK's optional optimistic execution mode is not a universal,
one-size-fits-all feature. Its implementation is highly case-specific to the
module's access patterns. Gonka therefore implements its own OCC scheme tailored
to inference-chain workloads.

---

## Design: OCC for Gonka

### Scheduling

A scheduler takes **N** messages from the mempool (where N scales with CPU
cores). Messages are sorted and grouped by similarity — similar messages tend to
access similar store keys and have comparable execution times.

The selected N messages run as a **parallel batch**. The scheduler waits until
all messages in the batch complete, then takes the next batch.

### Conflict Detection

During execution, each transaction's read-set and write-set are recorded by the
`conflictTracker` inside every `OptimisticStore`. After the batch completes:

- **Read-Write conflict:** Transaction A read key K, transaction B wrote key K
  → A must be rolled back and rescheduled.
- **Write-Write conflict:** Transactions A and B both wrote key K → all but one
  (the one earlier in block order) must be rolled back and rescheduled.

Only the *losing* transactions are rescheduled to the next batch. When ordering
and batch-fill rules are deterministic, the entire parallel execution remains
deterministic across all validators.

### Conflict Resolution Ordering

When two transactions in the same batch both write the same key, the winner is
determined by **block order** — the transaction that appears earlier in the
ordered block survives; the later one is rolled back. This ensures every
validator makes the same choice deterministically.

### Grouping by Similarity

Grouping criterion is the *predicted access pattern* of the message type.
Two `MsgValidation` for the same model and epoch are
"similar" — they access the same keys and take roughly the same time to execute.
Static analysis per message type provides the access prediction.
Most of hot messages will be gone when shardchains start to work,
but anyway this approach continues to work and will be useful.

### Gas for Rescheduled Transactions

When a transaction is rolled back due to a conflict, its **gas meter resets**.
The rescheduled execution is a fresh attempt with a fresh meter. Cosmos gas
accounting therefore remains correct — the user's gas limit applies to the
successful execution, not to failed speculative attempts.

### High-Contention Liveness

By design, there should be no persistent hot keys. Shared data is mostly read
and then written in deterministic flow, so sustained conflicts should not appear
in normal operation.

As a safety fallback only, if repeated conflicts still happen due to unexpected
workload shape, a transaction can be forced into **sequential execution** after
**N retries** (configurable), guaranteeing forward progress.

### Throughput Improvement

Implementing this OCC scheme could yield **N × k** times better throughput for
the mainchain, where:

- **N** = number of CPU cores
- **k** = efficiency coefficient (< 1.0, depends on conflict rate)

For workloads with low key overlap (e.g. inferences touching different models),
k approaches 1.0 and throughput scales nearly linearly with cores.

---

## Phase 1: Two-Level Block/Tx Cache (Current Implementation)

Phase 1 is fully implemented and merged. There is **no parallel scheduler** yet.
All transactions still execute sequentially. This is not optimistic execution by
itself; it is a deterministic 2-level cache (tx draft + block cache) with
conflict detection that can be used later in optimistic concurrent mode.

This cache provides these benefits:

1. **Eliminate repeated marshal/unmarshal** for store values accessed multiple
   times within a block (e.g. `Params`, `EpochGroupData`).
2. **Lay the foundation for Phase 2** by tracking read/write sets per
   transaction, so conflict detection can be enabled with a single environment
   variable (`COSMOS_OCC_ENABLED=1`).

### Shared System Data and Cache Scope

We use this 2-level block/tx cache for frequently read shared system data:

- `Params`
- `EpochGroupData`

These are hot system reads, so caching removes repeated codec overhead on the
same values inside a block and transaction flow.

### Protobuf Marshal/Unmarshal and Gas

Cosmos SDK marshals/unmarshals protobuf data on store reads/writes, including as
part of store gas accounting. When reads/writes are served from this cache,
repeated codec operations are skipped and no extra gas is spent for those cached
operations in these system paths.

This is acceptable in our design because we already use no-gas or fixed-gas
policies for system messages (to reduce DDoS spam risk while keeping deterministic
execution). For example, `EpochGroupData` reads may be used by endpoint
protection logic to identify participants slashed for missing inferences or
missing cPoCs; this is a system check where fast execution is preferred over
repeated marshalling/unmarshalling.

### Architecture

```
┌───────────────────────────────────────────────────┐
│                   OptimisticStore                  │
│                                                    │
│  Layer 1: Tx Draft (context-scoped, per-tx)        │
│           ↓ miss                                   │
│  Layer 2: Block Cache (in-memory, per-block)       │
│           ↓ miss                                   │
│  Layer 3: Store Backend (persistent KV / protobuf) │
│                                                    │
│  + conflictTracker (read/write sets per tx)        │
└───────────────────────────────────────────────────┘
```

- **Tx Draft:** Write-behind buffer scoped to a single transaction via
  `context.Value`. Created in `AnteHandler`, committed to block cache on tx
  success in `PostHandler`, discarded on failure.
- **Block Cache:** Shared in-memory map for the current block. Populated on
  first read (cache-aside pattern). Flushed to the persistent store backend in
  `EndBlock`.
- **Store Backend:** Pluggable interface (`Load`/`Save`/`Delete`/`Clone`) that
  wraps any persistent storage (collections map, raw KV, etc.).

### Lifecycle: CheckTx vs DeliverTx

#### CheckTx (Mempool Validation)

During `CheckTx`, the SDK validates a transaction before it enters the mempool.
The optimistic store **does not commit drafts** during `CheckTx`:

```go
PostHandler:
  if ctx.IsCheckTx() || simulate {
      return  // skip commit — mempool validation only
  }
```

This means `CheckTx` sees the persistent store state (or block cache if already
warm), but its writes are discarded. This is correct because `CheckTx` must be
side-effect-free.

#### DeliverTx (Block Execution)

During `DeliverTx`, the full lifecycle executes:

```
AnteHandler (OptimisticStoreDraftDecorator):
  → StoreGroup.WithDraftAll(ctx)    // attach per-tx drafts
  → StoreGroup.RegisterTxAll(ctx)   // register for OCC tracking

Message Execution:
  → store.Get(ctx, key)  // reads: draft → block cache → backend
  → store.Set(ctx, key)  // writes: go to draft

PostHandler (OptimisticStoreCommitPostDecorator):
  if success:
      → StoreGroup.CommitDraftAll(ctx)   // merge draft → block cache
  else:
      → StoreGroup.ReleaseDraftAll(ctx)  // discard draft

EndBlock:
  → StoreGroup.FlushAll(ctx)             // block cache → persistent store

BeginBlock (next block):
  → StoreGroup.InvalidateAll()           // clear block cache + conflict tracker
```

### Branch Drafts

For operations that need speculative execution within a single tx (e.g.
`CacheContext` patterns), `OptimisticStore` supports **branch drafts**:

```go
cacheCtx, writeCache := keeper.CacheContext(ctx)
// speculative work on cacheCtx...
writeCache()  // merge branch draft into parent tx draft + SDK cache
```

The `StoreGroup.CacheContext` method creates branch drafts for every registered
store and returns a merged commit function.

---

## How OptimisticStore Works Around Existing Storage

`OptimisticStore` is a **decorator** — it wraps existing storage without
replacing it. The `StoreBackend` interface makes this explicit:

```go
type StoreBackend[K comparable, V any] struct {
    Load   func(ctx context.Context, key K) (V, bool)
    Save   func(ctx context.Context, key K, val V)
    Delete func(ctx context.Context, key K)
    Clone  func(val V) V
}
```

Any existing `collections.Map`, `collections.Item`, or raw KV store can be
wrapped by providing these four functions.

### Example: Wrapping a Collections Map

For `EpochGroupData`, which is stored in a `collections.Map[Pair[uint64,string],
EpochGroupData]`:

```go
// In keeper.go NewKeeper:
epochGroupStore: NewOptimisticCollMap[
    epochGroupCacheKey,
    collections.Pair[uint64, string],
    types.EpochGroupData,
](
    sb,
    types.EpochGroupDataPrefix,
    "epoch_group_data",
    collections.PairKeyCodec(collections.Uint64Key, collections.StringKey),
    codec.CollValue[types.EpochGroupData](cdc),
    cacheConfig,
    func(key epochGroupCacheKey) collections.Pair[uint64, string] {
        return collections.Join(key.Epoch, key.ModelId)
    },
),
```

`NewOptimisticCollMap` creates the `collections.Map` AND wraps it with an
`OptimisticStore` in one call. The `Load`/`Save`/`Delete` functions delegate to
the collection; `Clone` uses `proto.Clone`.

### Example: Wrapping a Singleton (Params)

For `Params`, stored as a single protobuf blob at a fixed KV key:

```go
paramsStore: NewOptimisticProtoItem[types.Params](
    storeService,
    cdc,
    types.ParamsKey,
    "params",
    cacheConfig,
),
```

`NewOptimisticProtoItem` handles marshal/unmarshal internally. `GetParams` and
`SetParams` simply call `paramsStore.Get(ctx)` / `paramsStore.Set(ctx, val)`.

### Registering with the StoreGroup

Every optimistic store must be registered with the keeper's `StoreGroup` so
lifecycle methods (`InvalidateAll`, `FlushAll`, `WithDraftAll`, etc.) apply to
all stores uniformly:

```go
k.storeGroup.Register(k.epochGroupStore.OptimisticStore)
k.storeGroup.Register(k.paramsStore.Store())
```

### Adding a New Optimistic Store to the Keeper

To wrap a new collection with optimistic caching:

1. Define a cache key type (must be `comparable`):
   ```go
   type myNewCacheKey struct {
       Field1 uint64
       Field2 string
   }
   ```

2. Add the optimistic store field to `Keeper`:
   ```go
   myNewStore *OptimisticCollMap[myNewCacheKey, collections.Pair[uint64, string], types.MyNewType]
   ```

3. Initialize in `NewKeeper` using `NewOptimisticCollMap` (or
   `NewOptimisticProtoItem` for singletons).

4. Register with the store group:
   ```go
   k.storeGroup.Register(k.myNewStore.OptimisticStore)
   ```

5. Write getter/setter methods on `Keeper` that delegate to the store:
   ```go
   func (k Keeper) GetMyNewData(ctx context.Context, key myNewCacheKey) (types.MyNewType, bool) {
       return k.myNewStore.Get(ctx, key)
   }
   func (k Keeper) SetMyNewData(ctx context.Context, val types.MyNewType) {
       key := myNewCacheKey{Field1: val.Field1, Field2: val.Field2}
       k.myNewStore.Set(ctx, key, val)
   }
   ```

No changes are needed to `AnteHandler`, `PostHandler`, `BeginBlock`, or
`EndBlock` — the `StoreGroup` handles all registered stores automatically.

---

## Wiring: AnteHandler, PostHandler, BeginBlock, EndBlock

### AnteHandler (`ante.go`)

```go
type OptimisticStoreDraftDecorator struct {
    InferenceKeeper *inferencemodulekeeper.Keeper
}

func (d OptimisticStoreDraftDecorator) AnteHandle(ctx sdk.Context, tx sdk.Tx, simulate bool, next sdk.AnteHandler) (sdk.Context, error) {
    g := d.InferenceKeeper.StoreGroup()
    newCtx := ctx.WithContext(g.WithDraftAll(ctx.Context()))
    g.RegisterTxAll(newCtx)
    return next(newCtx, tx, simulate)
}
```

### PostHandler (`ante.go`)

```go
func (d OptimisticStoreCommitPostDecorator) PostHandle(ctx sdk.Context, tx sdk.Tx, simulate, success bool, next sdk.PostHandler) (sdk.Context, error) {
    newCtx, err := next(ctx, tx, simulate, success)
    if ctx.IsCheckTx() || simulate {
        return newCtx, nil  // no side effects during CheckTx
    }
    g := d.InferenceKeeper.StoreGroup()
    if success {
        g.CommitDraftAll(newCtx)   // merge draft → block cache
    } else {
        g.ReleaseDraftAll(newCtx)  // discard draft
    }
    return newCtx, nil
}
```

### BeginBlock (`module.go`)

```go
func (am AppModule) BeginBlock(ctx context.Context) error {
    am.keeper.StoreGroup().InvalidateAll()  // clear block caches + conflict tracker
    // ... rest of begin block
}
```

### EndBlock (`module.go`)

```go
func (am AppModule) EndBlock(ctx context.Context) error {
    defer am.keeper.StoreGroup().FlushAll(ctx)  // persist block caches to store
    // ... rest of end block
}
```

---

## Phase 2 Roadmap: OCC Scheduler

Phase 2 will add a deterministic parallel scheduler. The current
`conflictTracker` inside `OptimisticStore` already tracks per-tx read/write sets
and can detect conflicts via `DetectConflicts()`. What remains:

1. **Batch Scheduler** — select N messages, group by predicted access pattern,
   execute in parallel.
2. **Conflict Resolution** — after batch completion, call `DetectConflicts()` on
   each store, roll back losers (by block order), reschedule to next batch.
3. **Retry Budget** — force sequential execution after N failed attempts.
4. **Integration** — replace the sequential `DeliverTx` loop with the batched
   scheduler; keep the same `AnteHandler`/`PostHandler` draft lifecycle.

The `OptimisticStore` API is already designed for this:

```go
// Enable conflict detection at startup:
// COSMOS_OCC_ENABLED=1

// After parallel batch completes:
conflictedReads, conflictedWrites := store.DetectConflicts()
// Roll back and reschedule conflicted txIDs...
store.ResetConflictTracker()
```

No changes to the store, keeper, or cache layer are expected for Phase 2 — only
the addition of the scheduler and the `DeliverTx` loop replacement.
