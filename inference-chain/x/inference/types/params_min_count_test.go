package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func pocParamsWithMinCount(sampleSize int32, minCount uint32) *PocParams {
	p := DefaultPocParams()
	p.ValidationSampleSize = sampleSize
	model := DefaultPoCModelConfig()
	model.ModelId = "model-a"
	model.StatTest.MinCount = minCount
	p.Models = []*PoCModelConfig{model}
	return p
}

func TestPocParamsValidateMinCount(t *testing.T) {
	cases := []struct {
		name       string
		sampleSize int32
		minCount   uint32
		wantErr    bool
	}{
		{"disabled", 200, 0, false},
		{"equal to sample size", 200, 200, false},
		{"below sample size", 200, 64, false},
		{"above sample size", 200, 201, true},
		{"default sample size", 0, 200, false},
		{"above default sample size", 0, 201, true},
		{"negative sample size", -1, 1, true},
		{"negative sample size without min count", -1, 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := pocParamsWithMinCount(tc.sampleSize, tc.minCount).Validate()
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestPocParamsValidateRejectsDeprecatedMinCount(t *testing.T) {
	p := pocParamsWithMinCount(200, 0)
	p.StatTest = DefaultPoCStatTestParams()
	p.StatTest.MinCount = 100
	require.Error(t, p.Validate())
}

func TestDefaultParamsHaveNoMinCount(t *testing.T) {
	p := DefaultPocParams()
	require.Zero(t, p.GetStatTest().GetMinCount())
	for _, model := range p.Models {
		require.Zero(t, model.GetStatTest().GetMinCount())
	}
	require.NoError(t, p.Validate())
}

func TestMinCommitCount(t *testing.T) {
	p := pocParamsWithMinCount(200, 150)
	require.Equal(t, uint32(150), p.MinCommitCount("model-a"))
	require.Zero(t, p.MinCommitCount("model-b"))

	var nilParams *PocParams
	require.Zero(t, nilParams.MinCommitCount("model-a"))
	require.Equal(t, int64(DefaultPocValidationSampleSize), nilParams.EffectiveValidationSampleSize())
}
