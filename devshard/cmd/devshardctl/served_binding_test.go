package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/user"
)

// Test flow:
//  1. Seed the host with one failure, one short of the participant failure threshold.
//  2. Record the case's binding verdict for an attempt that delivered content, with or without an error stream.
//  3. Assert the threshold is crossed only for a mismatch, including on an error stream.
func TestServedBindingFailureStrikesTheHost(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		verdict     user.ServedBinding
		errorSource string
		wantStrike  bool
	}{
		{name: "bound", verdict: user.ServedBindingBound},
		{name: "no finish", verdict: user.ServedBindingNoFinish},
		{name: "unverified finish", verdict: user.ServedBindingUnverifiedFinish},
		{name: "nothing received", verdict: user.ServedBindingNothingReceived},
		{name: "mismatch", verdict: user.ServedBindingMismatch, wantStrike: true},
		{name: "mismatch on an error stream", verdict: user.ServedBindingMismatch, errorSource: "stream", wantStrike: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			perf := NewPerfTracker(nil)
			perf.Record(RequestSample{ParticipantKey: "host:0", Responsive: false, SendTime: time.Now()})
			require.False(t, perf.ParticipantFailureThresholdExceeded("host:0"), "precondition: one failure short of the threshold")
			redundancy := &Redundancy{perf: perf}
			attempt := &inflight{hostID: "host-A", escrowID: "escrow-x", nonce: 1, sendTime: time.Now(), errorSource: testCase.errorSource}
			attempt.contentChunks.Store(1)

			redundancy.recordServedBinding(context.Background(), attempt, user.InferenceParams{Model: "m"}, testCase.verdict)

			require.Equal(t, testCase.wantStrike, perf.ParticipantFailureThresholdExceeded("host:0"))
		})
	}
}
