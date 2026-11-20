package mlnodeclient

import (
	"encoding/json"
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestStatTestParamsFromChainCarriesMinCount(t *testing.T) {
	chain := types.DefaultPoCStatTestParams()
	chain.MinCount = 200

	params := StatTestParamsFromChain(chain)
	require.Equal(t, uint32(200), params.MinCount)

	raw, err := json.Marshal(params)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"min_count":200`)

	raw, err = json.Marshal(StatTestParamsFromChain(types.DefaultPoCStatTestParams()))
	require.NoError(t, err)
	require.NotContains(t, string(raw), "min_count")
}

func TestValidatedResultReadsInsufficientSample(t *testing.T) {
	var result ValidatedResultV2
	require.NoError(t, json.Unmarshal([]byte(`{"n_total":4,"fraud_detected":false,"insufficient_sample":true}`), &result))
	require.True(t, result.InsufficientSample)
}
