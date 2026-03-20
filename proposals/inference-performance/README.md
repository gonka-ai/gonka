# Inference Performance Proposal

`MsgStartInference` and `MsgFinishInference` are too slow in production.

Blocks should be processed by nodes within 1-2 seconds so block time stays below 6 seconds. To process 1000 inferences in one block, we need to record:

- 1000 `MsgStartInference` transactions
- 1000 `MsgFinishInference` transactions
- 100-200 `MsgValidation` transactions

These transactions should be processed in under 1ms each. In tests they are fast, but in production with large state they require 10-20ms, and on some nodes 50ms or more.

## Main Time Contributors

1. Signature validation (57% of `FinishInference` and 63% of `StartInference`)
2. Stats query and recording (40% of `FinishInference` and 30% of `StartInference`)

## Profiling

Download profiling file:

<https://drive.google.com/file/d/1yxY91lzMHxv_MeloAxW1zczcpbkBjZ0t/>

Run:

```bash
go tool pprof -http=:8080 /Users/davidliberman/Downloads/pprof.inferenced.samples.cpu.001.pb.gz
```

Then open Flame Graph for exploration.

## Signature Validation

Signature validation can be significantly optimized, reducing validated signatures in most scenarios by 5x (from 5 signatures to 1).

- Related issue: <https://github.com/gonka-ai/gonka/issues/608>
- Status: implemented by `@DimaOrekhovPS`
- Note: do not implement it here
- PR reference: `(ADD PR)`

## Stats Query and Recording

Stats query/recording exists to simplify usage-stat queries for inference operations by storing this data on-chain. However, it is too heavy for on-chain operations and should be removed.

Goal: avoid reading/writing large state records in `MsgStartInference`, `MsgFinishInference`, and `MsgValidation`.

### SetInference Cost

`SetInference` (including the second execution in `HandleInferenceComplete`) accounts for:

- 10% of `FinishInference`
- 12% of `StartInference`
- 14% of `Validation`

Breakdown:

- 33% Logging
- 38% `SetOrUpdateInferenceStatsByEpoch`
- 22% `SetOrUpdateInferenceStatusByTime` (without logging)

### HandleInferenceComplete Cost (excluding SetInference)

`HandleInferenceComplete` (excluding `SetInference`) accounts for:

- 16% of `FinishInference`
- 4% of `StartInference` (second `StartInference` is rare)

Breakdown:

- 20% Logging
- 45% `2xGetEpochGroupData`
- 5% `GetEpochIndex`
- 10% `SetEpochGroupData`
- 20% `SetParticipant`/`GetParticipants` (without logging)

### ProcessInferencePayment Cost

`ProcessInferencePayment` accounts for:

- 14% of `FinishInference`
- 12% of `StartInference`

Breakdown:

- 63% Logging
- 18% `SetParticipant`/`2xGetParticipant` (without logging)
- 9% `Add`/`GetTokenomicsData`

## Task I

Measure transaction execution on mainnet with `INFO` logging disabled.

- Expected improvement: 15% for `FinishInference`, 13% for `StartInference`
- Report results before proceeding
- If confirmed, continue with Task I.2

Task I.2: test whether writing logs to files (instead of stdout) changes performance.

- Report results before proceeding
- If successful, do a final clean implementation
- If not successful, continue with Task I.3 (attach Alex Petrov message)

Task I.3: move most `StartInference` and `FinishInference` logs to `DEBUG` (except one log per transaction), then measure and report results.

## Task II

`SetInference` currently runs twice:

- in the main `StartInference`/`FinishInference` flow
- in `HandleInferenceComplete` for the second Start/Finish

It should be executed once.

Move `SetDeveloperStats`, `SetOrUpdateInferenceStatsByEpoch`, and `SetOrUpdateInferenceStatusByTime` from `SetInference` to off-chain storage (API node), because these structures are large for on-chain state.

Add required fields to the emitted `inference_finished` event in `HandleInferenceComplete`, then update the API-node event listener to store this data independently (using the same storage pattern as payload storage on API node).

Check which endpoints are used by the dashboard and decide whether we need:

- per-inference stats (current behavior), or
- only cumulative per-block/per-model stats

## Task III

In `HandleInferenceComplete`, we currently call `GetEpochGroupData` to populate `InferenceValidationDetails` with:

- `ExecutorReputation`
- `ExecutorPower`
- `TotalPower` (model group)

We also increment and persist `NumberOfRequests` for the epoch group.

This should move to `EndBlocker`:

- run `GetEpochGroup` (main and required models) once per block
- run `SetEpochGroup` once per block

Add a `Block+InferenceId` key in `HandleInferenceComplete`, then iterate these keys in `EndBlocker` to fetch inferences by ID and store `InferenceValidationDetails`. Clean keys immediately in `EndBlocker` after iteration.

After moving operations to `EndBlocker`, validate that `EndBlocker` time does not increase significantly.

- Target: <= 50-100ms for 1000 inferences on a mainnet node
- Test method: add only read operations to mainnet-node `EndBlocker` (no writes), so node state remains unchanged

## Task IV

`SetParticipant` is executed twice in `ProcessInferencePayment` and `HandleInferenceComplete` for the second Start/Finish transaction; it should be executed once.

Most `SetParticipant` time is consumed by `ComputeStatus` and `GetParams` (excluding Logging, which is ~50% and already covered in Task I).

- `Decimal.Ln` in `ComputeStatus` can be optimized
- `GetParams` is already read per transaction, so pass params into `SetParticipant` and reuse them
