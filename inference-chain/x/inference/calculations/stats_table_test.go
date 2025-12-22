package calculations

import (
	"fmt"
	"testing"

	"github.com/shopspring/decimal"
)

func TestMissedStatTestLookupWithP0ErrorConditions(t *testing.T) {
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
			got, err := MissedStatTestLookupWithP0(tt.nMissed, tt.nTotal, 100)
			if (err != nil) != tt.wantErr {
				t.Errorf("MissedStatTestLookupWithP0() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("MissedStatTestLookupWithP0() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestMissedStatTestLookupWithP0_P010(t *testing.T) {
	// Table values for p0=0.10, alpha=0.05:
	// n=10: critical=2, n=20: critical=4, n=100: critical=14, n=500: critical=61
	tests := []struct {
		name     string
		nMissed  int
		nTotal   int
		expected bool
		wantErr  bool
	}{
		// Test cases with exact lookup map values (updated for new tables)
		{
			name:     "n=10, 1 missed (passes)",
			nMissed:  1,
			nTotal:   10,
			expected: true, // 1 <= 2 (critical value)
		},
		{
			name:     "n=10, 2 missed (boundary - passes)",
			nMissed:  2,
			nTotal:   10,
			expected: true, // 2 <= 2 (critical value)
		},
		{
			name:     "n=10, 3 missed (exceeds)",
			nMissed:  3,
			nTotal:   10,
			expected: false, // 3 > 2 (critical value)
		},
		{
			name:     "n=20, 3 missed (passes)",
			nMissed:  3,
			nTotal:   20,
			expected: true, // 3 <= 4 (critical value)
		},
		{
			name:     "n=20, 4 missed (boundary - passes)",
			nMissed:  4,
			nTotal:   20,
			expected: true, // 4 <= 4 (critical value)
		},
		{
			name:     "n=20, 5 missed (exceeds)",
			nMissed:  5,
			nTotal:   20,
			expected: false, // 5 > 4 (critical value)
		},
		{
			name:     "n=100, 13 missed (passes)",
			nMissed:  13,
			nTotal:   100,
			expected: true, // 13 <= 14 (critical value)
		},
		{
			name:     "n=100, 14 missed (boundary - passes)",
			nMissed:  14,
			nTotal:   100,
			expected: true, // 14 <= 14 (critical value)
		},
		{
			name:     "n=100, 15 missed (exceeds)",
			nMissed:  15,
			nTotal:   100,
			expected: false, // 15 > 14 (critical value)
		},
		{
			name:     "n=500, 60 missed (passes)",
			nMissed:  60,
			nTotal:   500,
			expected: true, // 60 <= 61 (critical value)
		},
		{
			name:     "n=500, 61 missed (boundary - passes)",
			nMissed:  61,
			nTotal:   500,
			expected: true, // 61 <= 61 (critical value)
		},
		{
			name:     "n=500, 62 missed (exceeds)",
			nMissed:  62,
			nTotal:   500,
			expected: false, // 62 > 61 (critical value)
		},

		// Large n uses 10% rule (nMissed * 10 <= nTotal)
		{
			name:     "n=1000, 99 missed (passes)",
			nMissed:  99,
			nTotal:   1000,
			expected: true, // 99*10 = 990 <= 1000
		},
		{
			name:     "n=1000, 100 missed (boundary - passes)",
			nMissed:  100,
			nTotal:   1000,
			expected: true, // 100*10 = 1000 <= 1000
		},
		{
			name:     "n=1000, 101 missed (exceeds)",
			nMissed:  101,
			nTotal:   1000,
			expected: false, // 101*10 = 1010 > 1000
		},

		// Test cases with values between lookup map entries
		{
			name:     "n=15, 1 missed (uses n=10's critical value)",
			nMissed:  1,
			nTotal:   15,
			expected: true, // Uses critical value for 10 (2), 1 <= 2
		},
		{
			name:     "n=15, 2 missed (boundary)",
			nMissed:  2,
			nTotal:   15,
			expected: true, // Uses critical value for 10 (2), 2 <= 2
		},
		{
			name:     "n=15, 3 missed (exceeds)",
			nMissed:  3,
			nTotal:   15,
			expected: false, // Uses critical value for 10 (2), 3 > 2
		},
		{
			name:     "n=75, 10 missed (uses n=70's critical value)",
			nMissed:  10,
			nTotal:   75,
			expected: true, // Uses critical value for 70 (11), 10 <= 11
		},
		{
			name:     "n=75, 11 missed (boundary)",
			nMissed:  11,
			nTotal:   75,
			expected: true, // Uses critical value for 70 (11), 11 <= 11
		},
		{
			name:     "n=75, 12 missed (exceeds)",
			nMissed:  12,
			nTotal:   75,
			expected: false, // Uses critical value for 70 (11), 12 > 11
		},

		// Edge cases
		{
			name:     "zero missed",
			nMissed:  0,
			nTotal:   10,
			expected: true,
		},
		{
			name:     "zero missed, large total",
			nMissed:  0,
			nTotal:   1000,
			expected: true,
		},
		{
			name:     "all missed",
			nMissed:  10,
			nTotal:   10,
			expected: false,
		},
		{
			name:     "small total n=5, 0 missed",
			nMissed:  0,
			nTotal:   5,
			expected: true,
		},
		{
			name:     "small total n=5, 1 missed (boundary)",
			nMissed:  1,
			nTotal:   5,
			expected: true, // Critical value for n=5 is 1
		},
		{
			name:     "small total n=5, 2 missed (exceeds)",
			nMissed:  2,
			nTotal:   5,
			expected: false, // 2 > 1 (critical value for n=5)
		},
		{
			name:     "very small total n=1, 0 missed",
			nMissed:  0,
			nTotal:   1,
			expected: true,
		},
		{
			name:     "very small total n=1, 1 missed (100% miss rate passes - no statistical power at n=1)",
			nMissed:  1,
			nTotal:   1,
			expected: true, // idx=0 from binary search, returns true unconditionally
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MissedStatTestLookupWithP0(tt.nMissed, tt.nTotal, 100)
			if (err != nil) != tt.wantErr {
				t.Errorf("MissedStatTestLookupWithP0() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("MissedStatTestLookupWithP0() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestMissedStatTestLookupWithP0(t *testing.T) {
	tests := []struct {
		name       string
		nMissed    int
		nTotal     int
		p0Permille int
		expected   bool
		wantErr    bool
	}{
		// p0=0.05 (permille=50)
		{
			name:       "p0=0.05, n=100, 7 missed (passes)",
			nMissed:    7,
			nTotal:     100,
			p0Permille: 50,
			expected:   true, // 7 <= 8 (critical value)
		},
		{
			name:       "p0=0.05, n=100, 8 missed (boundary)",
			nMissed:    8,
			nTotal:     100,
			p0Permille: 50,
			expected:   true, // 8 <= 8 (critical value)
		},
		{
			name:       "p0=0.05, n=100, 9 missed (exceeds)",
			nMissed:    9,
			nTotal:     100,
			p0Permille: 50,
			expected:   false, // 9 > 8 (critical value)
		},

		// p0=0.20 (permille=200)
		{
			name:       "p0=0.20, n=100, 25 missed (passes)",
			nMissed:    25,
			nTotal:     100,
			p0Permille: 200,
			expected:   true, // 25 <= 26 (critical value)
		},
		{
			name:       "p0=0.20, n=100, 26 missed (boundary)",
			nMissed:    26,
			nTotal:     100,
			p0Permille: 200,
			expected:   true, // 26 <= 26 (critical value)
		},
		{
			name:       "p0=0.20, n=100, 27 missed (exceeds)",
			nMissed:    27,
			nTotal:     100,
			p0Permille: 200,
			expected:   false, // 27 > 26 (critical value)
		},

		// p0=0.30 (permille=300)
		{
			name:       "p0=0.30, n=100, 36 missed (passes)",
			nMissed:    36,
			nTotal:     100,
			p0Permille: 300,
			expected:   true, // 36 <= 37 (critical value)
		},
		{
			name:       "p0=0.30, n=100, 37 missed (boundary)",
			nMissed:    37,
			nTotal:     100,
			p0Permille: 300,
			expected:   true, // 37 <= 37 (critical value)
		},
		{
			name:       "p0=0.30, n=100, 38 missed (exceeds)",
			nMissed:    38,
			nTotal:     100,
			p0Permille: 300,
			expected:   false, // 38 > 37 (critical value)
		},

		// p0=0.40 (permille=400)
		{
			name:       "p0=0.40, n=100, 47 missed (passes)",
			nMissed:    47,
			nTotal:     100,
			p0Permille: 400,
			expected:   true, // 47 <= 48 (critical value)
		},
		{
			name:       "p0=0.40, n=100, 48 missed (boundary)",
			nMissed:    48,
			nTotal:     100,
			p0Permille: 400,
			expected:   true, // 48 <= 48 (critical value)
		},
		{
			name:       "p0=0.40, n=100, 49 missed (exceeds)",
			nMissed:    49,
			nTotal:     100,
			p0Permille: 400,
			expected:   false, // 49 > 48 (critical value)
		},

		// p0=0.50 (permille=500)
		{
			name:       "p0=0.50, n=100, 57 missed (passes)",
			nMissed:    57,
			nTotal:     100,
			p0Permille: 500,
			expected:   true, // 57 <= 58 (critical value)
		},
		{
			name:       "p0=0.50, n=100, 58 missed (boundary)",
			nMissed:    58,
			nTotal:     100,
			p0Permille: 500,
			expected:   true, // 58 <= 58 (critical value)
		},
		{
			name:       "p0=0.50, n=100, 59 missed (exceeds)",
			nMissed:    59,
			nTotal:     100,
			p0Permille: 500,
			expected:   false, // 59 > 58 (critical value)
		},

		// Large n tests (using simple threshold)
		{
			name:       "p0=0.05, n=2000, 100 missed (boundary)",
			nMissed:    100,
			nTotal:     2000,
			p0Permille: 50,
			expected:   true, // 100*20 = 2000 <= 2000*1
		},
		{
			name:       "p0=0.05, n=2000, 101 missed (exceeds)",
			nMissed:    101,
			nTotal:     2000,
			p0Permille: 50,
			expected:   false, // 101*20 = 2020 > 2000*1
		},
		{
			name:       "p0=0.20, n=2000, 400 missed (boundary)",
			nMissed:    400,
			nTotal:     2000,
			p0Permille: 200,
			expected:   true, // 400*5 = 2000 <= 2000*1
		},
		{
			name:       "p0=0.20, n=2000, 401 missed (exceeds)",
			nMissed:    401,
			nTotal:     2000,
			p0Permille: 200,
			expected:   false, // 401*5 = 2005 > 2000*1
		},
		{
			name:       "p0=0.50, n=2000, 1000 missed (boundary)",
			nMissed:    1000,
			nTotal:     2000,
			p0Permille: 500,
			expected:   true, // 1000*2 = 2000 <= 2000*1
		},
		{
			name:       "p0=0.50, n=2000, 1001 missed (exceeds)",
			nMissed:    1001,
			nTotal:     2000,
			p0Permille: 500,
			expected:   false, // 1001*2 = 2002 > 2000*1
		},

		// Large n overflow protection
		{
			name:       "very large n overflow protection (boundary)",
			nMissed:    100000000,
			nTotal:     1000000000,
			p0Permille: 100,
			expected:   true, // 100M/1B = 10% exactly
		},
		{
			name:       "very large n overflow protection (exceeds)",
			nMissed:    100000001,
			nTotal:     1000000000,
			p0Permille: 100,
			expected:   false,
		},

		// Unsupported p0
		{
			name:       "unsupported p0=0.15",
			nMissed:    10,
			nTotal:     100,
			p0Permille: 150,
			expected:   false,
			wantErr:    true,
		},

		// Edge cases
		{
			name:       "zero total",
			nMissed:    0,
			nTotal:     0,
			p0Permille: 100,
			expected:   true,
		},
		{
			name:       "negative missed",
			nMissed:    -1,
			nTotal:     10,
			p0Permille: 100,
			expected:   false,
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := MissedStatTestLookupWithP0(tt.nMissed, tt.nTotal, tt.p0Permille)
			if (err != nil) != tt.wantErr {
				t.Errorf("MissedStatTestLookupWithP0() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.expected {
				t.Errorf("MissedStatTestLookupWithP0() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDecimalToPermille(t *testing.T) {
	tests := []struct {
		name     string
		p0       decimal.Decimal
		expected int
	}{
		{"p0=0.05", decimal.NewFromFloat(0.05), 50},
		{"p0=0.10", decimal.NewFromFloat(0.10), 100},
		{"p0=0.20", decimal.NewFromFloat(0.20), 200},
		{"p0=0.30", decimal.NewFromFloat(0.30), 300},
		{"p0=0.40", decimal.NewFromFloat(0.40), 400},
		{"p0=0.50", decimal.NewFromFloat(0.50), 500},
		{"p0=0.15 (unsupported)", decimal.NewFromFloat(0.15), -1},
		{"p0=0.25 (unsupported)", decimal.NewFromFloat(0.25), -1},
		{"p0=0.01 (unsupported)", decimal.NewFromFloat(0.01), -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := decimalToPermille(tt.p0)
			if got != tt.expected {
				t.Errorf("decimalToPermille() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestMissedStatTestLookupConsistency(t *testing.T) {
	// Test that the function is consistent - if a test passes for a given rate,
	// it should also pass for lower rates with the same total
	p0Values := []int{50, 100, 200, 300, 400, 500}
	testTotals := []int{10, 50, 100, 500}

	for _, p0 := range p0Values {
		for _, total := range testTotals {
			t.Run(fmt.Sprintf("p0=%d_total=%d", p0, total), func(t *testing.T) {
				// Find the boundary where the test starts failing
				var lastPassingMissed int = -1
				for missed := 0; missed <= total; missed++ {
					result, err := MissedStatTestLookupWithP0(missed, total, p0)
					if err != nil {
						t.Errorf("Unexpected error for missed=%d, total=%d: %v", missed, total, err)
						continue
					}

					if result {
						lastPassingMissed = missed
					} else {
						// Once we find a failing case, all higher values should also fail
						for higherMissed := missed + 1; higherMissed <= total && higherMissed <= missed+5; higherMissed++ {
							higherResult, err := MissedStatTestLookupWithP0(higherMissed, total, p0)
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

				// Verify that all values up to lastPassingMissed pass
				for missed := 0; missed <= lastPassingMissed; missed++ {
					result, err := MissedStatTestLookupWithP0(missed, total, p0)
					if err != nil {
						continue
					}
					if !result {
						t.Errorf("Expected missed=%d to pass for total=%d (lastPassing=%d)", missed, total, lastPassingMissed)
					}
				}
			})
		}
	}
}

func TestCriticalValueTablesConsistency(t *testing.T) {
	// Verify all tables have the same structure and valid values
	for p0Permille, table := range criticalValueTables {
		t.Run(fmt.Sprintf("p0=%d", p0Permille), func(t *testing.T) {
			// Table should not be empty
			if len(table) == 0 {
				t.Error("Table is empty")
				return
			}

			// First entry should have small n
			if table[0].Total > 10 {
				t.Errorf("First entry has Total=%d, expected <= 10", table[0].Total)
			}

			// Last entry should be 990
			if table[len(table)-1].Total != 990 {
				t.Errorf("Last entry has Total=%d, expected 990", table[len(table)-1].Total)
			}

			// Check monotonicity: Total should be increasing
			for i := 1; i < len(table); i++ {
				if table[i].Total <= table[i-1].Total {
					t.Errorf("Total not increasing: entry[%d].Total=%d <= entry[%d].Total=%d",
						i, table[i].Total, i-1, table[i-1].Total)
				}
			}

			// Check critical values are reasonable (between 0 and Total)
			for _, entry := range table {
				if entry.CriticalMisses < 0 || entry.CriticalMisses > entry.Total {
					t.Errorf("Invalid CriticalMisses=%d for Total=%d", entry.CriticalMisses, entry.Total)
				}
			}

			// Check critical rate is reasonable (roughly around p0 + margin)
			// For small n, the margin is larger due to statistical variance
			p0Rate := float64(p0Permille) / 1000
			for _, entry := range table {
				if entry.Total < 30 {
					continue // Skip small n where margin is large
				}
				criticalRate := float64(entry.CriticalMisses) / float64(entry.Total)
				// Critical rate should be between p0 and p0 + margin
				// Margin decreases with sqrt(n), so use 0.15 for moderate n
				margin := 0.15
				if entry.Total >= 100 {
					margin = 0.10
				}
				if criticalRate < p0Rate || criticalRate > p0Rate+margin {
					t.Errorf("Critical rate %.2f%% out of range for Total=%d (p0=%.1f%%, max=%.1f%%)",
						criticalRate*100, entry.Total, p0Rate*100, (p0Rate+margin)*100)
				}
			}
		})
	}
}

// Benchmark tests
func BenchmarkMissedStatTestLookupWithP0(b *testing.B) {
	p0Values := []int{50, 100, 200, 300, 400, 500}

	for _, p0 := range p0Values {
		b.Run(fmt.Sprintf("p0=%d", p0), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				_, _ = MissedStatTestLookupWithP0(50, 500, p0)
			}
		})
	}
}

func BenchmarkMissedStatTestLookupWithP0WorstCase(b *testing.B) {
	b.Run("worst_case_search", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, _ = MissedStatTestLookupWithP0(113, 989, 100)
		}
	})
}
