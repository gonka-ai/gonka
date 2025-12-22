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
2. If supported: O(log n) binary search in table
3. For n > 990: simple threshold (nMissed * denominator <= nTotal * numerator)
4. If unsupported p0: fall back to exact O(n) computation

### Small n Behavior (n < 5)

For p0 >= 0.20 (tables P020, P030, P040, P050), samples with n < 5 are not penalized. The critical value equals n, meaning all requests pass regardless of miss count.

Rationale: statistical power at very small sample sizes is insufficient for meaningful hypothesis testing. This only applies to higher p0 thresholds; p0=0.05 and p0=0.10 retain their computed critical values.

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
- `x/inference/calculations/stats_table_test.go` - Table validation tests
- `x/inference/calculations/generate_tables_test.go` - Table generation and validation

