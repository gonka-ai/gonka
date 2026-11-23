package scheduler

import (
	"errors"
	"testing"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
)

// Test flow:
//  1. Build a scheduler with an escrow of each of two models over the same groups and an allowlist, set only on the first model, that no group holds.
//  2. Pick an escrow for the first model and for the second.
//  3. Assert the first fails with `ErrAllowlistUnreachable` and the second is picked: the allowlist narrows one model's routing, not the other's.
func TestAnAllowlistThatNoEscrowReachesRefusesOnlyItsModel(t *testing.T) {
	groups := map[string][]string{"one": {"someone"}, "two": {"another"}}
	scheduler := schedulerWithAllowlist(t, nil, groups)
	escrows, weights := scheduler.escrows.(*fakeEscrows), scheduler.capacity.(*fakeWeights)
	for _, escrow := range escrows.byModel[modelA] {
		other := escrow
		other.ID, other.Model = escrow.ID+"-other", otherModel
		escrows.byModel[otherModel] = append(escrows.byModel[otherModel], other)
		weights.byEscrow[other.ID] = 1
	}
	settings, allowlist := config.Defaults(), []string{"gonka1nobodyholds"}
	if err := settings.ResolveModels(map[string]config.ModelSettings{modelA: {ParticipantAllowlist: &allowlist}}); err != nil {
		t.Fatalf("ResolveModels() = %v", err)
	}
	scheduler.settings = config.NewHolder(&settings)

	if _, err := pickWith(scheduler, RequestProfile{Model: modelA}, chain.PhaseSnapshot{}); !errors.Is(err, ErrAllowlistUnreachable) {
		t.Errorf("pickEscrow(first model) = %v, want ErrAllowlistUnreachable", err)
	}
	if picked, err := pickWith(scheduler, RequestProfile{Model: otherModel}, chain.PhaseSnapshot{}); err != nil {
		t.Errorf("pickEscrow(second model) = %v (%q), want an escrow", err, picked.ID)
	}
}

// Test flow:
//  1. Build a scheduler whose network allowlist is `gonka1net`, with one model replacing it, one clearing it with an empty list and one setting only an unthrottled list.
//  2. Read each model's allowlist and unthrottled list through the getters the drain and the pick share.
//  3. Assert a model's own list replaces the network one, an empty list clears it, and a model naming neither list reads the network ones.
func TestTheParticipantListsAreReadPerModel(t *testing.T) {
	t.Parallel()
	settings := config.Defaults()
	settings.Scheduler.ParticipantAllowlist = []string{"gonka1netwrk"}
	own, none, fast := []string{"gonka1ownlist"}, []string{}, []string{"gonka1fastone"}
	if err := settings.ResolveModels(map[string]config.ModelSettings{
		modelA:     {ParticipantAllowlist: &own},
		otherModel: {ParticipantAllowlist: &none},
		"model-c":  {UnthrottledParticipants: &fast},
	}); err != nil {
		t.Fatalf("ResolveModels() = %v", err)
	}
	probe := &Scheduler{settings: config.NewHolder(&settings)}

	for model, want := range map[string][]string{modelA: own, otherModel: none, "model-c": {"gonka1netwrk"}, "unnamed": {"gonka1netwrk"}} {
		if got := probe.participantAllowlist(model); len(got) != len(want) || (len(got) == 1 && got[0] != want[0]) {
			t.Errorf("participantAllowlist(%q) = %v, want %v", model, got, want)
		}
	}
	if got := probe.unthrottledParticipants("model-c"); len(got) != 1 || got[0] != "gonka1fastone" {
		t.Errorf("unthrottledParticipants(model-c) = %v, want its own [gonka1fast]", got)
	}
	if got := probe.unthrottledParticipants(modelA); len(got) != 0 {
		t.Errorf("unthrottledParticipants(modelA) = %v, want the network's none", got)
	}
}
