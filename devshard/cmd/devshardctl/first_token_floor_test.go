package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	"devshard"
	"devshard/stub"
	"devshard/user"
	"github.com/stretchr/testify/require"
)

func TestFirstTokenFallbackDelayHonorsFloorWithLearnedHistory(t *testing.T) {
	setSpeculativeTiming(t, 5*time.Second, 20*time.Second, 0, time.Second)
	for _, tc := range []struct {
		name    string
		learned time.Duration
		want    time.Duration
	}{
		{"below floor", 5 * time.Second, 20 * time.Second},
		{"at floor", 20 * time.Second, 20 * time.Second},
		{"above floor", 25 * time.Second, 25 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			perf := NewPerfTracker(nil)
			seedFirstTokenFallbackDelay(t, perf, "model", 50, tc.learned)
			e := &Redundancy{perf: perf, model: "model"}
			require.Equal(t, tc.want, e.firstTokenFallbackDelay(user.InferenceParams{Model: "model", InputLength: 50}))
			require.Equal(t, tc.want, e.firstTokenFallbackDelay(user.InferenceParams{InputLength: 50}), "use the runtime model when the request omits it")
		})
	}
}

func TestFirstTokenFallbackFloorWithoutLearnedHistory(t *testing.T) {
	setSpeculativeTiming(t, 5*time.Second, 20*time.Second, 0, time.Second)
	for _, e := range []*Redundancy{nil, {}, {perf: NewPerfTracker(nil)}} {
		require.Equal(t, 20*time.Second, e.firstTokenFallbackDelay(user.InferenceParams{}))
		require.Equal(t, defaultFirstTokenFallbackDelay(200_000), e.firstTokenFallbackDelay(user.InferenceParams{InputLength: 200_000}))
	}
}

type firstTokenFloorDelayedEngine struct {
	devshard.InferenceEngine
}

func (e firstTokenFloorDelayedEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	select {
	case <-time.After(150 * time.Millisecond):
		return e.InferenceEngine.Execute(ctx, req)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestRunInferenceLearnedFirstTokenFloorAllowsSlowerSuccess(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	setSpeculativeTiming(t, 50*time.Millisecond, time.Second, 0, 50*time.Millisecond)
	env := setupTestProxy(t, 1, []devshard.InferenceEngine{firstTokenFloorDelayedEngine{stub.NewInferenceEngine()}}, true)
	params := defaultParams()
	seedFirstTokenFallbackDelay(t, env.proxy.redundancy.perf, params.Model, params.InputLength, 5*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var buf bytes.Buffer
	require.NoError(t, env.proxy.redundancy.RunInference(ctx, params, &buf, nil))
}

func TestFirstTokenFallbackFloorDoesNotReplaceLearnedDelayWithFormula(t *testing.T) {
	setSpeculativeTiming(t, 5*time.Second, time.Second, 0, time.Second)
	perf := NewPerfTracker(nil)
	seedFirstTokenFallbackDelay(t, perf, "model", 100_000, 2*time.Second)
	e := &Redundancy{perf: perf}
	require.Greater(t, defaultFirstTokenFallbackDelay(100_000), 2*time.Second)
	require.Equal(t, 2*time.Second, e.firstTokenFallbackDelay(user.InferenceParams{Model: "model", InputLength: 100_000}))
}

func TestLearnedFirstTokenFloorUsedByEscalationAndReceiptHolder(t *testing.T) {
	setSpeculativeTiming(t, 5*time.Second, 20*time.Second, 0, time.Second)
	perf := NewPerfTracker(nil)
	params := user.InferenceParams{Model: "model", InputLength: 50}
	seedFirstTokenFallbackDelay(t, perf, params.Model, params.InputLength, time.Second)
	e := &Redundancy{perf: perf}
	inf := &inflight{sendTime: time.Now().Add(-5 * time.Second), done: make(chan struct{})}
	inf.setReceiptAt(inf.sendTime.Add(time.Millisecond))
	trigger, ok := e.escalationForInflight(inf, params)
	require.True(t, ok)
	require.Equal(t, "first_token_timeout_wait_elapsed", trigger.stage)
	require.Equal(t, inf.sendTime.Add(20*time.Second), trigger.deadline)
	holder, until := e.liveReceiptHolder([]*inflight{inf}, nil, params)
	require.Same(t, inf, holder, "a learned short delay must not invalidate a live receipt before the configured floor")
	require.Equal(t, trigger.deadline, until)
	require.Equal(t, 20*time.Second, e.escalationDelay(trigger.stage, params))
	inf.setReceiptAt(time.Time{})
	trigger, ok = e.escalationForInflight(inf, params)
	require.True(t, ok)
	require.Equal(t, "receipt_timeout_wait_elapsed", trigger.stage)
	require.Equal(t, inf.sendTime.Add(5*time.Second), trigger.deadline, "receipt timeout remains independent")
}

func TestRunInferenceLearnedFirstTokenFloorStillFailsClosed(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	const floor = 200 * time.Millisecond
	setSpeculativeTiming(t, 50*time.Millisecond, floor, 0, 50*time.Millisecond)
	env := setupTestProxyWithClients(t, []user.HostClient{receiptThenHangClient{}})
	params := defaultParams()
	seedFirstTokenFallbackDelay(t, env.proxy.redundancy.perf, params.Model, params.InputLength, 5*time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(ctx, params, &buf, nil)
	require.ErrorIs(t, err, errAllHostsFirstTokenTimeout)
	require.GreaterOrEqual(t, time.Since(start), floor)
	require.Less(t, time.Since(start), 2*time.Second)
}
