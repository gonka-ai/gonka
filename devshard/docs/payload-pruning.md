# Per-inference payload pruning (post RevealSeed removal)

Status: planned. Companion to [remove-reveal-seed.md](./remove-reveal-seed.md).

> **Scope note.** This plan targets the *payload* storage at
> `decentralized-api/payloadstorage/` -- the per-inference prompt/response
> bytes. It is **independent** of the *session* storage redesign at
> `devshard/storage/` described in [storage-design.md](./storage-design.md),
> which partitions diffs, signatures, and snapshots by `epoch_id` and
> prunes them by partition drop / file delete at `retain=3`. The two
> subsystems live side-by-side: pruning a payload here never touches a
> devshard session row, and pruning a session epoch in `devshard/storage/`
> never touches a payload file. Cross-references to `storage-design.md`
> appear in the "Alignment with session-storage design" section below.

## Motivation

Executor payload storage today is **epoch-granular**. The `ManagedStorage`
wrapper in `decentralized-api/payloadstorage/managed_storage.go` deletes
whole epochs once `maxEpoch - retainCount` is crossed (lookback of 10
epochs). Until that point a finished inference's full prompt and response
stay on disk (or in Postgres) for the entire epoch lifetime.

This was sized for the commit-reveal model: validators were expected to
fetch payloads any time before `RevealSeed`, so payload retention had to
straddle the whole session plus the reveal phase. With
[`MsgRevealSeed` removed](./remove-reveal-seed.md) the reveal step is
gone, and the validation window shrinks to *Active phase only*:

- A validator picks inferences via local `ShouldValidate(ownSeed, ...)`.
- It produces `MsgValidation` while the session is in `PhaseActive`.
- Once `PhaseFinalizing -> PhaseSettlement` (deadline-only), no more
  validations or challenges can land.

We can prune executor-side payloads aggressively as soon as the local
validation window for an inference is over. The hard constraint is that
no validator should be unable to validate an inference *that we are still
counting against it on chain*; with commit-reveal gone, **no chain code
counts skipped validations** (see `attacks.md` and
`remove-reveal-seed.md`), so missed validations are invisible. That
slack is what lets us prune early.

## Goals

1. Add a per-inference `Delete` operation to `PayloadStorage` and wire it
   through `ManagedStorage` and all backends (file / postgres / hybrid).
2. Have the host emit an event (callback) when an inference reaches a
   prunable state, and have `decentralized-api`'s `HostManager` translate
   those events into `PayloadStorage` `Delete` calls.
3. Prune in three layers (Tier A + B + C). Tier C is the headline
   change; A and B are no-cost cleanup that falls out naturally.
4. Make a validator silently skip when the executor returns 404 for a
   payload, instead of failing the validation or treating it as Invalid.

## Non-goals

- No change to epoch-granular `PruneEpoch` / `ManagedStorage`
  cleanup loop. It remains as a backstop for orphans (e.g. host crash
  between `Store` and the callback).
- No change to the chain-side payload retention or validator dispute
  flow. This is an executor-internal optimization.
- No change to `ShouldValidate` or per-host seed derivation.
- No new on-chain protocol field (no `FinishNonce` in
  `InferenceRecord`, no consensus impact, no state-root change).

## Decisions (confirmed)

| ID | Choice |
|----|--------|
| D1 | Add `DeleteInference(ctx, inferenceId, epochId) error` to `PayloadStorage`. |
| D2 | Host signals prune-eligible inferences via a **callback** registered by `decentralized-api`. No coupling to consensus or state hash. |
| D3 | Implement **Tier C (soft)** as a temporary in-session per-inference deadline. Executor prunes Finished payloads only after both a nonce gate and a wall-clock gate pass. State machine unchanged. |
| D4 | Validator behavior on missing payload: **skip silently**. No `MsgValidation`, no challenge, no failure record. |

## Design overview

Three pruning tiers, all driven by host-emitted events. Tiers A and B
are the stable baseline; Tier C is a temporary pressure-relief measure
until the validation engine is changed so challenge / vote handling does
not depend on long-lived executor payload retention.

### Tier A -- terminal-status pruning

When an applied diff transitions an inference to a **terminal status**
(`StatusValidated`, `StatusInvalidated`, `StatusTimedOut`), the host
emits one prune event for that inference. There is no protocol path
that needs the payload after this point.

Trigger sites (all inside `state.StateMachine.ApplyDiff`-induced
transitions, observed by the host in `applyAndPersist`):

- `applyValidation` flips `Status` from `Finished/Challenged` to
  `Validated` or `Invalidated`.
- `applyTimeoutInference` flips to `TimedOut`.

### Tier B -- settlement-entry sweep

When `PhaseFinalizing -> PhaseSettlement` transitions (deadline-only
post RevealSeed removal), no further `MsgValidation`, `MsgChallenge` or
`MsgVote` will be accepted by the state machine. Any inference still in
`StatusFinished` or `StatusChallenged` will never need its payload
again. The host emits one bulk prune event for all such inferences.

Trigger site: the deadline-only transition in `machine.go` (already
implemented for RevealSeed removal).

### Tier C -- in-session per-inference deadline (soft)

For inferences that linger in `StatusFinished` during long Active
phases, the host prunes their payload only after **both**:

- enough nonces have passed since `MsgFinishInference`, and
- enough wall-clock time has passed since the host observed that finish.

A validator that wakes up later just gets a 404 from the executor and
skips (D4).

Concretely:

- Each host maintains **off-state** metadata for finished inferences:
  - `finishedAt map[inferenceID]uint64` = nonce of `MsgFinishInference`
  - `finishedAtTime map[inferenceID]time.Time` = wall-clock time when
    the finish was applied locally
  This metadata is host-local; it is **not** part of the state root and
  **not** persisted in the diff store.
- After applying a diff at nonce `N`, the host scans `finishedAt` and
  flags any inference with
  both:
  - `N >= finishedAt[id] + validationGraceNonces`
  - `finishedAtTime[id] + graceInferenceClear < now`
  and the inference is still in `StatusFinished`. Each flagged ID
  becomes a prune event; its metadata entry is removed.
- Nonce-side default: `validationGraceNonces = 10 * len(group)`.
- Time-side default: `graceInferenceClear = 2 minutes`.
- This two-part gate is necessary because:
  - nonce growth is traffic-dependent, so nonce-only pruning can become
    dangerously short in wall-clock time at high throughput;
  - a later `StatusChallenged` path may still need the payload for
    validation voters.
- Therefore Tier C remains a **temporary trade-off**. It reduces payload
  retention pressure, but it is not the long-term design; the intended
  follow-up is to change the validation engine / flow so this pruning no
  longer risks dropping late validation or vote work.

Why "soft": we never coordinate this pruning with peers. The state
machine doesn't know about it, no `MsgValidation` deadline is enforced,
and no slashing fires. The only consequence of a validator arriving
late is its own `Validate` call returning a 404 from the executor; per
D4 it just logs and moves on. Since `RequiredValidations` /
`CompletedValidations` are zeroed (see `remove-reveal-seed.md`), this
is invisible on chain.

### Callback contract

Host -> manager event sink. One small interface keeps the host package
free of `payloadstorage` imports and lets tests stub it:

```go
package host

type PruneEventSink interface {
    // OnInferencePrunable is called once per inference that has reached
    // a state where its payload can be deleted. Multiple calls for the
    // same inferenceID are safe (sink must dedupe / treat as idempotent).
    OnInferencePrunable(event InferencePruneEvent)
}

type InferencePruneEvent struct {
    EscrowID          string
    InferenceID       uint64
    Reason            PruneReason
    // PayloadEpoch is the epoch the payload was Stored under, if the host
    // has observed an ExecuteResult for this inference. Adapters should
    // prefer this over any global "current epoch" lookup.
    PayloadEpoch      uint64
    PayloadEpochKnown bool
}

type PruneReason uint8

const (
    PruneReasonTerminal      PruneReason = iota // Tier A
    PruneReasonSettlement                       // Tier B
    PruneReasonStaleFinished                    // Tier C
)
```

- Set via `host.WithPruneSink(sink PruneEventSink) HostOption`.
- Default sink is nil (host emits nothing -- preserves existing
  behavior for unit tests and tools that don't manage payloads).
- Calls happen **after** `applyAndPersist` succeeds, while still under
  the host mutex, so we never emit a prune for state that wasn't
  durably applied. The sink is expected to be cheap and non-blocking
  (queue + async, see manager side below).

Manager side (`decentralized-api/internal/devshard`):

```go
type payloadPruneSink struct {
    store payloadstorage.PayloadStorage
    // Used only when the host did not stamp PayloadEpoch on the event,
    // typically only during a brief startup window before the first
    // execution observed the epoch.
    currentEpoch func() uint64
}

func (s *payloadPruneSink) OnInferencePrunable(event host.InferencePruneEvent) {
    primaryEpoch := uint64(0)
    if event.PayloadEpochKnown {
        primaryEpoch = event.PayloadEpoch
    } else if s.currentEpoch != nil {
        primaryEpoch = s.currentEpoch()
    }
    storageKey := devshardserver.PayloadKey(event.EscrowID, event.InferenceID)
    // Fire-and-forget; ManagedStorage will also cover us via PruneEpoch.
    go func() {
        ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        if err := deletePayloadWithAdjacentEpochs(
            ctx, s.store, storageKey, primaryEpoch,
        ); err != nil && !errors.Is(err, payloadstorage.ErrNotFound) {
            logging.Warn("payload prune failed",
                "escrow_id", event.EscrowID,
                "inference_id", event.InferenceID,
                "reason", event.Reason,
                "epoch_id", primaryEpoch,
                "error", err)
        }
    }()
}
```

Epoch resolution preference:

1. The host stamps `PayloadEpoch` (and `PayloadEpochKnown = true`) on the
   event whenever it has observed an `ExecuteResult` for that inference.
   This is the epoch the payload was actually stored under and avoids any
   guess work at the manager.
2. Per [storage-design.md](./storage-design.md), `RecoverSessions` now
   iterates `[]storage.ActiveSession{EscrowID, EpochID}` and re-creates
   `Host` instances with their epoch already known. The host carries that
   epoch into Tier A/B/C events emitted during replay, so the manager
   does not need to fall back to a global current-epoch resolver during
   recovery.
3. Only as a defensive last resort, when the event predates an
   `ExecuteResult` (essentially impossible for Tier A/B/C in practice),
   the manager falls back to a current-epoch supplier. The
   `deletePayloadWithAdjacentEpochs` helper additionally probes
   `epoch-1` and `epoch+1` to cover the narrow case of an inference that
   straddled an epoch boundary between `Store` and the prune event.

### Validator skip on missing payload (D4)

In `decentralized-api/internal/devshard/shared_runtime.go`,
`ValidateInferenceWithExecutor` -> `FetchPayloadsFromExecutor` ->
`validationpkg.FetchPayloadsHTTP` already surfaces a 404 from the
executor. Today that 404 propagates as an error and the validation
fails noisily. New behavior:

- If `FetchPayloadsFromExecutor` returns `ErrPayloadGone` (a new
  sentinel that wraps a 404 response from the executor), the validator
  shall:
  1. log at info with `inferenceId`, `executor`, `epoch`,
  2. return `(nil, devshard.ErrValidationSkipped)` (new sentinel),
  3. the caller (`host.validateAsync`) drops the validation silently
     without producing a `MsgValidation` and clears its
     `validating[id]` entry.

Already-in-flight validations whose payload races with prune still
complete normally (the response body is already buffered). Only
*future* validators that didn't fetch yet get the 404.

## File-by-file changes

### `decentralized-api/payloadstorage/storage.go`

Add the new method to the interface:

```go
type PayloadStorage interface {
    Store(ctx context.Context, inferenceId string, epochId uint64, prompt, response []byte) error
    Retrieve(ctx context.Context, inferenceId string, epochId uint64) (prompt, response []byte, err error)
    PruneEpoch(ctx context.Context, epochId uint64) error
    // DeleteInference removes a single payload. Returns ErrNotFound when
    // the payload is already gone (caller may ignore that case).
    DeleteInference(ctx context.Context, inferenceId string, epochId uint64) error
}
```

### `decentralized-api/payloadstorage/file_storage.go`

```go
func (f *FileStorage) DeleteInference(ctx context.Context, inferenceId string, epochId uint64) error {
    filename := inferenceIdToFilename(inferenceId)
    path := filepath.Join(f.baseDir, strconv.FormatUint(epochId, 10), filename+".json")
    if err := os.Remove(path); err != nil {
        if os.IsNotExist(err) {
            return ErrNotFound
        }
        return fmt.Errorf("remove payload file: %w", err)
    }
    return nil
}
```

### `decentralized-api/payloadstorage/postgres_storage.go`

`DELETE FROM payloads WHERE inference_id = $1 AND epoch_id = $2`,
returning `ErrNotFound` if `RowsAffected == 0`.

### `decentralized-api/payloadstorage/hybrid_storage.go`

Delegate to both backends (best-effort: drop cache, then file, then
postgres). Tolerate `ErrNotFound` from either.

### `decentralized-api/payloadstorage/managed_storage.go`

```go
func (m *ManagedStorage) DeleteInference(ctx context.Context, inferenceId string, epochId uint64) error {
    m.mu.Lock()
    delete(m.cache, inferenceId)
    m.mu.Unlock()
    return m.storage.DeleteInference(ctx, inferenceId, epochId)
}
```

The cache eviction is important: ManagedStorage caches read results for
`cacheTTL`, so a delete without cache eviction would let a late
validator's `Retrieve` continue to succeed from cache, defeating the
prune.

### `devshard/host/host.go`

- New option:
  ```go
  func WithPruneSink(s PruneEventSink) HostOption {
      return func(h *Host) { h.pruneSink = s }
  }
  ```
- New struct fields:
  ```go
  pruneSink             PruneEventSink
  finishedAt            map[uint64]uint64       // inferenceID -> nonce of FinishInference
  finishedAtTime        map[uint64]time.Time    // inferenceID -> wall-clock; Tier C only
  payloadEpochs         map[uint64]uint64       // inferenceID -> epoch payload was Stored under
  skippedValidations    map[uint64]struct{}     // de-dupes 404-skip path so we don't re-fire
  validationGraceNonces uint64                  // default 10 * len(group)
  inferenceClearGrace   time.Duration           // default 2 minutes
  ```
  `finishedAtTime` exists only because Tier C currently needs a
  wall-clock grace until the validation engine / vote flow is
  redesigned; see Tier C above.
- In `applyAndPersist` (already iterates `diff.Txs`), additionally:
  - On `MsgFinishInference`: record `finishedAt[id] = diff.Nonce`.
  - On `MsgTimeoutInference`: emit Tier A prune for that ID; delete
    from `finishedAt`.
  - On `MsgValidation` that just terminalized the inference (status now
    Validated/Invalidated): emit Tier A prune; delete from `finishedAt`.
  - After all txs in the diff are processed, if the diff caused
    `Finalizing -> Settlement`, iterate `state.Inferences` and emit
    Tier B prunes for any remaining `Finished` or `Challenged`.
  - Then scan finished inferences and emit Tier C prunes only when
    both the nonce gate and the wall-clock gate are satisfied and the
    inference is still `Finished`.
- The status-flip detection in `applyValidation` reads
  `sm.SnapshotState()` (or a cheaper helper) to compare before/after;
  alternative is to expose a tiny `LastTerminalized() []uint64` accessor
  on `StateMachine` to avoid the snapshot cost. The accessor approach
  is preferable; see "Open questions".
- Emission happens *after* the diff is durably applied (post `ApplyDiff`
  and `RemoveIncluded`, before unlocking). The sink call is expected to
  be fast (the manager's implementation enqueues to a goroutine).

### `devshard/host/host.go` -- `validateAsync`

On `validator.Validate` returning the new `ErrValidationSkipped` (or
`errors.Is` matches), do not push a `MsgValidation` into the mempool.
Just clear `validating[id]` and log.

### `decentralized-api/internal/devshard/shared_runtime.go`

- Define new sentinels:
  ```go
  var ErrPayloadGone = errors.New("payload no longer available on executor")
  ```
- In `FetchPayloadsFromExecutor`, when the HTTP status is 404 wrap
  the error as `fmt.Errorf("%w", ErrPayloadGone)`.
- In `ValidateInferenceWithExecutor`, recognize `ErrPayloadGone` and
  return it directly, decorated:
  ```go
  if errors.Is(err, ErrPayloadGone) {
      return nil, fmt.Errorf("%w: %w", devshardpkg.ErrValidationSkipped, err)
  }
  ```

### `devshard/errors.go` (or wherever pkg-level errors live)

Add `ErrValidationSkipped` sentinel exported from the `devshard`
package, so the `host` package and adapters can both reference it
without cycles.

### `decentralized-api/internal/devshard/manager.go`

- `HostManager` gains `pruneSink *payloadPruneSink` field.
- Constructed in `NewHostManager` with the existing `payloadStore` and
  the current epoch resolver (already injected via `phaseTracker` in
  `engine.go`; pass it through to the manager).
- `create` and `recoverSession` pass `host.WithPruneSink(m.pruneSink)`
  to `host.NewHost`.

### `decentralized-api/internal/devshard/engine.go`

No change needed beyond exposing whatever phase tracker the manager
needs to compute `currentEpochID`.

## Worked example

A 5-slot group running a long Active phase. Validator selection picks
inference 42 for slot 3.

1. Nonce 100: `MsgFinishInference{id=42}`. Host records
   `finishedAt[42] = 100`. Payload retained.
2. Nonce 105: slot 3's validator fetches payload, validates, posts
   `MsgValidation{id=42, valid=true}`.
3. Nonce 108: diff includes the validation; vote weight > threshold ->
   `rec.Status = StatusValidated`. Host emits **Tier A** prune for 42,
   `finishedAt[42]` cleared. Manager deletes payload immediately.
4. Other host's late `Validate(42)` call: 404 from executor ->
   `ErrPayloadGone` -> `ErrValidationSkipped`. No `MsgValidation`
   produced. No on-chain effect.

Alternative path -- slot 3 is offline:

1. Nonce 100: Finish at 100. `finishedAt[42] = 100`.
2. Nonces 101..119: no validation lands.
3. After both conditions hold:
   - nonce gate: `currentNonce >= 100 + 10*len(group)`
   - wall-clock gate: `now >= finishedAtTime[42] + graceInferenceClear`
   the host emits **Tier C** prune for 42. Payload deleted.
4. Slot 3 comes back, runs `Validate(42)`, executor returns 404,
   validator skips. Off chain.
5. Settlement entry: 42 is `Finished`. Cost is still charged to the
   executor exactly as today (no `Invalid` mark).

## Failure modes and edge cases

- **Host crash between `Store` and the prune callback**: the payload
  stays on disk past its useful life. `ManagedStorage.cleanupLoop`
  (epoch-granular) still sweeps it after `retainCount` epochs. No
  consensus impact.
- **Prune callback succeeds but `DeleteInference` fails**: logged at
  warn, payload remains until epoch sweep. No consensus impact.
- **Late `MsgValidation` for a Tier C-pruned inference**: validator's
  fetch sees 404, skip path triggers. Even if a hypothetical buggy peer
  *did* sequence a validation tx for an already-Finished inference, the
  state machine handles it: vote weight is still counted toward
  `VotesValid` / `VotesInvalid` (see `applyValidation`), and the
  pruning was for *payload bytes*, not for the protocol's view of the
  inference. So consensus is unaffected.
- **Diff replays on recovery**: `recoverSession` replays diffs through a
  fresh state machine. Two paths exist on the post-`storage-design.md`
  branch:
  - *No persisted snapshot* (or snapshot is older than the legacy
    cutover): the host replays every diff from nonce 1. `finishedAt`,
    `finishedAtTime`, and `payloadEpochs` are repopulated as normal.
    Tier A and Tier B events re-fire on the same
    `Validated`/`Invalidated`/`TimedOut` transitions and on
    settlement entry. `DeleteInference` is idempotent (ignores
    `ErrNotFound`), so re-emission is safe.
  - *Persisted snapshot from `storage.LoadSnapshot`*: the state machine
    is rehydrated from the snapshot's serialized `Mutable`, then only
    diffs with nonce greater than the snapshot's nonce are replayed.
    Inferences that **finished before the snapshot** are not re-tracked
    in `finishedAt` / `finishedAtTime` (those fields are host-local and
    not part of the snapshot). Consequence: Tier C will not fire for
    such inferences, but Tier A still fires on any post-snapshot
    terminal-status flip, Tier B still fires on settlement entry, and
    the epoch sweep is the final backstop. This is consistent with the
    "soft / temporary" framing of Tier C.
- **`ValidationRate` set low**: nothing changes. Validators that didn't
  pick an inference never fetch its payload, so 404s are irrelevant.
- **Hybrid storage where one backend lags**: `DeleteInference` is
  best-effort across backends; the next `Retrieve` returns whichever
  copy still exists, so a partial delete just delays the prune until
  the next opportunity. Final cleanup remains the epoch sweep.

## Tests

### `decentralized-api/payloadstorage/*_test.go`

- `TestFileStorage_DeleteInference` (store, delete, retrieve ->
  ErrNotFound; delete missing -> ErrNotFound; delete wrong epoch ->
  ErrNotFound; delete preserves other inferences in same epoch).
- Same for `PostgresStorage` and `HybridStorage`.
- `TestManagedStorage_DeleteInference_EvictsCache`: store, retrieve to
  warm cache, delete, retrieve -> ErrNotFound (proves cache eviction).

### `devshard/host/host_test.go`

- `TestHost_PruneSink_OnValidatedTerminal`: drive
  Start/ConfirmStart/Finish + sufficient `MsgValidation` votes,
  assert sink received `(escrow, id, PruneReasonTerminal)` exactly
  once after the diff that terminalized the inference.
- `TestHost_PruneSink_OnInvalidatedTerminal`: same with valid=false
  challenge path -> `StatusInvalidated`.
- `TestHost_PruneSink_OnTimeout`: drive timeout votes -> StatusTimedOut.
- `TestHost_PruneSink_OnSettlement`: drive a session where one inference
  is Finished but no validation, transition to Settlement, assert sink
  received `PruneReasonSettlement` for that ID.
- `TestHost_PruneSink_StaleFinish`: drive a session where Finish lands
  at nonce N and no validation arrives, apply enough no-op diffs to
  cross `N + validationGraceNonces`, assert sink fires
  `PruneReasonStaleFinished` exactly once and the host does not refire
  on later nonces.
- `TestHost_PruneSink_Nil`: with `WithPruneSink` unset, none of the
  above paths should panic; functional behavior unchanged.
- `TestHost_ValidateAsync_PayloadGone_NoMempoolEntry`: stub validator
  returning `ErrValidationSkipped`, assert mempool has no
  `MsgValidation` and `validating[id]` is cleared.

### `decentralized-api/internal/devshard/*_test.go`

- Integration test: bring up a `HostManager` with file storage, run
  one inference end-to-end with mock executor + validator, observe
  `DeleteInference` was called once status flipped to Validated.
- Integration test: simulate validator fetch after Tier C prune ->
  receives 404 -> `Validate` returns `ErrValidationSkipped`, no
  `MsgValidation` enters mempool.

## Alignment with session-storage design

This plan was originally written before the session-storage redesign
([storage-design.md](./storage-design.md)) landed. The two are
orthogonal: payload pruning operates only on
`decentralized-api/payloadstorage/`. The points below record the
interaction surface.

- **`ActiveSession{EscrowID, EpochID}`.** `RecoverSessions` now returns
  the per-escrow epoch directly, so the manager can pin each recovered
  `Host` to an explicit epoch instead of relying on a chain-wide phase
  tracker. Tier A/B/C events emitted during replay carry that epoch in
  `PayloadEpoch`.
- **Snapshots are session-scoped, not payload-scoped.**
  `SaveSnapshot`/`LoadSnapshot` in `devshard/storage/` persist the
  state machine's `Mutable` blob plus its nonce. The host-side maps
  (`finishedAt`, `finishedAtTime`, `payloadEpochs`,
  `skippedValidations`) are intentionally *not* persisted: they only
  drive Tier C, which is opt-in and soft. Snapshots therefore do not
  need to be regenerated for this change to land.
- **`ErrEpochPruned` on session storage** does not propagate into the
  payload-prune path. If a session's epoch has fallen behind the
  retention horizon, the session is gone, no Tier A/B/C events will
  ever fire for inferences in that epoch, and any orphan payload bytes
  are reclaimed by the payload storage's own `ManagedStorage`
  epoch sweep (which uses its own `retainCount` -- not the session
  storage's `retain=3`).
- **Hybrid stickiness** in `devshard/storage/hybrid.go` is irrelevant
  to payload pruning: payload storage has a separate hybrid backend
  with the same shape but its own routing state. `DeleteInference`
  delegates to both payload backends best-effort as described above.
- **Wiring order** for `decentralized-api` and `devshardd` is now (per
  `storage-design.md`):

  1. Create inner session storage.
  2. Run legacy session migration.
  3. Create `storage.ManagedStorage` (session).
  4. Run `HostManager.RecoverSessions`.
  5. Call `storage.ManagedStorage.Start`.

  The `payloadstorage.ManagedStorage` and the prune sink are wired
  earlier (alongside payload storage construction) and are not in this
  ordering. `NewHostManager` accepts `payloadStore` and constructs
  `payloadPruneSink` at the moment of host-manager creation, before
  step 4. Recovery in step 4 may legitimately emit prune events; they
  are idempotent and safe.

## Rollout

This is purely executor-side and produces no new wire bytes:

- No proto changes.
- No state-root changes.
- No new chain transactions.

Therefore it is safe to deploy host-by-host. A mixed cluster where some
hosts prune Tier C and others don't is fine -- validators that haven't
deployed yet keep their payloads and their validators don't get 404s;
validators that have deployed start getting 404s for the new prunes and
skip. Settlement is unaffected.

Suggested rollout:

1. PR 1: add `DeleteInference` to interface + all backends + tests.
2. PR 2: add `ErrValidationSkipped` / `ErrPayloadGone` + validator-side
   skip path. Verify mainnet validators in the wild keep working
   (executors not yet pruning, so 404s only on existing race
   conditions).
3. PR 3: add host `PruneEventSink`, manager wiring, Tier A + B.
4. PR 4: ship Tier C with conservative defaults (`10 * len(group)` and
   `2 minutes`) as a temporary mitigation, then replace it once the
   validation engine / vote flow is redesigned.

## Open questions

- Q1: Should `StateMachine` expose a `LastTerminalized() []uint64`
  helper (cheap, no allocation when empty) to avoid the host taking a
  full snapshot per diff just to detect status flips? Recommendation:
  yes, add the helper in PR 3.
- Q2: Should the host emit `PruneReasonStaleFinished` immediately on
  the diff that crosses the deadline, or batch by polling every N
  nonces? Current design: immediate (cheap O(|finishedAt|) per diff).
  Acceptable for typical session sizes (<<1000 in-flight inferences).
- Q3: `graceInferenceClear` default. Decision: `2 minutes` for now as a
  temporary mitigation. Revisit after the validation engine changes.
