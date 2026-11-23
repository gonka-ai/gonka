package scheduler

import (
	"errors"
	"testing"
	"time"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
)

const addressB = "gonka1participantb"

// Test flow:
//  1. For each case, resolve a network config and per-model shadow lists, and run one request through a two-slot escrow of the named model whose first nonce lands on `addressB`, with `addressB` healthy or cut off.
//  2. Compare the burn forecast of that escrow, read from the same gates the drain uses, with what the drain did.
//  3. Assert a shadow `addressB` is probed, never ghost-burned nor served and never counted, and a `addressB` that is no shadow on that model is served at nonce 1 (or ghost-burned once while cut off), and that forecast and drain agree on the burns in every case.
func TestAParticipantIsShadowOnTheModelsWhoseListNamesIt(t *testing.T) {
	onlyA := []string{addressB}
	empty := []string{}
	cases := []struct {
		name    string
		network config.Participants
		models  map[string]config.ModelSettings
		model   string
		cutOff  bool
		shadow  bool
	}{
		{name: "shadow on A only, probed on A", models: map[string]config.ModelSettings{modelA: {Participants: &config.ParticipantOverrides{Shadow: &onlyA}}}, model: modelA, shadow: true},
		{name: "shadow on A only, served on B", models: map[string]config.ModelSettings{modelA: {Participants: &config.ParticipantOverrides{Shadow: &onlyA}}}, model: otherModel},
		{name: "network shadow, absent from B's list, probed on A", network: config.Participants{Shadow: []string{addressB}}, models: map[string]config.ModelSettings{otherModel: {Participants: &config.ParticipantOverrides{Shadow: &empty}}}, model: modelA, shadow: true},
		{name: "network shadow, absent from B's list, served on B", network: config.Participants{Shadow: []string{addressB}}, models: map[string]config.ModelSettings{otherModel: {Participants: &config.ParticipantOverrides{Shadow: &empty}}}, model: otherModel},
		{name: "shadow on A only and cut off, no burn on A", models: map[string]config.ModelSettings{modelA: {Participants: &config.ParticipantOverrides{Shadow: &onlyA}}}, model: modelA, cutOff: true, shadow: true},
		{name: "shadow on A only and cut off, burned on B", models: map[string]config.ModelSettings{modelA: {Participants: &config.ParticipantOverrides{Shadow: &onlyA}}}, model: otherModel, cutOff: true},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			settings := config.Defaults()
			settings.Scheduler.Participants = testCase.network
			if err := settings.ResolveModels(testCase.models); err != nil {
				t.Fatalf("ResolveModels() = %v", err)
			}
			prober := &recordingProber{}
			cfg := harnessConfig{slots: []string{hostA, addressB}, model: testCase.model, settings: &settings, prober: prober, cutOff: func(participant string) bool { return testCase.cutOff && participant == addressB }}
			forecast := expectedBurns(
				Escrow{ID: escrowA, Model: testCase.model, Session: &fakeSession{slots: []string{hostA, addressB}}},
				cfg.predicates(chain.PhaseSnapshot{}), newWaiter(RequestProfile{Model: testCase.model}, time.Time{}), 0, 0,
			)
			test := newHarness(t, cfg)

			result := awaitReply(t, test.submit(t, test.clock.Now()))
			test.dispatcher.stop()

			_, _, commits := test.session.report()
			burns := len(test.observer.burns())
			switch {
			case testCase.shadow:
				wantAssignment(t, result, hostA, 2)
				if len(commits) != 2 || commits[0].ghost || commits[0].participant != addressB || len(prober.probed()) != 1 {
					t.Errorf("commits = %+v, probes = %d, want a probe of %s then the served nonce", commits, len(prober.probed()), addressB)
				}
			case testCase.cutOff:
				wantAssignment(t, result, hostA, 2)
				if len(commits) != 2 || !commits[0].ghost || len(prober.probed()) != 0 {
					t.Errorf("commits = %+v, probes = %d, want a ghost for the cut-off %s then the served nonce", commits, len(prober.probed()), addressB)
				}
			default:
				wantAssignment(t, result, addressB, 1)
				if len(commits) != 1 || commits[0].ghost || len(prober.probed()) != 0 {
					t.Errorf("commits = %+v, probes = %d, want the one served nonce", commits, len(prober.probed()))
				}
			}
			if forecast.burns != burns || !forecast.usable {
				t.Errorf("forecast = %+v, drain burned %d, want them equal on a usable escrow", forecast, burns)
			}
		})
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
