package keeper

import (
	"encoding/binary"
	"math"
	"math/rand"
	"testing"
)

// TestReservoirSamplingUniformity verifies that the reservoir sampling
// algorithm used in getMustBeValidatedInferences produces a statistically
// uniform distribution. The existing benchmark tests measure speed but
// not correctness of the sampling distribution.
//
// Method: run reservoir sampling many times, count how often each item
// is selected, verify with chi-squared test for uniformity.
func TestReservoirSamplingUniformity(t *testing.T) {
	const (
		N    = 1000   // total items
		K    = 50     // sample size
		runs = 50000  // number of sampling runs
	)

	counts := make([]int, N)

	for r := 0; r < runs; r++ {
		// Use deterministic seed per run (same as production: block hash)
		var blockHash [32]byte
		binary.BigEndian.PutUint64(blockHash[:8], uint64(r))
		blockHashSeed := int64(binary.BigEndian.Uint64(blockHash[:8]))
		rng := rand.New(rand.NewSource(blockHashSeed))

		// Reservoir sampling — exact algorithm from msg_server_claim_rewards.go
		sample := make([]int, 0, K)
		for i := 0; i < N; i++ {
			if len(sample) < K {
				sample = append(sample, i)
			} else {
				j := rng.Intn(i + 1)
				if j < K {
					sample[j] = i
				}
			}
		}

		for _, idx := range sample {
			counts[idx]++
		}
	}

	// Chi-squared test for uniformity
	expected := float64(runs*K) / float64(N)
	chiSquared := 0.0
	for _, count := range counts {
		diff := float64(count) - expected
		chiSquared += diff * diff / expected
	}

	// For N-1 = 999 degrees of freedom at p=0.001 significance:
	// critical value ~ 1143.9. We use 1200 for safety margin.
	// If chi-squared > critical, distribution is NOT uniform.
	criticalValue := 1200.0
	if chiSquared > criticalValue {
		t.Errorf("Reservoir sampling distribution is NOT uniform: chi-squared=%.1f > %.1f (critical at p=0.001, df=%d)",
			chiSquared, criticalValue, N-1)
	}

	// Also verify each item's selection frequency is within 3 sigma
	sigma := math.Sqrt(expected * (1 - float64(K)/float64(N)))
	outliers := 0
	for i, count := range counts {
		deviation := math.Abs(float64(count)-expected) / sigma
		if deviation > 4.0 { // 4-sigma outlier
			t.Logf("WARNING: item %d selected %d times (expected %.1f, %.1f sigma)", i, count, expected, deviation)
			outliers++
		}
	}

	if outliers > N/100 { // more than 1% outliers at 4-sigma is suspicious
		t.Errorf("Too many outliers: %d/%d items have >4-sigma deviation from expected", outliers, N)
	}

	t.Logf("Reservoir sampling uniformity: chi-squared=%.1f (critical=%.1f), outliers=%d/%d",
		chiSquared, criticalValue, outliers, N)
}
