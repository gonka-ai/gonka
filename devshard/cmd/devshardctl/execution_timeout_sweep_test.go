package main

import (
	"context"
	"errors"
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
	prepared, err := env.session.PrepareInference(t.Context(), params)
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

func pendingInferenceStartedAt(t *testing.T, env *testProxyEnv, startedAt time.Time) uint64 {
	t.Helper()
	params := defaultParams()
	params.StartedAt = startedAt.Unix()
	prepared, err := env.session.PrepareInference(t.Context(), params)
	require.NoError(t, err)
	nonce := prepared.Nonce()
	record, tracked := env.sm.GetInference(nonce)
	require.True(t, tracked)
	require.Equal(t, types.StatusPending, record.Status)
	require.Equal(t, startedAt.Unix(), record.StartedAt)
	return nonce
}

// Test flow:
//  1. Leave a Pending inference whose refusal deadline passed an hour ago, and whose race kept its payload after its own vote failed.
//  2. Run the sweep.
//  3. Expect the sweep to post the refusal with that payload, so the record times out and its reservation returns to the balance.
func TestSweepExecutionTimeoutsReturnsAnOverduePendingRefusal(t *testing.T) {
	gateway, runtime, env := newSweepTestGateway(t)
	nonce, payload := pendingRefusalStartedAt(t, env, time.Now().Add(-time.Hour))
	env.proxy.redundancy.rememberRefusalPayload(nonce, payload)
	reserved := env.sm.SnapshotState().Inferences[nonce].ReservedCost
	balanceBefore := env.sm.Balance()

	due, applied, failed := gateway.sweepExecutionTimeouts(t.Context(), []*devshardRuntime{runtime}, executionTimeoutSweepBudget)

	require.Equal(t, 1, due)
	require.Equal(t, 1, applied)
	require.Zero(t, failed)
	require.Equal(t, types.StatusTimedOut, env.sm.SnapshotState().Inferences[nonce].Status)
	require.Equal(t, balanceBefore+reserved, env.sm.Balance())
	require.Zero(t, runtime.pendingRaceCleanup.Load())
}

// Test flow:
//  1. Leave a Pending inference whose refusal deadline passed an hour ago, with no payload kept for it.
//  2. Run the sweep.
//  3. Expect nothing due: every verifier declines a refusal without the request's payload, so sweeping it would only spend a group round and the sweep's budget.
func TestSweepExecutionTimeoutsLeavesAPendingRefusalWithoutItsPayload(t *testing.T) {
	gateway, runtime, env := newSweepTestGateway(t)
	nonce, _ := pendingRefusalStartedAt(t, env, time.Now().Add(-time.Hour))

	due, _, _ := gateway.sweepExecutionTimeouts(t.Context(), []*devshardRuntime{runtime}, executionTimeoutSweepBudget)

	require.Zero(t, due)
	require.Equal(t, types.StatusPending, env.sm.SnapshotState().Inferences[nonce].Status)
}

// Test flow:
//  1. Leave an overdue Pending refusal with its payload, against verifiers that decline every vote.
//  2. Run the sweep twice.
//  3. Expect one failed attempt and nothing due on the second tick: a refusal the group declined with its payload is not retried every tick.
func TestSweepExecutionTimeoutsTriesAPendingRefusalOnce(t *testing.T) {
	gateway, runtime, env := newSweepTestGateway(t)
	nonce, payload := pendingRefusalStartedAt(t, env, time.Now().Add(-time.Hour))
	env.proxy.redundancy.rememberRefusalPayload(nonce, payload)
	for _, verifier := range env.verifiers {
		verifier.accept = false
	}

	firstDue, _, firstFailed := gateway.sweepExecutionTimeouts(t.Context(), []*devshardRuntime{runtime}, executionTimeoutSweepBudget)
	secondDue, _, _ := gateway.sweepExecutionTimeouts(t.Context(), []*devshardRuntime{runtime}, executionTimeoutSweepBudget)

	require.Equal(t, 1, firstDue)
	require.Equal(t, 1, firstFailed)
	require.Zero(t, secondDue)
	require.Equal(t, types.StatusPending, env.sm.SnapshotState().Inferences[nonce].Status)
}

// Test flow:
//  1. Leave an overdue Pending refusal with its payload, against verifiers that cannot be reached.
//  2. Run the sweep four times.
//  3. Expect the refusal to be posted on each of the first three ticks and dropped after that: a group that could not judge the vote is asked again, a dead one does not spend the sweep budget forever.
func TestSweepExecutionTimeoutsRetriesAPendingRefusalTheGroupCouldNotJudge(t *testing.T) {
	gateway, runtime, env := newSweepTestGateway(t)
	nonce, payload := pendingRefusalStartedAt(t, env, time.Now().Add(-time.Hour))
	env.proxy.redundancy.rememberRefusalPayload(nonce, payload)
	for _, verifier := range env.verifiers {
		verifier.failWith = errors.New("verifier unreachable")
	}

	var dues []int
	for range 4 {
		due, _, _ := gateway.sweepExecutionTimeouts(t.Context(), []*devshardRuntime{runtime}, executionTimeoutSweepBudget)
		dues = append(dues, due)
	}

	require.Equal(t, []int{1, 1, 1, 0}, dues)
	require.Equal(t, types.StatusPending, env.sm.SnapshotState().Inferences[nonce].Status)
}

func pendingRefusalStartedAt(t *testing.T, env *testProxyEnv, startedAt time.Time) (uint64, *host.InferencePayload) {
	t.Helper()
	params := defaultParams()
	params.StartedAt = startedAt.Unix()
	nonce := pendingInferenceStartedAt(t, env, startedAt)
	return nonce, env.proxy.redundancy.inferenceTimeoutPayload(params)
}

// Test flow:
//  1. Leave a fresh Pending inference still inside its refusal window.
//  2. Run the sweep.
//  3. Expect nothing due: the sweep must not race the refusal deadline.
func TestSweepExecutionTimeoutsLeavesAFreshPendingToItsRace(t *testing.T) {
	gateway, runtime, env := newSweepTestGateway(t)
	nonce := pendingInferenceStartedAt(t, env, time.Now())

	due, applied, failed := gateway.sweepExecutionTimeouts(t.Context(), []*devshardRuntime{runtime}, executionTimeoutSweepBudget)

	require.Zero(t, due)
	require.Zero(t, applied)
	require.Zero(t, failed)
	require.Equal(t, types.StatusPending, env.sm.SnapshotState().Inferences[nonce].Status)
}

// Test flow:
//  1. Take every free timeout-vote place, then leave an inference whose execution deadline passed an hour ago.
//  2. Run the sweep, then give the places back and run it again.
//  3. Expect no vote while every place is taken and the timeout applied once one is free: the sweep's votes count against the same limit as the races', and it skips rather than waits under the finalize lock.
func TestSweepExecutionTimeoutsSkipsWhileEveryTimeoutVotePlaceIsTaken(t *testing.T) {
	gateway, runtime, env := newSweepTestGateway(t)
	nonce := startedInferenceConfirmedAt(t, env, time.Now().Add(-time.Hour))
	var releases []func()
	for {
		release, acquired := tryAcquireTimeoutVotePost(timeoutVotePosts)
		if !acquired {
			break
		}
		releases = append(releases, release)
	}
	releaseAll := func() {
		for _, release := range releases {
			release()
		}
		releases = nil
	}
	t.Cleanup(releaseAll)

	_, blockedApplied, _ := gateway.sweepExecutionTimeouts(t.Context(), []*devshardRuntime{runtime}, executionTimeoutSweepBudget)
	statusWhileBlocked := env.sm.SnapshotState().Inferences[nonce].Status
	releaseAll()
	_, freedApplied, _ := gateway.sweepExecutionTimeouts(t.Context(), []*devshardRuntime{runtime}, executionTimeoutSweepBudget)

	require.Zero(t, blockedApplied)
	require.Equal(t, types.StatusStarted, statusWhileBlocked)
	require.Equal(t, 1, freedApplied)
	require.Equal(t, types.StatusTimedOut, env.sm.SnapshotState().Inferences[nonce].Status)
}

// Test flow:
//  1. Leave a Pending refusal whose deadline passed an hour ago, against verifiers that decline every vote.
//  2. Let its race vote it and wait for the race cleanup to finish.
//  3. Expect no payload kept for the sweep: the group judged the refusal, so voting it again would only repeat the same round.
func TestARaceRefusalTheGroupDeclinedLeavesNothingToSweep(t *testing.T) {
	env := setupTestProxy(t, 3, nil, false)
	startedAt := time.Now().Add(-time.Hour)
	nonce, _ := pendingRefusalStartedAt(t, env, startedAt)
	params := defaultParams()
	params.StartedAt = startedAt.Unix()
	redundancy := env.proxy.redundancy
	done := make(chan struct{})
	close(done)
	refused := &inflight{
		escrowID: redundancy.devshardID,
		nonce:    nonce,
		hostID:   "refused",
		hostIdx:  int(nonce % 3),
		sendTime: startedAt,
		done:     done,
		err:      errors.New("host refused"),
	}

	redundancy.goTrackedRaceCleanup(context.Background(), func(ctx context.Context) {
		redundancy.voteTimeoutsForFailedRequest(ctx, []*inflight{refused}, params)
	})
	redundancy.raceCleanupWG.Wait()

	require.Equal(t, types.StatusPending, env.sm.SnapshotState().Inferences[nonce].Status)
	require.False(t, redundancy.hasRefusalPayload(nonce))
}
