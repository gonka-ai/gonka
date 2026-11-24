package config

import (
	"slices"
	"strings"
	"testing"

	"devshard/cmd/gateway/env"
)

// Test flow:
//  1. Build a config with a `Participants.Shadow` override.
//  2. Assert the built scheduler's shadow list matches it and the other two lists stay empty.
func TestShadowListReachesTheSnapshot(t *testing.T) {
	t.Parallel()
	wanted := []string{"gonka1scskt"}

	built, err := Build(env.Values{}, Overrides{Participants: &ParticipantOverrides{Shadow: &wanted}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !slices.Equal(built.Scheduler.Participants.Shadow, wanted) {
		t.Errorf("shadow = %v, want %v", built.Scheduler.Participants.Shadow, wanted)
	}
	if len(built.Scheduler.Participants.Trust) != 0 || len(built.Scheduler.Participants.Unthrottled) != 0 {
		t.Errorf("trust = %v, unthrottled = %v, want both empty: naming a shadow list must route and waive nothing", built.Scheduler.Participants.Trust, built.Scheduler.Participants.Unthrottled)
	}
}

// Test flow:
//  1. Build a config whose shadow list names a participant that `trust` also names.
//  2. Assert `Build` refuses it and the error names the participant and both lists.
//  3. Repeat with `unthrottled` in place of `trust`.
//  4. Repeat with the shadow entry padded with space and assert it is still refused.
func TestShadowOverlappingARoutedListIsRefused(t *testing.T) {
	t.Parallel()
	overlap := []string{"gonka1scskt"}
	padded := []string{"  gonka1scskt "}
	cases := map[string]ParticipantOverrides{
		"trust":       {Shadow: &overlap, Trust: &overlap},
		"unthrottled": {Shadow: &overlap, Unthrottled: &overlap},
		"padded":      {Shadow: &padded, Trust: &overlap},
	}
	for name, participants := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := Build(env.Values{}, Overrides{Participants: &participants})

			if err == nil {
				t.Fatal("a participant both probed only and routed client traffic must be refused")
			}
			if !strings.Contains(err.Error(), "gonka1scskt") || !strings.Contains(err.Error(), "participants.shadow") {
				t.Errorf("the complaint must name the participant and the shadow list, got %v", err)
			}
		})
	}
}

// Test flow:
//  1. Build a config naming one participant in both `trust` and `unthrottled`.
//  2. Assert `Build` accepts it, as `trust` plus `unthrottled` is allowed.
func TestTrustAndUnthrottledMayNameTheSameParticipant(t *testing.T) {
	t.Parallel()
	both := []string{"gonka1scskt"}

	if _, err := Build(env.Values{}, Overrides{Participants: &ParticipantOverrides{Trust: &both, Unthrottled: &both}}); err != nil {
		t.Errorf("Build: %v, want nil", err)
	}
}

// Test flow:
//  1. Build a config with a `Participants.Shadow` entry that is a log line's host tail.
//  2. Assert `Build` refuses it and the error names the entry by its list and index.
func TestShadowEntryMustLookLikeAnAddress(t *testing.T) {
	t.Parallel()
	tailFromALogLine := []string{"zalyrmkx"}

	_, err := Build(env.Values{}, Overrides{Participants: &ParticipantOverrides{Shadow: &tailFromALogLine}})

	if err == nil || !strings.Contains(err.Error(), "participants.shadow[0]") {
		t.Errorf("err = %v, want a complaint naming participants.shadow[0]", err)
	}
}

// Test flow:
//  1. Start from stored participants naming trust and shadow, and patch only `shadow`.
//  2. Assert trust stays and shadow is replaced.
//  3. Patch `trust` to null and assert it is cleared while shadow stays.
//  4. Patch with an empty `participants` object and assert nothing changes.
//  5. Patch `participants` itself to null and assert the whole key is cleared.
func TestMergingParticipantsWorksPerSubKey(t *testing.T) {
	t.Parallel()
	trust, shadow := []string{"gonka1trust"}, []string{"gonka1shadow"}
	stored := Overrides{Participants: &ParticipantOverrides{Trust: &trust, Shadow: &shadow}}

	replaced, err := stored.Merged([]byte(`{"participants":{"shadow":["gonka1other"]}}`))
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if !slices.Equal(*replaced.Participants.Trust, trust) || !slices.Equal(*replaced.Participants.Shadow, []string{"gonka1other"}) {
		t.Fatalf("replaced = %v / %v, want trust kept and shadow replaced", *replaced.Participants.Trust, *replaced.Participants.Shadow)
	}

	cleared, err := replaced.Merged([]byte(`{"participants":{"trust":null}}`))
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if cleared.Participants.Trust != nil || cleared.Participants.Shadow == nil {
		t.Fatalf("cleared = %+v, want trust cleared and shadow kept", cleared.Participants)
	}

	untouched, err := cleared.Merged([]byte(`{"participants":{}}`))
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if untouched.Participants.Shadow == nil {
		t.Fatalf("untouched = %+v, want shadow kept", untouched.Participants)
	}

	wiped, err := untouched.Merged([]byte(`{"participants":null}`))
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}
	if wiped.Participants != nil {
		t.Errorf("wiped = %+v, want participants cleared", wiped.Participants)
	}
}

// Test flow:
//  1. Merge a patch naming `participants` with an unknown sub-key into empty overrides.
//  2. Assert it is refused: a typo inside `participants` must not pass as a no-op write.
func TestMergingParticipantsRefusesAnUnknownSubKey(t *testing.T) {
	t.Parallel()
	if _, err := (Overrides{}).Merged([]byte(`{"participants":{"shadows":["gonka1scskt"]}}`)); err == nil {
		t.Fatal("Merged() = nil, want an unknown-field error")
	}
}

// Test flow:
//  1. Merge a `participants` patch into empty overrides.
//  2. Assert the sub-keys it names are set and the others stay nil.
func TestMergingParticipantsIntoNothingSetsTheNamedSubKeys(t *testing.T) {
	t.Parallel()
	merged, err := (Overrides{}).Merged([]byte(`{"participants":{"unthrottled":["gonka1scskt"]}}`))
	if err != nil {
		t.Fatalf("Merged: %v", err)
	}

	if merged.Participants == nil || merged.Participants.Unthrottled == nil || merged.Participants.Trust != nil || merged.Participants.Shadow != nil {
		t.Errorf("participants = %+v, want only unthrottled set", merged.Participants)
	}
}

func participantsOf(model string, lists ParticipantOverrides) Overrides {
	return Overrides{Models: map[string]ModelSettings{model: {Participants: &lists}}}
}

// Test flow:
//  1. Build with the network shadowing one participant and trusting another, and model-a naming its own shadow list.
//  2. Assert model-a shadows only its own participant, not the network's (the list replaced, not joined), and keeps the network trust list.
//  3. Assert model-b and an unnamed model shadow the network's participant and not model-a's.
func TestAModelsShadowListReplacesTheNetworkOneForThatModelOnly(t *testing.T) {
	network, trusted, own := []string{"gonka1network"}, []string{"gonka1trusted"}, []string{"gonka1modela"}
	overrides := participantsOf("model-a", ParticipantOverrides{Shadow: &own})
	overrides.Participants = &ParticipantOverrides{Shadow: &network, Trust: &trusted}

	built, err := buildWithModels(t, nil, overrides)
	if err != nil {
		t.Fatalf("Build() = %v", err)
	}

	for _, probe := range []struct {
		model, participant string
		shadow             bool
	}{
		{"model-a", "gonka1modela", true},
		{"model-a", "gonka1network", false},
		{"model-b", "gonka1network", true},
		{"model-b", "gonka1modela", false},
		{"unnamed", "gonka1network", true},
	} {
		if got := namesParticipant(built.For(probe.model).Scheduler.Participants.Shadow, probe.participant); got != probe.shadow {
			t.Errorf("%s shadowed on %s = %v, want %v", probe.participant, probe.model, got, probe.shadow)
		}
	}
	if got := built.For("model-a").Scheduler.Participants.Trust; !slices.Equal(got, trusted) {
		t.Errorf("model-a trust = %v, want the network list %v", got, trusted)
	}
}

// Test flow:
//  1. Build with a model list that shadows a participant the network trusts, one that trusts a participant the network shadows, and one that waives throttling for the participant its own list shadows.
//  2. Assert each is refused with the `models[<id>].` prefix, and that a model list moving the participant out of the network's trust list is accepted.
func TestAModelListThatShadowsATrustedParticipantIsRefusedWithThePrefix(t *testing.T) {
	address := []string{"gonka1scskt"}
	empty := []string{}
	for name, testCase := range map[string]struct {
		network ParticipantOverrides
		model   ParticipantOverrides
		want    string
	}{
		"shadow in the network trust":    {network: ParticipantOverrides{Trust: &address}, model: ParticipantOverrides{Shadow: &address}, want: "models[model-a].participants.shadow"},
		"trust in the network shadow":    {network: ParticipantOverrides{Shadow: &address}, model: ParticipantOverrides{Trust: &address}, want: "models[model-a].participants.shadow"},
		"shadow and unthrottled of one":  {model: ParticipantOverrides{Shadow: &address, Unthrottled: &address}, want: "models[model-a].participants.shadow"},
		"blank entry in the model list":  {model: ParticipantOverrides{Shadow: &[]string{"label"}}, want: "models[model-a].participants.shadow[0]"},
		"the participant left the trust": {network: ParticipantOverrides{Trust: &address}, model: ParticipantOverrides{Shadow: &address, Trust: &empty}},
	} {
		t.Run(name, func(t *testing.T) {
			overrides := participantsOf("model-a", testCase.model)
			overrides.Participants = &testCase.network
			_, err := buildWithModels(t, nil, overrides)
			if testCase.want == "" {
				if err != nil {
					t.Fatalf("Build() = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Build() = %v, want a refusal naming %s", err, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Store a model block with a shadow and a trust list, and merge a patch naming only `participants.unthrottled` of that model.
//  2. Assert the stored shadow and trust lists stay and the unthrottled list is set.
//  3. Merge a null for `participants.shadow` and assert only that list clears, then a null for `participants` and assert the model's lists are all gone.
func TestMergingAModelsParticipantsWorksPerList(t *testing.T) {
	shadow, trust := []string{"gonka1shadow"}, []string{"gonka1trust"}
	stored := participantsOf("model-a", ParticipantOverrides{Shadow: &shadow, Trust: &trust})

	merged, err := stored.Merged([]byte(`{"models":{"model-a":{"participants":{"unthrottled":["gonka1fast"]}}}}`))
	if err != nil {
		t.Fatalf("Merged() = %v", err)
	}
	lists := merged.Models["model-a"].Participants
	if lists == nil || lists.Shadow == nil || lists.Trust == nil || lists.Unthrottled == nil || (*lists.Unthrottled)[0] != "gonka1fast" {
		t.Fatalf("model-a participants = %+v, want shadow and trust kept and unthrottled set", lists)
	}
	if stored.Models["model-a"].Participants.Unthrottled != nil {
		t.Error("the stored overrides changed under a merge")
	}

	partial, err := merged.Merged([]byte(`{"models":{"model-a":{"participants":{"shadow":null}}}}`))
	if err != nil {
		t.Fatalf("Merged() with a null list = %v", err)
	}
	if lists := partial.Models["model-a"].Participants; lists.Shadow != nil || lists.Trust == nil {
		t.Errorf("model-a participants after a null list = %+v, want only shadow cleared", lists)
	}

	cleared, err := merged.Merged([]byte(`{"models":{"model-a":{"participants":null}}}`))
	if err != nil {
		t.Fatalf("Merged() with null participants = %v", err)
	}
	if cleared.Models["model-a"].Participants != nil {
		t.Errorf("model-a participants after a null block = %+v, want none", cleared.Models["model-a"].Participants)
	}
}
