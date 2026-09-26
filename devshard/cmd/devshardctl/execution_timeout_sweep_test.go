package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"

	"devshard/host"
	"devshard/types"
)

func startedInferenceConfirmedAt(t *testing.T, env *testProxyEnv, confirmedAt time.Time) uint64 {
	t.Helper()
	params := defaultParams()
	params.StartedAt = confirmedAt.Unix()
	prepared, err := env.session.PrepareInference(params)
	require.NoError(t, err)
	nonce := prepared.Nonce()
	record, tracked := env.sm.GetInference(nonce)
	require.True(t, tracked)
	receipt, err := proto.MarshalOptions{Deterministic: true}.Marshal(&types.ExecutorReceiptContent{
		InferenceId: nonce,
		PromptHash:  record.PromptHash,
		Model:       record.Model,
		InputLength: record.InputLength,
		MaxTokens:   record.MaxTokens,
		StartedAt:   record.StartedAt,
		EscrowId:    "escrow-proxy",
		ConfirmedAt: confirmedAt.Unix(),
	})
	require.NoError(t, err)
	signature, err := env.verifiers[record.ExecutorSlot].signer.Sign(receipt)
	require.NoError(t, err)
	require.NoError(t, env.session.ProcessResponse(prepared.HostIdx(), &host.HostResponse{
		Nonce: nonce, Receipt: signature, ConfirmedAt: confirmedAt.Unix(),
	}, nonce))
	require.NoError(t, env.session.SendPendingDiff(t.Context()))
	started, tracked := env.sm.GetInference(nonce)
	require.True(t, tracked)
	require.Equal(t, types.StatusStarted, started.Status, "the fixture must reach the status it is named for")
	return nonce
}

func newSweepTestGateway(t *testing.T) (*Gateway, *devshardRuntime, *testProxyEnv) {
	t.Helper()
	env := setupTestProxy(t, 3, nil, true)
	runtime := &devshardRuntime{id: "escrow-proxy", model: "llama", proxy: env.proxy, session: env.session}
	runtime.active.Store(true)
	gateway := NewGateway([]*devshardRuntime{runtime}, NewGatewayLimiter(0, 0), "llama")
	require.Eventually(t, func() bool { return gateway.executionTimeoutSweepRunning.CompareAndSwap(false, true) }, testWaitLimit, 10*time.Millisecond,
		"the test must own the sweep, or the gateway's own tick could vote under it")
	return gateway, runtime, env
}

// Test flow:
//  1. Start an inference whose executor confirmed it an hour ago and never finished, and that no race will ever vote on.
//  2. Run the sweep.
//  3. Expect the sweep to find it due and apply its execution timeout, so the record is timed out and its whole reservation is back in the balance before any settlement.
func TestSweepExecutionTimeoutsReturnsAStartedReservationNoRaceVotesOn(t *testing.T) {
	gateway, runtime, env := newSweepTestGateway(t)
	nonce := startedInferenceConfirmedAt(t, env, time.Now().Add(-time.Hour))
	reserved := env.sm.SnapshotState().Inferences[nonce].ReservedCost
	balanceBefore := env.sm.Balance()

	due, applied, failed := gateway.sweepExecutionTimeouts(t.Context(), []*devshardRuntime{runtime}, executionTimeoutSweepBudget)

	require.Equal(t, 1, due)
	require.Equal(t, 1, applied)
	require.Zero(t, failed)
	require.Equal(t, types.StatusTimedOut, env.sm.SnapshotState().Inferences[nonce].Status)
	require.Equal(t, balanceBefore+reserved, env.sm.Balance(), "the whole reservation must come back")
	require.Zero(t, runtime.pendingRaceCleanup.Load(), "the sweep must release the runtime it held")
}

// Test flow:
//  1. Start an inference confirmed just now, so a race may still be voting on it.
//  2. Run the sweep.
//  3. Expect nothing due: the sweep only takes over once the race's deadline and the sweep grace have passed.
func TestSweepExecutionTimeoutsLeavesAFreshReservationToItsRace(t *testing.T) {
	gateway, runtime, env := newSweepTestGateway(t)
	nonce := startedInferenceConfirmedAt(t, env, time.Now())

	due, applied, failed := gateway.sweepExecutionTimeouts(t.Context(), []*devshardRuntime{runtime}, executionTimeoutSweepBudget)

	require.Zero(t, due)
	require.Zero(t, applied)
	require.Zero(t, failed)
	require.Equal(t, types.StatusStarted, env.sm.SnapshotState().Inferences[nonce].Status)
}

// Test flow:
//  1. Start two overdue inferences on one escrow.
//  2. Run the sweep with a budget of one.
//  3. Expect exactly one vote and one record timed out, since the budget bounds the vote traffic of a whole tick.
func TestSweepExecutionTimeoutsStaysWithinItsBudget(t *testing.T) {
	gateway, runtime, env := newSweepTestGateway(t)
	firstNonce := startedInferenceConfirmedAt(t, env, time.Now().Add(-time.Hour))
	secondNonce := startedInferenceConfirmedAt(t, env, time.Now().Add(-time.Hour))

	due, applied, _ := gateway.sweepExecutionTimeouts(t.Context(), []*devshardRuntime{runtime}, 1)

	require.Equal(t, 1, due)
	require.Equal(t, 1, applied)
	inferences := env.sm.SnapshotState().Inferences
	require.ElementsMatch(t, []types.InferenceStatus{types.StatusTimedOut, types.StatusStarted}, []types.InferenceStatus{inferences[firstNonce].Status, inferences[secondNonce].Status})
}

// Test flow:
//  1. Register two escrows, the first with two overdue inferences and the second with one.
//  2. Run the sweep twice with a budget of one.
//  3. Expect both escrows served: each tick starts one escrow further along, so the first escrow cannot take every tick's budget.
func TestSweepExecutionTimeoutsStartsOneEscrowFurtherAlongEachTick(t *testing.T) {
	gateway, firstRuntime, firstEnv := newSweepTestGateway(t)
	secondEnv := setupTestProxy(t, 3, nil, true)
	secondRuntime := &devshardRuntime{id: "escrow-second", model: "llama", proxy: secondEnv.proxy, session: secondEnv.session}
	gateway.mu.Lock()
	gateway.runtimes[secondRuntime.id] = secondRuntime
	gateway.mu.Unlock()
	startedInferenceConfirmedAt(t, firstEnv, time.Now().Add(-time.Hour))
	startedInferenceConfirmedAt(t, firstEnv, time.Now().Add(-time.Hour))
	secondNonce := startedInferenceConfirmedAt(t, secondEnv, time.Now().Add(-time.Hour))
	runtimes := []*devshardRuntime{firstRuntime, secondRuntime}

	gateway.sweepExecutionTimeouts(t.Context(), runtimes, 1)
	gateway.sweepExecutionTimeouts(t.Context(), runtimes, 1)

	require.Equal(t, types.StatusTimedOut, secondEnv.sm.SnapshotState().Inferences[secondNonce].Status, "the second escrow must not wait until the first one runs out of overdue records")
}

// Test flow:
//  1. Start an overdue inference on an escrow the gateway no longer has registered.
//  2. Run the sweep over it.
//  3. Expect it untouched: a retired escrow's session may already be closed, and its settlement owns it now.
func TestSweepExecutionTimeoutsSkipsAnEscrowThatIsNoLongerRegistered(t *testing.T) {
	gateway, runtime, env := newSweepTestGateway(t)
	nonce := startedInferenceConfirmedAt(t, env, time.Now().Add(-time.Hour))
	gateway.mu.Lock()
	gateway.dropRegisteredRuntimeLocked(runtime.id)
	gateway.mu.Unlock()

	due, _, _ := gateway.sweepExecutionTimeouts(t.Context(), []*devshardRuntime{runtime}, executionTimeoutSweepBudget)

	require.Zero(t, due)
	require.Equal(t, types.StatusStarted, env.sm.SnapshotState().Inferences[nonce].Status)
}
