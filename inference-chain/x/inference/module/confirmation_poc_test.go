package inference

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/require"
)

// Review-3 verification scenario: 1 participant with 2 MLNodes (weights 10 and 1).
// Rotating preserved across confirmation events. Honest at every event -> reward
// stays at the full-weight reading of 11. A dishonest event collapses it exactly.
func TestFoldEventReadings_RotatingPreservedHonestThenDishonest(t *testing.T) {
	addr := "participant1"
	initial := &types.EpochGroupData{
		EpochIndex: 1,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: addr, Weight: 11, ConfirmationWeight: 11},
		},
	}

	// Event 1: preserved = {node-A(10)}. Participating = {node-B(1)}. Honest -> measured 1.
	// reading = 10 + 1 = 11 = initial; no change.
	updated, ratios := foldEventReadings(
		initial,
		map[string]int64{addr: 1},  // measured from node-B
		map[string]int64{addr: 10}, // preservedHere
		map[string]int64{addr: 11}, // formation-time expected
	)
	require.False(t, updated, "honest reading equal to initial must not lower ConfirmationWeight")
	require.Equal(t, int64(11), initial.ValidationWeights[0].ConfirmationWeight)
	requireRatioEqual(t, ratios[addr], 1, 1)

	// Event 2: preserved rotated -> {node-B(1)}. Participating = {node-A(10)}. Honest -> measured 10.
	// reading = 1 + 10 = 11; still no change.
	updated, ratios = foldEventReadings(
		initial,
		map[string]int64{addr: 10},
		map[string]int64{addr: 1},
		map[string]int64{addr: 11},
	)
	require.False(t, updated, "honest reading with rotated preservation must also stay at 11")
	require.Equal(t, int64(11), initial.ValidationWeights[0].ConfirmationWeight)
	requireRatioEqual(t, ratios[addr], 1, 1)

	// Event 3: preserved = {node-B(1)}. Participating node-A(10) cheats -> measured 4.
	// reading = 1 + 4 = 5; ConfirmationWeight drops to 5.
	updated, ratios = foldEventReadings(
		initial,
		map[string]int64{addr: 4},
		map[string]int64{addr: 1},
		map[string]int64{addr: 11},
	)
	require.True(t, updated, "dishonest event must lower ConfirmationWeight")
	require.Equal(t, int64(5), initial.ValidationWeights[0].ConfirmationWeight)
	// Slashing ratio at this event: reading/totalExpected = 5/11 ~= 0.45;
	// divided by pocDeviationCoeff(0.909) ~= 0.50.
	slashRatio := ratios[addr].ToDecimal()
	require.True(t, slashRatio.LessThan(decimal.NewFromFloat(0.6)), "slashing must kick in on dishonest event")
	require.True(t, slashRatio.GreaterThan(decimal.NewFromFloat(0.4)), "slashing ratio matches reading/totalExpected with deviation coeff")
}

// Honest operation with all-preserved for a participant: measured = 0 but
// reading = preserved, which must not cause the participant to be slashed or
// lose their ConfirmationWeight.
func TestFoldEventReadings_AllPreservedZeroMeasuredIsNotPenalized(t *testing.T) {
	addr := "participant1"
	ege := &types.EpochGroupData{
		EpochIndex: 1,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: addr, Weight: 100, ConfirmationWeight: 100},
		},
	}

	updated, ratios := foldEventReadings(
		ege,
		map[string]int64{addr: 0},   // participant submitted nothing for this event
		map[string]int64{addr: 100}, // every one of their nodes was preserved this event
		map[string]int64{addr: 100},
	)

	require.False(t, updated)
	require.Equal(t, int64(100), ege.ValidationWeights[0].ConfirmationWeight)
	requireRatioEqual(t, ratios[addr], 1, 1)
}

// No expected confirmation at all results in no ratio write and no change to
// ConfirmationWeight, because this event has no enforceable weight for the
// participant.
func TestFoldEventReadings_EmptyEventKeepsRatioAtOne(t *testing.T) {
	addr := "participant1"
	ege := &types.EpochGroupData{
		EpochIndex: 1,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: addr, Weight: 50, ConfirmationWeight: 50},
		},
	}

	updated, ratios := foldEventReadings(
		ege,
		map[string]int64{},
		map[string]int64{},
		map[string]int64{},
	)

	require.False(t, updated)
	require.Equal(t, int64(50), ege.ValidationWeights[0].ConfirmationWeight)
	require.NotContains(t, ratios, addr)
}

func TestConfirmationScalesInSnapshot(t *testing.T) {
	scales := []*types.ConfirmationWeightScale{
		{ModelId: "model-a", WeightScaleFactor: types.DecimalFromFloat(1)},
		{ModelId: "model-b", WeightScaleFactor: types.DecimalFromFloat(2)},
		{ModelId: "model-c", WeightScaleFactor: types.DecimalFromFloat(3)},
	}
	got := confirmationScalesInSnapshot(scales, []*types.ModelVotingPowers{
		{ModelId: "model-c"},
		{ModelId: "model-a"},
		{ModelId: "extra-model"},
	})

	require.Equal(t, []*types.ConfirmationWeightScale{scales[0], scales[2]}, got)
}

func requireRatioEqual(t *testing.T, got *types.Decimal, numerator, denominator int64) {
	t.Helper()
	require.NotNil(t, got)
	expected := decimal.NewFromInt(numerator).Div(decimal.NewFromInt(denominator))
	require.True(t, got.ToDecimal().Equal(expected), "got=%s expected=%s", got.ToDecimal().String(), expected.String())
}
