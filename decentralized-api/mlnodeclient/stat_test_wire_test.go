package mlnodeclient

import (
	"encoding/json"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestStatTestChainValuesUsePluginWireNames(t *testing.T) {
	for _, alpha := range []float64{0.05, 0.025, 0} {
		params := StatTestParamsFromChain(&types.PoCStatTestParams{
			DistThreshold:   types.DecimalFromFloat(0),
			PMismatch:       types.DecimalFromFloat(0),
			PValueThreshold: types.DecimalFromFloat(alpha),
			MinCount:        200,
		})
		wire, err := json.Marshal(PoCGenerateRequestV2{StatTest: params})
		require.NoError(t, err)
		var request struct {
			StatTest map[string]float64 `json:"stat_test"`
		}
		require.NoError(t, json.Unmarshal(wire, &request))
		require.Contains(t, request.StatTest, "fraud_threshold")
		require.Equal(t, alpha, request.StatTest["fraud_threshold"])
		require.NotContains(t, request.StatTest, "p_value_threshold")
		require.Contains(t, request.StatTest, "dist_threshold")
		require.Contains(t, request.StatTest, "p_mismatch")
		require.Equal(t, float64(0), request.StatTest["dist_threshold"])
		require.Equal(t, float64(0), request.StatTest["p_mismatch"])
		require.Equal(t, float64(200), request.StatTest["min_count"])
	}
}
