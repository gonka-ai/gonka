package config

import (
	"slices"
	"testing"

	"devshard/cmd/gateway/env"
)

// Test flow:
//  1. Build a config with a `Participants.Trust` override.
//  2. Assert the built scheduler's trust list matches the override.
func TestTrustOverrideReachesTheSnapshot(t *testing.T) {
	t.Parallel()
	wanted := []string{"gonka1scskt", "gonka1f0u3y", "gonka1z6xwd"}

	built, err := Build(env.Values{}, Overrides{Participants: &ParticipantOverrides{Trust: &wanted}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !slices.Equal(built.Scheduler.Participants.Trust, wanted) {
		t.Errorf("trust = %v, want %v", built.Scheduler.Participants.Trust, wanted)
	}
}

// Test flow:
//  1. Build a config with a one-entry `Participants.Trust` override and assert the trust list has exactly one entry.
//  2. Build a second config with an explicit empty `Participants.Trust` override.
//  3. Assert the explicit empty list clears the narrowing to zero entries.
func TestTrustSurvivesAnUnrelatedOverride(t *testing.T) {
	t.Parallel()
	wanted := []string{"gonka1scskt"}
	built, err := Build(env.Values{}, Overrides{Participants: &ParticipantOverrides{Trust: &wanted}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(built.Scheduler.Participants.Trust) != 1 {
		t.Fatalf("setup: trust = %v", built.Scheduler.Participants.Trust)
	}

	empty := []string{}
	cleared, err := Build(env.Values{}, Overrides{Participants: &ParticipantOverrides{Trust: &empty}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(cleared.Scheduler.Participants.Trust) != 0 {
		t.Errorf("an explicit empty list must clear the narrowing, got %v", cleared.Scheduler.Participants.Trust)
	}
}

// Test flow:
//  1. Build a config from an override slice.
//  2. Mutate the original slice's first element.
//  3. Assert the built config's trust list keeps its original value, since `Build` clones it rather than aliasing the caller's slice.
func TestTrustIsClonedFromTheOverride(t *testing.T) {
	t.Parallel()
	source := []string{"gonka1scskt"}

	built, err := Build(env.Values{}, Overrides{Participants: &ParticipantOverrides{Trust: &source}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	source[0] = "gonka1other"

	if built.Scheduler.Participants.Trust[0] != "gonka1scskt" {
		t.Error("the snapshot aliased the caller's slice")
	}
}

// Test flow:
//  1. Build a config with a trust list override containing a blank entry.
//  2. Assert `Build` returns an error rather than silently narrowing dispatch.
func TestBlankTrustEntryIsRefused(t *testing.T) {
	t.Parallel()
	blank := []string{"gonka1scskt", "  "}

	if _, err := Build(env.Values{}, Overrides{Participants: &ParticipantOverrides{Trust: &blank}}); err == nil {
		t.Error("a blank entry must be refused rather than silently narrowing dispatch")
	}
}
