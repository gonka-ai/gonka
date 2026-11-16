package calculations

import (
	"fmt"
	"math"
	"math/big"
	"testing"
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

func binomialUpperTails(n, p0Permille int) ([]*big.Int, *big.Int) {
	a := big.NewInt(int64(p0Permille))
	b := big.NewInt(int64(1000 - p0Permille))
	scale := new(big.Int).Exp(big.NewInt(1000), big.NewInt(int64(n)), nil)

	tails := make([]*big.Int, n+2)
	tails[n+1] = big.NewInt(0)
	term := new(big.Int).Exp(a, big.NewInt(int64(n)), nil)
	mul := new(big.Int)
	div := new(big.Int)
	for i := n; i >= 0; i-- {
		tails[i] = new(big.Int).Add(tails[i+1], term)
		if i == 0 {
			break
		}
		term.Mul(term, mul.Mul(big.NewInt(int64(i)), b))
		term.Quo(term, div.Mul(big.NewInt(int64(n-i+1)), a))
	}
	return tails, scale
}

func exactCriticalMisses(tails []*big.Int, scale *big.Int) int {
	scaled := new(big.Int)
	for k := len(tails) - 2; k > 0; k-- {
		if scaled.Mul(tails[k], big.NewInt(20)).Cmp(scale) >= 0 {
			return k
		}
	}
	return 0
}

func failProbability(tails []*big.Int, scale *big.Int, criticalMisses int) float64 {
	if criticalMisses+1 >= len(tails) {
		return 0
	}
	p, _ := new(big.Rat).SetFrac(tails[criticalMisses+1], scale).Float64()
	return p
}

func TestValidateNormalApproxAgainstExact(t *testing.T) {
	for _, p0Permille := range []int{50, 100, 200, 300, 400, 500} {
		t.Run(fmt.Sprintf("p0=%d", p0Permille), func(t *testing.T) {
			oneStricter := 0
			for n := 1; n <= exactSweepMaxN; n++ {
				tails, scale := binomialUpperTails(n, p0Permille)
				exactK := exactCriticalMisses(tails, scale)
				approxK := criticalValueNormalApprox(n, float64(p0Permille)/1000)
				if approxK > n {
					approxK = n
				}

				diff := approxK - exactK
				if diff < -1 || diff > 0 {
					t.Errorf("n=%d: exact=%d, approx=%d (diff=%d)", n, exactK, approxK, diff)
				}
				if diff == -1 {
					oneStricter++
				}
			}
			t.Logf("approx is one miss stricter than exact at %d of %d sizes", oneStricter, exactSweepMaxN)
		})
	}
}

const exactSweepMaxN = 990

var manualTableRows = map[int]map[int]int{
	50:  {2: 2, 3: 3, 4: 4, 5: 4, 6: 4, 7: 4, 8: 4, 9: 4, 10: 4, 20: 4, 30: 4},
	100: {2: 2, 3: 3, 4: 4, 5: 4, 6: 4, 7: 4, 8: 4, 9: 4, 10: 4},
	200: {2: 2, 3: 3, 4: 4, 5: 4, 6: 4, 7: 4, 8: 4, 9: 4},
	300: {2: 2, 3: 3, 4: 4, 5: 4, 6: 4},
	400: {2: 2, 3: 3, 4: 4, 5: 4},
	500: {3: 3, 4: 4},
}

func expectedRowCriticalMisses(n, p0Permille int) int {
	if v, ok := manualTableRows[p0Permille][n]; ok {
		return v
	}
	approxK := criticalValueNormalApprox(n, float64(p0Permille)/1000)
	if approxK > n {
		approxK = n
	}
	return approxK
}

func TestShippedTablesMatchGenerator(t *testing.T) {
	if maxTableN != exactSweepMaxN {
		t.Fatalf("maxTableN=%d, expected %d", maxTableN, exactSweepMaxN)
	}
	if len(criticalValueTables) != len(manualTableRows) {
		t.Fatalf("got %d tables, expected %d", len(criticalValueTables), len(manualTableRows))
	}

	grid := generateNValues()
	for _, p0Permille := range []int{50, 100, 200, 300, 400, 500} {
		t.Run(fmt.Sprintf("p0=%d", p0Permille), func(t *testing.T) {
			table, ok := criticalValueTables[p0Permille]
			if !ok {
				t.Fatalf("no table for p0=%d", p0Permille)
			}
			if len(table) != len(grid) {
				t.Fatalf("table has %d rows, expected %d", len(table), len(grid))
			}

			for i, row := range table {
				if row.Total != grid[i] {
					t.Errorf("row %d: Total=%d, expected %d", i, row.Total, grid[i])
					continue
				}
				if want := expectedRowCriticalMisses(row.Total, p0Permille); row.CriticalMisses != want {
					t.Errorf("n=%d: table=%d, expected %d", row.Total, row.CriticalMisses, want)
				}
			}

			for n, v := range manualTableRows[p0Permille] {
				approxK := criticalValueNormalApprox(n, float64(p0Permille)/1000)
				if approxK > n {
					approxK = n
				}
				if v == approxK {
					t.Errorf("n=%d: listed as hand-set but equals the generator (%d)", n, approxK)
				}
			}
		})
	}
}

func TestLookupThresholdsMatchSpecification(t *testing.T) {
	grid := generateNValues()
	for _, p0Permille := range []int{50, 100, 200, 300, 400, 500} {
		t.Run(fmt.Sprintf("p0=%d", p0Permille), func(t *testing.T) {
			for n := 1; n <= exactSweepMaxN+10; n++ {
				want := n
				switch {
				case n > exactSweepMaxN:
					want = n * p0Permille / 1000
				case n >= grid[0]:
					lower := grid[0]
					for _, g := range grid {
						if g <= n {
							lower = g
						}
					}
					want = expectedRowCriticalMisses(lower, p0Permille)
				}
				if got := tableCriticalMisses(t, n, p0Permille); got != want {
					t.Errorf("n=%d: lookup accepts up to %d misses, expected %d", n, got, want)
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
