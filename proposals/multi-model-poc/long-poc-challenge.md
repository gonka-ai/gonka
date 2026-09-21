# PoCChallenge v1

## Goal

A governance-approved address can force one participant to generate PoC from the challenge start until the safety window before the next regular PoC. The target is removed from inference for that interval. All of its current-epoch confirmation-weight hardware generates PoC instead.

The challenger locks payment `P` in the inference module account. If the target passes, it receives `P`. If the target fails, `P` is returned to the challenger and the target is punished the same way as a failed Confirmation PoC.

This first version stores one commit stream per segment, not per-slice pages. That cuts chain storage. Slices are [not implemented].

## Create

Governance params (`PoCChallengeParams`):

- `allowed_challengers`: empty keeps create off
- `payment_ratio` (`k` in `[0, 1]`)
- `max_active_challenges`
- `min_punishable_segment_blocks` (0 means 300)

The safety window starts at `nextPoCStart - EpochParams.confirmation_poc_safety_window`. Create is rejected from that height. The last segment seals there.

Create is allowed during inference, outside Confirmation PoC, and before the safety window. Confirmation PoC alpha must be set. Create is also rejected while a Confirmation PoC event is still stored, including `COMPLETED`.

The create transaction carries only the target address.

The handler:

1. Rejects if the sender is not on the whitelist.
2. The target must be in the current-epoch live set, have status `ACTIVE`, be distinct from the sender, have no live challenge, and have no current or scheduled maintenance before the next epoch switch.
3. The total active challenges must be less than `max_active_challenges`.
4. Computes `E_full` = target current-epoch weight / total current-epoch weight * current fixed epoch Bitcoin-style reward. This is the gross share, before downtime and Confirmation PoC reductions.
5. Computes `scale` = blocks from now to the safety window / blocks from this epoch's `SetNewValidators` to the safety window.
6. Freezes `E = floor(E_full * scale)` and `P = floor(k * E)`. Rejects if `P = 0`.
7. Sets `start_height` to the next block. Remaining blocks to the safety window must be at least `min_punishable_segment_blocks`.
8. Moves `P` from the challenger to the inference module account. If the transfer fails, the challenge is not created.
9. Stores epoch, challenger, target, `E`, `P`, `start_height`, and `seed` = previous-block hash at create (`HeaderInfo().Hash`).

Challenges are final once created. Active challenges remain valid if the sender is removed from the whitelist.

## Generate

The target generates from `start_height` on every MLNode that currently contributes to its confirmation weight. Artifacts are stored off-chain.

An accepted commit must come from the target and have a count greater than the last accepted count for that model. `PocStageStartBlockHeight` is the current segment `start_height`.

New inference assignments stop at `start_height`. Missed inferences are waived while the challenge is open. Maintenance is disabled for the target during the challenge.

DAPI uses the challenge seed while the target's own challenge is generating. The target does not generate Confirmation PoC. It still takes every Confirmation PoC validation window. It votes other participants' Confirmation PoC. Everyone including the target votes open challenges. Chain skip is scoring only. A same-epoch challenge record is not scored in Confirmation PoC (`ConfirmationWeight` / `ConfirmationPoCRatio` / `INACTIVE` from that event).

At the safety window, `generating` is false. The target joins regular PoC N+1 like everyone else. It generates, validates, and has no challenge overlay. Regular PoC preserved sampling is not stripped. The challenge record is paid later at `SetNewValidators`.

## Segments

A segment is the unit that can fail the challenge. It runs from one validation to the next. A segment at least `min_punishable_segment_blocks` is voted.

Each segment is a PoC stage with its own `start_height`. That height is `PocStageStartBlockHeight` for commits, votes, seed, and DAPI.

- The first segment's `start_height` is the next block after create.
- While a same-epoch Confirmation PoC event is not `COMPLETED`, `ChallengeFinish` is `exchangeEnd + 1`.
- Evaluate runs at Confirmation PoC `COMPLETED`, after confirmation weights. If `duration < min_punishable_segment_blocks`, the segment is not voted and rotates when more time remains.
- The next segment's `start_height` is the complete-block height, if that height is still before the safety window. New seed, new commits, new votes.
- The last segment is evaluated at regular PoC validation (`EndOfPoCValidation`), before `SettleAccounts`.

The seed for a new rotated segment is the hash of the previous block at rotate. DAPI callbacks require the artifact `block_height` to match the active segment's `start_height`.

## Validate

On each Confirmation PoC validation, and at regular PoC N+1 validation, DAPI runs `ValidateAll` then `ValidateOpenChallenges`. The challenged target is not excluded from either pass.

Validators vote the challenge with the same Confirmation PoC path. Votes are accepted after the segment finish until evaluation.

Each punishable segment updates `ConfirmationWeight` and `ConfirmationPoCRatio` the same way Confirmation PoC does. Duration is scaled against `pocStageDuration + pocExchangeDuration`. A passing segment can still lower those values.

A failed segment goes through the Confirmation PoC status path and sets `INACTIVE` exactly when a failed Confirmation PoC would. Generation stops.

An evaluation error or unrelated leave marks `ABORTED`. That is not a challenge-caused failure.

## Result

The record stays until `PayAndDeleteOldChallenges` at `SetNewValidators` of the next epoch.

It passes if every voted segment passed. A segment shorter than `min_punishable_segment_blocks` is not voted.

The challenge fails if a punishable segment fails the weight check. `ABORTED` does not set `INACTIVE`.

A failure in any segment immediately terminates the challenge.

## Pay

Create freezes `E` and `P`. Payout is `PayAndDeleteOldChallenges` at `SetNewValidators`, after epoch-N `SettleAccounts`. A payout error keeps the record for retry. It does not roll back settlement.

Pass (`UNSET`): vest `P` to the target.

Fail (`CHALLENGE_FAILED`) and abort (`ABORTED`): refund `P` to the challenger.

[not implemented] vest `min(E, forfeited reward)` to the challenger on a target-caused fail.

## Timeline

```
[Regular PoC N] -> [Inference] -> [cPoC gen][cPoC val] -> [Inference] -> ... -> [Regular PoC N+1] -> [SettleAccounts] -> [SetNewValidators / pay]
                   create OK       generate challenge;     create OK             generate+validate
                                   vote others' cPoC                             usual PoC
                                                                                 (safety already on)
PoCChallenge:
                   [create][segment ........][evaluate][rotate][segment ...][last evaluate at PoC N+1 val][pay at flip]
```

## Flow

```
Whitelist sender
  --> lock P = k * E
  --> target leaves inference routing, generates PoC
  --> commit count+root on the current segment
  --> Confirmation PoC generate: keep the challenge seed; do not generate cPoC
  --> Confirmation PoC validate: vote others' cPoC and open challenges
  --> Confirmation PoC COMPLETED: evaluate the clipped segment; rotate if still before safety
  --> safety window: leave challenge generation
  --> Regular PoC N+1: generate and validate as usual
  --> last punishable segment evaluated during that PoC N+1 validation
  --> SetNewValidators pays P
        |
        +--> fail --> ConfirmationWeight / ConfirmationPoCRatio; INACTIVE; refund P
        +--> abort --> refund P; no INACTIVE
        +--> pass --> vest P to the target
```

## State

One record keyed by target: epoch, challenger, target, `start_height`, seed, `E`, `P`, `failure_kind`.

The chain stores segment commits and validations. They are deleted on rotate, abort, fail, or pay.

## Rules

- Each target can have at most one active challenge.
- Network-wide active challenges are capped at `max_active_challenges`.
- Create only during inference, not during Confirmation PoC, and outside the safety window. Remaining time must be at least `min_punishable_segment_blocks`.
- Any same-epoch challenge record is skipped from Confirmation PoC scoring, not from Confirmation PoC voting.
- The target joins regular PoC N+1 after the safety window.
- The record is paid and deleted at the next `SetNewValidators`.
- Active challenges remain valid regardless of later whitelist changes.
