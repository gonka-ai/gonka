package detsample

import (
	"sort"
	"testing"
)

// pipelineCase adds the pipeline inputs to the shared vector fixture.
type pipelineCase struct {
	Name            string            `json:"name"`
	Logprobs        map[string]string `json:"logprobs"`
	Temperature     string            `json:"temperature"`
	TopP            *string           `json:"top_p"`
	TopK            *int              `json:"top_k"`
	MinP            *string           `json:"min_p"`
	SeedStr         string            `json:"seed_str"`
	ExpectedWeights map[string]int64  `json:"expected_weights"`
	ExpectedToken   string            `json:"expected_token"`
}

// TestPipelineWeightsMatch is the cross-language parity gate: it runs the Go
// decimal pipeline over each vector's logprobs and asserts the integer weights
// are bit-identical to the vLLM reference (expected_weights). If apd's decimal
// arithmetic (esp. Exp) diverges from CPython's libmpdec at prec=10, this fails
// and tells us exactly where.
func TestPipelineWeightsMatch(t *testing.T) {
	cases := loadPipelineCases(t)
	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			got, err := LogprobsToWeights(c.Logprobs, c.Temperature, c.TopP, c.TopK, c.MinP)
			if err != nil {
				t.Fatalf("LogprobsToWeights: %v", err)
			}
			if len(got) != len(c.ExpectedWeights) {
				t.Fatalf("weight count %d, want %d (got=%v want=%v)",
					len(got), len(c.ExpectedWeights), got, c.ExpectedWeights)
			}
			var sum int64
			for tid, w := range got {
				sum += w
				if want, ok := c.ExpectedWeights[tid]; !ok || w != want {
					t.Errorf("weight[%s] = %d, want %d", tid, w, c.ExpectedWeights[tid])
				}
			}
			if sum != weightScale {
				t.Errorf("weights sum %d, want %d", sum, weightScale)
			}
		})
	}
}

// TestPipelineTokenMatch runs the full pipeline + sampling and asserts the
// sampled token matches the reference. This is the end-to-end §4+§5+§6 gate.
func TestPipelineTokenMatch(t *testing.T) {
	cases := loadPipelineCases(t)
	for _, c := range cases {
		c := c
		t.Run(c.Name, func(t *testing.T) {
			rng := NewFromSeedString(c.SeedStr)
			got, err := DecimalSampleFromLogprobs(
				c.Logprobs, rng, c.Temperature, c.TopP, c.TopK, c.MinP)
			if err != nil {
				t.Fatalf("DecimalSampleFromLogprobs: %v", err)
			}
			if got != c.ExpectedToken {
				t.Errorf("token = %q, want %q", got, c.ExpectedToken)
			}
		})
	}
}

// TestPipelineDeterministic: same inputs twice -> identical weights.
func TestPipelineDeterministic(t *testing.T) {
	cases := loadPipelineCases(t)
	c := cases[len(cases)-1] // the ten-token case
	a, err := LogprobsToWeights(c.Logprobs, c.Temperature, c.TopP, c.TopK, c.MinP)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LogprobsToWeights(c.Logprobs, c.Temperature, c.TopP, c.TopK, c.MinP)
	if err != nil {
		t.Fatal(err)
	}
	keys := make([]string, 0, len(a))
	for k := range a {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if a[k] != b[k] {
			t.Errorf("nondeterministic weight[%s]: %d vs %d", k, a[k], b[k])
		}
	}
}

func loadPipelineCases(t *testing.T) []pipelineCase {
	t.Helper()
	// Reuse the same fixture loader as rng_test via a fresh decode with the
	// richer case shape.
	var v struct {
		Cases []pipelineCase `json:"cases"`
	}
	decodeVectors(t, &v)
	if len(v.Cases) == 0 {
		t.Fatal("no cases")
	}
	return v.Cases
}
