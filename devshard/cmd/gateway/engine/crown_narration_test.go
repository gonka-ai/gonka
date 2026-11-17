package engine

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type recordedCrownDenial struct {
	participant string
	model       string
	strikes     int
}

type recordingCrownNarrator struct {
	denials      []recordedCrownDenial
	restorations []string
}

func (n *recordingCrownNarrator) HostDeniedCrown(participant, model string, strikes int) {
	n.denials = append(n.denials, recordedCrownDenial{participant: participant, model: model, strikes: strikes})
}

func (n *recordingCrownNarrator) HostCrownedAgain(participant, _ string) {
	n.restorations = append(n.restorations, participant)
}

// Test flow:
//  1. Build a `recordingCrownNarrator` and crown strikes tracking it.
//  2. Observe `crownDenialStrikes` consecutive denied outcomes for the same participant and model.
//  3. Assert exactly one denial was narrated, carrying the participant, model and strike count.
//  4. Assert the strikes tracker now reports that pair as denied.
func TestCrownDenialIsNarratedWhenItStarts(t *testing.T) {
	narrator := &recordingCrownNarrator{}
	strikes := newCrownStrikes(narrator)

	for range crownDenialStrikes {
		strikes.Observe("participant-a", "model-a", true)
	}

	require.Equal(t, []recordedCrownDenial{{participant: "participant-a", model: "model-a", strikes: crownDenialStrikes}}, narrator.denials)
	require.True(t, strikes.Denied("participant-a", "model-a"))
}

// Test flow:
//  1. Build a `recordingCrownNarrator` and crown strikes tracking it.
//  2. Observe `crownDenialStrikes + 2` consecutive denied outcomes for the same participant and model.
//  3. Assert only one denial was narrated despite the extra strikes past the threshold.
func TestFurtherStrikesAreNotNarrated(t *testing.T) {
	narrator := &recordingCrownNarrator{}
	strikes := newCrownStrikes(narrator)

	for range crownDenialStrikes + 2 {
		strikes.Observe("participant-a", "model-a", true)
	}

	require.Len(t, narrator.denials, 1, "a host already denied the crown is not news twice")
}

// Test flow:
//  1. Observe `crownDenialStrikes` denied outcomes to cross into denial.
//  2. Observe one outcome carrying content.
//  3. Assert the restoration is narrated for that participant.
//  4. Assert the strikes tracker no longer reports it as denied.
func TestCrownRestoredIsNarrated(t *testing.T) {
	narrator := &recordingCrownNarrator{}
	strikes := newCrownStrikes(narrator)
	for range crownDenialStrikes {
		strikes.Observe("participant-a", "model-a", true)
	}

	strikes.Observe("participant-a", "model-a", false)

	require.Equal(t, []string{"participant-a"}, narrator.restorations)
	require.False(t, strikes.Denied("participant-a", "model-a"))
}

// Test flow:
//  1. Observe a content outcome followed by a denied outcome, without crossing the denial threshold.
//  2. Assert neither a denial nor a restoration was narrated.
func TestAnOrdinaryAnswerIsNotNarrated(t *testing.T) {
	narrator := &recordingCrownNarrator{}
	strikes := newCrownStrikes(narrator)

	strikes.Observe("participant-a", "model-a", false)
	strikes.Observe("participant-a", "model-a", true)

	require.Empty(t, narrator.denials)
	require.Empty(t, narrator.restorations)
}

// Test flow:
//  1. Build crown strikes with a nil narrator.
//  2. Observe `crownDenialStrikes` denied outcomes.
//  3. Assert the strikes tracker still reports the pair as denied.
func TestStrikesWithoutANarratorStillDeny(t *testing.T) {
	strikes := newCrownStrikes(nil)

	for range crownDenialStrikes {
		strikes.Observe("participant-a", "model-a", true)
	}

	require.True(t, strikes.Denied("participant-a", "model-a"))
}
