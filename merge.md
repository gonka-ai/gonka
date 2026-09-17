# Merge upgrade-v0.2.16 into gm/dynamic-coeff

## Intent

Merge `origin/upgrade-v0.2.16` into `gm/dynamic-coeff`. Preserve the upgrade
branch's epoch-formation, weight-cap, fee-parameter migration, and error-handling
behavior while adding dynamic coefficients only at the existing pipeline
boundaries.

Maria's deadband/edge-freeze review comment is outside this merge.

## Command

```bash
git merge origin/upgrade-v0.2.16
```

## Isolation constraints

- Do not change coefficient formulas or exported APIs under
  `inference-chain/x/inference/coefficients/`.
- Pass `coefficients.Effective` into the existing weight calculator. Do not
  rewrite group aggregation, group caps, collateral, power capping, or voting
  power calculations.
- Do not change either simulation:
  - `proposals/multi-model-poc/simulation/`
  - `inference-chain/x/inference/coefficients/sim/`
- Do not edit `delegation_weight_calculator.go`, `previous_epoch_cap.go`,
  `epoch_fallback.go`, `keeper/power.go`, `confirmation_poc.go`, or
  `bitcoin_rewards.go`. Stop if compilation requires a change there.

## Conflict resolutions

| File | Resolution |
| --- | --- |
| `inference-chain/api/inference/inference/params.pulsar.go` | Generated. Do not hand-edit. Regenerate from the merged proto. |
| `inference-chain/x/inference/types/params.pb.go` | Generated. Do not hand-edit. Regenerate from the merged proto. |
| `inference-chain/app/upgrades/v0_2_16/upgrades.go` | Keep both migration sets in the exact order below. |
| `inference-chain/app/upgrades/v0_2_16/upgrades_test.go` | Keep all fee-group and dynamic-coefficient tests. |
| `inference-chain/x/inference/module/module.go` | Use upgrade's epoch-formation pipeline and inject dynamic coefficients only at the documented boundaries. |
| `inference-chain/x/inference/module/delegation_pipeline.go` | Compose dynamic coefficient resolution with upgrade's previous-confirmed-weight input. |
| `inference-chain/x/inference/module/confirmation_weight_scales.go` | Keep every coefficient scale; derive confirmation exclusion from real nodes. |
| `inference-chain/x/inference/module/confirmation_weight_scales_test.go` | Test the merged scale and exclusion semantics. |

`inference-chain/proto/inference/inference/params.proto` must retain
`PoCModelConfig.dynamic_coefficient = 6` and
`PocParams.dynamic_coefficient_params = 16`.

## Decision 1: upgrade handler order

Use this exact order:

```text
capability version fix
migrateDynamicCoefficientParams
freezeUpcomingCoefficientConfig
migrateCurrentEffectiveCoefficients
mm.RunMigrations
applyFeeGroupUpgradeInfo
```

The dynamic-coefficient migrations prepare state before module migration.
`applyFeeGroupUpgradeInfo` remains after `RunMigrations` because inference
migration 14 resets enabled fee groups.

Keep `encoding/json`, `fmt`, and `slices` imports. Keep
`ConsensusVersion() == 15`; version 15 runs `MigrateFeeParamsToTree`.

## Decision 2: epoch-formation skeleton

Keep upgrade's `onEndOfPoCValidationStage`, `runWeightPipeline`, seating,
fallback, previous-epoch confirmed-weight cap, trust capping, and
`CapWeightApplied` behavior.

Dynamic coefficients enter only through:

1. `prepareEpochParticipationState`, which resolves the coefficient result.
2. `buildConfirmationWeightScales`, which receives the final
   `participationState.coefficients`.

Do not restore dynamic-coeff's older inline formation path.

## Decision 3: delegation pipeline

Keep `resolveEpochCoefficients` and the `coefficients *coefficient.Result` field
on `epochParticipationState`.

Use this logical signature:

```go
prepareEpochParticipationState(
    ctx context.Context,
    activeParticipants []*types.ActiveParticipant,
    params types.Params,
    pocStageStartHeight int64,
    upcomingEpochIndex uint64,
    previous *previousConfirmedWeights,
) (*epochParticipationState, error)
```

`buildDelegationWeightCalculator` uses upgrade's
`previous *previousConfirmedWeights`. It receives
`coefficients.Effective` through its existing coefficient-map argument. Do not
use `getEffectiveValidationBaseState`.

`resolveBootstrapPenaltyModes` remains non-fatal: log the error and skip
bootstrap penalties. Only coefficient resolution is returned as an error.

If fallback reruns the pipeline, persist scales from the last
`pipeline.participationState.coefficients`.

## Decision 4: confirmation scales

Write every entry from `coefficient.Result.Scales`, because each entry carries
controller state.

Set:

```go
ExcludeFromConfirmation = !modelsWithRealNodes(activeParticipants, eligible)[modelID]
```

A real node has `PocWeight > 0`. Do not derive exclusion from `VotingPower`;
new hosts can have zero `CapWeight` and therefore zero voting power while still
having real PoC nodes.

Tests must prove:

- every scale retains its effective coefficient;
- a model without real nodes remains present and is excluded;
- a model with positive-PoC-weight nodes and no voting power is not excluded.

## Decision 5: confirmation-weight helpers

The auto-merged `inference-chain/x/inference/types/weight.go` must contain all
of:

1. upgrade's `EffectiveConfirmedWeight` and `math/big` import;
2. exclusion of `ExcludeFromConfirmation` scales;
3. preference for `EffectiveCoefficient`, with deprecated
   `WeightScaleFactor` fallback.

Keep tests from both branches in `weight_test.go`.

## Decision 6: coefficient-resolution errors

The following are fatal during epoch formation:

- missing upcoming or previous root `EpochGroupData`;
- epoch-group store read failure;
- raw-weight overflow;
- invalid input rejected by `coefficient.Calculate`.

A missing model subgroup is not fatal; its raw total is zero.

Use upgrade's closest existing error policy:

```text
resolveEpochCoefficients error
  -> runWeightPipeline returns error
  -> onEndOfPoCValidationStage returns error
  -> EndBlock returns error
  -> chain halts
```

No seating or zero-weight fallback applies because no effective coefficient map
exists.

## Auto-merge verification

Stop rather than guess if an auto-merged file loses one side.

- `params.go`: retain dynamic-coefficient validation and upgrade parameter
  changes.
- `weight.go` and `weight_test.go`: retain all Decision 5 behavior.
- `testermint/src/main/kotlin/Main.kt`,
  `testermint/src/main/kotlin/data/AppExport.kt`, and
  `testermint/src/test/kotlin/DelegationTests.kt`: retain dynamic-coefficient
  fields and upgrade changes.
- `bitcoin_rewards_test.go`: retain tests from both branches.
- `delegation_pipeline_test.go`: retain tests from both branches and compile
  with the merged signature.

## Protobuf regeneration

After source proto resolution:

```bash
cd inference-chain
ignite generate proto-go
git add -u
```

Use Ignite v28.11.0. Never resolve generated files manually.

## Verification

```bash
cd inference-chain
go test ./app/upgrades/v0_2_16/ \
  ./x/inference/types/ \
  ./x/inference/coefficients/ \
  ./x/inference/module/ \
  ./x/inference/keeper/ \
  -count=1
```

If resolving the merge requires coefficient-math, simulation, weight-cap, or
fallback changes outside the boundaries above, stop and report the issue.
