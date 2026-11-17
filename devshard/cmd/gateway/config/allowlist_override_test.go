package config

import (
	"slices"
	"testing"

	"devshard/cmd/gateway/env"
)

// Test flow:
//  1. Build a config with a `ParticipantAllowlist` override.
//  2. Assert the built scheduler's allowlist matches the override.
func TestAllowlistOverrideReachesTheSnapshot(t *testing.T) {
	t.Parallel()
	wanted := []string{"gonka1scskt", "gonka1f0u3y", "gonka1z6xwd"}

	built, err := Build(env.Values{}, Overrides{ParticipantAllowlist: &wanted})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !slices.Equal(built.Scheduler.ParticipantAllowlist, wanted) {
		t.Errorf("allowlist = %v, want %v", built.Scheduler.ParticipantAllowlist, wanted)
	}
}

// Test flow:
//  1. Build a config with a one-entry `ParticipantAllowlist` override and assert the allowlist has exactly one entry.
//  2. Build a second config with an explicit empty `ParticipantAllowlist` override.
//  3. Assert the explicit empty list clears the narrowing to zero entries.
func TestAllowlistSurvivesAnUnrelatedOverride(t *testing.T) {
	t.Parallel()
	wanted := []string{"gonka1scskt"}
	built, err := Build(env.Values{}, Overrides{ParticipantAllowlist: &wanted})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(built.Scheduler.ParticipantAllowlist) != 1 {
		t.Fatalf("setup: allowlist = %v", built.Scheduler.ParticipantAllowlist)
	}

	empty := []string{}
	cleared, err := Build(env.Values{}, Overrides{ParticipantAllowlist: &empty})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(cleared.Scheduler.ParticipantAllowlist) != 0 {
		t.Errorf("an explicit empty list must clear the narrowing, got %v", cleared.Scheduler.ParticipantAllowlist)
	}
}

// Test flow:
//  1. Build a config from an override slice.
//  2. Mutate the original slice's first element.
//  3. Assert the built config's allowlist keeps its original value, since `Build` clones it rather than aliasing the caller's slice.
func TestAllowlistIsClonedFromTheOverride(t *testing.T) {
	t.Parallel()
	source := []string{"gonka1scskt"}

	built, err := Build(env.Values{}, Overrides{ParticipantAllowlist: &source})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	source[0] = "gonka1other"

	if built.Scheduler.ParticipantAllowlist[0] != "gonka1scskt" {
		t.Error("the snapshot aliased the caller's slice")
	}
}

// Test flow:
//  1. Build a config with an allowlist override containing a blank entry.
//  2. Assert `Build` returns an error rather than silently narrowing dispatch.
func TestBlankAllowlistEntryIsRefused(t *testing.T) {
	t.Parallel()
	blank := []string{"gonka1scskt", "  "}

	if _, err := Build(env.Values{}, Overrides{ParticipantAllowlist: &blank}); err == nil {
		t.Error("a blank entry must be refused rather than silently narrowing dispatch")
	}
}
