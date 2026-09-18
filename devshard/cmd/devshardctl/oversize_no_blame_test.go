package main

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/transport"
	"devshard/types"
	"devshard/user"
)

type unfinishedNonceSession struct{}

func (unfinishedNonceSession) IsNonceFinished(uint64) bool { return false }

func failedInflight(err error) *inflight {
	inf := &inflight{nonce: 7, err: err, done: make(chan struct{})}
	close(inf.done)
	return inf
}

func probeInflight() *inflight {
	inf := failedInflight(fmt.Errorf("send: %w", user.ErrPromptTooLargeForHost))
	inf.probe = true
	return inf
}

func hostAnswered413() *inflight {
	return failedInflight(&transport.UpstreamStatusError{
		Path: "/chat/completions", StatusCode: http.StatusRequestEntityTooLarge, Body: "request body too large",
	})
}

func TestEverySpentNonceStillOwesItsVote(t *testing.T) {
	for _, testCase := range []struct {
		name string
		inf  *inflight
		owes bool
	}{
		{name: "body we refused to send", inf: failedInflight(fmt.Errorf("send: %w", user.ErrPromptTooLargeForHost)), owes: true},
		{name: "413 the host answered", inf: hostAnswered413(), owes: true},
		{name: "probe the gateway spent on itself", inf: probeInflight(), owes: false},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.owes, shouldRunHandleTimeoutOn(testCase.inf, unfinishedNonceSession{}, false))
		})
	}
}

func TestABodyWeRefusedToSendScoresNoHostSample(t *testing.T) {
	perf := NewPerfTracker(nil)
	redundancy := &Redundancy{perf: perf, groupSize: 1}
	inf := failedInflight(fmt.Errorf("send: %w", user.ErrPromptTooLargeForHost))

	redundancy.recordSampleOnce(inf, user.InferenceParams{Model: "llama"}, false)

	require.Zero(t, perf.Stats(inf.hostIdx).TotalSamples,
		"a body the host never saw cannot count against its responsiveness")
}

func TestAnUndrainableTailScoresNoHostSample(t *testing.T) {
	perf := NewPerfTracker(nil)
	redundancy := &Redundancy{perf: perf, groupSize: 1}
	inf := failedInflight(fmt.Errorf("send: %w", user.ErrTailTooLargeForHost))

	redundancy.recordSampleOnce(inf, user.InferenceParams{Model: "llama"}, false)

	require.Zero(t, perf.Stats(inf.hostIdx).TotalSamples,
		"the guard covers the whole refusal family, not only the prompt case")
}

func TestADrainStateRootDisagreementScoresNoHostSample(t *testing.T) {
	perf := NewPerfTracker(nil)
	redundancy := &Redundancy{perf: perf, groupSize: 1}
	inf := failedInflight(fmt.Errorf("catch-up ahead of nonce 7 to host 0: %w", types.ErrStateHashMismatch))

	redundancy.recordSampleOnce(inf, user.InferenceParams{Model: "llama"}, false)

	require.Zero(t, perf.Stats(inf.hostIdx).TotalSamples,
		"a state-root disagreement was already exempt when it surfaced through processErr")
}

func TestAHostAnswer413DoesScoreASample(t *testing.T) {
	perf := NewPerfTracker(nil)
	redundancy := &Redundancy{perf: perf, groupSize: 1}

	redundancy.recordSampleOnce(hostAnswered413(), user.InferenceParams{Model: "llama"}, false)

	require.Equal(t, 1, perf.Stats(0).TotalSamples,
		"a 413 the gateway measured as fitting is the host's answer and stays on its record")
}

func TestABodyWeRefusedToSendIsNotEscalated(t *testing.T) {
	redundancy := &Redundancy{}
	inf := failedInflight(fmt.Errorf("send: %w", user.ErrPromptTooLargeForHost))

	_, escalate := redundancy.escalationForInflight(inf, user.InferenceParams{})

	require.False(t, escalate,
		"a prompt over the budget is too large for every host in the group, so a new nonce buys nothing")
}

func TestAnUndrainableTailIsStillEscalated(t *testing.T) {
	redundancy := &Redundancy{}
	inf := failedInflight(fmt.Errorf("send: %w", user.ErrTailTooLargeForHost))

	_, escalate := redundancy.escalationForInflight(inf, user.InferenceParams{})

	require.True(t, escalate,
		"a host that already holds the tail can serve the request, so another host is worth trying")
}

func TestADrainDivergenceStillBlocksTheHost(t *testing.T) {
	require.True(t, isStateRootDivergenceError(
		fmt.Errorf("catch-up ahead of nonce 7 to host 0: %w", types.ErrStateHashMismatch)),
		"the drain reports the disagreement by sentinel, not in the host's wording")
	require.False(t, isStateRootDivergenceError(fmt.Errorf("connection refused")))
}

func TestAnAttemptThatNeverReachedTheHostScoresNoSample(t *testing.T) {
	perf := NewPerfTracker(nil)
	redundancy := &Redundancy{perf: perf, groupSize: 1}
	inf := failedInflight(fmt.Errorf("send: %w", user.ErrCatchUpNotStarted))

	redundancy.recordSampleOnce(inf, user.InferenceParams{Model: "llama"}, false)

	require.Zero(t, perf.Stats(inf.hostIdx).TotalSamples,
		"an attempt cancelled while queued behind another drain never cost the host anything")
}
