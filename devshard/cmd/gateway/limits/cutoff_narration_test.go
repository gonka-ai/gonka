package limits

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type recordedCutoff struct {
	participant  string
	model        string
	reason       string
	backoffCount int
	cutOffFor    time.Duration
}

type recordedLift struct {
	participant  string
	model        string
	backoffCount int
}

type recordingCutoffNarrator struct {
	cutoffs []recordedCutoff
	lifts   []recordedLift
}

func (n *recordingCutoffNarrator) HostCutOff(participant, model, reason string, backoffCount int, cutOffFor time.Duration) {
	n.cutoffs = append(n.cutoffs, recordedCutoff{
		participant: participant, model: model, reason: reason, backoffCount: backoffCount, cutOffFor: cutOffFor,
	})
}

func (n *recordingCutoffNarrator) HostCutOffLifted(participant, model string, backoffCount int) {
	n.lifts = append(n.lifts, recordedLift{participant: participant, model: model, backoffCount: backoffCount})
}

// Test flow:
//  1. Build a `newTestParticipantLimiter` with jitter disabled and attach a `recordingCutoffNarrator`.
//  2. Answer with `TransportFault` enough times to trip the breaker.
//  3. Assert one cut-off is recorded with reason "consecutive_transport_faults", backoff count 1 and a 5-second duration.
func TestCutoffIsNarratedWhenItOpens(t *testing.T) {
	limiter := newTestParticipantLimiter(t)
	limiter.jitter = func(time.Duration) time.Duration { return 0 }
	narrator := &recordingCutoffNarrator{}
	limiter.SetNarrator(narrator)

	for range int(limiter.cfg.AfterFailures) {
		limiter.answered("participant-a", "model-a", TransportFault)
	}

	require.Equal(t, []recordedCutoff{{
		participant: "participant-a", model: "model-a", reason: "consecutive_transport_faults",
		backoffCount: 1, cutOffFor: 5 * time.Second,
	}}, narrator.cutoffs)
}

// Test flow:
//  1. Trip the breaker on a fixed clock with `AfterFailures` transport faults.
//  2. Answer with three times as many transport faults while the cut-off is still open, as the attempts already in flight fail.
//  3. Assert the host was cut off once, at backoff count 1 and for 5 seconds: faults behind an open cut-off neither reopen nor lengthen it.
func TestFaultsWhileTheCutoffIsOpenNeitherReopenNorLengthenIt(t *testing.T) {
	settings := testConfig()
	settings.BaseOpen, settings.MaxOpen = 5*time.Second, time.Minute
	limiter := NewParticipantLimiter(settings, fixedNow(testEpoch))
	limiter.jitter = func(time.Duration) time.Duration { return 0 }
	narrator := &recordingCutoffNarrator{}
	limiter.SetNarrator(narrator)
	for range int(limiter.cfg.AfterFailures) {
		limiter.answered("participant-a", "model-a", TransportFault)
	}

	for range 3 * int(limiter.cfg.AfterFailures) {
		limiter.answered("participant-a", "model-a", TransportFault)
	}

	require.Equal(t, []recordedCutoff{{
		participant: "participant-a", model: "model-a", reason: "consecutive_transport_faults",
		backoffCount: 1, cutOffFor: 5 * time.Second,
	}}, narrator.cutoffs)
}

// Test flow:
//  1. Trip the breaker with `TransportFault` answers, then force it half-open via `markHalfOpen`, before attaching a narrator.
//  2. Attach a `recordingCutoffNarrator` and answer with `TransportFault` again.
//  3. Assert the recorded cut-off's reason is "half_open_probe_failed", distinct from the earlier trip.
func TestAFailedProbeIsNarratedAsItsOwnReason(t *testing.T) {
	limiter := newTestParticipantLimiter(t)
	for range int(limiter.cfg.AfterFailures) {
		limiter.answered("participant-a", "model-a", TransportFault)
	}
	limiter.markHalfOpen("participant-a", "model-a")
	narrator := &recordingCutoffNarrator{}
	limiter.SetNarrator(narrator)

	limiter.answered("participant-a", "model-a", TransportFault)

	require.Len(t, narrator.cutoffs, 1)
	require.Equal(t, "half_open_probe_failed", narrator.cutoffs[0].reason)
}

// Test flow:
//  1. Trip the breaker with `TransportFault` answers and force it half-open via `markHalfOpen`, before attaching a narrator.
//  2. Attach a `recordingCutoffNarrator` and answer with `Success`.
//  3. Assert one lift is recorded for the participant with backoff count 0.
func TestCutoffCloseIsNarrated(t *testing.T) {
	limiter := newTestParticipantLimiter(t)
	for range int(limiter.cfg.AfterFailures) {
		limiter.answered("participant-a", "model-a", TransportFault)
	}
	limiter.markHalfOpen("participant-a", "model-a")
	narrator := &recordingCutoffNarrator{}
	limiter.SetNarrator(narrator)

	limiter.answered("participant-a", "model-a", Success)

	require.Equal(t, []recordedLift{{participant: "participant-a", model: "model-a", backoffCount: 0}}, narrator.lifts)
}

// Test flow:
//  1. Attach a `recordingCutoffNarrator` to a fresh limiter.
//  2. Answer with `Success`, `Overload` and `UpstreamFault` in turn.
//  3. Assert nothing was recorded as a cut-off or a lift.
func TestAnOrdinaryResultIsNotNarrated(t *testing.T) {
	limiter := newTestParticipantLimiter(t)
	narrator := &recordingCutoffNarrator{}
	limiter.SetNarrator(narrator)

	limiter.answered("participant-a", "model-a", Success)
	limiter.answered("participant-a", "model-a", Overload)
	limiter.answered("participant-a", "model-a", UpstreamFault)

	require.Empty(t, narrator.cutoffs)
	require.Empty(t, narrator.lifts)
}

// Test flow:
//  1. Build a limiter with no narrator attached and answer with `TransportFault` enough times to trip the breaker.
//  2. Assert the participant is no longer `Available`.
func TestAnUnnarratedLimiterStillCutsOff(t *testing.T) {
	limiter := newTestParticipantLimiter(t)

	for range int(limiter.cfg.AfterFailures) {
		limiter.answered("participant-a", "model-a", TransportFault)
	}

	require.False(t, limiter.Available("participant-a", "model-a"))
}

func newTestParticipantLimiter(t *testing.T) *ParticipantLimiter {
	t.Helper()
	settings := testConfig()
	settings.BaseOpen, settings.MaxOpen = 5*time.Second, time.Minute
	return NewParticipantLimiter(settings, time.Now)
}

// markHalfOpen puts the breaker in the state an expired cut-off leaves it in.
func (l *ParticipantLimiter) markHalfOpen(participant, model string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	state := l.stateLocked(key{participant: participant, model: model})
	state.halfOpen = true
	state.openUntil = time.Time{}
}
