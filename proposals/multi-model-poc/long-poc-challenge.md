# PoCChallenge v1

## Goal

A governance-approved address can force one participant to generate PoC from the challenge start until the safety window before the next regular PoC. The target is removed from inference for that interval. All of its current-epoch confirmation-weight hardware generates PoC instead.

The challenger locks payment `P` in the inference module account. If the target passes, it receives `P`. If the target fails, `P` is returned to the challenger and the target is punished the same way as a failed Confirmation PoC.

## Create

Governance params:

- whitelist of addresses that may create a challenge
- `k` in `[0, 1]`: payment ratio
- `slice_blocks`: complete slice length (500)
- `max_active_challenges`: network-wide cap

The safety window starts at `nextPoCStart - EpochParams.confirmation_poc_safety_window`. That field is the existing epoch param. From that height, create is rejected. At that height the open segment seals and the target stops challenge generation, then joins regular PoC N+1.

Create is allowed during inference, outside Confirmation PoC, and before the safety window.

The create transaction carries only the target address. The challenger does not pick a start height.

The handler:

1. Rejects if the sender is not on the whitelist.
2. The target must be in the current-epoch live set, have status `ACTIVE`, be distinct from the sender, have no live challenge, and have no current or scheduled maintenance before the next epoch switch.
3. The total active challenges must be less than `max_active_challenges`.
4. Computes `E_full` = target current-epoch weight / total current-epoch weight * current fixed epoch Bitcoin-style reward. This is the gross share, before downtime and Confirmation PoC reductions. It is not the existing net reward estimator.
5. Computes `scale` = blocks from now to the safety window / blocks from this epoch's inference start to the safety window.
6. Freezes `E = floor(E_full * scale)` and `P = floor(k * E)`. Rejects if `P = 0`.
7. Sets `start_height` to the next block after this transaction. The remaining blocks to the safety window (`nextPoCStart - EpochParams.confirmation_poc_safety_window`) must be at least 300.
8. Moves `P` from the challenger to the inference module account. If the transfer fails, the challenge is not created.
9. Stores epoch, challenger, target, challenge start, `E`, `P`, and first segment `start_height` = challenge start. `seed_hash` is filled at `start_height` with the hash of the block before it.

Challenges are final once created. Active challenges remain valid if the sender is removed from the whitelist.

## Generate

The target generates from `start_height` on every MLNode that currently contributes to its confirmation weight. Artifacts are stored off-chain.

A slice is `slice_blocks` of generation under the current segment seed. The chain stores one commit per slice: artifact count, root hash, and slice index. DAPI rotates the local store when a slice fills.

An accepted commit must come from the target, name the current slice of the open segment, and have a count greater than the last accepted count for that slice. The current slice is `floor((block_height - start_height) / slice_blocks)`. Earlier slices are closed.

When Confirmation PoC generation ends, or when the safety window starts, the last slice is committed even if it is shorter than `slice_blocks`. After the segment seals, only that last slice still accepts a commit, until votes for that segment start. Nonces must be unique across slices of the same segment.

New inference assignments stop at `start_height`. Expired requests during challenge generation are exempt from missed-inference. Maintenance is disabled for the target during the challenge. The target skips Confirmation PoC generation. When Confirmation PoC generation ends, it stops the open segment and validates Confirmation PoC with everyone else. It joins the next regular PoC.

Expected artifacts for a slice = last regular PoC artifacts-per-block for this target * slice length in blocks. Each slice must pass the same weight check Confirmation PoC uses for that length. The chain skips evaluation of incomplete last slices shorter than 300 blocks.

## Segments

A segment is the unit that can fail the challenge. It runs from one validation to the next. A segment at least 300 blocks is voted VALID.

Each segment is a PoC stage with its own `start_height`. That height is `PocStageStartBlockHeight` for commits, votes, seed, and DAPI callbacks.

- The first segment's `start_height` is the challenge start (the next block after create).
- Confirmation PoC generation uses the current segment seed. The chain skips the target in Confirmation PoC evaluation if the challenge was active at that event's generation start.
- When Confirmation PoC generation ends, the open segment is sealed. Both sides stop and switch to validation. Validators vote the segment after they finish Confirmation PoC votes. The next segment's `start_height` is the first inference block after Confirmation PoC validation ends, if that height is still before the safety window. New seed, new commits, new votes.
- The safety window seals the last segment. The target leaves challenge generation there and joins regular PoC N+1 with everyone else.

A segment is made of slices. Slice rotation does not change the seed.

The seed for a segment is the hash of the block before that segment's `start_height`. The next regular PoC uses its own seed. DAPI callbacks require the artifact `block_height` to match the active segment's `start_height`.

## Validate

Validators finish regular PoC or Confirmation PoC votes first. They then vote sealed segments that are at least 300 blocks. Votes are accepted after the slices are committed until epoch settlement.

Before votes, the chain fails a punishable segment if a counted slice has no commit or fails the weight check for its length.

The vote uses the same Confirmation PoC path with the target excluded. Sampling draws nonces from the counted slices, in proportion to each slice count, and checks each sample against that slice's root. One vote covers the whole segment.

Each punishable segment updates `ConfirmationWeight` and `ConfirmationPoCRatio` the same way Confirmation PoC does. A passing segment can still lower those values. A rejected segment writes a zero reading. An underweight segment writes its actual reading.

The last sealed segment that is at least 300 blocks is voted during the next regular PoC validation if it was not already voted after a Confirmation PoC. The chain writes that ConfirmationWeight before `SettleAccounts`, so it still affects epoch N payment. If any punishable segment has no final vote by then, the challenge fails.

A failed segment writes `ConfirmationWeight` and `ConfirmationPoCRatio` the same way Confirmation PoC does. That write goes through the Confirmation PoC status path and sets INACTIVE exactly when a failed Confirmation PoC would. Generation stops. Existing inactive locks apply.

## Result

The challenge is live until settlement of the epoch it was created in.

It passes only if every segment passed. A segment shorter than 300 blocks is already passed.

The challenge fails if any punishable segment fails the weight check, is rejected, has a missing slice commit, or lacks a final vote before `SettleAccounts`, or if the target leaves the current-epoch active set for a reason other than this challenge.

A failure in any segment immediately terminates the challenge. Upon failure, generation stops and the target becomes INACTIVE. Payout waits for epoch settlement.

## Pay

`SettleAccounts` runs first on current statuses, the same as today. Challenge payment runs after it. The locked `P` is still in the inference module account.

Pass:

- target already received its normal epoch settlement, including work coins earned before the challenge
- `P` is paid to the target with the normal reward vesting period

A pass can still lower ConfirmationWeight from passing segments, as Confirmation PoC already does.

Fail:

- `SettleAccounts` already applied INACTIVE: zero bitcoin reward and zero work coins, leftover to governance
- `P` is returned to the challenger
- `F` is this target's unpaid share of the fixed epoch reward after that settlement
- `min(E, F)` is paid to the challenger with the normal reward vesting period, taken from that unpaid share now in governance
- the challenger receives `min(E, F)` only if a punishable segment is rejected or underweight

`P` is the challenger's coins. `min(E, F)` is coins this target already lost from the fixed epoch reward.

## Timeline

```
[Regular PoC N] -> [Inference] -> [cPoC] -> [Inference] -> ... -> [cPoC] -> [Inference] -> [Regular PoC N+1] -> [SettleAccounts] -> [SWITCH N+1]
                   create OK                     create OK                    create OK     create blocked
                                                                                            (safety window)
PoCChallenge:
                   [create][segment ........][stop+val][segment ...][stop at safety][join PoC N+1]
                             vote with cPoC              last segment vote during
                                                         PoC N+1 validation
                                                         SettleAccounts then Pay
```

Confirmation PoC is the existing chain trigger. The target generates the open segment through Confirmation PoC generation, then stops with everyone else and validates. A segment is sealed when Confirmation PoC generation ends, or when the safety window starts.

## Flow

```
Whitelist sender
  --> lock P = k * E in the inference module account
  --> target leaves inference routing, generates PoC
  --> commit each slice (count+root)
  --> Confirmation PoC generation: keep the segment seed, do not generate Confirmation PoC
  --> Confirmation PoC generation ends: seal, stop, flush; validate Confirmation PoC with everyone else
  --> validators sample nonces across slices, vote the sealed segment
  --> next segment: new start_height after Confirmation PoC, if still before the safety window
  --> safety window: seal last segment, leave challenge generation, join PoC N+1
  --> validators vote the regular result, then the last sealed segment if it is at least 300 blocks
        |
        +--> segment fail
        |      --> write ConfirmationWeight / ConfirmationPoCRatio; INACTIVE; existing locks
        |      --> after SettleAccounts: refund P; vest min(E, F) unless the fail was no vote or unrelated leave
        |
        +--> pass, more inference remains before the safety window
        |      --> next segment: new start_height after Confirmation PoC
        |
        +--> pass, last segment sealed at the safety window
               --> after SettleAccounts: vest P to the target
```

## State

One record keyed by target:

- epoch
- challenger, target
- start height (challenge start; first segment `start_height`)
- `E`, `P`
- current segment `start_height` and `seed_hash`
- failed flag

The chain stores slice commits (by that segment's `start_height` and slice index) and validation votes by that segment's `start_height`. They are deleted at settlement.

## Rules

- Each target can have at most one active challenge.
- The network-wide active challenges are capped at `max_active_challenges`.
- The challenge concludes at this epoch's `SettleAccounts`.
- Create only during inference, not during Confirmation PoC, and outside the safety window. Remaining time to the safety window must be at least 300 blocks.
- The target generates through Confirmation PoC generation, then stops and validates Confirmation PoC. The safety window ends challenge generation.
- Active challenges remain valid regardless of subsequent whitelist changes.
