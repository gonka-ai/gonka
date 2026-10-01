package detsample

import "testing"

// TestRejectionLimit pins the boundary computation. The pipeline only ever uses
// n = 2^16 (a power of two -> sentinel 0), so these non-power-of-2 values are
// the only coverage of the reject path's limit math.
func TestRejectionLimit(t *testing.T) {
	// n divides 2^64 -> sentinel 0 (accept all).
	for _, n := range []uint64{1, 2, 4, 256, 65536, 1 << 40} {
		if got := rejectionLimit(n); got != 0 {
			t.Errorf("rejectionLimit(%d) = %d, want 0 (n divides 2^64)", n, got)
		}
	}
	// n=3: 2^64 mod 3 = 1 -> limit = 2^64 - 1 = MaxUint64.
	if got, want := rejectionLimit(3), ^uint64(0); got != want {
		t.Errorf("rejectionLimit(3) = %d, want %d", got, want)
	}
	// n=10: 2^64 mod 10 = 6 -> limit = 2^64 - 6 = MaxUint64 - 5.
	if got, want := rejectionLimit(10), ^uint64(0)-5; got != want {
		t.Errorf("rejectionLimit(10) = %d, want %d", got, want)
	}
}

// TestReduceUnbiasedRejects forces the reject-and-retry branch with an injected
// draw source (impossible to hit deterministically via the real RNG), proving a
// draw >= limit is rejected and the next accepted.
func TestReduceUnbiasedRejects(t *testing.T) {
	limit := rejectionLimit(10) // 2^64 - 6
	draws := []uint64{limit, limit + 1, 43}
	i := 0
	got := reduceUnbiased(10, func() uint64 { v := draws[i]; i++; return v })
	if got != 43%10 {
		t.Errorf("reduceUnbiased = %d, want %d", got, 43%10)
	}
	if i != 3 {
		t.Errorf("consumed %d draws, want 3 (first two rejected)", i)
	}
}

// TestUint64BelowParity replays the non-power-of-2 uint64_below vectors from the
// Python reference — cross-language parity of the limit + modulo path.
func TestUint64BelowParity(t *testing.T) {
	var v struct {
		Uint64Below struct {
			Seed  string `json:"seed"`
			Cases []struct {
				N     uint64   `json:"n"`
				Draws []uint64 `json:"draws"`
			} `json:"cases"`
		} `json:"uint64_below"`
	}
	decodeVectors(t, &v)
	if len(v.Uint64Below.Cases) == 0 {
		t.Fatal("no uint64_below cases")
	}
	for _, c := range v.Uint64Below.Cases {
		rng := NewFromSeedString(v.Uint64Below.Seed)
		for i, want := range c.Draws {
			got, err := Uint64Below(rng, c.N)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("n=%d draw %d: got %d, want %d", c.N, i, got, want)
			}
		}
	}
}

// TestSampleCategoricalWeightsErrors covers the guard paths (no silent fallback).
func TestSampleCategoricalWeightsErrors(t *testing.T) {
	rng := NewFromSeedString("x")
	if _, err := SampleCategoricalWeights([]int64{1, -2, 3}, rng); err == nil {
		t.Error("negative weight should error")
	}
	if _, err := SampleCategoricalWeights([]int64{0, 0}, rng); err == nil {
		t.Error("zero total should error")
	}
	if _, err := SampleCategoricalWeights(nil, rng); err == nil {
		t.Error("empty weights should error")
	}
}
