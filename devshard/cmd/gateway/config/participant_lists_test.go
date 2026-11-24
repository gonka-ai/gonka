package config

import (
	"slices"
	"strings"
	"testing"

	"devshard/cmd/gateway/env"
)

// Test flow:
//  1. Build a config with an `Participants.Unthrottled` override.
//  2. Assert the built scheduler's unthrottled list matches the override.
func TestUnthrottledListReachesTheSnapshot(t *testing.T) {
	t.Parallel()
	wanted := []string{"gonka1scskt", "gonka1f0u3y"}

	built, err := Build(env.Values{}, Overrides{Participants: &ParticipantOverrides{Unthrottled: &wanted}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !slices.Equal(built.Scheduler.Participants.Unthrottled, wanted) {
		t.Errorf("unthrottled = %v, want %v", built.Scheduler.Participants.Unthrottled, wanted)
	}
}

// Test flow:
//  1. Build a config from an `Participants.Unthrottled` override slice.
//  2. Mutate the original slice's first element.
//  3. Assert the built config's unthrottled list keeps its original value, since `Build` clones rather than aliases it.
func TestUnthrottledListIsClonedFromTheOverride(t *testing.T) {
	t.Parallel()
	source := []string{"gonka1scskt"}

	built, err := Build(env.Values{}, Overrides{Participants: &ParticipantOverrides{Unthrottled: &source}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	source[0] = "gonka1other"

	if built.Scheduler.Participants.Unthrottled[0] != "gonka1scskt" {
		t.Error("the snapshot aliased the caller's slice")
	}
}

// Test flow:
//  1. Build a config with only a `Participants.Trust` override set.
//  2. Assert the unthrottled list stays empty, since naming a trust list must waive nothing.
func TestUnthrottledListIsIndependentOfTrust(t *testing.T) {
	t.Parallel()
	trust := []string{"gonka1scskt"}

	built, err := Build(env.Values{}, Overrides{Participants: &ParticipantOverrides{Trust: &trust}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(built.Scheduler.Participants.Unthrottled) != 0 {
		t.Errorf("unthrottled = %v, want empty: naming a trust list must waive nothing", built.Scheduler.Participants.Unthrottled)
	}
}

// Test flow:
//  1. Build a config with a `Participants.Trust` override containing only a log line's eight-character host tail, not a real address.
//  2. Assert `Build` refuses it rather than narrowing dispatch to nobody.
//  3. Assert the error names the offending entry by its index.
func TestParticipantEntryMustLookLikeAnAddress(t *testing.T) {
	t.Parallel()
	tailFromALogLine := []string{"zalyrmkx"}

	_, err := Build(env.Values{}, Overrides{Participants: &ParticipantOverrides{Trust: &tailFromALogLine}})

	if err == nil {
		t.Fatal("a short host label must be refused rather than narrowing dispatch to nobody")
	}
	if !strings.Contains(err.Error(), "participants.trust[0]") {
		t.Errorf("the complaint must name the entry, got %v", err)
	}
}

// Test flow:
//  1. Build a config with an `Participants.Unthrottled` override containing the same log-line host tail.
//  2. Assert `Build` refuses it here too.
func TestUnthrottledEntryMustLookLikeAnAddress(t *testing.T) {
	t.Parallel()
	tailFromALogLine := []string{"zalyrmkx"}

	if _, err := Build(env.Values{}, Overrides{Participants: &ParticipantOverrides{Unthrottled: &tailFromALogLine}}); err == nil {
		t.Error("a short host label must be refused here too")
	}
}
