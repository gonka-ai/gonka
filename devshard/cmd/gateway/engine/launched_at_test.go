package engine

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// Test flow:
//  1. Build a coordinator whose race started at one instant and an unreported attempt launched at a later one.
//  2. Ask for the attempt's record.
//  3. Assert the record carries the race's start and, apart from it, the attempt's own launch time, which is what the perf tracker judges a sample's age by.
func TestAnAttemptRecordCarriesItsOwnLaunchTime(t *testing.T) {
	raceStarted := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	launched := raceStarted.Add(40 * time.Second)
	sim := newSimulator(t, EscalationPolicy{}, 1, "qwen")
	coordinator := &raceCoordinator{started: raceStarted, deps: raceDeps{Snapshots: sim.snapshots}}

	record := coordinator.attemptRecord(&liveAttempt{participant: "participant-a", launchTime: launched})

	require.Equal(t, raceStarted, record.StartedAt)
	require.Equal(t, launched, record.LaunchedAt)
}

// Test flow:
//  1. Build a successful race and a silent cancelled loser that missed its first-token deadline and carries its own launch time.
//  2. Turn the pair into the perf sample.
//  3. Assert the sample is recorded for the host and carries the attempt's launch time, which the tracker judges its age by.
func TestThePerfSampleCarriesTheAttemptsLaunchTime(t *testing.T) {
	launched := time.Date(2026, 1, 1, 0, 3, 0, 0, time.UTC)
	outcome := RaceOutcome{Model: testModel, WinnerNonce: 2, Succeeded: true}
	attempt := AttemptOutcome{
		Participant: "silent", Nonce: 1, LaunchedAt: launched, SendTime: launched.Add(time.Millisecond),
		Completed: launched.Add(35 * time.Second), Terminal: TerminalClientCancelled, FirstTokenDeadlineMissed: true,
	}

	sample, exemption := outcome.Sample(attempt)

	require.Equal(t, SampleRecorded, exemption)
	require.Equal(t, "silent", sample.ParticipantKey)
	require.Equal(t, launched, sample.LaunchedAt)
}
