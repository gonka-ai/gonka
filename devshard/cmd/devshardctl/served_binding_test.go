package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"testing"
	"time"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/stub"
	"devshard/user"
)

// Test flow:
//  1. Seed the executor with one failure, one short of the participant failure threshold.
//  2. Hand the gateway the case's binding verdict the way the session does, and wait for any strike it started.
//  3. Assert the threshold is crossed only for a mismatch.
func TestServedBindingMismatchStrikesTheExecutor(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		verdict    user.ServedBinding
		wantStrike bool
	}{
		{name: "bound", verdict: user.ServedBindingBound},
		{name: "mismatch", verdict: user.ServedBindingMismatch, wantStrike: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			perf := NewPerfTracker(nil)
			perf.Record(RequestSample{ParticipantKey: "host:0", Responsive: false, SendTime: time.Now()})
			require.False(t, perf.ParticipantFailureThresholdExceeded("host:0"), "precondition: one failure short of the threshold")
			redundancy := &Redundancy{perf: perf, model: "m"}

			redundancy.handleServedBinding(3, 0, "host:0", testCase.verdict)
			redundancy.servedBindingStrikes.Wait()

			require.Equal(t, testCase.wantStrike, perf.ParticipantFailureThresholdExceeded("host:0"))
		})
	}
}

// Test flow:
//  1. Start a three-host gateway whose clients report the case's hashes as the stream they received.
//  2. Run two inferences, wait for their background attempts, and send the pending diff so every Finish received applies.
//  3. Assert the gateway reports the case's verdict for the applied Finish and never the other one.
func TestTheGatewayJudgesTheStreamAgainstTheAppliedFinish(t *testing.T) {
	storedSum := sha256.Sum256(stub.NewInferenceEngine().ResponseBody)
	for _, testCase := range []struct {
		name         string
		received     [32]byte
		wantVerdict  user.ServedBinding
		otherVerdict user.ServedBinding
	}{
		{name: "the stored view arrived", received: storedSum, wantVerdict: user.ServedBindingBound, otherVerdict: user.ServedBindingMismatch},
		{name: "another answer arrived", received: sha256.Sum256([]byte("tampered")), wantVerdict: user.ServedBindingMismatch, otherVerdict: user.ServedBindingBound},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			env := setupTestProxy(t, 3, nil, true)
			metrics := NewDevshardMetrics()
			env.proxy.redundancy.metrics = metrics
			for _, client := range env.killables {
				client.ReportReceivedHashes([][32]byte{testCase.received})
			}

			for range 2 {
				var served bytes.Buffer
				require.NoError(t, env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &served, nil))
			}
			env.proxy.redundancy.waitRaceCleanups()
			require.NoError(t, env.session.SendPendingDiff(context.Background()))

			verdictCount := func(verdict user.ServedBinding) float64 {
				return promtestutil.ToFloat64(metrics.servedBindings.WithLabelValues(string(verdict)))
			}
			require.GreaterOrEqual(t, verdictCount(testCase.wantVerdict), float64(1))
			require.Zero(t, verdictCount(testCase.otherVerdict))
		})
	}
}

// Test flow:
//  1. Describe attempts that returned a host response with and without a transport error, a probe, and a phase-aborted attempt.
//  2. Ask whether each one's received stream is bound for judging.
//  3. Assert an attempt that errored after its answer is still bound, because the hasher already refuses a stream that never completed.
func TestAnAttemptThatErroredAfterItsAnswerIsStillBound(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		attempt   *inflight
		wantBound bool
	}{
		{name: "completed", attempt: &inflight{resp: &host.HostResponse{}}, wantBound: true},
		{name: "cut after its answer", attempt: &inflight{resp: &host.HostResponse{}, err: errors.New("stream ended before meta")}, wantBound: true},
		{name: "no host response", attempt: &inflight{err: errors.New("dial failed")}},
		{name: "probe", attempt: &inflight{resp: &host.HostResponse{}, probe: true}},
		{name: "aborted by a phase transition", attempt: &inflight{resp: &host.HostResponse{}, phaseTransitionAborted: true}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.wantBound, bindsReceivedStream(testCase.attempt))
		})
	}
}
