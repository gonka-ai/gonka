package main

import (
	"bytes"
	"context"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard"
	"devshard/host"
	"devshard/stub"
	"devshard/user"
)

func TestInflightClockStart_WaitsForOwnCatchUp(t *testing.T) {
	now := time.Now()
	plain := &inflight{sendTime: now.Add(-time.Second)}
	start, ok := plain.clockStart(now)
	require.True(t, ok)
	require.Equal(t, plain.sendTime, start)

	inf := &inflight{sendTime: now.Add(-10 * time.Second), catchUpFirst: true}
	start, ok = inf.clockStart(now)
	require.False(t, ok, "the clock has not started while the catch-up runs")
	require.Equal(t, now, start)

	sent := now.Add(-2 * time.Second)
	inf.requestSentNano.Store(sent.UnixNano())
	start, ok = inf.clockStart(now)
	require.True(t, ok)
	require.Equal(t, sent.UnixNano(), start.UnixNano(), "the clock starts when the request goes out")

	stuck := &inflight{sendTime: now.Add(-2 * maxCatchUpBeforeSend), catchUpFirst: true}
	start, ok = stuck.clockStart(now)
	require.True(t, ok)
	require.Equal(t, stuck.sendTime.Add(maxCatchUpBeforeSend), start, "a catch-up holds the clock back only so long")
}

// An attempt to a far-behind host spends its first seconds on catch-up; its
// receipt deadline must not pass during that time.
func TestEscalationForInflight_ReceiptClockSkipsCatchUp(t *testing.T) {
	e := &Redundancy{}
	params := defaultParams()
	sendTime := time.Now().Add(-2 * receiptTimeoutForInput(params.InputLength))

	plain := &inflight{sendTime: sendTime, done: make(chan struct{})}
	trig, ok := e.escalationForInflight(plain, params, true)
	require.True(t, ok)
	require.Equal(t, "receipt_timeout_wait_elapsed", trig.stage)
	require.True(t, time.Now().After(trig.deadline), "without a catch-up the receipt is overdue")

	catching := &inflight{sendTime: sendTime, catchUpFirst: true, done: make(chan struct{})}
	trig, ok = e.escalationForInflight(catching, params, true)
	require.True(t, ok)
	require.True(t, trig.deadline.After(time.Now()), "the receipt clock has not started during the catch-up")

	catching.requestSentNano.Store(time.Now().UnixNano())
	trig, _ = e.escalationForInflight(catching, params, true)
	require.WithinDuration(t, time.Now().Add(receiptTimeoutForInput(params.InputLength)), trig.deadline, time.Second)
}

func TestLiveReceiptHolder(t *testing.T) {
	e := &Redundancy{}
	params := defaultParams()
	patience := receiptHolderFirstTokenPatience * e.firstTokenFallbackDelay(params)
	primary := newTestInflight(1, 1, "h1")
	primary.sendTime = time.Now()
	secondary := newTestInflight(2, 2, "h2")
	secondary.sendTime = time.Now()
	attempts := []*inflight{primary, secondary}

	holder := func() *inflight {
		h, _ := e.liveReceiptHolder(attempts, params)
		return h
	}
	require.Nil(t, holder(), "no receipt, no holder")

	primary.setReceiptAt(time.Now())
	h, until := e.liveReceiptHolder(attempts, params)
	require.Same(t, primary, h)
	require.WithinDuration(t, primary.sendTime.Add(patience), until, time.Millisecond,
		"the holder is live for receiptHolderFirstTokenPatience first-token budgets")

	primary.sendTime = time.Now().Add(-e.firstTokenFallbackDelay(params) - time.Millisecond)
	require.Same(t, primary, holder(), "one missed first-token budget does not end the holder")

	primary.sendTime = time.Now().Add(-patience - time.Millisecond)
	require.Nil(t, holder(), "a holder past its patience has lost")

	primary.setFirstTokenAt(time.Now())
	h, until = e.liveReceiptHolder(attempts, params)
	require.Same(t, primary, h, "a streaming holder is live")
	require.True(t, until.IsZero(), "a streaming holder has no first-token deadline")

	close(primary.done)
	require.Nil(t, holder(), "a finished attempt holds nothing")
}

func TestPicker_CatchingUpHost_BurnsGhostNoSend(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	env.proxy.redundancy.picker.stop()

	ghost := &fakeGhost{}
	p := newSessionPicker(env.session, "llama", ghost.dispatch, nil, nil)
	p.catchingUp = func(hostIdx int) bool { return hostIdx == 1 }
	p.start()
	t.Cleanup(p.stop)

	req := defaultPickerRequest()
	p.submit(req)

	res := waitReply(t, req, 2*time.Second)
	require.NoError(t, res.err)
	require.False(t, res.isProbe)
	require.NotEqual(t, 1, res.prepared.HostIdx(), "a real request does not wait on a host's catch-up")
	require.GreaterOrEqual(t, ghost.kindCount(ghostCatchingUp), 1)
	require.Contains(t, ghost.reasons, ghostCatchingUp.reason())
}

// lateReceiptThenHangClient returns the first inference's receipt after
// delay; every later inference hangs without a receipt.
type lateReceiptThenHangClient struct {
	inner    user.HostClient
	delay    time.Duration
	payloads *atomic.Int32
}

func (c *lateReceiptThenHangClient) Send(ctx context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	if req.Payload == nil {
		return c.inner.Send(ctx, req, stream, receiptHandler)
	}
	if c.payloads.Add(1) > 1 {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	select {
	case <-time.After(c.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return c.inner.Send(ctx, req, stream, receiptHandler)
}

type slowFirstTokenEngine struct {
	inner *stub.InferenceEngine
	delay time.Duration
}

func (e *slowFirstTokenEngine) Execute(ctx context.Context, req devshard.ExecuteRequest) (*devshard.ExecuteResult, error) {
	select {
	case <-time.After(e.delay):
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return e.inner.Execute(ctx, req)
}

// Test flow:
//  1. The primary's receipt is late, so secondaries start; they never send one.
//  2. The last secondary's receipt timeout hits the attempt limit after the
//     primary already holds a receipt.
//  3. The race waits for the primary, which streams and wins.
func TestRunInference_SecondaryReceiptTimeoutDoesNotFailClosedWhilePrimaryHoldsReceipt(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	setSpeculativeTiming(t, 50*time.Millisecond, 2*time.Second, 0, 50*time.Millisecond)
	engines := make([]devshard.InferenceEngine, 3)
	for i := range engines {
		engines[i] = &slowFirstTokenEngine{inner: stub.NewInferenceEngine(), delay: 300 * time.Millisecond}
	}
	env := setupTestProxy(t, 3, engines, true)
	var payloads atomic.Int32
	for _, kc := range env.killables {
		kc.inner = &lateReceiptThenHangClient{inner: kc.inner, delay: 80 * time.Millisecond, payloads: &payloads}
	}
	params := defaultParams()
	seedFirstTokenFallbackDelay(t, env.proxy.redundancy.perf, params.Model, params.InputLength, time.Second)

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), params, &buf, nil)
	require.NoError(t, err, "the primary signed for the work; a secondary's timeout must not fail the race")
	require.GreaterOrEqual(t, payloads.Load(), int32(2), "a secondary was started and timed out")
	require.NotEmpty(t, buf.String())
}

// Test flow:
//  1. As above, the last secondary's receipt timeout is put off because the
//     primary holds a receipt.
//  2. The primary sends no first token within receiptHolderFirstTokenPatience
//     of its 300ms budgets.
//  3. The race fails closed at that patience instead of waiting on the hung
//     primary, which would only stream after 4s.
func TestRunInference_DeferredFailClosedFiresWhenTheReceiptHolderStalls(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	setSpeculativeTiming(t, 50*time.Millisecond, 2*time.Second, 0, 50*time.Millisecond)
	engines := make([]devshard.InferenceEngine, 3)
	for i := range engines {
		engines[i] = &slowFirstTokenEngine{inner: stub.NewInferenceEngine(), delay: 4 * time.Second}
	}
	env := setupTestProxy(t, 3, engines, true)
	var payloads atomic.Int32
	for _, kc := range env.killables {
		kc.inner = &lateReceiptThenHangClient{inner: kc.inner, delay: 80 * time.Millisecond, payloads: &payloads}
	}
	params := defaultParams()
	seedFirstTokenFallbackDelay(t, env.proxy.redundancy.perf, params.Model, params.InputLength, 300*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(ctx, params, &buf, nil)
	require.Error(t, err, "a receipt holder past its patience no longer holds off the fail-closed")
	require.Less(t, time.Since(start), 3*time.Second, "the race did not wait on the hung primary")
	require.GreaterOrEqual(t, payloads.Load(), int32(2))
}
