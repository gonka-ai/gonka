# Participant Status Calculation — Technical Specification

Last updated: 2025-10-27 14:54

This document provides a formal description of the status calculation implemented in `status.go` for the Inference module. It defines inputs, outputs, decision logic, and the statistical foundations used to classify participants.


## Inputs and Outputs

- Inputs to `ComputeStatus`:
  - `validationParameters` (`*types.ValidationParams`), notably `FalsePositiveRate` (denoted $`p \in (0,1)`$) and `MinRampUpMeasurements` (a positive integer cap for ramp-up observations).
  - `participant` (`types.Participant`) with fields:
    - `ConsecutiveInvalidInferences` (denoted $`N`$)
    - `CurrentEpochStats` (may be `nil`), with fields
      - `ValidatedInferences` (denoted $`V`$)
      - `InvalidatedInferences` (denoted $`I`$)
      - $`\text{InferenceCount} = V + I`$ (denoted $`n`$)
    - `EpochsCompleted` (denoted $`E`$)

- Outputs from `ComputeStatus`:
  - $`\text{status} \in \{\text{ACTIVE},\ \text{RAMPING},\ \text{INVALID}\}`$
  - $`\text{reason} \in \{\text{""},\ \text{consecutive\_failures},\ \text{ramping},\ \text{statistical\_invalidations}\}`$


## High-Level Decision Procedure

The function `ComputeStatus(p, participant)` returns a status and reason according to the following ordered checks:

1) Genesis short-circuit
- If `validationParameters == nil` or `validationParameters.FalsePositiveRate == nil`:
  - Return `ACTIVE, ""` (test-only genesis behavior).

2) Consecutive failure improbability test
- Using expected false-positive rate $`p = \text{FalsePositiveRate.ToFloat()}`$ and consecutive failures $`N = \text{participant.ConsecutiveInvalidInferences}`$, compute
  - $`P(F^N \mid G) = p^N`$.
- If $`p^N < 10^{-6}`$, return `INVALID, consecutive_failures`.

3) Epoch stats defaulting
- If `participant.CurrentEpochStats == nil`, instantiate an empty `CurrentEpochStats{}` (for genesis tests/automation only).

4) Z-score deviation test and ramping gate
- Compute the one-sided standardized deviation of observed invalidation rate from `p` using
  - $`z = (I/n - p) / \sqrt{p(1-p)/n}`$, with the conventions:
    - If $`n = 0`$, then $`z := 0`$.
    - If $`\frac{p(1-p)}{n} = 0`$, then $`z := 0`$.
- Compute the minimal measurement count required by `MeasurementsNeeded(p, max)` (see Section “Ramp-up threshold”). Let $`\text{needed}`$ be its return and $`E`$ be $`\text{EpochsCompleted}`$.
- If $`n < \text{needed}`$ and $`E < 1`$, return `RAMPING, ramping`.
- Else if $`z > 1`$, return `INVALID, statistical_invalidations`.

5) Default
- Return `ACTIVE, ""`.

The order is strict and deterministic.


## Formal Components

### 1) Probability of consecutive failures

Given a participant that is good (null hypothesis `G`), where each inference has an independent false-positive probability `p` (invalidated despite being correct), the probability of observing `N` consecutive invalidations is

$$
P(F^N \mid G) = p^N.
$$

Decision rule:

- If $`p^N < 10^{-6}`$ (i.e., rarer than one in a million), classify as `INVALID` due to `consecutive_failures`.

Example:
- For $`p = 0.01`$, $`p^3 = 10^{-6}`$. Because the code uses a strict inequality $`< 10^{-6}`$, one needs $`N \ge 4`$ consecutive invalidations to trigger this rule.


### 2) Z-score for observed invalidation rate

Let $`n = V + I`$ be the number of validated measurements in the current epoch, and let the observed invalidation rate be $`\hat{q} = I/n`$. Under the binomial model with null proportion $`p`$, the standard error of $`\hat{q}`$ is

$$
\operatorname{SE}(\hat{q}) = \sqrt{\frac{p(1-p)}{n}}.
$$

The standardized deviation (z-score) used is

$$
 z = \frac{\hat{q} - p}{\sqrt{\frac{p(1-p)}{n}}}.
$$

Conventions:
- If $`n = 0`$, set $`z = 0`$.
- If $`\frac{p(1-p)}{n} = 0`$, set $`z = 0`$.

Decision rule (one-sided):
- If $`z > 1`$, classify as `INVALID` due to `statistical_invalidations`.

Interpretation:
- This is a one-sided test at approximately the 84th percentile threshold; it flags participants whose observed invalidation rate is more than one standard deviation higher than expected. This threshold is intentionally lenient and is gated behind the ramp-up rule for early measurements.


### 3) Ramp-up threshold: `MeasurementsNeeded(p, max)`

Purpose: Ensure sufficient sample size so that a single invalidation is not trivially expected to exceed one standard deviation (preventing premature classification while data is scarce).

Implementation:

```
func MeasurementsNeeded(p float64, max uint64) uint64 {
    if p <= 0 || p >= 1 { panic("Probability p must be between 0 and 1, exclusive") }
    requiredValue := (3 + math.Sqrt(5)) / 2
    n := requiredValue / p
    needed := uint64(math.Ceil(n))
    if needed > max { return max }
    return needed
}
```

The function caps the requirement by `max = MinRampUpMeasurements` to bound the ramp-up window.

Heuristic derivation (as per in-code comment): consider wanting the deviation of one failure from expectation to lie within one standard deviation. Let `y = np`. Starting from

$$
\left|1 - np\right| \le \sqrt{np(1-p)},
$$

this can be manipulated into a quadratic inequality in `y` which admits the sufficient condition

$$
 y \ge \frac{3 + \sqrt{5}}{2} \approx 2.618.
$$

Thus, a sufficient number of measurements is

$$
 n \ge \frac{\tfrac{3 + \sqrt{5}}{2}}{p}.
$$

The implementation takes the ceiling and applies an upper bound `max`.

Remarks:
- The derivation is heuristic and intentionally simple to keep runtime and determinism constraints; the chosen constant produces conservative behavior across a broad range of `p` values.
- Panics on invalid `p` (outside `(0,1)`).


## Complete Decision Logic (pseudo-code)

```text
Input: p (false-positive rate), N (consecutive invalidations),
       V, I (validated/invalidated counts), E (epochs completed),
       max = MinRampUpMeasurements

If p is unset -> return ACTIVE, ""
If p^N < 1e-6 -> return INVALID, consecutive_failures
If CurrentEpochStats is nil -> set to empty
n := V + I
z := 0
if n > 0 and p(1-p)/n > 0 then
    z := (I/n - p) / sqrt(p(1-p)/n)
needed := min( ceil(((3+sqrt(5))/2)/p), max )
If n < needed and E < 1 -> return RAMPING, ramping
If z > 1 -> return INVALID, statistical_invalidations
return ACTIVE, ""
```


## Determinism and Chain Safety

- The algorithm uses only basic arithmetic on scalar values; it does not iterate over Go maps nor use randomness in state derivation. This preserves deterministic behavior across nodes, conforming to blockchain consensus requirements.
- All thresholds and inequalities are fixed, with no dependence on runtime-ordering of non-deterministic data structures.


## Edge Cases and Guards

- If $`n = 0`$, z-score computation returns $`0`$.
- If $`\frac{p(1-p)}{n} = 0`$, z-score computation returns $`0`$ (prevents division by zero when $`p`$ is extreme or $`n=0`$).
- `probabilityOfConsecutiveFailures(p, N)` panics if $`p \notin [0,1]`$ or $`N < 0`$.
- `MeasurementsNeeded(p, max)` panics if $`p \notin (0,1)`$.
- `CurrentEpochStats` is defaulted to an empty struct when `nil` to simplify test/genesis flows.


## Complexity

- Time: O(1)
- Space: O(1)


## Practical Examples

- Consecutive failure tripwire:
  - $`p = 0.01`$, $`N = 4`$ → $`p^N = 10^{-8} < 10^{-6}`$ → `INVALID (consecutive_failures)`.
  - $`p = 0.01`$, $`N = 3`$ → $`p^N = 10^{-6}`$ (not strictly less) → does not trigger.

- Ramping threshold (with `max = 10_000` for illustration):
  - $`p = 0.02`$ → $`\text{needed} = \lceil 2.618/0.02 \rceil = \lceil 130.9 \rceil = 131`$.
  - If $`n = 100`$ and $`E = 0`$ → `RAMPING`.

- Statistical invalidations:
  - $`p = 0.02`$, $`n = 500`$, $`I = 20`$ → $`\hat{q} = 0.04`$.
  - $`\operatorname{SE} = \sqrt{\tfrac{0.02\cdot 0.98}{500}} \approx 0.00626`$, $`z \approx \tfrac{0.04-0.02}{0.00626} \approx 3.19 > 1`$ → `INVALID (statistical_invalidations)` (assuming not in ramping state).


## Notes on Thresholds and Tuning

- The $`10^{-6}`$ rarity threshold is a hard guardrail against streaks of invalidation inconsistent with the declared false-positive rate for good actors.
- The one-sided $`z > 1`$ rule is permissive and intended for early detection of elevated invalidation rates while avoiding excessive false positives; more conservative thresholds (e.g., $`z > 2`$) can be considered if needed by policy.
- The ramp-up requirement ensures sufficient evidence before enabling the z-score gate in the first epoch.


## Source References

- `ComputeStatus` orchestrates the checks and returns `(status, reason)`.
- `CalculateZScoreFromFPR` implements the z-score computation.
- `MeasurementsNeeded` provides the ramp-up `n` threshold.
- `probabilityOfConsecutiveFailures` implements the `p^N` calculation.

All of the above are implemented in `x/inference/calculations/status.go`.