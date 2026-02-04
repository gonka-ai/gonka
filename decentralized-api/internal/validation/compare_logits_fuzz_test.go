package validation

import (
	"decentralized-api/completionapi"
	"math"
	"testing"
)

func FuzzCompareLogits(f *testing.F) {
	// Seed corpus with extreme or suspicious values
	seedLogprobs := []struct {
		origTokens []string
		origProbs  []float64
		valTokens  []string
		valProbs   []float64
	}{
		{
			origTokens: []string{"a"},
			origProbs:  []float64{0.0},
			valTokens:  []string{"a"},
			valProbs:   []float64{0.0},
		},
		{
			origTokens: []string{"a"},
			origProbs:  []float64{math.NaN()},
			valTokens:  []string{"a"},
			valProbs:   []float64{0.0},
		},
		{
			origTokens: []string{"a"},
			origProbs:  []float64{math.Inf(1)},
			valTokens:  []string{"a"},
			valProbs:   []float64{math.Inf(-1)},
		},
		{
			origTokens: []string{"a"},
			origProbs:  []float64{1e308},
			valTokens:  []string{"a"},
			valProbs:   []float64{-1e308},
		},
	}

	for _, seed := range seedLogprobs {
		f.Add("token1", 0.0, "token1", 0.0, seed.origTokens[0], seed.origProbs[0], seed.valTokens[0], seed.valProbs[0], 0, 0)
	}

	f.Fuzz(func(t *testing.T,
		origToken string, origLogprob float64,
		valToken string, valLogprob float64,
		topToken1 string, topProb1 float64,
		topToken2 string, topProb2 float64,
		origLen int, valLen int) {

		if origLen < 0 {
			origLen = -origLen
		}
		if valLen < 0 {
			valLen = -valLen
		}
		origLen = origLen % 10
		valLen = valLen % 10

		orig := make([]completionapi.Logprob, origLen)
		for i := 0; i < origLen; i++ {
			orig[i] = completionapi.Logprob{
				Token: origToken,
				TopLogprobs: []completionapi.TopLogprobs{
					{Token: topToken1, Logprob: topProb1},
				},
			}
		}

		val := make([]completionapi.Logprob, valLen)
		for i := 0; i < valLen; i++ {
			val[i] = completionapi.Logprob{
				Token: valToken,
				TopLogprobs: []completionapi.TopLogprobs{
					{Token: topToken2, Logprob: topProb2},
				},
			}
		}

		baseResult := BaseValidationResult{
			InferenceId:   "test-id",
			ResponseBytes: []byte("test-response"),
		}

		// compareLogits calls customSimilarity -> customDistance -> positionDistance
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Recovered from panic: %v", r)
			}
		}()

		result := compareLogits(orig, val, baseResult)
		if result == nil {
			t.Errorf("compareLogits returned nil")
		}
	})
}
