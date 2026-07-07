package detsample

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// The vectors live in the parent package's testdata; this sub-package reads them
// directly so its tests stay dependency-free (importable/testable offline).
type vectorCase struct {
	Name            string           `json:"name"`
	SeedStr         string           `json:"seed_str"`
	ExpectedWeights map[string]int64 `json:"expected_weights"`
	ExpectedToken   string           `json:"expected_token"`
}

type vectors struct {
	ContractVersion string `json:"contract_version"`
	WeightScale     int64  `json:"weight_scale"`
	RNGReference    struct {
		Seed     string   `json:"seed"`
		FirstU64 []uint64 `json:"first_u64"`
	} `json:"rng_reference"`
	Cases []vectorCase `json:"cases"`
}

func loadVectors(t *testing.T) vectors {
	t.Helper()
	path := filepath.Join("..", "testdata", "conformance_vectors.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var v vectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return v
}

// TestRNGReference: the SHA256 counter RNG matches the pinned reference stream.
func TestRNGReference(t *testing.T) {
	v := loadVectors(t)
	got := IterU64(v.RNGReference.Seed, len(v.RNGReference.FirstU64))
	for i, want := range v.RNGReference.FirstU64 {
		if got[i] != want {
			t.Errorf("IterU64(%q)[%d] = %d, want %d",
				v.RNGReference.Seed, i, got[i], want)
		}
	}
	const wantFirst = uint64(4286832458236889005)
	if got[0] != wantFirst {
		t.Errorf("first u64 = %d, want %d", got[0], wantFirst)
	}
}

// TestCategoricalReplayFromExpectedWeights cross-validates the RNG + integer
// categorical sampler against the reference implementation *without* the decimal
// pipeline: it feeds each vector's committed expected_weights into the Go
// sampler and asserts the sampled token matches expected_token. This proves the
// §5/§6 half of cross-language parity is bit-identical today. The decimal half
// (§4, logprobs -> weights) is validated once that pipeline lands.
func TestCategoricalReplayFromExpectedWeights(t *testing.T) {
	v := loadVectors(t)
	for _, c := range v.Cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			// Build the weight list in lexicographic token-ID-string order
			// (contract §3), matching Python's sorted(weights.keys()).
			tids := make([]string, 0, len(c.ExpectedWeights))
			for tid := range c.ExpectedWeights {
				tids = append(tids, tid)
			}
			sort.Strings(tids)

			weights := make([]int64, len(tids))
			var sum int64
			for i, tid := range tids {
				weights[i] = c.ExpectedWeights[tid]
				sum += weights[i]
			}
			if sum != v.WeightScale {
				t.Fatalf("%s: weights sum %d != scale %d", c.Name, sum, v.WeightScale)
			}

			rng := NewFromSeedString(c.SeedStr)
			idx, err := SampleCategoricalWeights(weights, rng)
			if err != nil {
				t.Fatalf("%s: sample error: %v", c.Name, err)
			}
			if got := tids[idx]; got != c.ExpectedToken {
				t.Errorf("%s: sampled %q, want %q", c.Name, got, c.ExpectedToken)
			}
		})
	}
}
