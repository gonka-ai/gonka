package types

import (
	"strings"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/stretchr/testify/require"
)

func TestCanonicalAccAddress_RejectsUppercase(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	canonical := sdk.AccAddress([]byte("0123456789abcdefghij")).String()
	upper := strings.ToUpper(canonical)
	decoded, err := sdk.AccAddressFromBech32(upper)
	require.NoError(t, err)
	require.True(t, decoded.Equals(sdk.MustAccAddressFromBech32(canonical)))

	_, err = CanonicalAccAddress(upper)
	require.ErrorIs(t, err, sdkerrors.ErrInvalidAddress)
	got, err := CanonicalAccAddress(canonical)
	require.NoError(t, err)
	require.Equal(t, canonical, got.String())
}

func TestEpochValidationDefaultsStayDistinct(t *testing.T) {
	got := DefaultEpochValidationParams()
	require.Equal(t, uint32(256), got.MaxPositiveDistributionRows)
	require.Equal(t, uint32(128), got.MaxValidationsPerMessage)
	require.Equal(t, uint32(256), got.MaxValidationsPerSignerPerStage)
	require.Equal(t, 256, got.PositiveDistributionRowLimit())
	require.Equal(t, 128, got.ValidationsPerMessageLimit())
	require.Equal(t, 256, got.ValidationsPerSignerPerStageLimit())
	require.NotEqual(t, got.ValidationsPerMessageLimit(), got.PositiveDistributionRowLimit())
	require.NotEqual(t, got.ValidationsPerMessageLimit(), got.ValidationsPerSignerPerStageLimit())
	require.Equal(t, 65536, MaxMLNodeWeightRawRowsPerEntry)

	var missing *EpochValidationParams
	require.Equal(t, DefaultMaxPositiveDistributionRows, missing.PositiveDistributionRowLimit())
	require.Equal(t, MaxValidationsPerMessage, missing.ValidationsPerMessageLimit())
	require.Equal(t, MaxValidationsPerSignerPerStage, missing.ValidationsPerSignerPerStageLimit())
}

func TestCanonicalMLNodeWeightEntries_DropsZerosAndRejectsEmptyShape(t *testing.T) {
	original := []*MLNodeDistributionEntry{{
		ModelId: "model-a",
		Weights: []*MLNodeWeight{
			{NodeId: "a", Weight: 0},
			{NodeId: "b", Weight: 33},
			{NodeId: "c", Weight: 33},
			{NodeId: "d", Weight: 9934},
		},
	}}
	got, err := CanonicalMLNodeWeightEntries(original)
	require.NoError(t, err)
	require.Equal(t, []*MLNodeWeight{
		{NodeId: "b", Weight: 33},
		{NodeId: "c", Weight: 33},
		{NodeId: "d", Weight: 9934},
	}, got[0].Weights)
	require.Equal(t, uint32(0), original[0].Weights[0].Weight, "canonicalization must not mutate the decoded message")

	_, err = CanonicalMLNodeWeightEntries([]*MLNodeDistributionEntry{{
		ModelId: "model-a",
		Weights: []*MLNodeWeight{{}},
	}})
	require.Error(t, err)

	_, err = CanonicalMLNodeWeightEntries([]*MLNodeDistributionEntry{{
		ModelId: "model-a",
		Weights: []*MLNodeWeight{{NodeId: "only-zero"}},
	}})
	require.Error(t, err)

	_, err = CanonicalMLNodeWeightEntries([]*MLNodeDistributionEntry{
		nil,
	})
	require.Error(t, err)

	longID := strings.Repeat("n", MaxMLNodeWeightNodeIDBytes+1)
	_, err = CanonicalMLNodeWeightEntries([]*MLNodeDistributionEntry{{
		ModelId: "model-a",
		Weights: []*MLNodeWeight{{NodeId: longID, Weight: 1}},
	}})
	require.Error(t, err)

	_, err = CanonicalMLNodeWeightEntries([]*MLNodeDistributionEntry{
		{ModelId: "model-a", Weights: []*MLNodeWeight{{NodeId: "n", Weight: 1}}},
		{ModelId: "model-a", Weights: []*MLNodeWeight{{NodeId: "m", Weight: 1}}},
	})
	require.Error(t, err)

	_, err = CanonicalMLNodeWeightEntries([]*MLNodeDistributionEntry{{
		ModelId: "model-a",
		Weights: []*MLNodeWeight{
			{NodeId: "same", Weight: 0},
			{NodeId: "same", Weight: 1},
		},
	}})
	require.Error(t, err)
}

func TestValidateRawBounds_BeforeStructuralMaps(t *testing.T) {
	msg := &MsgMLNodeWeightDistribution{
		Entries: make([]*MLNodeDistributionEntry, MaxMLNodeWeightEntriesPerMessage+1),
	}
	err := msg.ValidateRawBounds()
	require.Error(t, err)
	require.Error(t, msg.ValidateBasic())

	overEntry := &MsgMLNodeWeightDistribution{
		Entries: []*MLNodeDistributionEntry{{
			Weights: make([]*MLNodeWeight, MaxMLNodeWeightRawRowsPerEntry+1),
		}},
	}
	require.Error(t, overEntry.ValidateRawBounds())
}
