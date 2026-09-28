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
				ModelId:  "m",
				SeqLen:   128,
				StatTest: DefaultPoCStatTestParams(),
				WeightScaleFactor: DecimalFromFloat(1),
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
	flat := &PoCModelConfig{StatTest: prefill}
	require.Equal(t, prefill, StatTestForScheme(PocScheme_POC_SCHEME_PREFILL, flat))
	require.Nil(t, StatTestForScheme(PocScheme_POC_SCHEME_DECODE, flat))
	withDecode := &PoCModelConfig{
		StatTest: prefill,
		Schemes: []*PocSchemeParams{
			{Scheme: PocScheme_POC_SCHEME_PREFILL, StatTest: prefill},
			{Scheme: PocScheme_POC_SCHEME_DECODE, StatTest: decode},
		},
	}
	require.Equal(t, decode, StatTestForScheme(PocScheme_POC_SCHEME_DECODE, withDecode))
}

func TestSnapshotPocStageRecipe_IsolatesFromLaterParamChanges(t *testing.T) {
	p := testSchemeParams(PocScheme_POC_SCHEME_PREFILL, PocScheme_POC_SCHEME_DECODE, 1)
	block := &PocSchemeParams{
		Scheme:            PocScheme_POC_SCHEME_DECODE,
		SeqLen:            256,
		MaxTokens:         256,
		StatTest:          &PoCStatTestParams{DistThreshold: DecimalFromFloat(0.03)},
		WeightScaleFactor: DecimalFromFloat(4),
	}
	p.Models[0].Schemes = []*PocSchemeParams{block}
	event := &ConfirmationPoCEvent{EventSequence: 0, TriggerHeight: 50}
	recipe, err := SnapshotPocStageRecipe(p, event, 50, 3, 0, false)
	require.NoError(t, err)
	require.Equal(t, PocScheme_POC_SCHEME_DECODE, recipe.Scheme)
	require.True(t, recipe.Tracking)
	require.True(t, recipe.PocStrongerRngEnabled)
	require.Equal(t, int64(256), recipe.Models[0].Schemes[0].MaxTokens)

	p.ConfirmationSchemeEvents = 0
	p.PocStrongerRngEnabled = false
	p.Models[0].Schemes[0].MaxTokens = 1
	require.Equal(t, PocScheme_POC_SCHEME_DECODE, recipe.Scheme, "live param change must not mutate a snapshot")
	require.True(t, recipe.Tracking)
	require.True(t, recipe.PocStrongerRngEnabled)
	require.Equal(t, int64(256), recipe.Models[0].Schemes[0].MaxTokens)
}

func TestSnapshotPocStageRecipe_RejectsMissingDecodeBlock(t *testing.T) {
	p := testSchemeParams(PocScheme_POC_SCHEME_PREFILL, PocScheme_POC_SCHEME_DECODE, 1)
	event := &ConfirmationPoCEvent{EventSequence: 0, TriggerHeight: 50}
	recipe, err := SnapshotPocStageRecipe(p, event, 50, 3, 0, false)
	require.Error(t, err)
	require.Nil(t, recipe)
}

func TestSnapshotPocStageRecipe_PrefillFromFlatFields(t *testing.T) {
	p := testSchemeParams(PocScheme_POC_SCHEME_PREFILL, PocScheme_POC_SCHEME_PREFILL, 0)
	recipe, err := SnapshotPocStageRecipe(p, nil, 50, 3, 0, false)
	require.NoError(t, err)
	require.Equal(t, PocScheme_POC_SCHEME_PREFILL, recipe.Scheme)
	require.Equal(t, int64(128), recipe.Models[0].Schemes[0].SeqLen)
	require.Equal(t, int64(0), recipe.Models[0].Schemes[0].MaxTokens)
}

func TestPocParamsValidate_DecodeRequiresSchemeBlock(t *testing.T) {
	p := testSchemeParams(PocScheme_POC_SCHEME_PREFILL, PocScheme_POC_SCHEME_PREFILL, 0)
	require.NoError(t, p.Validate(), "flat PREFILL stays valid")

	p.PocScheme = PocScheme_POC_SCHEME_DECODE
	require.Error(t, p.Validate(), "DECODE requires a scheme block")

	p.Models[0].Schemes = []*PocSchemeParams{{
		Scheme:    PocScheme_POC_SCHEME_DECODE,
		SeqLen:    256,
		MaxTokens: 256,
	}}
	require.NoError(t, p.Validate())

	p.Models[0].Schemes = append(p.Models[0].Schemes, &PocSchemeParams{
		Scheme:    PocScheme_POC_SCHEME_DECODE,
		SeqLen:    256,
		MaxTokens: 256,
	})
	require.Error(t, p.Validate(), "duplicate scheme")

	p.Models[0].Schemes = []*PocSchemeParams{{
		Scheme:    PocScheme(9),
		SeqLen:    256,
		MaxTokens: 256,
	}}
	require.Error(t, p.Validate(), "unknown scheme")

	p.PocScheme = PocScheme_POC_SCHEME_PREFILL
	p.Models[0].Schemes = []*PocSchemeParams{{
		Scheme:    PocScheme_POC_SCHEME_PREFILL,
		SeqLen:    128,
		MaxTokens: 4,
	}}
	require.Error(t, p.Validate(), "PREFILL max_tokens must be 0")
}

func TestPocParamsValidate_ActiveSlotsNeedBlocks(t *testing.T) {
	decodeOnly := []*PocSchemeParams{{
		Scheme:    PocScheme_POC_SCHEME_DECODE,
		SeqLen:    256,
		MaxTokens: 256,
	}}
	both := []*PocSchemeParams{
		{Scheme: PocScheme_POC_SCHEME_PREFILL, SeqLen: 128},
		{Scheme: PocScheme_POC_SCHEME_DECODE, SeqLen: 256, MaxTokens: 256},
	}

	p := testSchemeParams(PocScheme_POC_SCHEME_DECODE, PocScheme_POC_SCHEME_PREFILL, 1)
	p.Models[0].Schemes = decodeOnly
	require.Error(t, p.Validate(), "confirmation PREFILL still runs")

	p.Models[0].Schemes = both
	require.NoError(t, p.Validate())

	p.ConfirmationSchemeEvents = 0
	p.Models[0].Schemes = decodeOnly
	require.NoError(t, p.Validate(), "K=0 does not run the confirmation slot")

	p = testSchemeParams(PocScheme(9), PocScheme_POC_SCHEME_PREFILL, 0)
	require.Error(t, p.Validate(), "unknown regular scheme")

	p = testSchemeParams(PocScheme_POC_SCHEME_PREFILL, PocScheme(9), 1)
	require.Error(t, p.Validate(), "unknown confirmation scheme")

	p.ConfirmationSchemeEvents = 0
	require.NoError(t, p.Validate(), "inactive confirmation scheme is not read")
}

func TestSchemeBlockDecimalExponent(t *testing.T) {
	params := DefaultParams()
	params.PocParams.Models[0].Schemes = []*PocSchemeParams{{
		Scheme:            PocScheme_POC_SCHEME_PREFILL,
		SeqLen:            128,
		WeightScaleFactor: &Decimal{Value: 1, Exponent: MaxDecimalExponentAbs + 1},
	}}
	require.ErrorIs(t, params.Validate(), ErrInvalidDecimalExponent)
}
