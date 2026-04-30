package inference

import (
	"testing"

	mathsdk "cosmossdk.io/math"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func applyDelegationPenalties(
	participants []*types.ActiveParticipant,
	dwc *DelegationWeightCalculator,
	eligibleModels []string,
	modes map[string]map[string]ParticipationMode,
	params DelegationAdjustmentParams,
	upcomingEpochIndex uint64,
	penaltyStartEpochByModel map[string]uint64,
) {
	acc := NewPenaltyAccumulator(participants)
	AccumulateDelegationPenalties(acc, dwc, eligibleModels, modes, params, upcomingEpochIndex, penaltyStartEpochByModel)
	acc.Apply(participants)
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

	applyDelegationPenalties(participants, dwc, []string{"model1"}, nil, params, 1, nil)
	require.Equal(t, int64(1000), participants[0].Weight)
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

	applyDelegationPenalties(participants, dwc, []string{"model1"}, modes, params, 1, nil)
	require.Equal(t, int64(1000), participants[0].Weight)
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

	applyDelegationPenalties(participants, dwc, []string{"model1"}, modes, params, 1, nil)
	require.Equal(t, int64(850), participants[0].Weight)
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

	applyDelegationPenalties(participants, dwc, []string{"model1"}, modes, params, 1, nil)

	// alice delegates 10% to bob
	require.Equal(t, int64(900), participants[0].Weight)
	require.Equal(t, int64(600), participants[1].Weight)
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

	applyDelegationPenalties(participants, dwc, []string{"model1"}, modes, params, 1, nil)

	// bob is absent from the active participant set, so delegation_share
	// must be skipped rather than burned.
	require.Equal(t, int64(1000), participants[0].Weight)
}

func TestAccumulateDelegationPenalties_TransferClampedByPenalty(t *testing.T) {
	// When penalties reduce weight below the transfer delta, the recipient
	// should only receive what remains -- not the full original-weight-based delta.
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

	applyDelegationPenalties(participants, dwc, []string{"model1", "model2", "model3", "model4"}, modes, params, 1, nil)

	// Penalty: 3 refusals * 0.2 = 0.6, deducts 600 from 1000 -> 400 remaining
	// Transfer: 0.3 * 1000 = 300 desired, but only 400 available -> transfers 300
	// Alice: 1000 - 600 - 300 = 100
	// Bob: 500 + 300 = 800
	require.Equal(t, int64(100), participants[0].Weight)
	require.Equal(t, int64(800), participants[1].Weight)

	// Verify weight conservation: total before = 1500, penalty destroys 600
	// total after should be 1500 - 600 = 900
	require.Equal(t, int64(900), participants[0].Weight+participants[1].Weight)
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

	applyDelegationPenalties(participants, dwc, []string{"model1", "model2", "model3", "model4", "model5"}, modes, params, 1, nil)

	// Penalty: 4 refusals * 0.3 = 1.2, capped at 1.0, deducts 1000 -> 0 remaining
	// Transfer: 0.3 * 1000 = 300 desired, but 0 available -> transfers 0
	// Alice: 0
	// Bob: 500 + 0 = 500
	require.Equal(t, int64(0), participants[0].Weight)
	require.Equal(t, int64(500), participants[1].Weight)
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

	applyDelegationPenalties(participants, dwc, []string{"model1", "model2"}, modes, params, 1, nil)

	// Additive: penalty = 0.1 + 0.1 = 0.2, result = 1000 * (1 - 0.2) = 800
	require.Equal(t, int64(800), participants[0].Weight)
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
	acc.Apply(participants)

	// Additive: 0.1 (delegation) + 0.1 (bootstrap) = 0.2, result = 1000 * 0.8 = 800
	require.Equal(t, int64(800), participants[0].Weight)
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

	applyDelegationPenalties(participants, dwc, eligibleModels, modes, params, 1, nil)

	// 11 * 0.1 = 1.1, capped at 1.0, weight -> 0
	require.Equal(t, int64(0), participants[0].Weight)
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
	acc.Apply(participants)

	require.Equal(t, int64(100), participants[0].Weight) // Direct: no penalty
	require.Equal(t, int64(80), participants[1].Weight)  // Delegate: no penalty
	require.Equal(t, int64(25), participants[2].Weight)  // IntentMissed: no_participation_penalty 50*0.5=25
	require.Equal(t, int64(25), participants[3].Weight)  // None: no_participation_penalty 50*0.5=25
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
	acc.Apply(participants)

	require.Equal(t, int64(100), participants[0].Weight) // Direct: untouched
	require.Equal(t, int64(50), participants[1].Weight)  // None: 100*0.5=50
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

	applyDelegationPenalties(participants, dwc, []string{"model1", "model2"}, modes, params, 1, nil)

	// Additive: 0.05 (refuse) + 0.1 (none) = 0.15, result = 1000 * 0.85 = 850
	require.Equal(t, int64(850), participants[0].Weight)
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

	applyDelegationPenalties(participants, dwc, []string{"model1"}, modes, params, 4, map[string]uint64{"model1": 5})
	require.Equal(t, int64(1000), participants[0].Weight)
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

	applyDelegationPenalties(participants, dwc, []string{"model1"}, modes, params, 5, map[string]uint64{"model1": 5})
	require.Equal(t, int64(900), participants[0].Weight)
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
	acc.Apply(participants)

	require.Equal(t, int64(50), participants[0].Weight)
}
