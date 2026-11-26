package validation

import (
	"encoding/json"
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// The values torch 2.x wrote on an RTX 3090 (vLLM 0.30 image) for a vocabulary
// its launch is not capped on: testdata/torch_exponential_100000.json.
func TestTorchExponential_MatchesTorch(t *testing.T) {
	raw, err := os.ReadFile("testdata/torch_exponential_100000.json")
	require.NoError(t, err)
	var fixture struct {
		Vocab  int `json:"vocab"`
		Values []struct {
			Seed uint64  `json:"seed"`
			Step uint32  `json:"step"`
			J    int     `json:"j"`
			Q    float64 `json:"q"`
		} `json:"values"`
	}
	require.NoError(t, json.Unmarshal(raw, &fixture))
	require.NotEmpty(t, fixture.Values)
	for _, v := range fixture.Values {
		got := TorchExponential(v.Seed, v.Step, fixture.Vocab, v.J)
		// torch's fast-math log is off by up to ~4e-7 near u = 1.
		require.InDelta(t, v.Q, got, 2e-6+1e-6*v.Q, "seed %d step %d j %d", v.Seed, v.Step, v.J)
	}
}

func TestTapeNoise_IsAGumbelDraw(t *testing.T) {
	// Over many tokens the noise has the Gumbel mean and spread.
	const n = 20000
	sum, sumSq := 0.0, 0.0
	for j := 0; j < n; j++ {
		g := TapeNoise(99, 0, n, j)
		sum += g
		sumSq += g * g
	}
	mean := sum / n
	sd := math.Sqrt(sumSq/n - mean*mean)
	require.InDelta(t, eulerGamma, mean, 0.03)
	require.InDelta(t, gumbelSigma, sd, 0.03)
}
