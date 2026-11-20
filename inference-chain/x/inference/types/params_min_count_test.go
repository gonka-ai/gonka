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
	for _, block := range model.Schemes {
		block.StatTest.MinCount = minCount
	}
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
		for _, block := range model.Schemes {
			require.Zero(t, block.GetStatTest().GetMinCount())
		}
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

func TestPocParamsValidateRejectsFlatMinCountWithSchemes(t *testing.T) {
	p := pocParamsWithMinCount(200, 0)
	p.Models[0].StatTest = DefaultPoCStatTestParams()
	p.Models[0].StatTest.MinCount = 100
	require.Error(t, p.Validate())
}

func TestMinCommitCountReadsActiveScheme(t *testing.T) {
	p := pocParamsWithMinCount(200, 0)
	for _, block := range p.Models[0].Schemes {
		if block.Scheme == PocScheme_POC_SCHEME_DECODE {
			block.StatTest.MinCount = 120
		} else {
			block.StatTest.MinCount = 150
		}
	}
	require.NoError(t, p.Validate())
	p.PocScheme = PocScheme_POC_SCHEME_PREFILL
	require.Equal(t, uint32(150), p.MinCommitCount("model-a"))
	p.PocScheme = PocScheme_POC_SCHEME_DECODE
	require.Equal(t, uint32(120), p.MinCommitCount("model-a"))
}
