package validation

// Cross-language conformance harness for deterministic sampling.
//
// testdata/conformance_vectors.json is generated from the vLLM reference
// implementation (see DETERMINISTIC_SAMPLING_CONTRACT.md) and is the executable
// form of the contract. The Go validator MUST reproduce every vector bit-for-bit
// before it can be trusted for cross-language sampling replay.
//
// Status (item ①): this harness loads and validates the vectors' invariants so
// the fixture is wired in and CI-checked. The actual Go decimal pipeline + RNG
// (item ②) is not implemented yet; TestConformanceReplay skips until it lands,
// at which point it becomes the gate for cross-language parity.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type conformanceCase struct {
	Name              string            `json:"name"`
	Logprobs          map[string]string `json:"logprobs"`
	Temperature       string            `json:"temperature"`
	TopP              *string           `json:"top_p"`
	TopK              *int              `json:"top_k"`
	MinP              *string           `json:"min_p"`
	SeedStr           string            `json:"seed_str"`
	ExpectedWeights   map[string]int64  `json:"expected_weights"`
	ExpectedWeightSum int64             `json:"expected_weight_sum"`
	ExpectedToken     string            `json:"expected_token"`
}

type conformanceVectors struct {
	ContractVersion string `json:"contract_version"`
	WeightScale     int64  `json:"weight_scale"`
	RNGReference    struct {
		Seed     string   `json:"seed"`
		FirstU64 []uint64 `json:"first_u64"`
	} `json:"rng_reference"`
	Cases []conformanceCase `json:"cases"`
}

func loadConformanceVectors(t *testing.T) conformanceVectors {
	t.Helper()
	path := filepath.Join("testdata", "conformance_vectors.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var v conformanceVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return v
}

// TestConformanceVectorsWellFormed locks the fixture in: it must parse, cover the
// pinned contract constants, and be internally consistent. This runs today.
func TestConformanceVectorsWellFormed(t *testing.T) {
	v := loadConformanceVectors(t)

	if v.ContractVersion != "1.0.0" {
		t.Errorf("contract_version = %q, want 1.0.0", v.ContractVersion)
	}
	if v.WeightScale != 65536 {
		t.Errorf("weight_scale = %d, want 65536 (2^16)", v.WeightScale)
	}
	if len(v.Cases) == 0 {
		t.Fatal("no cases in conformance vectors")
	}

	// RNG reference vector pinned by the contract (§5).
	if len(v.RNGReference.FirstU64) == 0 {
		t.Fatal("missing rng_reference.first_u64")
	}
	const wantFirstU64 = uint64(4286832458236889005)
	if got := v.RNGReference.FirstU64[0]; got != wantFirstU64 {
		t.Errorf("rng_reference.first_u64[0] = %d, want %d", got, wantFirstU64)
	}

	for _, c := range v.Cases {
		var sum int64
		for _, w := range c.ExpectedWeights {
			if w < 0 {
				t.Errorf("%s: negative weight %d", c.Name, w)
			}
			sum += w
		}
		if sum != v.WeightScale {
			t.Errorf("%s: weights sum to %d, want %d", c.Name, sum, v.WeightScale)
		}
		if c.ExpectedWeightSum != v.WeightScale {
			t.Errorf("%s: expected_weight_sum = %d, want %d",
				c.Name, c.ExpectedWeightSum, v.WeightScale)
		}
		if _, ok := c.ExpectedWeights[c.ExpectedToken]; !ok {
			t.Errorf("%s: expected_token %q not among expected_weights",
				c.Name, c.ExpectedToken)
		}
	}
}

// TestConformanceReplay: the cross-language parity gate is IMPLEMENTED and
// PASSING in the ./detsample sub-package, which hosts the Go decimal pipeline +
// Sha256CounterRNG and is dependency-free so it runs offline. See
// detsample.TestPipelineWeightsMatch (bit-identical integer weights vs the vLLM
// reference) and detsample.TestPipelineTokenMatch (identical sampled token) over
// the same conformance_vectors.json. This placeholder stays skipped; the parent
// `validation` package invokes detsample once the validator is wired to it.
func TestConformanceReplay(t *testing.T) {
	t.Skip("parity gate lives in ./detsample (TestPipelineWeightsMatch / TestPipelineTokenMatch)")
}
