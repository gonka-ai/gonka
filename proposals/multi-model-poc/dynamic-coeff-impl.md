# Dynamic Coefficients: Implementation Requirements

This document records implementation decisions for `dynamic-coeff.md`. The
mathematical specification remains authoritative.

## Integration constraints

- Pin coefficient config once at PoC start and compute base/effective
  coefficients once at PoC end.
- Keep coefficient mathematics in pure functions in
  `x/inference/coefficients/coefficients.go`.
- Pass the effective coefficient map into the existing weight pipeline.
- Do not change group aggregation, group caps, collateral, power capping, or
  voting-power calculations.
- Store the same effective coefficient in `ConfirmationWeightScales` so
  consensus, confirmation PoC, and reward capping use the same units.

## Epoch inputs

For the PoC that forms epoch N:

- Enabled means present in `PocParams.models` with a non-nil
  `dynamic_coefficient` block.
- Live config is frozen into N's root `EpochGroupData` at PoC start. PoC-end
  calculation uses only that snapshot, so mid-PoC governance changes apply to
  the next PoC.
- N-1 totals include every snapshotted host for each model enabled at N
  formation, including hosts excluded after the snapshot.
- Models removed or disabled before N formation are not in either share
  denominator.
- N totals include all model allocations of validated `activeParticipants`,
  including models that later fail group eligibility.
- An enabled model with no subgroup has raw total zero.
- At epoch 0, where no prior model totals exist, skip `f`, keep the seeded base
  coefficient, and still apply N dilution.
- A later store read failure aborts formation.

The effective map contains every model appearing in `activeParticipants`.
Models not enabled at formation have explicit coefficient zero.

## State and snapshots

The root epoch uses one snapshot mechanism. Global config is stored once on
`EpochGroupData`; the existing `ConfirmationWeightScales` list stores frozen
per-model config plus three controller values per enabled model:

- base coefficient
- adaptive step `s`
- previous error sign

Shares and raw totals are not duplicated. The next PoC reconstructs N-1 shares
from model subgroup data.

When no prior dynamic snapshot exists, every enabled model starts at
`coeff_min`, with `s = step_max / 2` and `prev_sign = 0`. Existing models retain
their old coefficient because the v0.2.16 migration pins `coeff_min` and
`coeff_max` to the deprecated `weight_scale_factor`.

Disabled and removed models have no controller state. Re-enabling one starts it
as a new model.

## Precedence and deviations

Rules apply in this order:

1. A nil `dynamic_coefficient` block disables a model and emits coefficient
   zero.
2. `target_share_bps = 0` pins the base coefficient at `coeff_min` and resets
   `s = step_max / 2`, `prev_sign = 0`.
3. Clamp the carried or seeded base coefficient to the bounds frozen at PoC
   start.
4. Apply deadband and adaptive-step logic.
5. Apply current-share dilution.

Resetting controller state for a zero target is deliberate; the specification
only defines the coefficient pin. Clamping before the deadband deliberately
extends the formula so narrowed governance bounds apply immediately.

The fixed base model still receives `target_share_bps` so targets total 10,000.
Setting `coeff_min = coeff_max = 1` makes its allocation operationally
residual.

## Arithmetic

Shares and share thresholds use 10,000 basis points. Coefficients,
difficulties, and steps use `LegacyDec` for calculation and `types.Decimal`
for config and storage.

Compare shares without division. For normalized model weight `w_i` and total
normalized weight `W`, the deadband comparison uses:

```text
w_i * 10000 compared with (target_share_bps +/- target_zone_bps) * W
```

Computed values use the maximum precision representable by `types.Decimal`.
All 18 `LegacyDec` fractional places are preserved when the int64 coefficient
fits. For larger magnitudes, only the least-significant digits required for
storage are truncated. Exact governance bounds round-trip unchanged.

No binary floating point is used in consensus code.

## Governance and upgrade

Dynamic mode requires complete config for every enabled model. A model without
per-model dynamic config is disabled.

The v0.2.16 migration enables the pipeline with pinned ranges:

- `coeff_min = coeff_max = deprecated weight_scale_factor`
- `D_i = 1`
- targets split deterministically to total 10,000 bps
- controller globals use the specification defaults

Pinned ranges make adjustment and dilution inert, preserving pre-upgrade
weights. The migration clears `weight_scale_factor`; post-migration runtime
never reads it. Governance later supplies real difficulties, targets, and
ranges.

After migration, governance cannot set `dynamic_coefficient_params` to nil.
Legacy static mode is accepted only while every model still carries the
deprecated `weight_scale_factor`.

The migration also copies the current epoch's deprecated confirmation scale
into `effective_coefficient` and clears the old field. Historical epochs keep
the deprecated field and are supported only by the historical read fallback.

## Other paths

Bootstrap pre-eligibility uses dynamic config presence and `coeff_min`
positivity. It runs before current PoC allocation exists and must recognize
newly governed models.

At PoC start, the upcoming epoch writes frozen config into
`ConfirmationWeightScales`; at PoC end, those same entries receive
base/effective coefficient, adaptive step, previous sign, and cPoC exclusion.
The deprecated `weight_scale_factor` snapshot field is read only for historical
pre-v0.2.16 epochs. Computed base/effective coefficients are exposed by the
dynamic-coefficients query, not by governance params.

Empty-epoch fallback participants run through the normal coefficient path. If
fallback also produces no participants, formation writes no new snapshot.

The runnable experiment is colocated in `x/inference/coefficients/sim/`. It
imports the production coefficient package, reads `config.json`, and writes
JSON plus SVG artifacts without copying protocol formulas.
