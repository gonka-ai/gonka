package calculations

import (
	"testing"

	"github.com/shopspring/decimal"
)

var defaultP0 = decimal.NewFromFloat(0.10)

func TestMissedStatTestErrorConditions(t *testing.T) {
	tests := []struct {
		name     string
		nMissed  int
		nTotal   int
		expected bool
		wantErr  bool
	}{
		{
			name:     "negative missed count",
			nMissed:  -1,
			nTotal:   10,
			expected: false,
			wantErr:  true,
		},
		{
			name:     "zero total",
			nMissed:  0,
			nTotal:   0,
			expected: true,
			wantErr:  false,
		},
		{
			name:     "negative total",
			nMissed:  0,
			nTotal:   -5,
			expected: false,
			wantErr:  true,
		},
		{
			name:     "missed greater than total",
			nMissed:  15,
			nTotal:   10,
			expected: false,
			wantErr:  true,
		},
		{
			name:     "both negative",
			nMissed:  -1,
			nTotal:   -1,
			expected: false,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MissedStatTest(tt.nMissed, tt.nTotal, defaultP0)
			if (err != nil) != tt.wantErr {
				t.Errorf("MissedStatTest() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("MissedStatTest() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestMissedStatTestBasicBehavior(t *testing.T) {
	// Test basic behavior: low miss rates should pass, high miss rates should fail
	// For n=100, p0=0.10, alpha=0.05, the critical value is 14 (using normal approximation)
	testCases := []struct {
		name     string
		nMissed  int
		nTotal   int
		expected bool
	}{
		{"0% miss rate passes", 0, 100, true},
		{"5% miss rate passes", 5, 100, true},
		{"10% miss rate passes", 10, 100, true},
		{"14% miss rate passes (at boundary)", 14, 100, true},
		{"15% miss rate fails", 15, 100, false},
		{"20% miss rate fails", 20, 100, false},
		{"50% miss rate fails", 50, 100, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			passed, err := MissedStatTest(tc.nMissed, tc.nTotal, defaultP0)
			if err != nil {
				t.Errorf("MissedStatTest(%d, %d) error: %v", tc.nMissed, tc.nTotal, err)
				return
			}
			if passed != tc.expected {
				t.Errorf("MissedStatTest(%d, %d) = %v, expected %v", tc.nMissed, tc.nTotal, passed, tc.expected)
			}
		})
	}
}

func TestMissedStatTestFindsCriticalValues(t *testing.T) {
	// Find and verify critical values for p0=0.10, alpha=0.05
	// The critical value is the highest k where the test still passes
	testTotals := []int{10, 20, 50, 100}

	for _, total := range testTotals {
		var criticalValue int = -1
		for k := 0; k <= total; k++ {
			passed, err := MissedStatTest(k, total, defaultP0)
			if err != nil {
				t.Errorf("MissedStatTest(%d, %d) error: %v", k, total, err)
				break
			}
			if passed {
				criticalValue = k
			} else {
				break
			}
		}

		// Verify the critical value makes sense (should be roughly 10% of total or slightly below)
		expectedMin := int(float64(total) * 0.05)
		expectedMax := int(float64(total) * 0.15)
		if criticalValue < expectedMin || criticalValue > expectedMax {
			t.Logf("Total=%d: critical value=%d (expected between %d and %d)", total, criticalValue, expectedMin, expectedMax)
		}

		// Verify boundary behavior
		if criticalValue >= 0 && criticalValue < total {
			// criticalValue should pass
			passed, _ := MissedStatTest(criticalValue, total, defaultP0)
			if !passed {
				t.Errorf("Critical value %d should pass for total=%d", criticalValue, total)
			}
			// criticalValue+1 should fail
			passed, _ = MissedStatTest(criticalValue+1, total, defaultP0)
			if passed {
				t.Errorf("Critical value+1 (%d) should fail for total=%d", criticalValue+1, total)
			}
		}
	}
}

func TestMissedStatTestWithDifferentP0(t *testing.T) {
	tests := []struct {
		name     string
		nMissed  int
		nTotal   int
		p0       decimal.Decimal
		expected bool
	}{
		{
			name:     "p0=0.05 - 2% miss rate should pass",
			nMissed:  2,
			nTotal:   100,
			p0:       decimal.NewFromFloat(0.05),
			expected: true,
		},
		{
			name:     "p0=0.05 - 12% miss rate should fail",
			nMissed:  12,
			nTotal:   100,
			p0:       decimal.NewFromFloat(0.05),
			expected: false,
		},
		{
			name:     "p0=0.20 - 15% miss rate should pass",
			nMissed:  15,
			nTotal:   100,
			p0:       decimal.NewFromFloat(0.20),
			expected: true,
		},
		{
			name:     "p0=0.20 - 35% miss rate should fail",
			nMissed:  35,
			nTotal:   100,
			p0:       decimal.NewFromFloat(0.20),
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MissedStatTest(tt.nMissed, tt.nTotal, tt.p0)
			if err != nil {
				t.Errorf("MissedStatTest() error = %v", err)
				return
			}
			if got != tt.expected {
				t.Errorf("MissedStatTest() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestBinomialPValue(t *testing.T) {
	tests := []struct {
		name     string
		k        int
		n        int
		p0       decimal.Decimal
		expected decimal.Decimal
		delta    decimal.Decimal
	}{
		{
			name:     "k=0 should give p-value=1",
			k:        0,
			n:        10,
			p0:       decimal.NewFromFloat(0.1),
			expected: decimal.NewFromInt(1),
			delta:    decimal.NewFromFloat(0.0001),
		},
		{
			name:     "k=n should give p-value=p0^n",
			k:        10,
			n:        10,
			p0:       decimal.NewFromFloat(0.1),
			expected: decimal.NewFromFloat(0.0000000001), // 0.1^10
			delta:    decimal.NewFromFloat(0.0000000001),
		},
		{
			name:     "specific case - 5 out of 10 with p0=0.1",
			k:        5,
			n:        10,
			p0:       decimal.NewFromFloat(0.1),
			expected: decimal.NewFromFloat(0.00163), // From scipy.stats.binomtest(5, 10, 0.1, alternative='greater').pvalue
			delta:    decimal.NewFromFloat(0.0001),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := BinomialPValue(tt.k, tt.n, tt.p0, 16)
			if err != nil {
				t.Errorf("BinomialPValue() error = %v", err)
				return
			}
			diff := got.Sub(tt.expected).Abs()
			if diff.GreaterThan(tt.delta) {
				t.Errorf("BinomialPValue() = %v, want %v (diff=%v)", got, tt.expected, diff)
			}
		})
	}
}

func TestMissedStatTestConsistency(t *testing.T) {
	// Test that if a test passes for a given rate, it passes for lower rates
	testTotals := []int{10, 50, 100, 500}

	for _, total := range testTotals {
		var lastPassingMissed int = -1
		for missed := 0; missed <= total; missed++ {
			result, err := MissedStatTest(missed, total, defaultP0)
			if err != nil {
				t.Errorf("Unexpected error for missed=%d, total=%d: %v", missed, total, err)
				continue
			}

			if result {
				lastPassingMissed = missed
			} else {
				// Once we find a failing case, all higher values should also fail
				for higherMissed := missed + 1; higherMissed <= total && higherMissed <= missed+5; higherMissed++ {
					higherResult, err := MissedStatTest(higherMissed, total, defaultP0)
					if err != nil {
						continue
					}
					if higherResult {
						t.Errorf("Inconsistency: missed=%d passes but missed=%d fails for total=%d", higherMissed, missed, total)
					}
				}
				break
			}
		}

		// Verify all values up to lastPassingMissed pass
		for missed := 0; missed <= lastPassingMissed; missed++ {
			result, err := MissedStatTest(missed, total, defaultP0)
			if err != nil {
				continue
			}
			if !result {
				t.Errorf("Expected missed=%d to pass for total=%d (lastPassing=%d)", missed, total, lastPassingMissed)
			}
		}
	}
}

func BenchmarkMissedStatTest(b *testing.B) {
	testCases := []struct {
		name    string
		nMissed int
		nTotal  int
	}{
		{"small_values", 3, 10},
		{"medium_values", 50, 500},
		{"large_values", 100, 1000},
	}

	for _, tc := range testCases {
		b.Run(tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = MissedStatTest(tc.nMissed, tc.nTotal, defaultP0)
			}
		})
	}
}
