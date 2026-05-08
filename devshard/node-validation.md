# Devshard validation: invalidation never resolves

## Symptom

After running ~5000 inferences for 12+ hours against a 16-host group with
mostly-reachable validators, individual inferences reach `Challenged` and stop.
`votes_invalid` stays equal to the first challenger's slot weight (e.g. 3),
`votes_valid` stays at 0, and no inference ever transitions to `Invalidated`
or `Validated`. As a consequence, `host_stats[X].Invalid` stays at 0 even when
many inferences from a single executor are visibly bad.

## Cause

Phase 1 weight accumulation in `applyValidation` is gated on `Finished`
status only (`devshard/state/machine.go:743-757`):

```go
if rec.Status == types.StatusFinished {
    weight := sm.addressToSlotCount[validatorAddr]
    if msg.Valid {
        rec.VotesValid += weight
    } else {
        rec.VotesInvalid += weight
        rec.Status = types.StatusChallenged
    }
}
```

The first `MsgValidation` with `valid=false` flips status to `Challenged`.
Every subsequent `MsgValidation` for the same inference still passes dedup,
still sets the bit in `ValidatedBy`, but the `if rec.Status == StatusFinished`
gate is now closed, so the weight is never added.

Phase 2 weight accumulation lives in `applyValidationVote`
(`devshard/state/machine.go:790`), which is gated on `Challenged` and is the
only path that can transition to `Validated`/`Invalidated`. It works
correctly when invoked. The problem is that nothing invokes it: no
production code path constructs `MsgValidationVote`.

## Audit confirming no producer

Searches run against the working tree:

- `grep -rn "MsgValidationVote\b"` over `devshard/`, `decentralized-api/`,
  `inference-chain/`. Non-test, non-generated matches are all consumers:
  - `devshard/state/machine.go:539,540,790,816` (apply / type-switch)
  - `devshard/user/user.go:172,750,1014,1061,1062,1082` (dedup key, classifier)
  - `devshard/cmd/devshardctl/proxy.go:528,529,631` (debug listing)
- `grep -rn "DevshardTx{Tx:"` over the same directories. Every producer
  builds one of: `StartInference`, `ConfirmStart`, `FinishInference`,
  `RevealSeed`, `Validation`, `FinalizeRound`, `TimeoutInference`. None
  build `ValidationVote`.
- `Host.validateAsync` (`devshard/host/host.go:715-761`) is the sole
  validation-tx emitter. It unconditionally builds `MsgValidation`; there
  is no branch that switches on the inference's current status.
- `ValidationEngine` (`devshard/engine.go:11-15`) is the validator
  contract; its `Validate` returns `*ValidateResult{Valid bool}` only.
  All three implementations (`decentralized-api/internal/devshard/validation.go`,
  `decentralized-api/cmd/devshardd/validation.go`, `devshard/stub/engine.go`)
  return `Valid bool` and nothing more, so no caller can distinguish
  pre-challenge vs post-challenge to choose between the two message types.
- `inference-chain/x/inference/epochgroup/voting.go` and
  `keeper/msg_server_validation.go` reference `StartValidationVote` /
  `startValidationVoteWithPolicy`. These belong to the legacy
  `x/inference` cosmos module's governance flow and are unrelated to
  devshard's per-session `MsgValidationVote`.
- The only constructors of `MsgValidationVote{...}` or
  `DevshardTx_ValidationVote{...}` live in `devshard/state/machine_test.go`
  and `devshard/storage/sqlite_test.go`.

The state machine, proto, and design docs (`proposals/inference/design.md:38,
169-170,183`) all expect `MsgValidationVote` to exist as a host-proposed
Phase-2 tx. The host code does not implement that emission. Either it was
deferred during the initial devshard rollout or removed in a refactor and
never reinstated.

## Why the existing fixes do not help

- Network-side fetching (background mempool poller, explicit
  `/v1/debug/mempool/fetch`, gossip recovery) only changes when
  `MsgValidation` reaches the user. Once it reaches the state machine,
  the `Finished`-only gate drops it. Faster delivery cannot resolve a
  challenge.
- Aggregating from multiple validators does not help either: each
  additional `MsgValidation` flips the same gate, so only the first one
  contributes weight.

## Fix options

(A) State-machine fix, one file. In `applyValidation`, after the
`Finished` branch, fall through to a `Challenged` branch that performs
the same slot-weighted tally and threshold check as
`applyValidationVote`. Treats `MsgValidation` as the universal
validation tx; `MsgValidationVote` becomes redundant. Smallest change;
every host running this code converges. Cost: blurs the proto's
two-phase distinction.

(B) Host fix, matches design intent. In `Host.collectValidationJobs`
(or in `validateAsync` before emission), read the current inference
status. Emit `MsgValidation` when `Finished`, emit `MsgValidationVote`
when `Challenged`. Larger change: requires a vote-content sig, status
re-check on emission to handle the race where the inference becomes
`Challenged` between job creation and emission, and the validator
already produced a `Valid bool` result that maps cleanly to either
message. Honors the existing two-phase protocol.

Either fix is sufficient on its own. (A) is the unblock. (B) is the
final shape if the two-phase distinction is wanted.

## Verification

Once a fix is applied, the same operational data that failed today
should show `votes_invalid` and `votes_valid` continuing to grow past
the first challenger's weight, threshold being crossed (`> VoteThreshold`,
default `total_slots/2`), inferences moving to `Invalidated`/`Validated`,
and `host_stats[X].Invalid` incrementing for misbehaving executors.
The two new INFO logs added for instrumentation are sufficient signals:

- `validation applied (valid|post-challenge)` in `applyValidation`
- `validation vote applied` in `applyValidationVote` (option A makes
  this redundant; option B keeps it as the Phase-2 path)
