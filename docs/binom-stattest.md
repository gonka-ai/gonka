# Binomial Statistical Test Implementation

One-sided binomial test for miss rate validation in the inference chain.

## Motivation

The statistical test determines whether an executor's miss rate exceeds a threshold p0.

### Performance Problem

Exact binomial computation is O(n) with decimal arithmetic:

| n | Exact (ns) | Lookup (ns) | Speedup | Allocations |
|---|------------|-------------|---------|-------------|
| 100 | 106,283 | 5.7 | 18,552x | 2,401 → 0 |
| 500 | 242,262 | 5.1 | 47,502x | 9,439 → 0 |
| 1000 | 521,157 | 2.0 | 255,370x | 18,656 → 0 |

For consensus-path code, this is unacceptable. Pre-computed lookup tables provide O(log n) performance with zero allocations.

### Numerical Instability

The exact computation has numerical overflow for large n (>350) because binomial coefficients exceed decimal precision. The normal approximation provides a numerically stable alternative.

## Approach

### Pre-computed Tables

Critical value tables for six p0 values:
- p0 = 0.05, 0.10, 0.20, 0.30, 0.40, 0.50

Each table maps sample size n to the maximum number of misses that passes the test at alpha=0.05.

### Integer Permille Keys

To avoid floating-point comparison issues, p0 is represented as integer permille (thousandths):
- 0.05 → 50
- 0.10 → 100
- etc.

### Lookup Algorithm

1. Convert decimal p0 to permille
2. If supported: O(log n) binary search for the largest table row with `Total <= n`
3. For n > 990: simple threshold (nMissed * denominator <= nTotal * numerator)
4. If unsupported p0: fall back to exact O(n) computation

### Small n Behavior (hand-set rows)

The rows for the smallest n are set by hand instead of by the formula below:

| p0 | hand-set rows (n) |
|----|-------------------|
| 0.05 | 2-10, 20, 30 |
| 0.10 | 2-10 |
| 0.20 | 2-9 |
| 0.30 | 2-6 |
| 0.40 | 2-5 |
| 0.50 | 3, 4 |

In every table, samples with n < 5 are not penalized: the critical value equals n, so all requests pass regardless of miss count. From n = 5 the hand-set rows allow 4 misses.

Rationale: statistical power at very small sample sizes is insufficient for meaningful hypothesis testing. These rows are more lenient than the normal approximation. Compared with the exact binomial test, the lookup is more lenient only for the sizes listed in "Accuracy and scope"; some hand-set rows (for example p0 = 0.50 at n = 3 and 4, p0 = 0.05 at n = 30) equal the exact test.

### Large n Behavior (n > 990)

For n > 990, the test uses a simple rate check (`nMissed/nTotal <= p0`) rather than the statistical test. This is more conservative - it rejects at the exact p0 rate rather than allowing the statistical margin.

Example for p0=0.10, n=1000:
- Statistical test would allow: ~114 misses (10% + margin)
- Simple threshold allows: 100 misses (exactly 10%)

This conservative approach is intentional for consensus safety.

### Table Generation

Tables generated using the normal approximation to the binomial distribution:

```
criticalK = floor(n * p0 + z_alpha * sqrt(n * p0 * (1 - p0)))
```

where z_alpha = 1.6448536269514722 for alpha = 0.05 one-sided.

The formula is evaluated only at the table rows (n = 2..10, then every 10 up to 990), and the hand-set rows above replace it for small n. `TestGenerateCriticalValueTables` prints the formula values. `TestShippedTablesMatchGenerator` checks the row grid, the hand-set values and that every other row matches the formula, and `TestLookupThresholdsMatchSpecification` checks the accepted miss count for every n up to 1000. For every n up to 990 the formula is never more lenient than the exact test and is at most one miss stricter (`TestValidateNormalApproxAgainstExact`).

## Accuracy and scope

The lookup tables are an approximation of the one-sided binomial test for low inference counts per epoch. They are not an exact implementation of the miss tolerance `binom_test_p0`:

- n between two rows is judged by the lower row. For example, at p0 = 0.40, n = 19 uses the row for n = 10 and accepts 6 misses, while the exact test accepts 11.
- Because of this, the lookup is stricter than the exact test for most n, and a host whose true miss rate equals p0 fails more often than alpha = 0.05.
- The hand-set small-n rows make the lookup more lenient than the exact test for the smallest n (including sizes such as p0 = 0.05, n = 11, which use a hand-set row).
- alpha is fixed at 0.05 (`alpha` in `stats.go`). It is not read from `ValidationParams.FalsePositiveRate`.

Deviation from the exact test for n = 1..990:

| p0 | stricter than exact at | by at most | more lenient than exact at | worst P(fail) for a host at exactly p0 |
|----|------------------------|------------|-------------------------|----------------------------------------|
| 0.05 | 880 of 990 sizes | 2 misses | n = 2-28, by up to 3 misses | 0.130639 at n = 69 |
| 0.10 | 923 of 990 sizes | 2 misses | n = 2-14, by up to 2 misses | 0.190259 at n = 39 |
| 0.20 | 935 of 990 sizes | 3 misses | n = 2-7, by up to 2 misses | 0.357139 at n = 29 |
| 0.30 | 938 of 990 sizes | 4 misses | n = 3-5, by 1 miss | 0.526137 at n = 19 |
| 0.40 | 935 of 990 sizes | 5 misses | n = 4, by 1 miss | 0.691931 at n = 19 |
| 0.50 | 932 of 990 sizes | 6 misses | never | 0.820358 at n = 19 |

For hosts with miss rates well below p0 the difference is small: at p0 = 0.10, the largest failure probability for a host with a true miss rate of 2% at any n up to 990 is about 0.000254 (at n = 29).

`TestCriticalValueTablesAgainstExactBinomial` compares the lookup with an independent exact integer computation of the binomial test at every n up to 990 and pins these numbers. Changing a table requires updating this section and `acceptedTableDeviation` together.

## Usage

```go
// Preferred: uses O(log n) lookup for supported p0 values
passed, err := MissedStatTest(nMissed, nTotal, decimal.NewFromFloat(0.40))

// Direct lookup (always O(log n))
passed, err := MissedStatTestLookupWithP0(nMissed, nTotal, 400) // p0=0.40
```

## Files

- `x/inference/calculations/stats.go` - Public interface, exact computation
- `x/inference/calculations/stats_table.go` - Pre-computed tables, lookup functions
- `x/inference/calculations/stats_test.go` - Basic tests
- `x/inference/calculations/stats_table_test.go` - Table validation tests, including the comparison with the exact binomial test
- `x/inference/calculations/generate_tables_test.go` - Table generation, the exact integer binomial reference, and generator checks

