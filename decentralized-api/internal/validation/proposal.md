# Intro

The task can be separated into three somewhat independent parts. 
I think it's better to release them 1 by 1, so we're not overwhelmed 
with the amount of changes in a single update: easier to test and debug.
Especially since we're releasing it combined with the scheduling changes.

# 1. Claim reward

## Current state

In `msg_server_claim_reward.go` there's a `getMustBeValidatedInferences` that:

1. Fetches all `InferenceValidationDetails` using `{epochGroupId}` key
2. Uses the revealed seed that is part of the `MsgClaimReward` to filter the inferences that were meant to be validated by this participant
3. If there's a mismatch, then the participant doesn't get their reward
4. `InferenceValidationDetails` is populated in `msg_server_finish_inference.go` in the `MsgInferenceValidation` handler

**Problem:** Participants who're fully occupied with PoC can't validate inferences made during PoC and get no reward.

## Proposed solution

1. **Migration:** `InferenceValidationDetails` now indexed by `{epochId}` instead of `{epochGroupId}`
**Note:** we can skip the migration for now and keep using `{epochGroupId}` as an index for now.
2. If `max(Inference.startBlockHeight, Inference.endBlockHeight) >= nextPocStart - inferenceValidationCutoff` 
then it gets assigned `epochId + 1`
**Migration:**: set the `inferenceValidationCutoff` in `EpochParams`
3. If we're doing this task first (this is what I'd suggest, since it's the most tricky part)
then we're just filtering such inferences out of `getMustBeValidatedInferences`
4. Then, when we make #2, we can filter them back in

**To discuss:**

1. `Inference` entities also have `epoch_id`. Should they match the `epoch_id` of `InferenceValidationDetails`?
Or is it ok if `Inference.epoch_id = X`, but `InferenceValidationDetails.epoch_id = X + 1`? 
Happens for inferences that happened during or right before the PoC.

# 2. Validation trigger

## Current state

1. `event_listener.go` listens to finish inference/requires revalidation events and feeds them to `InferenceValidator`
2. `InferenceValidator` immediately spawns a new goroutine to execute the validation request

**Problem:**
The nodes might be unavailable and we will lose the validation request.

## Proposed solution

1. Create a `InferenceValidationTaskStorage` interface and it's first implementation: `InMemoryInferenceValidationTaskStorage`
2. In `event_listener.go` instead of immediately spawning a goroutine, we will store the validation request in the `InferenceValidationTaskStorage`
3. `InferenceValidator` now will spawn a woker pool that will periodically check the tasks in the storage and execute them when nodes are available

# 3. Persistent validation task storage

**Problem**: The current implementation of `InferenceValidationTaskStorage` is in-memory, which means that if the node restarts, all tasks are lost.

## Proposed solution (short term)

On server reboot, when initializing `InferenceValidationTaskStorage`, query the chain and try to determine which inferences need to be validated.

## Proposed solution (long term)

`OnDiskValidationTaskStorage`

## Proposed solution (ideal)

A separate middleware service between the chain node and the API that will analyze each transaction and create a task in a participant's local DB.
Like a more reliable event listener that will guarantee not to miss any blocks.
