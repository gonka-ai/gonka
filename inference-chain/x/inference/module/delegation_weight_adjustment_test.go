package inference

import (
	"testing"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func buildAdjustmentsForTest(
	t *testing.T,
	participants []*types.ActiveParticipant,
	dwc *DelegationWeightCalculator,
	eligibleModels []string,
	modes map[string]map[string]ParticipationMode,
	params DelegationAdjustmentParams,
	upcomingEpochIndex uint64,
	penaltyStartEpochByModel map[string]uint64,
) ([]*types.DelegationRewardPenalty, []*types.DelegationRewardTransfer) {
	t.Helper()
	acc := NewPenaltyAccumulator(participants)
	AccumulateDelegationPenalties(acc, dwc, eligibleModels, modes, params, upcomingEpochIndex, penaltyStartEpochByModel)
	rewardTransfers := BuildDelegationRewardTransfers(dwc, eligibleModels, modes, params, upcomingEpochIndex, penaltyStartEpochByModel)
	return acc.RewardPenalties(), rewardTransfers.Records()
}

func requireTransfer(t *testing.T, transfer *types.DelegationRewardTransfer, modelID, from, to string, share mathsdk.LegacyDec) {
	t.Helper()
	require.Equal(t, modelID, transfer.ModelId)
	require.Equal(t, from, transfer.From)
	require.Equal(t, to, transfer.To)
	got, err := transfer.Share.ToLegacyDec()
	require.NoError(t, err)
	require.True(t, share.Equal(got), "expected %s, got %s", share.String(), got.String())
}

func requirePenalty(t *testing.T, penalty *types.DelegationRewardPenalty, participant string, fraction mathsdk.LegacyDec) {
	t.Helper()
	require.Equal(t, participant, penalty.Participant)
	got, err := penalty.PenaltyFraction.ToLegacyDec()
	require.NoError(t, err)
	require.True(t, fraction.Equal(got), "expected %s, got %s", fraction.String(), got.String())
}

func TestAccumulateDelegationPenalties_NoOp(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyZeroDec(),
		NoParticipationPenalty: mathsdk.LegacyZeroDec(),
		DelegationShare:        mathsdk.LegacyZeroDec(),
	}

	penalties, transfers := buildAdjustmentsForTest(t, participants, dwc, []string{"model1"}, nil, params, 1, nil)
	require.Equal(t, int64(1000), participants[0].Weight)
	require.Empty(t, penalties)
	require.Empty(t, transfers)
}

func TestAccumulateDelegationPenalties_DirectNoPenalty(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	modes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeDirect},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyMustNewDecFromStr("0.1"),
		NoParticipationPenalty: mathsdk.LegacyMustNewDecFromStr("0.2"),
		DelegationShare:        mathsdk.LegacyMustNewDecFromStr("0.05"),
	}

	penalties, transfers := buildAdjustmentsForTest(t, participants, dwc, []string{"model1"}, modes, params, 1, nil)
	require.Equal(t, int64(1000), participants[0].Weight)
	require.Empty(t, penalties)
	require.Empty(t, transfers)
}

func TestAccumulateDelegationPenalties_RefusePenalty(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	modes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeRefuse},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyMustNewDecFromStr("0.15"),
		NoParticipationPenalty: mathsdk.LegacyZeroDec(),
		DelegationShare:        mathsdk.LegacyZeroDec(),
	}

	penalties, transfers := buildAdjustmentsForTest(t, participants, dwc, []string{"model1"}, modes, params, 1, nil)
	require.Equal(t, int64(1000), participants[0].Weight)
	require.Empty(t, transfers)
	require.Len(t, penalties, 1)
	requirePenalty(t, penalties[0], "alice", mathsdk.LegacyMustNewDecFromStr("0.15"))
}

func TestAccumulateDelegationPenalties_DelegateTransfer(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
		{Index: "bob", Weight: 500},
	}
	dwc := &DelegationWeightCalculator{
		Delegations: map[string]map[string]string{
			"model1": {"alice": "bob"},
		},
	}
	modes := map[string]map[string]ParticipationMode{
		"model1": {
			"alice": ModeDelegate,
			"bob":   ModeDirect,
		},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyZeroDec(),
		NoParticipationPenalty: mathsdk.LegacyZeroDec(),
		DelegationShare:        mathsdk.LegacyMustNewDecFromStr("0.1"),
	}

	penalties, transfers := buildAdjustmentsForTest(t, participants, dwc, []string{"model1"}, modes, params, 1, nil)

	// alice delegates 10% to bob
	require.Equal(t, int64(1000), participants[0].Weight)
	require.Equal(t, int64(500), participants[1].Weight)
	require.Empty(t, penalties)
	require.Len(t, transfers, 1)
	requireTransfer(t, transfers[0], "model1", "alice", "bob", mathsdk.LegacyMustNewDecFromStr("0.1"))
}

func TestBuildDelegationRewardTransfers_RewardOnlyDoesNotChangeConsensusWeight(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
		{Index: "bob", Weight: 500},
	}
	dwc := &DelegationWeightCalculator{
		Delegations: map[string]map[string]string{
			"model1": {"alice": "bob"},
		},
	}
	modes := map[string]map[string]ParticipationMode{
		"model1": {
			"alice": ModeDelegate,
			"bob":   ModeDirect,
		},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyZeroDec(),
		NoParticipationPenalty: mathsdk.LegacyZeroDec(),
		DelegationShare:        mathsdk.LegacyMustNewDecFromStr("0.1"),
	}

	acc := NewPenaltyAccumulator(participants)
	AccumulateDelegationPenalties(acc, dwc, []string{"model1"}, modes, params, 1, nil)
	rewardTransfers := BuildDelegationRewardTransfers(dwc, []string{"model1"}, modes, params, 1, nil)

	require.Equal(t, int64(1000), participants[0].Weight)
	require.Equal(t, int64(500), participants[1].Weight)
	require.Len(t, rewardTransfers.Records(), 1)
	requireTransfer(t, rewardTransfers.Records()[0], "model1", "alice", "bob", mathsdk.LegacyMustNewDecFromStr("0.1"))
}

func TestAccumulateDelegationPenalties_MissingRecipientDoesNotBurnTransfer(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{
		Delegations: map[string]map[string]string{
			"model1": {"alice": "bob"},
		},
	}
	modes := map[string]map[string]ParticipationMode{
		"model1": {
			"alice": ModeDelegate,
			"bob":   ModeDirect,
		},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyZeroDec(),
		NoParticipationPenalty: mathsdk.LegacyZeroDec(),
		DelegationShare:        mathsdk.LegacyMustNewDecFromStr("0.1"),
	}

	penalties, transfers := buildAdjustmentsForTest(t, participants, dwc, []string{"model1"}, modes, params, 1, nil)

	// bob is absent from the active participant set, so delegation_share
	// is recorded for settlement, where zero-rewardable receivers are not revived.
	require.Equal(t, int64(1000), participants[0].Weight)
	require.Empty(t, penalties)
	require.Len(t, transfers, 1)
	requireTransfer(t, transfers[0], "model1", "alice", "bob", mathsdk.LegacyMustNewDecFromStr("0.1"))
}

func TestAccumulateDelegationPenalties_TransferClampedByPenalty(t *testing.T) {
	// Penalties and transfers are recorded separately; settlement applies the
	// penalty before the transfer so the transfer reads post-penalty weight.
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
		{Index: "bob", Weight: 500},
	}
	dwc := &DelegationWeightCalculator{
		Delegations: map[string]map[string]string{
			"model2": {"alice": "bob"},
		},
	}
	modes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeRefuse, "bob": ModeDirect},
		"model2": {"alice": ModeDelegate, "bob": ModeDirect},
		"model3": {"alice": ModeRefuse, "bob": ModeDirect},
		"model4": {"alice": ModeRefuse, "bob": ModeDirect},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyMustNewDecFromStr("0.2"),
		NoParticipationPenalty: mathsdk.LegacyZeroDec(),
		DelegationShare:        mathsdk.LegacyMustNewDecFromStr("0.3"),
	}

	penalties, transfers := buildAdjustmentsForTest(t, participants, dwc, []string{"model1", "model2", "model3", "model4"}, modes, params, 1, nil)

	require.Equal(t, int64(1000), participants[0].Weight)
	require.Equal(t, int64(500), participants[1].Weight)
	require.Len(t, penalties, 1)
	requirePenalty(t, penalties[0], "alice", mathsdk.LegacyMustNewDecFromStr("0.6"))
	require.Len(t, transfers, 1)
	requireTransfer(t, transfers[0], "model2", "alice", "bob", mathsdk.LegacyMustNewDecFromStr("0.3"))
}

func TestAccumulateDelegationPenalties_TransferFullyClampedByPenalty(t *testing.T) {
	// When penalties consume all weight, transfer recipient gets nothing.
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
		{Index: "bob", Weight: 500},
	}
	dwc := &DelegationWeightCalculator{
		Delegations: map[string]map[string]string{
			"model2": {"alice": "bob"},
		},
	}
	modes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeRefuse, "bob": ModeDirect},
		"model2": {"alice": ModeDelegate, "bob": ModeDirect},
		"model3": {"alice": ModeRefuse, "bob": ModeDirect},
		"model4": {"alice": ModeRefuse, "bob": ModeDirect},
		"model5": {"alice": ModeRefuse, "bob": ModeDirect},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyMustNewDecFromStr("0.3"),
		NoParticipationPenalty: mathsdk.LegacyZeroDec(),
		DelegationShare:        mathsdk.LegacyMustNewDecFromStr("0.3"),
	}

	penalties, transfers := buildAdjustmentsForTest(t, participants, dwc, []string{"model1", "model2", "model3", "model4", "model5"}, modes, params, 1, nil)

	require.Equal(t, int64(1000), participants[0].Weight)
	require.Equal(t, int64(500), participants[1].Weight)
	require.Len(t, penalties, 1)
	requirePenalty(t, penalties[0], "alice", mathsdk.LegacyOneDec())
	require.Len(t, transfers, 1)
	requireTransfer(t, transfers[0], "model2", "alice", "bob", mathsdk.LegacyMustNewDecFromStr("0.3"))
}

func TestAccumulateDelegationPenalties_AdditiveAcrossGroups(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	modes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeNone},
		"model2": {"alice": ModeNone},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyZeroDec(),
		NoParticipationPenalty: mathsdk.LegacyMustNewDecFromStr("0.1"),
		DelegationShare:        mathsdk.LegacyZeroDec(),
	}

	penalties, transfers := buildAdjustmentsForTest(t, participants, dwc, []string{"model1", "model2"}, modes, params, 1, nil)

	require.Equal(t, int64(1000), participants[0].Weight)
	require.Empty(t, transfers)
	require.Len(t, penalties, 1)
	requirePenalty(t, penalties[0], "alice", mathsdk.LegacyMustNewDecFromStr("0.2"))
}

func TestUnifiedPenalties_DelegationAndBootstrap_Additive(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	delegationModes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeNone},
	}
	bootstrapModes := map[string]map[string]BootstrapPenaltyMode{
		"bootstrap1": {"alice": BootstrapPenaltyNone},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyZeroDec(),
		NoParticipationPenalty: mathsdk.LegacyMustNewDecFromStr("0.1"),
		DelegationShare:        mathsdk.LegacyZeroDec(),
	}

	// Unified accumulator: both sources feed into one accumulator
	acc := NewPenaltyAccumulator(participants)
	AccumulateDelegationPenalties(acc, dwc, []string{"model1"}, delegationModes, params, 1, nil)
	AccumulateBootstrapPenalties(acc, bootstrapModes, nil, params, 1, nil)
	penalties := acc.RewardPenalties()

	require.Equal(t, int64(1000), participants[0].Weight)
	require.Len(t, penalties, 1)
	requirePenalty(t, penalties[0], "alice", mathsdk.LegacyMustNewDecFromStr("0.2"))
}

func TestAccumulatePenalties_CappedAtOne(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	// 11 models, each adding 0.1 penalty = 1.1 total, capped at 1.0
	modes := make(map[string]map[string]ParticipationMode, 11)
	eligibleModels := make([]string, 11)
	for i := 0; i < 11; i++ {
		model := "model" + string(rune('a'+i))
		modes[model] = map[string]ParticipationMode{"alice": ModeNone}
		eligibleModels[i] = model
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyZeroDec(),
		NoParticipationPenalty: mathsdk.LegacyMustNewDecFromStr("0.1"),
		DelegationShare:        mathsdk.LegacyZeroDec(),
	}

	penalties, transfers := buildAdjustmentsForTest(t, participants, dwc, eligibleModels, modes, params, 1, nil)

	require.Equal(t, int64(1000), participants[0].Weight)
	require.Empty(t, transfers)
	require.Len(t, penalties, 1)
	requirePenalty(t, penalties[0], "alice", mathsdk.LegacyOneDec())
}

func TestResolveBootstrapPenaltyModes_PreEligibleFalse(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "direct", Weight: 50},
		{Index: "delegator", Weight: 40},
		{Index: "intender", Weight: 30},
		{Index: "none", Weight: 20},
	}
	reportByModel := map[string]*types.BootstrapModelPreEligibility{
		"bootstrap-model": {ModelId: "bootstrap-model", PreEligible: false},
	}
	delegations := map[string]map[string]string{
		"bootstrap-model": {"delegator": "direct"},
	}
	intents := map[string]map[string]bool{
		"bootstrap-model": {"intender": true},
	}
	directCommitters := map[string]map[string]bool{
		"bootstrap-model": {"direct": true},
	}

	modes := ResolveBootstrapPenaltyModes(participants, reportByModel, delegations, intents, directCommitters)
	require.Equal(t, BootstrapPenaltyDirect, modes["bootstrap-model"]["direct"])
	require.Equal(t, BootstrapPenaltyDelegate, modes["bootstrap-model"]["delegator"])
	require.Equal(t, BootstrapPenaltyIntentOK, modes["bootstrap-model"]["intender"])
	require.Equal(t, BootstrapPenaltyNone, modes["bootstrap-model"]["none"])
}

func TestResolveBootstrapPenaltyModes_PreEligibleTrue(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "direct", Weight: 50},
		{Index: "delegator", Weight: 40},
		{Index: "intender", Weight: 30},
		{Index: "none", Weight: 20},
	}
	reportByModel := map[string]*types.BootstrapModelPreEligibility{
		"bootstrap-model": {ModelId: "bootstrap-model", PreEligible: true},
	}
	delegations := map[string]map[string]string{
		"bootstrap-model": {"delegator": "direct"},
	}
	intents := map[string]map[string]bool{
		"bootstrap-model": {"intender": true},
	}
	directCommitters := map[string]map[string]bool{
		"bootstrap-model": {"direct": true},
	}

	modes := ResolveBootstrapPenaltyModes(participants, reportByModel, delegations, intents, directCommitters)
	require.Equal(t, BootstrapPenaltyDirect, modes["bootstrap-model"]["direct"])
	require.Equal(t, BootstrapPenaltyDelegate, modes["bootstrap-model"]["delegator"])
	require.Equal(t, BootstrapPenaltyIntentMissed, modes["bootstrap-model"]["intender"])
	require.Equal(t, BootstrapPenaltyNone, modes["bootstrap-model"]["none"])
}

func TestAccumulateBootstrapPenalties_MapsIntentMissedAndNone(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "direct", Weight: 100},
		{Index: "delegate", Weight: 80},
		{Index: "intent_missed", Weight: 50},
		{Index: "none", Weight: 50},
	}
	modes := map[string]map[string]BootstrapPenaltyMode{
		"bootstrap-model": {
			"direct":        BootstrapPenaltyDirect,
			"delegate":      BootstrapPenaltyDelegate,
			"intent_missed": BootstrapPenaltyIntentMissed,
			"none":          BootstrapPenaltyNone,
		},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyMustNewDecFromStr("0.25"),
		NoParticipationPenalty: mathsdk.LegacyMustNewDecFromStr("0.5"),
		DelegationShare:        mathsdk.LegacyZeroDec(),
	}

	acc := NewPenaltyAccumulator(participants)
	AccumulateBootstrapPenalties(acc, modes, nil, params, 1, nil)
	penalties := acc.RewardPenalties()

	require.Equal(t, int64(100), participants[0].Weight) // Direct: no penalty
	require.Equal(t, int64(80), participants[1].Weight)  // Delegate: no penalty
	require.Equal(t, int64(50), participants[2].Weight)  // IntentMissed: reward-only penalty
	require.Equal(t, int64(50), participants[3].Weight)  // None: reward-only penalty
	require.Len(t, penalties, 2)
	requirePenalty(t, penalties[0], "intent_missed", mathsdk.LegacyMustNewDecFromStr("0.5"))
	requirePenalty(t, penalties[1], "none", mathsdk.LegacyMustNewDecFromStr("0.5"))
}

func TestAccumulateBootstrapPenalties_DirectCommitterOnNonPreEligibleNotPenalized(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "direct", Weight: 100},
		{Index: "none", Weight: 100},
	}
	reportByModel := map[string]*types.BootstrapModelPreEligibility{
		"bootstrap-model": {ModelId: "bootstrap-model", PreEligible: false},
	}
	directCommitters := map[string]map[string]bool{
		"bootstrap-model": {"direct": true},
	}

	modes := ResolveBootstrapPenaltyModes(participants, reportByModel, nil, nil, directCommitters)
	require.Equal(t, BootstrapPenaltyDirect, modes["bootstrap-model"]["direct"])
	require.Equal(t, BootstrapPenaltyNone, modes["bootstrap-model"]["none"])

	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyZeroDec(),
		NoParticipationPenalty: mathsdk.LegacyMustNewDecFromStr("0.5"),
		DelegationShare:        mathsdk.LegacyZeroDec(),
	}
	acc := NewPenaltyAccumulator(participants)
	AccumulateBootstrapPenalties(acc, modes, nil, params, 1, nil)
	penalties := acc.RewardPenalties()

	require.Equal(t, int64(100), participants[0].Weight) // Direct: untouched
	require.Equal(t, int64(100), participants[1].Weight) // None: reward-only penalty
	require.Len(t, penalties, 1)
	requirePenalty(t, penalties[0], "none", mathsdk.LegacyMustNewDecFromStr("0.5"))
}

func TestAccumulateDelegationPenalties_MixedModesAcrossModels(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	modes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeRefuse},
		"model2": {"alice": ModeNone},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyMustNewDecFromStr("0.05"),
		NoParticipationPenalty: mathsdk.LegacyMustNewDecFromStr("0.1"),
		DelegationShare:        mathsdk.LegacyZeroDec(),
	}

	penalties, transfers := buildAdjustmentsForTest(t, participants, dwc, []string{"model1", "model2"}, modes, params, 1, nil)

	require.Equal(t, int64(1000), participants[0].Weight)
	require.Empty(t, transfers)
	require.Len(t, penalties, 1)
	requirePenalty(t, penalties[0], "alice", mathsdk.LegacyMustNewDecFromStr("0.15"))
}

func TestAccumulateDelegationPenalties_SkipsUntilPenaltyStartEpoch(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	modes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeRefuse},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyMustNewDecFromStr("0.1"),
		NoParticipationPenalty: mathsdk.LegacyMustNewDecFromStr("0.2"),
		DelegationShare:        mathsdk.LegacyMustNewDecFromStr("0.05"),
	}

	penalties, transfers := buildAdjustmentsForTest(t, participants, dwc, []string{"model1"}, modes, params, 4, map[string]uint64{"model1": 5})
	require.Equal(t, int64(1000), participants[0].Weight)
	require.Empty(t, penalties)
	require.Empty(t, transfers)
}

func TestAccumulateDelegationPenalties_UsesUpcomingEpochIndexForPenaltyStart(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "alice", Weight: 1000},
	}
	dwc := &DelegationWeightCalculator{}
	modes := map[string]map[string]ParticipationMode{
		"model1": {"alice": ModeRefuse},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyMustNewDecFromStr("0.1"),
		NoParticipationPenalty: mathsdk.LegacyZeroDec(),
		DelegationShare:        mathsdk.LegacyZeroDec(),
	}

	penalties, transfers := buildAdjustmentsForTest(t, participants, dwc, []string{"model1"}, modes, params, 5, map[string]uint64{"model1": 5})
	require.Equal(t, int64(1000), participants[0].Weight)
	require.Empty(t, transfers)
	require.Len(t, penalties, 1)
	requirePenalty(t, penalties[0], "alice", mathsdk.LegacyMustNewDecFromStr("0.1"))
}

func TestAccumulateBootstrapPenalties_SkipsUntilPenaltyStartEpoch(t *testing.T) {
	participants := []*types.ActiveParticipant{
		{Index: "none", Weight: 50},
	}
	modes := map[string]map[string]BootstrapPenaltyMode{
		"bootstrap-model": {
			"none": BootstrapPenaltyNone,
		},
	}
	params := DelegationAdjustmentParams{
		RefusalPenalty:         mathsdk.LegacyZeroDec(),
		NoParticipationPenalty: mathsdk.LegacyMustNewDecFromStr("0.5"),
		DelegationShare:        mathsdk.LegacyZeroDec(),
	}

	acc := NewPenaltyAccumulator(participants)
	AccumulateBootstrapPenalties(acc, modes, nil, params, 4, map[string]uint64{"bootstrap-model": 5})
	penalties := acc.RewardPenalties()

	require.Equal(t, int64(50), participants[0].Weight)
	require.Empty(t, penalties)
}
