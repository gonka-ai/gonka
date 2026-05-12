# Remove RevealSeed phase and commit-reveal compliance

> **Scope note.** Removing `RevealedSeeds` changes the shape of the
> state machine's `Mutable` struct, which is also the payload of
> `storage.SaveSnapshot` / `storage.LoadSnapshot` introduced by the
> session-storage redesign (see [storage-design.md](./storage-design.md)).
> Persisted snapshots taken with the old `Mutable` shape are not
> forward-compatible with this change. The migration section below
> spells out the operational consequence.

## Motivation

The `MsgRevealSeed` transaction, the `state.RevealedSeeds` field, and the
associated `recomputeCompliance` / `penalizeUnrevealedSeeds` machinery are
inert today. The path they feed --
`HostStats.RequiredValidations` / `HostStats.CompletedValidations` --
travels into `SettlementPayload`, is aggregated on `inference-chain` into
`DevshardHostEpochStats`, and is then **never read** by any chain code:
no slashing, no reward adjustment, no downtime check, no eviction. The
chain's slashing logic uses `MissedRequests` (executor failures) and
participant status, not validation-compliance counters.

So the entire commit-reveal cycle costs gas on every devshard session,
adds wire surface, and changes nothing observable on chain. The proposal
is to remove it from devshard's runtime while keeping the chain proto
schema unchanged.

## Goals

1. Remove `MsgRevealSeed` and the commit-reveal compliance machinery from
   devshard's runtime path.
2. Keep per-host probabilistic validator self-selection
   (`ShouldValidate(h.ownSeed, ...)`) -- each host continues to pick which
   inferences to validate using its own locally-derived seed.
3. Keep the chain-side proto schema (`DevshardSettlementHostStats`,
   `DevshardHostEpochStats`) unchanged. `RequiredValidations` and
   `CompletedValidations` remain on the wire; devshard always sends `0`.
4. Simplify `PhaseFinalizing -> PhaseSettlement` to a deadline-only
   transition (`LatestNonce >= FinalizeNonce + len(group)`).

## Non-goals

- No new slashing for skipped validations. Validators who fail to validate
  the inferences their own seed selected remain invisible to the protocol.
- No changes to verdict recording. `MsgValidation`, `HostStats.Invalid`,
  and the existing "executor marked invalid" path stay as they are.
- No changes to `MsgFinalizeRound` or the Active -> Finalizing transition.
- No removal of `DeriveSeed` or `ShouldValidate`. Only the reveal half
  goes away.

## Design decisions

### D1. Wire compatibility for `MsgRevealSeed`

**Decision: deprecate-in-place (option C).** Keep the `MsgRevealSeed`
proto message and its `reveal_seed = 7` slot in the `DevshardTx` oneof.
Replace `applyRevealSeed` with a no-op that returns `nil` and emits a
debug log. Hosts stop emitting it via `maybeRevealSeed` removal.

Rationale:

- Persisted diffs in sqlite/memory storage that contain a `RevealSeed`
  still decode and replay cleanly (the tx becomes inert, but doesn't
  break decoding or state-machine dispatch).
- A stale host on the old binary that still gossips `MsgRevealSeed` can
  be sequenced into a diff; the new state machine treats it as a no-op.
- Smallest blast radius.

Alternatives considered:

- **(A) Hard delete + `reserved 7;`** -- cleanest schema but breaks
  decoding of any persisted diff that contains the tx.
- **(B) Reset all storage** -- pre-prod-only workable; doesn't apply
  because devshard runs against live storage in test environments.

A future hard-delete can land once we are confident no persistent
storage or live mempool carries the tx.

### D2. Chain-side proto fields

`RequiredValidations` and `CompletedValidations` stay on both
`DevshardSettlementHostStats` and `DevshardHostEpochStats`. Devshard
always sends `0` for both. The chain's aggregation
(`UpdateDevshardHostEpochStats`) keeps summing zeros (harmless). The
query endpoint `DevshardHostEpochStats` keeps returning the fields,
constantly at zero post-upgrade. **No chain code changes.**

### D3. Validator self-selection (option 1)

`h.ownSeed = DeriveSeed(Sign(EscrowID))` stays. `ShouldValidate(h.ownSeed, ...)`
stays. Each host independently picks ~`ValidationRate%` of inferences to
validate. Honest hosts behave identically to today. Skipped validations
are invisible to the protocol -- which they already are, in effect, since
the chain ignores compliance counters.

### D4. `PhaseFinalizing -> PhaseSettlement` transition

Today:

```
if allRevealed || deadlinePassed {
    sm.state.Phase = types.PhaseSettlement
}
```

After:

```
if deadlinePassed {
    sm.state.Phase = types.PhaseSettlement
}
```

where `deadlinePassed = LatestNonce >= FinalizeNonce + uint64(len(group))`.

Finalization always lasts exactly `len(group)` nonces after
`MsgFinalizeRound`. Marginal latency cost vs. the previous fast-path
when all hosts revealed early; in practice this is one round-robin pass.

### D5. `HostStats` struct shape

Keep `RequiredValidations` and `CompletedValidations` as fields on
devshard's `types.HostStats` and `types.HostStatsProto`. Both always
zero post-change. Reason: keep the proto-serialised `HostStatsProto`
wire-compatible with `DevshardSettlementHostStats` on the chain side, so
`BuildSettlement` -> `VerifySettlement` doesn't need a translator step.

Side effect: state-root values for any test that previously exercised
finalization will change, because `recomputeCompliance` no longer
populates non-zero values into the hash preimage. Golden values in
`state/machine_test.go` need refreshing.

## File-by-file change list

### Proto layer

- `proto/devshard/v1/tx.proto` -- keep `message MsgRevealSeed`, add a
  `// Deprecated:` comment.
- `proto/devshard/v1/diff.proto` -- keep `MsgRevealSeed reveal_seed = 7;`
  in the `DevshardTx` oneof, add a `// Deprecated:` comment.
- No regen needed.

### State machine (`devshard/state/`)

- `machine.go`:
  - `applyRevealSeed`: replace the body with `return nil` and a debug
    log noting the tx is deprecated. Keep the dispatch entry
    (`case *types.DevshardTx_RevealSeed: return sm.applyRevealSeed(...)`).
  - Delete `recomputeCompliance` (lines 959-1001).
  - Delete `penalizeUnrevealedSeeds` (lines 1003-1045).
  - Delete `allUniqueAddressesRevealed` (lines 1055-1063).
  - Remove the `recomputeCompliance()` / `penalizeUnrevealedSeeds()`
    calls in `ApplyDiff` (lines 277-278) and the local-apply path
    (lines 357-358).
  - Change finalize-phase gating (lines 279-283 and 361-365) from
    `if allRevealed || deadlinePassed` to `if deadlinePassed`.
  - Drop `RevealedSeeds` initialization in `NewStateMachine` (line 157).
  - Drop `RevealedSeeds` from the `Mutable` struct and from the
    in-memory rollback path `snapshotMutable` / `restoreMutable`
    (`devshard/state/machine.go` ~line 452-454, 475, 488-489, 502,
    515). This also removes it from anything in `Mutable` that gets
    serialized into a `storage.SaveSnapshot` payload (see "Migration
    / rollout" below).
  - Drop the `RevealedSlots` accessor (~line 1185, 1204-1208) and the
    `IsSlotRevealed` accessor (~line 1185), and remove their callers
    in `host.go` / `user.go`.
- `validation.go`: **no changes.** `DeriveSeed` and `ShouldValidate`
  remain.
- `types/domain.go`: remove `RevealedSeeds map[uint32]int64` from
  `EscrowState` (line 88).

### Host (`devshard/host/host.go`)

- Delete `maybeRevealSeed` and the helper that builds `MsgRevealSeed`
  and adds it to mempool.
- Delete the `h.maybeRevealSeed()` call in `HandleRequest` (step `(d)`,
  line 295).
- Keep the `ownSeed` field and `DeriveSeed(seedSig)` in `NewHost`.
- Keep `ShouldValidate(h.ownSeed, ...)` in `collectValidationJobs`.
- Update the `Host` struct doc comment to drop reveal references.

### User sequencer (`devshard/user/`)

- `user.go`:
  - Update the "Phase A+1 drains MsgRevealSeed" comment block (around
    line 422-424); the drain phase still runs for
    Confirm/Finish/Validation, just no longer for RevealSeed.
  - Keep the `*types.DevshardTx_RevealSeed` dedup case (line 509-510) as
    defensive code -- stale reveals from an old-binary host should still
    dedup cleanly.
  - Verify `txPriority` does not special-case RevealSeed (if it does,
    no action; the priority is harmless).

### devshardctl (`cmd/devshardctl/proxy.go`)

- Keep the `case *types.DevshardTx_RevealSeed` arm in the tx-type switch
  (line 426-427); harmless and keeps logging robust against stale
  reveals.

### Tests

- `state/machine_test.go`:
  - Remove or rewrite tests that assert on `RevealedSeeds`,
    `RequiredValidations`, `CompletedValidations`,
    `allUniqueAddressesRevealed`, `recomputeCompliance`, or
    `penalizeUnrevealedSeeds`.
  - Refresh golden state-hash values that hit the finalization phase.
  - Update finalization-flow tests: instead of "fast settle on full
    reveal," advance exactly `len(group)` nonces past `FinalizeRound`
    to reach `PhaseSettlement`.
  - Add `TestStateMachine_FinalizationDeadlineOnly`: apply
    `MsgFinalizeRound`; `len(group) - 1` empty diffs leave the SM in
    `PhaseFinalizing`; one more transitions to `PhaseSettlement`.
- `state/settlement_test.go`: refresh hash goldens.
- `host/host_test.go`: drop tests that produce or assert
  `MsgRevealSeed`.
- `user/user_test.go`, `user/fault_test.go`, `user/stress_test.go`,
  `user/warmkey_test.go`, `protocol/protocol_test.go`,
  `protocol/http_test.go`: search-and-fix RevealSeed references.
- `storage/sqlite_test.go`, `storage/postgres_test.go`,
  `storage/managed_test.go`: verify with the deprecate-in-place choice
  that persisted-then-replayed diffs containing `RevealSeed` still
  apply cleanly as no-ops, that `SaveSnapshot` / `LoadSnapshot` either
  rejects an old-format snapshot with the new sentinel or has been
  bumped past the old schema, and that `RecoverSessions` over
  `[]ActiveSession{EscrowID, EpochID}` still produces a green state
  machine after the change.
- `internal/testutil/testutil.go`: delete `SignRevealSeed` once all
  callers are gone.

### Chain side

**No code changes.** Optionally add a comment in
`inference-chain/x/inference/types/devshard_settlement.proto` noting
that `required_validations` / `completed_validations` are always zero
for devshard runtimes at or after this change.

### Docs

- `devshard/docs/attacks.md`:
  - Revise "Seed grinding via signature malleability" -- the threat
    surface narrows. `ownSeed` is now host-internal only, used for
    `ShouldValidate`; the workload it selects does not affect rewards
    in any chain-observable way, so the incentive to grind disappears.
  - Cross-link mempool gossip DoS as the remaining finalization-phase
    safety brake.
- Annotate any reveal-related entries under `proposals/inference/` as
  deprecated.

## Migration / rollout

- **Coordinated upgrade required, not rolling.** New-binary hosts stop
  emitting `MsgRevealSeed` and remove `RevealedSeeds` from the state.
  Old-binary hosts in the same group still apply reveals and update
  `RevealedSeeds`. The state root diverges. Mixed groups will not
  agree on state and will fail to gather a settlement quorum.
- **Sessions cannot be resumed across the version cutover.** Persisted
  diffs decode fine (deprecate-in-place keeps the wire schema), but the
  state hash computed on the new binary will not match the hash
  computed on the old binary because `RevealedSeeds` /
  `RequiredValidations` / `CompletedValidations` are no longer part of
  the preimage. New sessions only after upgrade.
- **Persisted state snapshots are not forward-compatible.** The
  session-storage redesign ([storage-design.md](./storage-design.md))
  introduced `storage.SaveSnapshot` / `storage.LoadSnapshot` for state
  machine recovery. A snapshot written before this change encodes a
  `Mutable` blob whose serialization assumes the `RevealedSeeds` field
  exists. After the change there are two acceptable strategies, pick
  one per the chosen serialization format:
  - *Schema-bump strategy* (preferred): tag snapshots with a version
    byte; `LoadSnapshot` returns `ErrSnapshotIncompatible` for older
    versions, the manager logs and falls back to full diff replay from
    `LastFinalized`.
  - *Hard-cut strategy*: delete `_meta`-tracked snapshot rows / files
    on first boot of the new binary; recover via diff replay only.
  Either way, recovery via diff replay still works because diffs are
  the source of truth, and persisted `MsgRevealSeed` diffs decode and
  apply as no-ops per D1.
- **Recovery flow** under the new storage design:
  1. Inner session storage created.
  2. Legacy migration runs (no-op for `MsgRevealSeed` diffs after this
     change).
  3. `storage.ManagedStorage` is created.
  4. `HostManager.RecoverSessions` iterates
     `[]storage.ActiveSession{EscrowID, EpochID}`. For each session it
     attempts `LoadSnapshot`; if absent or incompatible, it replays
     diffs from nonce 1.
  5. `ManagedStorage.Start` begins the background pruner.
  The deadline-only `Finalizing -> Settlement` transition (D4) is
  reached during this replay if appropriate, with no special handling.

## Risks

1. **State-hash skew during deploy** -- mitigated by coordinated
   upgrade.
2. **Test maintenance load** -- moderate. Golden-hash refreshes and
   finalization-flow rewrites dominate the diff. The new
   `storage/sqlite_test.go` and `storage/managed_test.go` paths under
   the redesigned `devshard/storage/` also need a pass to confirm
   reveals-as-no-op replay works (this is the explicit acceptance
   criterion for D1).
3. **Latent assumptions in `user.go` finalize-loop** -- verify the
   user-side phase loop terminates correctly with deadline-only
   transition. Trace explicitly during implementation.
4. **Loss of compliance observability** -- `DevshardHostEpochStats`
   query returns flat zeros for the two compliance fields. Document.
5. **Snapshot incompatibility** -- pre-upgrade snapshots in the
   session storage's snapshot tables/files are not loadable. Either
   schema-bump or delete on first boot (see Migration / rollout).
   Diff replay continues to work because `MsgRevealSeed` is kept on
   the wire as a no-op.

## Open items to confirm before implementation

1. Confirm **D1 = deprecate-in-place (C)**. Recommendation stands.
2. Confirm **D5 = keep zeroed compliance fields on devshard's
   `HostStatsProto`** so the chain wire stays bit-identical. Otherwise
   we need a translator layer in `BuildSettlement`.
3. Confirm that a stray `MsgRevealSeed` arriving in the mempool from an
   old-binary host should be silently sequenced and applied as a no-op
   (no error, no log spam).

If unanswered, proceed with the recommended defaults above.

## Phasing

Single PR.

PR title: **devshard: remove reveal-seed commit-reveal compliance**

PR body outline:

- Why: chain doesn't act on `RequiredValidations` / `CompletedValidations`;
  the entire commit-reveal machinery is bookkeeping.
- What stays: `DeriveSeed`, `ShouldValidate(ownSeed, ...)` -- hosts still
  self-select validation workload.
- What goes: `MsgRevealSeed` (deprecated, not deleted), `RevealedSeeds`,
  `recomputeCompliance`, `penalizeUnrevealedSeeds`,
  `allUniqueAddressesRevealed`, host `maybeRevealSeed`.
- Deploy: coordinated upgrade; no resumable sessions across the cutover.
