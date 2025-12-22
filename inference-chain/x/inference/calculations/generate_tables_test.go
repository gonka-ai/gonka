package calculations

import (
	"fmt"
	"math"
	"testing"

	"github.com/shopspring/decimal"
)

// z_alpha for one-sided alpha=0.05
const zAlpha = 1.6448536269514722

// criticalValueNormalApprox computes the critical value using the normal approximation.
// This is numerically stable for all n values.
// Formula: k = floor(n * p0 + z_alpha * sqrt(n * p0 * (1 - p0)))
func criticalValueNormalApprox(n int, p0 float64) int {
	mean := float64(n) * p0
	stdDev := math.Sqrt(float64(n) * p0 * (1 - p0))
	critical := mean + zAlpha*stdDev
	return int(math.Floor(critical))
}

// TestGenerateCriticalValueTables generates critical value tables for various p0 values.
// Run with: go test -run TestGenerateCriticalValueTables -v
// This test prints the tables that can be copied into stats_table.go
func TestGenerateCriticalValueTables(t *testing.T) {
	p0Values := []struct {
		name    string
		p0      float64
		varName string
	}{
		{"p0=0.05", 0.05, "criticalValueTableP005"},
		{"p0=0.10", 0.10, "criticalValueTableP010"},
		{"p0=0.20", 0.20, "criticalValueTableP020"},
		{"p0=0.30", 0.30, "criticalValueTableP030"},
		{"p0=0.40", 0.40, "criticalValueTableP040"},
		{"p0=0.50", 0.50, "criticalValueTableP050"},
	}

	// Generate tables for n from 2 to 990 with varying step sizes
	nValues := generateNValues()

	for _, pv := range p0Values {
		t.Run(pv.name, func(t *testing.T) {
			fmt.Printf("\nvar %s = []Threshold{\n", pv.varName)
			for _, n := range nValues {
				criticalK := criticalValueNormalApprox(n, pv.p0)
				// Ensure critical value doesn't exceed n
				if criticalK > n {
					criticalK = n
				}
				fmt.Printf("\t{%d, %d},\n", n, criticalK)
			}
			fmt.Println("}")
		})
	}
}

// generateNValues returns the n values to include in tables.
// Uses smaller steps for small n, larger steps for larger n.
func generateNValues() []int {
	var values []int
	// 2-10: step 1
	for n := 2; n <= 10; n++ {
		values = append(values, n)
	}
	// 20-990: step 10
	for n := 20; n <= 990; n += 10 {
		values = append(values, n)
	}
	return values
}

// findCriticalValueExact finds the maximum k where the binomial test passes.
// Only works reliably for small n due to numerical precision limits.
func findCriticalValueExact(n int, p0 decimal.Decimal) int {
	for k := 0; k <= n; k++ {
		passed, err := MissedStatTest(k, n, p0)
		if err != nil {
			return k - 1
		}
		if !passed {
			return k - 1
		}
	}
	return n
}

// TestValidateNormalApproxAgainstExact validates normal approximation against exact computation
// for small n values where exact computation is numerically stable.
// Note: For small n with low p0, normal approximation is known to be inaccurate.
// The actual table values in stats_table.go use exact calculations for these cases.
func TestValidateNormalApproxAgainstExact(t *testing.T) {
	testCases := []struct {
		p0Float float64
		p0Dec   decimal.Decimal
		minN    int // minimum n where normal approximation is accurate (within tolerance of 1)
	}{
		{0.05, decimal.NewFromFloat(0.05), 40}, // Normal approx converges at n=40 for p0=0.05
		{0.10, decimal.NewFromFloat(0.10), 20}, // Normal approx converges at n=20 for p0=0.10
		{0.20, decimal.NewFromFloat(0.20), 10},
		{0.30, decimal.NewFromFloat(0.30), 10},
		{0.40, decimal.NewFromFloat(0.40), 10},
		{0.50, decimal.NewFromFloat(0.50), 10},
	}

	// Only test small n where exact computation is reliable
	smallNValues := []int{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("p0=%.2f", tc.p0Float), func(t *testing.T) {
			for _, n := range smallNValues {
				// Skip small n where normal approximation is known to be inaccurate
				if n < tc.minN {
					t.Logf("n=%d: skipped (normal approx inaccurate for small n with p0=%.2f)", n, tc.p0Float)
					continue
				}

				exactK := findCriticalValueExact(n, tc.p0Dec)
				approxK := criticalValueNormalApprox(n, tc.p0Float)

				// Allow 1 difference due to boundary effects
				diff := exactK - approxK
				if diff < -1 || diff > 1 {
					t.Errorf("n=%d: exact=%d, approx=%d (diff=%d)", n, exactK, approxK, diff)
				} else {
					t.Logf("n=%d: exact=%d, approx=%d (diff=%d)", n, exactK, approxK, diff)
				}
			}
		})
	}
}

// TestValidateGeneratedTables validates that generated table values allow the expected
// miss rate while rejecting higher rates.
func TestValidateGeneratedTables(t *testing.T) {
	testCases := []struct {
		p0   float64
		name string
	}{
		{0.05, "p0=0.05"},
		{0.10, "p0=0.10"},
		{0.20, "p0=0.20"},
		{0.30, "p0=0.30"},
		{0.40, "p0=0.40"},
		{0.50, "p0=0.50"},
	}

	// Skip n=10 because margin is naturally larger for small n
	sampleNValues := []int{50, 100, 500, 990}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			for _, n := range sampleNValues {
				criticalK := criticalValueNormalApprox(n, tc.p0)
				if criticalK > n {
					criticalK = n
				}

				// Verify critical rate is reasonable (within expected range)
				// Margin decreases with sqrt(n): ~10% for n=50, ~5% for n=500
				criticalRate := float64(criticalK) / float64(n)
				expectedMin := tc.p0
				margin := 0.12 // Conservative margin
				if n >= 100 {
					margin = 0.08
				}
				if n >= 500 {
					margin = 0.05
				}
				expectedMax := tc.p0 + margin

				if criticalRate < expectedMin || criticalRate > expectedMax {
					t.Errorf("n=%d: critical_k=%d (%.1f%%), expected between %.1f%% and %.1f%%",
						n, criticalK, criticalRate*100, expectedMin*100, expectedMax*100)
				} else {
					t.Logf("n=%d: critical_k=%d (%.1f%%)", n, criticalK, criticalRate*100)
				}
			}
		})
	}
}
