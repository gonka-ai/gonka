package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/host"
	"devshard/types"
)

func TestJoinCanceledInflightsAbandonsHungDone(t *testing.T) {
	naturalDone := make(chan *inflight)
	start := time.Now()
	abandoned := joinCanceledInflights(naturalDone, 2, 20*time.Millisecond)
	elapsed := time.Since(start)
	require.Equal(t, 2, abandoned)
	require.GreaterOrEqual(t, elapsed, 20*time.Millisecond)
	require.Less(t, elapsed, time.Second)
}

func TestJoinCanceledInflightsDrainsBeforeTimeout(t *testing.T) {
	naturalDone := make(chan *inflight, 2)
	naturalDone <- &inflight{}
	naturalDone <- &inflight{}
	abandoned := joinCanceledInflights(naturalDone, 2, time.Second)
	require.Zero(t, abandoned)
}

func TestWaitForPendingLosersReturnsWhenSendIgnoresCancel(t *testing.T) {
	setSecondaryWaitAfterWinner(t, 15*time.Millisecond)

	e := &Redundancy{devshardID: "1"}
	winnerDone := make(chan struct{})
	close(winnerDone)
	winner := &inflight{nonce: 1, hostID: "winner", done: winnerDone}

	hungDone := make(chan struct{}) // never closed: SendOnly ignoring cancel
	var cancelCalls atomic.Int64
	loser := &inflight{
		escrowID: "escrow",
		nonce:    2,
		hostID:   "loser",
		done:     hungDone,
		cancel:   func() { cancelCalls.Add(1) },
	}

	start := time.Now()
	e.waitForPendingLosers(context.Background(), winner.nonce, []*inflight{winner, loser})
	elapsed := time.Since(start)

	require.Equal(t, int64(1), cancelCalls.Load(), "loser must be cancelled after the grace window")
	require.False(t, inflightDone(loser), "hung SendOnly stays open")
	require.GreaterOrEqual(t, elapsed, SecondaryWaitAfterWinner)
	require.Less(t, elapsed, SecondaryWaitAfterWinner+raceCleanupCancelGrace+time.Second,
		"must not wait for InferenceTimeout on a hung transport")
}

func TestGoTrackedRaceCleanupReleasesBarrierAfterHungLoserJoin(t *testing.T) {
	setSecondaryWaitAfterWinner(t, 15*time.Millisecond)

	var started, finished atomic.Int64
	e := &Redundancy{
		devshardID:         "1",
		onRaceCleanupStart: func() { started.Add(1) },
		onRaceCleanupDone:  func() { finished.Add(1) },
	}

	winnerDone := make(chan struct{})
	close(winnerDone)
	winner := &inflight{nonce: 1, hostID: "winner", done: winnerDone}
	hungDone := make(chan struct{})
	loser := &inflight{
		escrowID: "escrow",
		nonce:    2,
		hostID:   "loser",
		done:     hungDone,
		cancel:   func() {},
	}

	e.goTrackedRaceCleanup(context.Background(), func(ctx context.Context) {
		e.waitForPendingLosers(ctx, winner.nonce, []*inflight{winner, loser})
	})

	require.Equal(t, int64(1), started.Load())
	require.Eventually(t, func() bool { return finished.Load() == 1 },
		SecondaryWaitAfterWinner+raceCleanupCancelGrace+time.Second, 10*time.Millisecond,
		"pendingRaceCleanup must clear even when the loser SendOnly never returns")
	require.False(t, inflightDone(loser))
}

func TestProcessInflightOnceSkipsAttemptsStillSending(t *testing.T) {
	e := &Redundancy{devshardID: "1"}
	hungDone := make(chan struct{})
	inf := &inflight{
		done: hungDone,
		resp: &host.HostResponse{}, // would be unsafe to ProcessResponse while SendOnly runs
	}
	require.NoError(t, e.processInflightOnce(inf))
	require.Nil(t, inf.processErr)
}

func TestAwaitInflightDonesReturnsWhenSendIgnoresCancel(t *testing.T) {
	hungDone := make(chan struct{})
	inf := &inflight{done: hungDone}
	start := time.Now()
	awaitInflightDones(context.Background(), []*inflight{inf})
	elapsed := time.Since(start)
	require.False(t, inflightDone(inf))
	require.GreaterOrEqual(t, elapsed, raceCleanupCancelGrace)
	require.Less(t, elapsed, raceCleanupCancelGrace+time.Second)
}

func TestAwaitInflightDonesDrainsBeforeTimeout(t *testing.T) {
	done := make(chan struct{})
	close(done)
	inf := &inflight{done: done}
	start := time.Now()
	awaitInflightDones(context.Background(), []*inflight{inf})
	require.Less(t, time.Since(start), 200*time.Millisecond)
}

// Test flow:
//  1. Leave a Started loser whose execution deadline is still ahead.
//  2. Run voteTimeoutsForFailedRequest under goTrackedRaceCleanup.
//  3. Expect the barrier to clear without sleeping until that execution
//     deadline; the nonce stays Started for the timeout sweep. Refusal waits
//     still run under the barrier and are covered elsewhere.
func TestGoTrackedRaceCleanupReleasesBarrierWhenTimeoutDeadlineIsFuture(t *testing.T) {
	env := setupTestProxy(t, 3, nil, true)
	nonce := startedInferenceConfirmedAt(t, env, time.Now())
	due, reason, deadline := timeoutDeadlineDue(env.session, nonce, time.Now())
	require.False(t, due, "fixture must leave an execution deadline ahead")
	require.Equal(t, "execution", reason)
	require.True(t, deadline.After(time.Now()))

	var started, finished atomic.Int64
	e := env.proxy.redundancy
	e.onRaceCleanupStart = func() { started.Add(1) }
	e.onRaceCleanupDone = func() { finished.Add(1) }

	done := make(chan struct{})
	close(done)
	loser := &inflight{
		escrowID: e.devshardID,
		nonce:    nonce,
		hostID:   "loser",
		hostIdx:  int(nonce % 3),
		sendTime: time.Now(),
		done:     done,
		err:      errors.New("speculative loser failed"),
	}

	start := time.Now()
	e.goTrackedRaceCleanup(context.Background(), func(ctx context.Context) {
		e.voteTimeoutsForFailedRequest(ctx, []*inflight{loser}, defaultParams())
	})

	require.Equal(t, int64(1), started.Load())
	require.Eventually(t, func() bool { return finished.Load() == 1 }, 2*time.Second, 10*time.Millisecond,
		"pendingRaceCleanup must clear without waiting for a future HandleTimeout deadline")
	require.Less(t, time.Since(start), time.Until(deadline),
		"cleanup must finish before the protocol execution deadline")
	require.Equal(t, types.StatusStarted, env.sm.SnapshotState().Inferences[nonce].Status)
}
