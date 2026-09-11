# PoCChallenge v1: Implementation

Protocol: `long-poc-challenge.md`. This file is where the code goes and which existing functions it calls.

## Packages

Chain logic: `inference-chain/x/inference/keeper/pocchallenge/`

DAPI logic: `decentralized-api/pocchallenge/`

Existing PoC, Confirmation PoC, DevShard, and broker files call helpers from those packages. The helpers own create, commits, votes, seal, weight fold, payout, and the exclusion sets. Vote fold and `Finalize` live in `module` so they can call `PoCWeightCalculator` without a keeper/module import cycle.

Params: `PoCChallengeParams` (whitelist, `k`, `slice_blocks`, `max_active_challenges`). The safety window height is `nextPoCStart - EpochParams.ConfirmationPocSafetyWindow`. Create is rejected from that height. The last segment seals there.

## Sets

`Generating(target)` is true from `start_height` until the safety window seals the last segment, or the challenge fails. While it is true, the target is excluded from inference. The target remains in the generating set during a mid-Confirmation-PoC seal. The next segment starts only while it is still true and the height is before the safety window.

`Live(target)` is true until `Pay` after this epoch's `SettleAccounts`. Create rejects a second challenge while `Live`. `Finalize` and `Pay` read `Live`.

`SkipCPoC(triggerHeight)` is `Generating` copied at that Confirmation PoC's generation start. Confirmation PoC evaluation of the target's own work uses this copy. The target still submits Confirmation PoC validations.

A target that fails mid-epoch leaves `Generating`. The Confirmation PoC status write sets INACTIVE. Existing inactive locks apply. It stays `Live` until `Pay`. A target that reaches the safety window leaves `Generating` there and joins regular PoC N+1. It stays `Live` until `Pay`.

## Messages

Handlers live in `pocchallenge/`.

`MsgCreatePoCChallenge` carries the target. The target must be present in `GetRootGroupDataWithLiveMembers` and have `Participant.Status == ACTIVE`. Rejects while `GetActiveConfirmationPoCEvent` reports an active event. Remaining blocks to `nextPoCStart - EpochParams.ConfirmationPocSafetyWindow` must be at least 300. `E_full` is `vw.Weight / sum(vw.Weight) * CalculateFixedEpochReward(...)`. Lock `P` in the inference module account. `start_height = current_height + 1`. Leave `seed_hash` empty; the EndBlock hook fills it at `blockHeight == start_height` from `HeaderInfo().Hash` (previous block, same as Confirmation PoC at generation start).

`MsgSubmitPoCChallengeStoreCommit` is `MsgPoCV2StoreCommit` plus `slice_index`. `PocStageStartBlockHeight` is this segment's `start_height`. Key is `(target, start_height, model_id, slice_index)`. Each commit must have an increasing count. While the segment is open, `slice_index` must be `floor((blockHeight - start_height) / slice_blocks)`. After seal, only the last slice accepts a commit, until the first challenge vote is registered. That vote sets `vote_started` and closes commits.

Expected slice indexes come from `[start_height, segment_end)` and `slice_blocks`. Every complete slice is counted. The last incomplete slice is counted if it is at least 300 blocks. A missing counted slice fails the segment. Sealed segments shorter than 300 blocks are approved automatically.

`MsgSubmitPoCChallengeValidations` is `MsgSubmitPocValidationsV2` with `PocStageStartBlockHeight` = this segment's `start_height`. Validations are accepted from the completion of slice commits until settlement. Sample nonces across counted slices in proportion to count. Check each subset against that slice root. One vote covers the segment.

Build each slice vote using the Confirmation PoC logic. Remove the target from each model's voting-power map and subtract its weight from `TotalNetworkWeight` before building the calculator. The target still votes Confirmation PoC. Call the existing calculator once per counted slice with singleton `(participant, model)` maps. The raw per-node distribution sums to that slice's commit count. artifacts-per-block is the sum of `PocWeight` over the target's confirmation-weight nodes in the current epoch `ValidationWeights[].MlNodes`, divided by `PocStageDuration + PocExchangeDuration`. Expected count is that rate times the slice length. Call the calculator with `timeNormalizationFactor` = `LegacyOneDec()`. The `ConfirmationWeight` reading is `current ConfirmationWeight * validated_count / expected_count` (truncated), min-taken as `foldEventReadings` does. `ConfirmationPoCRatio` is `computeRatio(validated_count, expected_count)`.

At `event.GetGenerationEnd(epochParams) + 1`, `Seal` the open segment. GENERATION -> VALIDATION copies the snapshot just captured by `captureConfirmationValidationSnapshot`. VALIDATION -> COMPLETED runs `updateConfirmationWeights`, then one helper: decide the sealed segment only if at least one challenge vote was accepted; with no votes it stays pending and keeps accepting votes. A rejected or underweight punishable segment sets `failed` then writes `ConfirmationWeight` / `ConfirmationPoCRatio` through `SetParticipant`. The next segment's `start_height` is `event.GetValidationEnd + 1` if the target is still generating and that height is before the safety window. The EndBlock hook after `handleConfirmationPoC` writes `seed_hash` when `blockHeight == start_height`.

The last punishable segment uses the regular snapshot at `upcomingEpoch.PocStartBlockHeight` inside `Finalize`.

## State

`PoCChallenges` by target: epoch, challenger, challenge start, current segment `start_height`, `seed_hash`, `generating_end`, `E`, `P`, failed, unrelated-leave, `vote_started` per sealed segment.

`PoCChallengeCommits` by `(target, start_height, model_id, slice_index)`: root, count, height.

`PoCChallengeValidations` by `(target, start_height, model_id, validator)`: validated weight.

`SkipCPoC` by Confirmation PoC `TriggerHeight`. Copied `PoCValidationSnapshot` by that segment's `start_height`.

The chain deletes commits, votes, and snapshots at settlement, and removes the challenge record during `Pay`.

`PoCChallengeWork(target)` returns generating vs idle-for-PoC, `Live`, current segment `start_height`, `seed_hash`, current slice index, sealed segments waiting for votes. A query lists every live challenge with its sealed segments awaiting votes and their counted slice commits. Validators read it after Confirmation PoC votes and after regular PoC votes. DAPI reads `PoCChallengeWork` every block and after restart.

## Exclusion

Each existing path is one lookup.

`msg_server_create_devshard_escrow.go`: store `create_block_height` on `DevshardEscrow`. Before `PrepareSortedEntries`, drop `Generating` from the weight map. Escrows already created keep their slots.

`msg_server_settle_devshard_escrow.go`: one `pocchallenge` helper returns adjusted host stats. Host stats have no per-miss height, so an escrow that overlaps `[start_height, generating_end)` drops `hs.Missed` together, then `AggregateDevshardHostStatsIntoCurrentEpochStats`. Completions stay real. Escrows that start after `generating_end` count misses.

`msg_server_schedule_maintenance.go`: reject if `Generating`.

`module.go` `expireInferences`: one waiver lookup, skip the `MissedRequests` increment when `blockHeight` is in `[start_height, generating_end)`. After fail, `generating_end` is set and INACTIVE locks apply.

`confirmation_poc.go` GRACE -> GENERATION: `SnapshotSkip(ctx, event.TriggerHeight)`. After `SamplePreservedForEpisode`, strip `SkipCPoC` from the snapshot, then persist.

`module.go` `EndBlock`, `IsStartOfPocStage` branch: keeps the unfiltered sample so the target joins regular PoC N+1.

`confirmation_poc.go` at `GetGenerationEnd + 1`: `Seal` the open segment.

`confirmation_poc.go` GENERATION -> VALIDATION: after `captureConfirmationValidationSnapshot`, copy that snapshot.

`confirmation_poc.go` `evaluateConfirmation`: union `SkipCPoC(event.TriggerHeight)` into the skip map already passed to `foldEventReadings`. The target stays in the Confirmation PoC validator inputs.

`confirmation_poc.go` VALIDATION -> COMPLETED: after `updateConfirmationWeights`, one helper decides the sealed segment if votes exist, then writes the next `start_height`.

`module.go` `EndBlock`, after `handleConfirmationPoC`: one `pocchallenge` call fills `seed_hash` at `start_height` and, at `nextPoCStart - EpochParams.ConfirmationPocSafetyWindow`, `Seal`s the last segment and leaves `Generating`.

`module.go` `EndBlock`, `IsStartOfPocStage` branch: they already left `Generating`. Ordinary `StartPocCommand` path.

`keeper/participant_status.go` `removeFromEpochGroups`: one `pocchallenge` classifier. If `Live` and the record's `failed` flag is not set, `FailUnrelatedLeave`. The challenge fail path sets `failed` before its `SetParticipant` write.

`module.go` `onEndOfPoCValidationStage`: `Finalize(epoch)`, then `SettleAccounts`, then `Pay` only if `SettleAccounts` returns nil. `Finalize` auto-approves segments shorter than 300 blocks. It fails on a missing counted commit, a failed slice weight check, a missing vote, or the `unrelated_leave` flag set by `removeFromEpochGroups`. A still-pending punishable segment is decided from the votes present then, or fails as no-vote. It writes `ConfirmationWeight` for a last punishable segment that was voted in this window. `SettleAccounts` executes normally using the current participant statuses.

`Pay`: `P` stays in the inference module through `SettleAccounts`. Pass: vest `P` to the target with `PayParticipantFromModule`. Fail: send locked `P` back to the challenger. Rejected or underweight punishable segment: move `min(E, F)` from the governance module to the inference module with `SendCoinsFromModuleToModule`, then vest it to the challenger with `PayParticipantFromModule` (its vesting branch always draws from `types.ModuleName`). `F` is the target's share of the actual `BitcoinResult.Amount` minted in this settlement (`ParticipantFullWeight / TotalRewardWeight`) minus `RewardedCoins` on the just-written `EpochPerformanceSummary`. No-vote or unrelated-leave: `min(E, F)` is 0.

## DAPI

Store under `poc-challenge/`. Proof and callback routes keyed by `(start_height, slice_index, model_id)`. Accept when `body.BlockHeight` equals the current segment's `start_height`. After seal, the last slice still uses that `start_height` until the next segment's `start_height`. Stores stay queryable until `Pay`.

During Confirmation PoC generation, DAPI skips `StartPocCommand` and continues challenge generation on the segment seed.

When Confirmation PoC generation ends, stop and flush all challenge-generation nodes before `ValidateAll` selects nodes. The stop uses `StopPowV2`. `PoCChallengeWork` makes those nodes eligible in `InitValidateCommand` and `filterNodesForValidation`, including nodes normally reserved by `POC_SLOT`. Submit Confirmation PoC votes. After those, vote sealed challenge segments from the listing query.

If the target is still generating after Confirmation PoC, DAPI runs `StartPoCChallengeCommand` in place of `InferenceUpAllCommand`. The command uses every confirmation-weight node. Broker `PocIntendedStatus` is the challenge value. `getCommandForState`, `getCommandForPhase`, and `new_block_dispatcher.go` read `PoCChallengeWork`: skip `NewStartPocCommand` at `ShouldStartGeneration` while `Generating`, and queue `StartPoCChallengeCommand` instead of `NewInferenceUpAllCommand` at `ShouldReturnToInference` while still `Generating`.

At the next segment seed: stop --> flush --> wait --> `InitGenerateV2` with the new `start_height` and stored `seed_hash`. `StartPoCChallengeCommand` stores the last init key `(node_id, block_height, block_hash, model_id, callback_route)` and inits when the key changes.

At the safety window: stop --> flush --> wait --> `InferenceUpAllCommand`. Regular PoC N+1 uses `StartPocCommand` / `InitValidateCommand`.

On restart, DAPI reads `PoCChallengeWork` and inits if the stored key is missing. After `Pay`, it deletes local slice dirs.

## Lifecycle

  --> Create stores challenge start. First segment `start_height` is that value. EndBlock fills `seed_hash` at that height. DAPI inits with it (`PocStageStartBlockHeight`, seed = stored hash).
  --> Slice every `slice_blocks`. Slice rotation keeps this `start_height` and seed.
  --> Confirmation PoC generation: keep generating on this `start_height` and skip `StartPocCommand`.
  --> Confirmation PoC generation ends (`GetGenerationEnd + 1`): `Seal` --> stop --> flush all challenge-generation nodes --> copy snapshot at GENERATION -> VALIDATION --> existing validate path (`StopPowV2`) --> Confirmation PoC votes --> challenge votes. VALIDATION -> COMPLETED decides only if a vote exists. Next segment `start_height` is `GetValidationEnd + 1` if still `Generating` and before the safety window --> write `seed_hash` at that height --> `InitGenerateV2`.
  --> Safety window: `Seal` the last segment --> leave `Generating` --> flush --> `InferenceUpAllCommand`. Last punishable segment votes in the PoC N+1 validation window. `Finalize` decides any still-pending segment --> `SettleAccounts` --> `Pay` if settle returned nil.
  --> Fail: write `ConfirmationWeight` / `ConfirmationPoCRatio` --> INACTIVE --> set `generating_end` --> leave `Generating`. `Pay` after a successful `SettleAccounts`.
