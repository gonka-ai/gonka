package config

import (
	"slices"
	"strings"
	"testing"

	"devshard/cmd/gateway/env"
)

// Test flow:
//  1. Build a config with an `UnthrottledParticipants` override.
//  2. Assert the built scheduler's unthrottled list matches the override.
func TestUnthrottledListReachesTheSnapshot(t *testing.T) {
	t.Parallel()
	wanted := []string{"gonka1scskt", "gonka1f0u3y"}

	built, err := Build(env.Values{}, Overrides{UnthrottledParticipants: &wanted})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if !slices.Equal(built.Scheduler.UnthrottledParticipants, wanted) {
		t.Errorf("unthrottled = %v, want %v", built.Scheduler.UnthrottledParticipants, wanted)
	}
}

// Test flow:
//  1. Build a config from an `UnthrottledParticipants` override slice.
//  2. Mutate the original slice's first element.
//  3. Assert the built config's unthrottled list keeps its original value, since `Build` clones rather than aliases it.
func TestUnthrottledListIsClonedFromTheOverride(t *testing.T) {
	t.Parallel()
	source := []string{"gonka1scskt"}

	built, err := Build(env.Values{}, Overrides{UnthrottledParticipants: &source})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	source[0] = "gonka1other"

	if built.Scheduler.UnthrottledParticipants[0] != "gonka1scskt" {
		t.Error("the snapshot aliased the caller's slice")
	}
}

// Test flow:
//  1. Build a config with only a `ParticipantAllowlist` override set.
//  2. Assert the unthrottled list stays empty, since naming an allowlist must waive nothing.
func TestUnthrottledListIsIndependentOfTheAllowlist(t *testing.T) {
	t.Parallel()
	allowlist := []string{"gonka1scskt"}

	built, err := Build(env.Values{}, Overrides{ParticipantAllowlist: &allowlist})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if len(built.Scheduler.UnthrottledParticipants) != 0 {
		t.Errorf("unthrottled = %v, want empty: naming an allowlist must waive nothing", built.Scheduler.UnthrottledParticipants)
	}
}

// Test flow:
//  1. Build a config with a `ParticipantAllowlist` override containing only a log line's eight-character host tail, not a real address.
//  2. Assert `Build` refuses it rather than narrowing dispatch to nobody.
//  3. Assert the error names the offending entry by its index.
func TestParticipantEntryMustLookLikeAnAddress(t *testing.T) {
	t.Parallel()
	tailFromALogLine := []string{"zalyrmkx"}

	_, err := Build(env.Values{}, Overrides{ParticipantAllowlist: &tailFromALogLine})

	if err == nil {
		t.Fatal("a short host label must be refused rather than narrowing dispatch to nobody")
	}
	if !strings.Contains(err.Error(), "participant_allowlist[0]") {
		t.Errorf("the complaint must name the entry, got %v", err)
	}
}

// Test flow:
//  1. Build a config with an `UnthrottledParticipants` override containing the same log-line host tail.
//  2. Assert `Build` refuses it here too.
func TestUnthrottledEntryMustLookLikeAnAddress(t *testing.T) {
	t.Parallel()
	tailFromALogLine := []string{"zalyrmkx"}

	if _, err := Build(env.Values{}, Overrides{UnthrottledParticipants: &tailFromALogLine}); err == nil {
		t.Error("a short host label must be refused here too")
	}
}
