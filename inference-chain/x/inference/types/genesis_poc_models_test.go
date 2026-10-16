package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPrepareGenesisPocModels_ReplacesEmptyPlaceholder(t *testing.T) {
	params := DefaultParams()
	PrepareGenesisPocModels(&params, []string{"Qwen/Qwen2.5-7B-Instruct", "Qwen/QwQ-32B"})

	require.NoError(t, params.Validate())
	require.Len(t, params.PocParams.Models, 2)
	require.Equal(t, "Qwen/Qwen2.5-7B-Instruct", params.PocParams.Models[0].ModelId)
	require.Equal(t, "Qwen/QwQ-32B", params.PocParams.Models[1].ModelId)

	var shares uint64
	for _, model := range params.PocParams.Models {
		block, ok := model.SchemeParams(PocScheme_POC_SCHEME_PREFILL)
		require.True(t, ok)
		require.Equal(t, int64(1024), block.SeqLen)
		require.Equal(t, int64(0), block.MaxTokens)
		require.NotNil(t, block.StatTest)
		require.NotNil(t, block.DynamicCoefficient)
		shares += uint64(block.DynamicCoefficient.TargetShareBps)
		require.Equal(t, int64(0), model.SeqLen)
		require.Nil(t, model.DynamicCoefficient)
	}
	require.Equal(t, uint64(10000), shares)
}

func TestPrepareGenesisPocModels_KeepsExistingShares(t *testing.T) {
	params := DefaultParams()
	custom := DecimalFromFloat(2.5)
	params.PocParams.Models = []*PoCModelConfig{{
		ModelId: "kept",
		Schemes: []*PocSchemeParams{{
			Scheme:    PocScheme_POC_SCHEME_PREFILL,
			SeqLen:    1024,
			MaxTokens: 0,
			StatTest:  DefaultPoCStatTestParams(),
			DynamicCoefficient: &DynamicCoefficientModelConfig{
				CoeffMin:           custom,
				CoeffMax:           custom,
				RelativeDifficulty: DecimalFromFloat(1),
				TargetShareBps:     10000,
			},
		}},
	}}

	PrepareGenesisPocModels(&params, []string{"kept"})

	require.NoError(t, params.Validate())
	block, ok := params.PocParams.Models[0].SchemeParams(PocScheme_POC_SCHEME_PREFILL)
	require.True(t, ok)
	require.Equal(t, int64(1024), block.SeqLen)
	require.Equal(t, int64(custom.Value), block.DynamicCoefficient.CoeffMin.Value)
	require.Equal(t, uint32(10000), block.DynamicCoefficient.TargetShareBps)
}
