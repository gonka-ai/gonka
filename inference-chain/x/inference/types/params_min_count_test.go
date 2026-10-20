package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMinCommitCount(t *testing.T) {
	p := DefaultPocParams()
	model := DefaultPoCModelConfig()
	model.ModelId = "model-a"
	model.StatTest.MinCount = 150
	p.Models = []*PoCModelConfig{model}

	require.Equal(t, uint32(150), p.MinCommitCount("model-a"))
	require.Zero(t, p.MinCommitCount("model-b"))

	var nilParams *PocParams
	require.Zero(t, nilParams.MinCommitCount("model-a"))
	require.Equal(t, int64(DefaultPocValidationSampleSize), nilParams.EffectiveValidationSampleSize())
}

func TestDefaultParamsHaveNoMinCount(t *testing.T) {
	p := DefaultPocParams()
	require.Zero(t, p.GetStatTest().GetMinCount())
	for _, model := range p.Models {
		require.Zero(t, model.GetStatTest().GetMinCount())
	}
	require.NoError(t, p.Validate())
}
