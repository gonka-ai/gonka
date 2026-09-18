package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func testSchemeParams(poc, confirmation PocScheme, k uint32) *PocParams {
	return &PocParams{
		PocScheme:                poc,
		ConfirmationPocScheme:    confirmation,
		ConfirmationSchemeEvents: k,
		PocStrongerRngEnabled:    true,
		Models: []*PoCModelConfig{
			{
				ModelId:         "m",
				SeqLen:          128,
				DecodeMaxTokens: 256,
				StatTest:        DefaultPoCStatTestParams(),
				DecodeStatTest:  &PoCStatTestParams{DistThreshold: DecimalFromFloat(0.03)},
			},
		},
	}
}

func TestSchemeForStage(t *testing.T) {
	p := testSchemeParams(PocScheme_POC_SCHEME_PREFILL, PocScheme_POC_SCHEME_DECODE, 1)

	require.Equal(t, PocScheme_POC_SCHEME_PREFILL, SchemeForStage(p, nil), "regular PoC uses poc_scheme")
	require.Equal(t, PocScheme_POC_SCHEME_DECODE, SchemeForStage(p, &ConfirmationPoCEvent{EventSequence: 0}))
	require.Equal(t, PocScheme_POC_SCHEME_PREFILL, SchemeForStage(p, &ConfirmationPoCEvent{EventSequence: 1}))

	p.ConfirmationSchemeEvents = 0
	require.Equal(t, PocScheme_POC_SCHEME_PREFILL, SchemeForStage(p, &ConfirmationPoCEvent{EventSequence: 0}), "K=0 ignores slot 17")

	require.Equal(t, PocScheme_POC_SCHEME_PREFILL, SchemeForStage(nil, nil))
}

func TestIsSchemeTracking(t *testing.T) {
	p := testSchemeParams(PocScheme_POC_SCHEME_PREFILL, PocScheme_POC_SCHEME_DECODE, 1)
	require.False(t, IsSchemeTracking(p, nil))
	require.True(t, IsSchemeTracking(p, &ConfirmationPoCEvent{EventSequence: 0}))
	require.False(t, IsSchemeTracking(p, &ConfirmationPoCEvent{EventSequence: 1}))

	p.PocScheme = PocScheme_POC_SCHEME_DECODE
	p.ConfirmationSchemeEvents = 0
	require.False(t, IsSchemeTracking(p, &ConfirmationPoCEvent{EventSequence: 0}), "matching slots are enforced")
}

func TestIsSchemeTrackingWithGrace(t *testing.T) {
	p := testSchemeParams(PocScheme_POC_SCHEME_DECODE, PocScheme_POC_SCHEME_DECODE, 0)
	event := &ConfirmationPoCEvent{EventSequence: 0}
	require.False(t, IsSchemeTrackingWithGrace(p, event, 10, 9, true))
	require.True(t, IsSchemeTrackingWithGrace(p, event, 10, 10, true), "grace epoch dry-runs CPoC")
	require.False(t, IsSchemeTrackingWithGrace(p, nil, 10, 10, true))
}

func TestDecodeMaxForStage_NIsNotASwitch(t *testing.T) {
	require.Equal(t, int64(0), DecodeMaxForStage(PocScheme_POC_SCHEME_PREFILL, 256))
	require.Equal(t, int64(256), DecodeMaxForStage(PocScheme_POC_SCHEME_DECODE, 256))
}

func TestStatTestForScheme(t *testing.T) {
	prefill := DefaultPoCStatTestParams()
	decode := &PoCStatTestParams{DistThreshold: DecimalFromFloat(0.03)}
	model := &PoCModelConfig{StatTest: prefill, DecodeStatTest: decode}
	require.Equal(t, prefill, StatTestForScheme(PocScheme_POC_SCHEME_PREFILL, model))
	require.Equal(t, decode, StatTestForScheme(PocScheme_POC_SCHEME_DECODE, model))
	require.Equal(t, prefill, StatTestForScheme(PocScheme_POC_SCHEME_DECODE, &PoCModelConfig{StatTest: prefill}))
}

func TestSnapshotPocStageRecipe_IsolatesFromLaterParamChanges(t *testing.T) {
	p := testSchemeParams(PocScheme_POC_SCHEME_PREFILL, PocScheme_POC_SCHEME_DECODE, 1)
	event := &ConfirmationPoCEvent{EventSequence: 0, TriggerHeight: 50}
	recipe := SnapshotPocStageRecipe(p, event, 50, 3, 0, false)
	require.Equal(t, PocScheme_POC_SCHEME_DECODE, recipe.Scheme)
	require.True(t, recipe.Tracking)
	require.True(t, recipe.PocStrongerRngEnabled)
	require.Equal(t, int64(256), recipe.Models[0].DecodeMaxTokens)

	p.ConfirmationSchemeEvents = 0
	p.PocStrongerRngEnabled = false
	p.Models[0].DecodeMaxTokens = 1
	require.Equal(t, PocScheme_POC_SCHEME_DECODE, recipe.Scheme, "live param change must not mutate a snapshot")
	require.True(t, recipe.Tracking)
	require.True(t, recipe.PocStrongerRngEnabled)
	require.Equal(t, int64(256), recipe.Models[0].DecodeMaxTokens)
}

func TestPocParamsValidate_DecodeRequiresN(t *testing.T) {
	p := testSchemeParams(PocScheme_POC_SCHEME_PREFILL, PocScheme_POC_SCHEME_PREFILL, 0)
	require.NoError(t, p.Validate(), "PREFILL may keep N set")

	p.PocScheme = PocScheme_POC_SCHEME_DECODE
	p.Models[0].DecodeMaxTokens = 0
	require.Error(t, p.Validate())

	p.Models[0].DecodeMaxTokens = 256
	require.NoError(t, p.Validate())
}
