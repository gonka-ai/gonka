package types

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestEpochRecipeAndConfirmationModes(t *testing.T) {
	for _, scheme := range []PocScheme{PocScheme_POC_SCHEME_PREFILL, PocScheme_POC_SCHEME_DECODE} {
		t.Run(scheme.String(), func(t *testing.T) {
			p := DefaultPocParams()
			p.PocScheme = scheme
			p.PocStrongerRngEnabled = true
			epoch, err := SnapshotPocStageRecipe(p, 100)
			require.NoError(t, err)
			require.False(t, epoch.Tracking)
			p.PocScheme = PocScheme_POC_SCHEME_DECODE
			p.PocStrongerRngEnabled = false
			p.Models[0].Schemes[1].MaxTokens = 99
			for sequence := uint64(0); sequence < 3; sequence++ {
				event, err := epoch.ConfirmationRecipe(&ConfirmationPoCEvent{TriggerHeight: 200 + int64(sequence), EventSequence: sequence})
				require.NoError(t, err)
				trial := scheme == PocScheme_POC_SCHEME_PREFILL && sequence == 0
				want := scheme
				if trial {
					want = PocScheme_POC_SCHEME_DECODE
				}
				require.Equal(t, want, event.Scheme)
				require.Equal(t, trial, event.Tracking)
				require.True(t, event.PocStrongerRngEnabled)
				require.Equal(t, 200+int64(sequence), event.StageHeight)
				require.Len(t, event.Models[0].Schemes, 1)
				block := event.Models[0].Schemes[0]
				if want == PocScheme_POC_SCHEME_DECODE {
					require.Equal(t, int64(256), block.MaxTokens)
				}
				block.SeqLen = 7
				original, _ := epoch.Models[0].SchemeParams(want)
				require.NotEqual(t, int64(7), original.SeqLen)
			}
			require.Equal(t, scheme, epoch.Scheme)
			require.Equal(t, int64(100), epoch.StageHeight)
		})
	}
}

func TestPocParamsValidateSchemeBlocks(t *testing.T) {
	p := DefaultPocParams()
	require.NoError(t, p.Validate())
	p.Models[0].Schemes = p.Models[0].Schemes[:1]
	require.ErrorContains(t, p.Validate(), "DECODE scheme block")
	_, err := SnapshotPocStageRecipe(p, 100)
	require.Error(t, err)
	p.PocScheme = PocScheme_POC_SCHEME_DECODE
	p.Models[0].Schemes = []*PocSchemeParams{DefaultDecodeScheme()}
	require.NoError(t, p.Validate(), "DECODE mode does not require PREFILL")
	p.Models[0].Schemes[0].MaxTokens = 0
	require.Error(t, p.Validate())
	p.Models[0].Schemes[0].MaxTokens = 256
	p.Models[0].Schemes = append(p.Models[0].Schemes, DefaultDecodeScheme())
	require.ErrorContains(t, p.Validate(), "duplicate")
	p.PocScheme = PocScheme(9)
	require.Error(t, p.Validate())
}

func TestDecodeDefaultsIndependent(t *testing.T) {
	first := DefaultDecodeScheme()
	second := DefaultDecodeScheme()
	require.Equal(t, int64(256), first.SeqLen)
	require.Equal(t, int64(256), first.MaxTokens)
	first.StatTest.PMismatch.Value = 999
	first.DynamicCoefficient.CoeffMin.Value = 999
	require.NotEqual(t, first.StatTest, second.StatTest)
	require.NotEqual(t, first.DynamicCoefficient, second.DynamicCoefficient)
}
