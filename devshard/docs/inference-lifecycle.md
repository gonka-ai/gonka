# Inference lifecycle and state-leaning design

Design-decisions summary. The corresponding implementation plans live
in the three companion documents and are not repeated here:

- [remove-reveal-seed.md](./remove-reveal-seed.md) — RevealSeed removal.
- [inferences-pruning.md](./inferences-pruning.md) — in-memory and
  state-root pruning of inference records.
- [payload-pruning.md](./payload-pruning.md) — per-inference prompt /
  response payload pruning.

These three changes are independent in the source tree but share one
underlying observation: once the commit-reveal phase is gone, the chain
no longer needs devshard to keep inference state alive past the moment
the local validation window closes. We can therefore stop carrying
inference records in RAM, stop hashing them into the state root, and
delete their payloads.

## 1. Remove the RevealSeed phase

### What changes

`MsgRevealSeed`, `state.RevealedSeeds`, `recomputeCompliance`,
`penalizeUnrevealedSeeds`, and `allUniqueAddressesRevealed` are
removed from devshard's runtime. The `MsgRevealSeed` proto message
and its oneof slot are kept on the wire as a deprecated, inert tx so
that persisted diffs and stale-binary gossip still decode cleanly.
`PhaseFinalizing → PhaseSettlement` becomes deadline-only:

```
deadlinePassed := LatestNonce >= FinalizeNonce + uint64(len(group))
```

The `DeriveSeed` / `ShouldValidate(ownSeed, …)` path is **kept**: each
host still picks its own subset of inferences to validate using a
locally derived seed. Only the *reveal* half goes away.

### Why this is safe

The reveal path fed two settlement fields on
`DevshardSettlementHostStats` — `RequiredValidations` and
`CompletedValidations`. These propagate into `DevshardHostEpochStats`
on the chain side and are **never read** by any chain code: no
slashing, no reward adjustment, no downtime check, no eviction. The
chain's slashing logic uses `MissedRequests` (executor failures) and
participant status. So the entire commit-reveal cycle was bookkeeping
that cost gas on every session and changed nothing observable
on chain.

Both fields stay on `HostStats`/`HostStatsProto` and are sent as
zero, keeping the wire format bit-identical with
`DevshardSettlementHostStats`. No chain code changes.

### Operational consequences

- Coordinated upgrade, not rolling. Mixed binaries in one group
  diverge on state root and fail to assemble a settlement quorum.
- Sessions are not resumable across the cutover: removing
  `RevealedSeeds` from the state hash preimage changes the state
  root, and pre-upgrade persisted snapshots
  (`storage.SaveSnapshot` from `storage-design.md`) are not
  forward-compatible. New sessions only.
- Loss of compliance observability: the existing chain query keeps
  returning `RequiredValidations` / `CompletedValidations` constantly
  at zero.
- Host-local **validation observability** (outside the state root, exposed
  on `GET /v1/devshard/stats/shards/{escrow_id}`) is populated when signed
  diffs are applied; see
  [validation-observability-diff-apply.md](./validation-observability-diff-apply.md).

## 2. Drop inference records from RAM and state

### What changes

`Mutable.Inferences` no longer accumulates records for the lifetime of
a session. A record is evicted from RAM the moment the host fires its
prune trigger for that inference (see §4 for the trigger). The
diff log remains the canonical source of truth, so any record can be
rematerialized from disk if a late, post-eviction tx legitimately
needs it.

A small, durable per-inference index is maintained for the *only*
protocol path that may legitimately fire after eviction: the dedup /
verdict update in `applyValidation` for a late `MsgValidation`. This
index is a new `inferences` table inside the existing devshard
**session storage** (`devshard/storage/`, partitioned by `epoch_id`
alongside diffs and signatures — see
[storage-design.md](./storage-design.md)). It is host-local
bookkeeping derived from the diff log, not part of the state root.
Every other apply path resolves a missing record from a 32-byte
in-memory commit-hash discriminator (which doubles as the input to
the state-root hash) and either continues, rejects, or no-ops
without touching disk.

This is delivered in two phases:

- **Phase 0** — host-local. RAM is pruned. The per-inference durable
  index is added. The state root is **byte-identical** to today,
  because the same record content is fed into the inference hash via
  the commit-hash cache. No session-version bump.
- **Phase 1** — chain-coordinated. The inference hash composition is
  changed so it is computed from a rolling sealed accumulator plus
  the (now bounded) live set, instead of a full materialization of
  every record ever issued. Late `MsgValidation` becomes a
  deterministic reject. Requires a session-version bump and
  governance allowlisting of the new binary.

### Why this is safe

After an inference reaches a terminal status
(`StatusValidated` / `StatusInvalidated` / `StatusTimedOut`), the
only remaining protocol use of its in-memory record is `applyValidation`'s
late-dedup. Late `MsgValidationVote` is already a silent no-op.
Validation throughput is bounded by `ValidationRate`, and
post-terminal arrivals are even rarer (gossip catch-up only), so
hydrating that one cold-path tx from disk is acceptable.

Phase 0 keeps the state root unchanged by feeding the same
`InferenceRecord` bytes (via 32-byte commits) into the same hash
composition. Phase 1 changes the composition deliberately, under a
new version tag, with the chain treating `rest_hash` as opaque and
relying on the 2/3+1 host signature over `state_root` for
authenticity.

### Operational consequences

- Steady-state RAM is bounded by the **live** set (in-flight +
  in-grace), not by the lifetime inference count. State-root
  recomputation also walks only the live set plus a 32-byte commit
  per ever-issued id.
- Recovery treats the durable index as derived data: cold start
  truncates and rebuilds it from the diff log; snapshot-based recovery
  loads the live set and trusts the index for sealed rows.
- Phase 1 requires a coordinated rollout (binary version v2 plus
  chain-side allowlist) and pins composition per-binary, not
  per-session.

## 3. Delete inference payloads when they become unreachable

### What changes

`PayloadStorage` gains a per-inference `DeleteInference` operation
alongside the existing epoch-granular `PruneEpoch`. The host emits a
prune event over a small callback interface
(`PruneEventSink.OnInferencePrunable(InferencePruneEvent)`); the
adapter inside `decentralized-api` translates each event into a
`DeleteInference` call under the host's pinned escrow epoch (epoch
`PruneEpoch` sweep is the backstop for orphans). A validator that fetches a payload after it
has been deleted receives an HTTP 404 from the executor and **skips
silently**: no `MsgValidation`, no challenge, no failure record.

### Why this is safe

Validation feedback is invisible to the chain after the RevealSeed
removal (§1). A validator that arrives too late to find the payload
has no on-chain consequence whether it fails, retries, or skips, so
"skip and log" is the cheapest correct behavior. The host-side prune
events are emitted only after a diff has been durably applied, so we
never delete a payload speculatively. The pre-existing epoch sweep in
`payloadstorage.ManagedStorage` remains as a backstop for orphans
(e.g. a host crash between `Store` and the prune callback).

### Operational consequences

- Per-inference deletions reduce payload disk / Postgres pressure to
  the active validation window rather than the full epoch retention
  horizon.
- The change is purely executor-side. No proto bumps, no new chain
  txs, no state-root impact. It can be deployed host-by-host: hosts
  that have not upgraded still keep their payloads and their
  validators still see the same payloads as before.

## 4. Pruning triggers — when an inference is dropped

The same set of triggers drives both the in-RAM eviction (§2) and the
payload deletion (§3). They are evaluated by the host inside
`applyAndPersist`, after the diff has been applied and persisted, so
no host can emit a prune for state that did not durably land.

### Trigger A — terminal status

An applied diff transitions an inference to
`StatusValidated`, `StatusInvalidated`, or `StatusTimedOut`. There is
no further protocol path that needs either the inference record or
its payload. Fired by:

- `applyValidation` flipping `Status` from `Finished` / `Challenged`
  to `Validated` / `Invalidated`,
- `applyTimeoutInference` flipping to `TimedOut`.

### Trigger B — settlement entry

`PhaseFinalizing → PhaseSettlement` transitions (deadline-only after
§1). At that point no further `MsgValidation`, `MsgChallenge`, or
`MsgValidationVote` will be accepted by the state machine. The host
emits one bulk prune for every inference still in `StatusFinished` or
`StatusChallenged`.

### Trigger C — stale-finished grace (temporary)

An inference that finished during the Active phase but was never
touched by a validation, challenge, or timeout. The host evicts it
only when **both** of:

- enough nonces have passed since `MsgFinishInference`
  (`InferenceSealGraceNonces`, governance-pinned at session create, default
  `10 * len(group)` with floor 20), and
- enough wall-clock time has passed since the host observed that
  finish (`InferenceSealGraceSeconds`, governance-pinned at session
  create, default `3600` seconds / 1 hour).

A two-gate is needed because nonce growth is traffic-dependent — at
high throughput a nonce-only gate collapses to near-zero wall-clock
time, which is unsafe in the face of late validators or
challenge-induced voting that may still need the payload to verify.
A wall-clock-only gate is unsafe in the opposite direction at low
traffic. Both must pass.

**Trigger C is explicitly a temporary measure.** It is the only
trigger that prunes an inference whose validation outcome is still
formally undecided. It is justified today only because the chain
does not act on missed validations (§1) and the validation protocol
treats every host's verdict as advisory. The intent is to **redesign
the validation protocol** so that an inference's payload retention
requirement is bounded by a protocol-visible event (e.g. an explicit
"validation window closed" message or an on-chain commitment), at
which point Trigger C disappears.

Under v2 composition, **Trigger A** uses the same nonce and
wall-clock gates before sealing and pruning post-terminal inferences
(delayed seal after terminal). The grace values are frozen in
`SessionConfig` at session create (from chain `DevshardEscrowParams`
via `GetEscrow`) and do not feed into the state root, so adjusting
governance defaults only affects newly created sessions.

## 5. Cross-cutting consequences

- **Validator behavior on missing payload.** A 404 from the executor
  is mapped to a sentinel skip both at the validator side and at the
  host's `validateAsync` loop, so the host produces no
  `MsgValidation` and the validator does not retry the same id.
- **Diff replay on recovery.** The diff log is canonical and is
  replayed on cold start. Tier A and B events naturally re-fire on
  the same terminal transitions and settlement entry. Tier C's
  bookkeeping is host-local and not persisted, so inferences that
  finished before a snapshot are not Tier-C-eligible after recovery
  from that snapshot — Tier A, Tier B, and the epoch sweep remain
  the backstops.
- **Storage layering.** Inference payload storage
  (`decentralized-api/payloadstorage/`) and devshard session storage
  (`devshard/storage/`, see [storage-design.md](./storage-design.md))
  remain independent subsystems with separate retention policies and
  separate backends, but a prune trigger now writes to both at the
  same point in time:
  - **payload storage** loses the prompt / response bytes via
    `DeleteInference`,
  - **session storage** gains exactly one row in the new sealed
    `inferences` table (write-once at seal, single-row update on a
    late `MsgValidation`), and loses the live `Mutable.Inferences`
    entry from any future snapshot.
  Diffs and signatures in session storage are untouched. Epoch-level
  partition drops in either subsystem remain the long-tail
  reclamation path for everything else.
- **Wire compatibility.** No proto field is renumbered, removed, or
  reinterpreted by §1 or §3. §2 Phase 1 is gated by a binary
  version tag that the chain allowlists; v1 and v2 share the same
  opaque-`rest_hash` settlement shape, so chain code does not branch
  on the tag.
