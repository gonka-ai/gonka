# Participant Status Calculation — Technical Specification

Last updated: 2026-10-26

This document provides a formal description of the status calculation implemented in `status.go` for the Inference module. It defines inputs, outputs, decision logic, and the statistical foundations used to classify participants.

## Inputs and Outputs

- Inputs to `ComputeStatus`:
  - `validationParameters` (`*types.ValidationParams`), providing `FalsePositiveRate` ($`p \in (0,1)`$), `QuickFailureThreshold`, the invalidation SPRT parameters (`BadParticipantInvalidationRate`, `InvalidationHThreshold`) and the downtime SPRT parameters (`DowntimeGoodPercentage`, `DowntimeBadPercentage`, `DowntimeHThreshold`).
  - `confirmationPocParams` (`*types.ConfirmationPoCParams`), providing `AlphaThreshold`. `SlashFraction` is deprecated and not read here.
  - `participant` (`types.Participant`) with fields:
    - `Status` (any existing `INVALID`/`INACTIVE` short-circuits)
    - `ConsecutiveInvalidInferences` (denoted $`N`$)
    - `CurrentEpochStats` (may be `nil`), with fields
      - `ValidatedInferences` (denoted $`V`$)
      - `InvalidatedInferences` (denoted $`I`$)
      - `InferenceCount` (denoted $`n`$)
      - `MissedRequests`, and the running likelihood ratios `InvalidLLR` / `InactiveLLR`
      - `ConfirmationPoCRatio`
  - `precomputed` (`types.SPRTPrecomputedValues`): log-likelihood increments derived from the parameters by `PrecomputeSPRTValues` in `keeper/sprt_precompute.go`.
- Outputs from `ComputeStatus`:
  - $`\text{status} \in \{\text{ACTIVE},\ \text{INACTIVE},\ \text{INVALID}\}`$
  - $`\text{reason} \in \{``,\ \text{consecutive\_failures},\ \text{statistical\_invalidations},\ \text{downtime},\ \text{failed\_confirmation\_poc},\ \text{algorithm\_error},\ \text{already\_set}\}`$
  - the updated `CurrentEpochStats` (the SPRT log-likelihood ratios advance in place)

`RAMPING` is a deprecated enum value. `computations.Ramping` is still declared as a reason constant, but nothing returns it.

## High-Level Decision Procedure

`ComputeStatus` returns a status and reason according to the following ordered checks:

1) Genesis short-circuit
- If `validationParameters == nil`, or `FalsePositiveRate == nil`, or `QuickFailureThreshold == nil`:
  - Return `ACTIVE, ""`.

2) Already terminal
- If `participant.Status` is `INVALID` or `INACTIVE`: return that status with `already_set`.
- These can only be reset deliberately, at epoch start.

3) Epoch stats defaulting
- If `participant.CurrentEpochStats == nil`, use an empty `CurrentEpochStats{}`. Missing LLRs default to zero.

4) Consecutive failure improbability test
- With expected false-positive rate $`p`$ and consecutive failures $`N`$, compute $`P(F^N \mid G) = p^N`$.
- If $`p^N < \text{QuickFailureThreshold}`$ (default $`10^{-6}`$), return `INVALID, consecutive_failures`.

5) Invalidation SPRT
- Advance `InvalidLLR` by the validated/invalidated deltas since the previous call and return its decision:
  - `Fail` → `INVALID, statistical_invalidations`
  - `Error` → `ACTIVE, algorithm_error` (missing `BadParticipantInvalidationRate`, `InvalidationHThreshold` or precomputed values)
  - `Pass`/`Undetermined` → continue

6) Downtime SPRT
- Advance `InactiveLLR` by the inference/missed-request deltas since the previous call and return its decision:
  - `Fail` → `INACTIVE, downtime`
  - `Error` → `ACTIVE, algorithm_error` (missing downtime parameters or precomputed values)
  - `Pass`/`Undetermined` → continue

7) Confirmation PoC
- If `ConfirmationPoCRatio < AlphaThreshold`, return `INACTIVE, failed_confirmation_poc`.
- A nil/zero `AlphaThreshold` or a missing ratio passes.

8) Default
- Return `ACTIVE, ""`.

The order is strict and deterministic.

## Formal Components

### 1) Probability of consecutive failures

Given a participant that is good (null hypothesis `G`), where each inference has an independent false-positive probability `p` (invalidated despite being correct), the probability of observing `N` consecutive invalidations is

$$
P(F^N \mid G) = p^N.
$$

Decision rule:

- If $`p^N < \text{QuickFailureThreshold}`$ (default $`10^{-6}`$, i.e. rarer than one in a million), classify as `INVALID` due to `consecutive_failures`.

Example:
- With the default $`p = 0.05`$, $`p^4 \approx 6.3 \times 10^{-6}`$ and $`p^5 \approx 3.1 \times 10^{-7}`$, so one needs $`N \ge 5`$ consecutive invalidations to trigger this rule.

### 2) Sequential Probability Ratio Test

Both the invalidation and the downtime checks are two-sided SPRTs over a running log-likelihood ratio, not z-scores. Each new measurement adds a fixed increment:

- on failure: $`\ln(p_1 / p_0)`$
- on pass: $`\ln((1 - p_1) / (1 - p_0))`$

where $`p_0`$ is the rate expected of a good participant and $`p_1`$ the rate expected of a bad one. The increments are computed once per block into the transient store by `PrecomputeSPRTValues` (`CalculateLogLLR`, 12-digit precision) and passed in as `precomputed`. When a precomputed value is missing, the zero default turns the test off rather than misclassifying anyone.

A state is reached once the accumulated LLR crosses a symmetric threshold $`\pm H`$:

- $`\text{LLR} \ge H`$ → `Fail`
- $`\text{LLR} \le -H`$ → `Pass`
- otherwise → `Undetermined`

Parameter mapping:

| Test | $`p_1`$ | $`p_0`$ | $`H`$ |
|---|---|---|---|
| Invalidation | `BadParticipantInvalidationRate` (default 0.20) | `FalsePositiveRate` (default 0.05) | `InvalidationHThreshold` (default 4) |
| Downtime | `DowntimeBadPercentage` (default 0.20) | `DowntimeGoodPercentage` (default 0.10) | `DowntimeHThreshold` (default 4) |

The LLRs are persisted per participant and per epoch, so the test accumulates across status computations within an epoch rather than restarting each time. They are reset with the rest of `CurrentEpochStats` at the epoch boundary.

Interpretation:
- Because both bounds are applied to the same statistic, a participant can be cleared (`Pass`) as well as failed. A `Pass` on downtime does not force `ACTIVE`; the later checks still run.

### 3) Ramp-up gate: deprecated

Earlier versions gated the statistical test behind `MeasurementsNeeded(p, max)`, with `max = MinRampUpMeasurements`. Neither function nor parameter is read by this code any more:

- `MeasurementsNeeded` and `CalculateZScoreFromFPR` no longer exist in the package.
- `ValidationParams.MinRampUpMeasurements` is deprecated and has no effect. So are `ValidationParams.PassValue`, `EpochParams.EpochMultiplier`, `PocParams.DefaultDifficulty`, the `TokenomicsParams` subsidy fields and the `BitcoinRewardParams` bonus factors; see `params.proto`.

Nothing in `ComputeStatus` inspects `EpochsCompleted`, so there is no sample-size floor: a participant is judged from its first measurement.

## Complete Decision Logic (pseudo-code)

```text
Input: p (false-positive rate), threshold (QuickFailureThreshold),
       N (consecutive invalidations), status, stats,
       invalidation SPRT (H, LLR, logFail, logPass),
       downtime SPRT (H, LLR, logFail, logPass),
       alpha (ConfirmationPoC.AlphaThreshold), ratio (ConfirmationPoCRatio)

If p is unset or threshold is unset -> return ACTIVE, ""
If status is INVALID or INACTIVE -> return status, already_set
If stats is nil -> set to empty
If p^N < threshold -> return INVALID, consecutive_failures

LLR_invalid += dInvalidated*logFail_invalid + dValidated*logPass_invalid
if LLR_invalid >= H_invalid -> return INVALID, statistical_invalidations
if SPRT params missing      -> return ACTIVE, algorithm_error

LLR_inactive += dMissed*logFail_inactive + dInference*logPass_inactive
if LLR_inactive >= H_inactive -> return INACTIVE, downtime
if SPRT params missing       -> return ACTIVE, algorithm_error

if alpha is set and ratio is set and ratio < alpha
    -> return INACTIVE, failed_confirmation_poc

return ACTIVE, ""
```

## Determinism and Chain Safety

- The algorithm uses only decimal arithmetic on scalar values; it does not iterate over Go maps nor use randomness in state derivation. This preserves deterministic behavior across nodes, conforming to blockchain consensus requirements.
- All thresholds and inequalities are fixed, with no dependence on runtime-ordering of non-deterministic data structures.
- The log-likelihood increments come from the transient store, recomputed identically on every node each block.

## Edge Cases and Guards

- A nil `ValidationParams`, `FalsePositiveRate` or `QuickFailureThreshold` short-circuits to `ACTIVE`.
- Missing SPRT parameters or precomputed values return `Error`, which maps to `ACTIVE` with `algorithm_error`, never to a punitive status.
- `probabilityOfConsecutiveFailures` returns `0` for a rate outside `[0,1]` or a negative count instead of panicking.
- A nil/zero `AlphaThreshold` or a missing `ConfirmationPoCRatio` passes the confirmation PoC check.
- `CurrentEpochStats` is defaulted to an empty struct when `nil` to simplify test/genesis flows.

## Complexity

- Time: O(1)
- Space: O(1)

## Practical Examples

- Consecutive failure tripwire with the default $`p = 0.05`$:
  - $`N = 5`$ → $`p^N \approx 3.1 \times 10^{-7} < 10^{-6}`$ → `INVALID (consecutive_failures)`.
  - $`N = 4`$ → $`p^N \approx 6.3 \times 10^{-6}`$ → does not trigger.
  - The inequality is strict: with $`p = 0.01`$, $`N = 3`$ gives exactly $`10^{-6}`$ and does not trigger.

- Statistical invalidations with the defaults ($`p_0 = 0.05`$, $`p_1 = 0.20`$, $`H = 4`$):
  - $`\ln(p_1/p_0) \approx 1.386`$ per invalidation and $`\ln(0.80/0.95) \approx -0.172`$ per validation.
  - Three invalidations take the LLR to about $`4.16 \ge 4`$ → `INVALID (statistical_invalidations)`; two only reach $`2.77`$. A single validation in between already keeps three below $`H`$ ($`3.99`$).
  - With the default $`p = 0.05`$ this test fires before the consecutive-failure tripwire.

- Downtime with the defaults ($`p_0 = 0.10`$, $`p_1 = 0.20`$, $`H = 4`$):
  - $`\ln(p_1/p_0) \approx 0.693`$ per missed request and $`\ln(0.80/0.90) \approx -0.118`$ per served inference.
  - Six straight misses from a zero start cross the threshold ($`4.16`$).
  - With 2 served inferences per miss (a 1/3 miss rate), it first crosses after 9 misses and 18 served ($`4.12`$).

- These are the code defaults; governance has set different values on mainnet (as of block 6,756,318, October 2026):
  - Invalidation: $`p_1 = 0.18`$, $`H = 40`$ with $`p_0 = 0.05`$, so about $`1.281`$ per invalidation and $`-0.147`$ per validation. It takes 32 straight invalidations to cross $`H`$, so the consecutive-failure tripwire ($`N \ge 5`$) fires first.
  - Downtime: `DowntimeGoodPercentage` and `DowntimeBadPercentage` are both 0.99, so both increments are $`\ln 1 = 0`$ and the downtime test never fires.

## Notes on Thresholds and Tuning

- The consecutive-failure threshold is a hard guardrail against streaks of invalidation inconsistent with the declared false-positive rate for good actors.
- $`H`$ trades sensitivity against the number of observations needed: raising it delays both `Fail` and `Pass`.
- The SPRT is sequential, so there is no fixed sample size. A participant can be classified after very few measurements if it is far from expected behavior, which is why the deprecated ramp-up gate left no sample-size floor behind.

## Source References

- `ComputeStatus` orchestrates the checks and returns `(status, reason, stats)`.
- `getInvalidationStatus` and `getInactiveStatus` implement the two SPRT checks.
- `ConfirmationPoCStatus` implements the alpha threshold check.
- `SPRT.UpdateCounts` / `SPRT.Decision` in `sprt.go` implement the test itself.
- `PrecomputeSPRTValues` / `CalculateLogLLR` in `keeper/sprt_precompute.go` derive the increments.
- `probabilityOfConsecutiveFailures` implements the $`p^N`$ calculation.
