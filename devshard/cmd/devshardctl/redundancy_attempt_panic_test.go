package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/user"
)

// panicsMidStreamClient receipts, starts streaming, then panics on the attempt
// goroutine, the way a parser bug reached from a host's response body would.
type panicsMidStreamClient struct {
	event string // written before the panic; defaults to a role-only chunk
	calls atomic.Int32
}

func (c *panicsMidStreamClient) Send(_ context.Context, req host.HostRequest, stream io.Writer, receiptHandler func(*host.HostResponse)) (*host.HostResponse, error) {
	if req.Payload == nil {
		return nil, errors.New("host unavailable")
	}
	c.calls.Add(1)
	if receiptHandler != nil {
		receiptHandler(&host.HostResponse{})
	}
	if stream != nil {
		event := c.event
		if event == "" {
			event = `data: {"choices":[{"delta":{"role":"assistant"}}]}` + "\n\n"
		}
		_, _ = io.WriteString(stream, event)
	}
	panic("host response parser panic")
}

func TestRunInference_APanickingAttemptFailsTheRequestInsteadOfTheProcess(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	shortRefusalWindow(t)
	clients := []*panicsMidStreamClient{{}, {}, {}}
	env := setupTestProxyWithClients(t, []user.HostClient{clients[0], clients[1], clients[2]})
	cleanupFinished := raceCleanupFinished(env.proxy.redundancy)

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)

	require.Error(t, err, "a request whose every attempt panicked must fail")
	calls := clients[0].calls.Load() + clients[1].calls.Load() + clients[2].calls.Load()
	require.Positive(t, calls, "at least one attempt must have reached a panicking host")
	requireClosedWithin(t, cleanupFinished, "the background cleanup never finished")
}

func TestRunInference_APanickingHostFailsOverToAHealthyOne(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	shortRefusalWindow(t)
	env := setupManualFloorProxy(t, 3, nil, true)
	target := int((env.session.Nonce() + 1) % 3)
	panicking := &panicsMidStreamClient{}
	env.killables[target].inner = panicking
	cleanupFinished := raceCleanupFinished(env.proxy.redundancy)

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)

	require.NoError(t, err)
	require.Equal(t, int32(1), panicking.calls.Load(), "the panicking host must have been tried")
	require.NotEmpty(t, buf.String(), "a healthy host must serve the request")
	requireClosedWithin(t, cleanupFinished, "the background cleanup never finished")
	env.raiseFloor(t, 200)
	require.Eventually(t, func() bool { return missesForSlot(t, env, target) == 1 }, testWaitLimit, 20*time.Millisecond,
		"the host whose attempt panicked still owes its nonce a timeout vote")
}

func TestRunInference_AWinnerThatPanicsMidStreamEndsTheRequest(t *testing.T) {
	withRedundancySpeedPolicyForProxyTest(t, RedundancySpeedPolicyLegacy)
	shortRefusalWindow(t)
	env := setupManualFloorProxy(t, 3, nil, true)
	target := int((env.session.Nonce() + 1) % 3)
	panicking := &panicsMidStreamClient{event: `data: {"choices":[{"delta":{"content":"partial"}}]}` + "\n\n"}
	env.killables[target].inner = panicking
	cleanupFinished := raceCleanupFinished(env.proxy.redundancy)

	var buf bytes.Buffer
	err := env.proxy.redundancy.RunInference(context.Background(), defaultParams(), &buf, nil)

	// A winner that fails after streaming surfaces either its own error or the
	// settle path's generic one, depending on which branch sees it first.
	require.Error(t, err, "a winner that panicked after streaming cannot fail over")
	require.Contains(t, buf.String(), "partial", "content streamed before the panic has already reached the caller")
	require.Equal(t, int32(1), panicking.calls.Load())
	requireClosedWithin(t, cleanupFinished, "the background cleanup never finished")
	env.raiseFloor(t, 200)
	require.Eventually(t, func() bool { return missesForSlot(t, env, target) == 1 }, testWaitLimit, 20*time.Millisecond,
		"the winner that panicked still owes its nonce a timeout vote")
}
