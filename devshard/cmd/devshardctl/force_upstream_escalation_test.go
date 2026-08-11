package main

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/user"
)

func TestEscalationForInflight_ForceUpstreamAppliesFirstTokenToNonStreamClient(t *testing.T) {
	withForceUpstreamStreaming(t, true)
	env := setupTestProxy(t, 2, nil, true)

	params := defaultParams()
	params.Stream = false

	inf := &inflight{
		hostID:   "host-0",
		nonce:    1,
		sendTime: time.Now().Add(-50 * time.Millisecond),
	}
	inf.setReceiptAt(time.Now().Add(-40 * time.Millisecond))

	trigger, ok := env.proxy.redundancy.escalationForInflight(inf, params)
	require.True(t, ok, "always-stream must arm first-token escalation for stream:false clients")
	require.Equal(t, "first_token_timeout", trigger.reason)
	require.Equal(t, "first_token_timeout_wait_elapsed", trigger.stage)
}

func TestEscalationForInflight_LegacyNonStreamSkipsFirstToken(t *testing.T) {
	withForceUpstreamStreaming(t, false)
	env := setupTestProxy(t, 2, nil, true)

	params := defaultParams()
	params.Stream = false

	inf := &inflight{
		hostID:   "host-0",
		nonce:    1,
		sendTime: time.Now().Add(-50 * time.Millisecond),
	}
	inf.setReceiptAt(time.Now().Add(-40 * time.Millisecond))

	_, ok := env.proxy.redundancy.escalationForInflight(inf, params)
	require.False(t, ok, "without ForceUpstreamStreaming, non-stream skips first-token escalation")
}

func TestEscalationForInflight_ForceUpstreamAttemptFailedOnEmptyDone(t *testing.T) {
	withForceUpstreamStreaming(t, true)
	env := setupTestProxy(t, 2, nil, true)

	params := defaultParams()
	params.Stream = false

	inf := &inflight{
		hostID: "host-0",
		nonce:  1,
		done:   make(chan struct{}),
	}
	close(inf.done)

	trigger, ok := env.proxy.redundancy.escalationForInflight(inf, params)
	require.True(t, ok)
	require.Equal(t, "attempt_failed", trigger.reason)
}

func TestLongNonStreamEmptyFailureExempt_DisabledUnderForceUpstream(t *testing.T) {
	withForceUpstreamStreaming(t, true)
	params := defaultParams()
	params.Stream = false
	inf := &inflight{
		hostIdx:  0,
		nonce:    1,
		sendTime: time.Now().Add(-(longResponseFailureExemption + time.Second)),
	}
	inf.setReceiptAt(time.Now().Add(-(longResponseFailureExemption + 900*time.Millisecond)))
	require.False(t, longNonStreamEmptyFailureExempt(inf, params))
}

// Step 11: stream:false client + ForceUpstreamStreaming must escalate empty /
// no-content hosts on the streaming attempt_failed / first-token path — not
// the legacy 140s reduced-max_tokens timer.
func TestRunInference_ForceUpstreamStreaming_NonStreamClientEscalatesWithoutReducedMaxTokens(t *testing.T) {
	withForceUpstreamStreaming(t, true)
	// Legacy timers deliberately long: if the old path still arms, this test
	// hangs or records reduced max_tokens.
	setNonStreamingTimeouts(t, 5*time.Second, 5*time.Second, 5*time.Second)
	setSpeculativeTiming(t, 20*time.Millisecond, 30*time.Millisecond, 0, time.Minute)
	disablePairwiseABSampling(t)

	client := &emptyNonStreamingRecorderClient{}
	env := setupTestProxyWithClients(t, []user.HostClient{client, client})

	params := defaultParams()
	params.Stream = false
	params.MaxTokens = 50
	params.Prompt = []byte(`{"model":"llama","max_tokens":50,"messages":[{"role":"user","content":"hello"}]}`)

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), params, &buf, nil)

	var reducedErr *nonStreamingReducedMaxTokensTimeoutError
	require.False(t, errors.As(err, &reducedErr), "must not use reduced-max_tokens timeout under always-stream: %v", err)
	require.GreaterOrEqual(t, client.sendCalls.Load(), int32(2), "empty primary must escalate to a secondary")
	require.Equal(t, []uint64{50, 50}, client.MaxTokens(), "secondary must keep full max_tokens (no reduced retry)")
}

func TestRunInference_ForceUpstreamStreaming_BlockingHostEscalatesWithinFirstTokenBudget(t *testing.T) {
	withForceUpstreamStreaming(t, true)
	// Legacy non-stream timers are far above the default first-token floor (~1.7s).
	setNonStreamingTimeouts(t, 30*time.Second, 30*time.Second, 30*time.Second)
	setSpeculativeTiming(t, 15*time.Millisecond, time.Second, 0, time.Minute)
	disablePairwiseABSampling(t)

	client := &blockingNonStreamingRecorderClient{}
	env := setupTestProxyWithClients(t, []user.HostClient{client, client})

	params := defaultParams()
	params.Stream = false
	params.MaxTokens = 50
	params.Prompt = []byte(`{"model":"llama","max_tokens":50,"messages":[{"role":"user","content":"hello"}]}`)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		var buf bytes.Buffer
		errCh <- env.proxy.redundancy.RunInference(ctx, params, &buf, nil)
	}()

	require.Eventually(t, func() bool {
		return client.sendCalls.Load() >= 2
	}, 4*time.Second, 10*time.Millisecond, "first-token budget must start a secondary before legacy 30s timers")

	require.Equal(t, []uint64{50, 50}, client.MaxTokens())
	cancel()
	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		t.Fatal("RunInference did not return after cancel")
	}
}
