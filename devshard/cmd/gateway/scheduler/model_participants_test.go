package scheduler

import (
	"errors"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
)

const (
	addressB       = "gonka1participantb"
	thirdModel     = "model-third"
	unnamedModel   = "model-unnamed"
	networkTrust   = "gonka1networktrust"
	thirdModelFast = "gonka1thirdfast"
)

// Test flow:
//  1. For each case, resolve a network config with its network lists and per-model lists over the models in the case.
//  2. Read the host gates the drain and the pick use for each model and ask them about `addressB`.
//  3. Assert a model whose own shadow list names it, or that inherits a network shadow list naming it, shadows it; a model whose own list leaves it out does not; and a model-only list shadows nowhere else.
func TestAParticipantIsShadowOnTheModelsWhoseListNamesIt(t *testing.T) {
	onlyA, empty := []string{addressB}, []string{}
	cases := []struct {
		name    string
		network config.Participants
		models  map[string]config.ModelSettings
		shadow  map[string]bool
	}{
		{
			name:   "named under one model only",
			models: map[string]config.ModelSettings{modelA: {Participants: &config.ParticipantOverrides{Shadow: &onlyA}}},
			shadow: map[string]bool{modelA: true, otherModel: false, unnamedModel: false},
		},
		{
			name:    "named by the network, one model clears it",
			network: config.Participants{Shadow: []string{addressB}},
			models:  map[string]config.ModelSettings{otherModel: {Participants: &config.ParticipantOverrides{Shadow: &empty}}},
			shadow:  map[string]bool{modelA: true, otherModel: false, unnamedModel: true},
		},
		{
			name:    "named by the network, no model sets a list",
			network: config.Participants{Shadow: []string{addressB}},
			models:  map[string]config.ModelSettings{otherModel: {MatchWaitMS: new(int64)}},
			shadow:  map[string]bool{modelA: true, otherModel: true, unnamedModel: true},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			scheduler := schedulerWithTrust(t, nil, nil)
			settings := config.Defaults()
			settings.Scheduler.Participants = testCase.network
			if err := settings.ResolveModels(testCase.models); err != nil {
				t.Fatalf("ResolveModels() = %v", err)
			}
			scheduler.settings = config.NewHolder(&settings)

			for model, want := range testCase.shadow {
				gates := scheduler.fleetGates(model, chain.PhaseSnapshot{})
				if got := gates.shadowed != nil && gates.shadowed(addressB); got != want {
					t.Errorf("shadowed(%s) on %s = %v, want %v", addressB, model, got, want)
				}
			}
		})
	}
}

// Test flow:
//  1. Resolve a network trust list over a model that sets its own trust list, one that clears it and one that names a model-only unthrottled list.
//  2. Read the gates of each model and ask them about the network's trusted participant, an outsider and the model's fast participant.
//  3. Assert each model's own list replaces the network's, an empty list admits everybody, and a model that sets no trust list keeps the network's.
func TestTheTrustAndUnthrottledListsAreReadPerModel(t *testing.T) {
	own, none, fast := []string{"gonka1ownlist"}, []string{}, []string{thirdModelFast}
	settings := config.Defaults()
	settings.Scheduler.Participants = config.Participants{Trust: []string{networkTrust}}
	if err := settings.ResolveModels(map[string]config.ModelSettings{
		modelA:     {Participants: &config.ParticipantOverrides{Trust: &own}},
		otherModel: {Participants: &config.ParticipantOverrides{Trust: &none}},
		thirdModel: {Participants: &config.ParticipantOverrides{Unthrottled: &fast}},
	}); err != nil {
		t.Fatalf("ResolveModels() = %v", err)
	}
	scheduler := schedulerWithTrust(t, nil, nil)
	scheduler.settings = config.NewHolder(&settings)

	for _, probe := range []struct {
		model, participant string
		refused            bool
	}{
		{modelA, "gonka1ownlist", false},
		{modelA, networkTrust, true},
		{otherModel, "gonka1outsider", false},
		{thirdModel, networkTrust, false},
		{thirdModel, "gonka1outsider", true},
		{thirdModel, thirdModelFast, false},
		{unnamedModel, "gonka1outsider", true},
	} {
		gates := scheduler.fleetGates(probe.model, chain.PhaseSnapshot{})
		if got := gates.untrusted != nil && gates.untrusted(probe.participant); got != probe.refused {
			t.Errorf("untrusted(%s) on %s = %v, want %v", probe.participant, probe.model, got, probe.refused)
		}
	}
	if waives := scheduler.fleetGates(thirdModel, chain.PhaseSnapshot{}).unthrottled; waives == nil || !waives(thirdModelFast) {
		t.Errorf("unthrottled(%s) on %s is not waived, want the model's own list", thirdModelFast, thirdModel)
	}
	if waives := scheduler.fleetGates(modelA, chain.PhaseSnapshot{}).unthrottled; waives != nil {
		t.Errorf("modelA waives throttling, want the network's empty unthrottled list")
	}
}

// Test flow:
//  1. Build a scheduler with an escrow of each of two models over the same groups and a trust list, set only on the first model, that no group holds.
//  2. Pick an escrow for the first model and for the second.
//  3. Assert the first fails with `ErrTrustUnreachable` and the second is picked: the trust list narrows one model's routing, not the other's.
func TestATrustListThatNoEscrowReachesRefusesOnlyItsModel(t *testing.T) {
	groups := map[string][]string{"one": {"someone"}, "two": {"another"}}
	scheduler := schedulerWithTrust(t, nil, groups)
	escrows, weights := scheduler.escrows.(*fakeEscrows), scheduler.capacity.(*fakeWeights)
	for _, escrow := range escrows.byModel[modelA] {
		other := escrow
		other.ID, other.Model = escrow.ID+"-other", otherModel
		escrows.byModel[otherModel] = append(escrows.byModel[otherModel], other)
		weights.byEscrow[other.ID] = 1
	}
	settings, trust := config.Defaults(), []string{"gonka1nobodyholds"}
	if err := settings.ResolveModels(map[string]config.ModelSettings{modelA: {Participants: &config.ParticipantOverrides{Trust: &trust}}}); err != nil {
		t.Fatalf("ResolveModels() = %v", err)
	}
	scheduler.settings = config.NewHolder(&settings)

	if _, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{}); !errors.Is(err, ErrTrustUnreachable) {
		t.Errorf("pickEscrow(first model) = %v, want ErrTrustUnreachable", err)
	}
	if picked, err := pickWith(scheduler, RequestProfile{Model: otherModel}, chain.PhaseSnapshot{}); err != nil {
		t.Errorf("pickEscrow(second model) = %v (%q), want an escrow", err, picked.ID)
	}
}
