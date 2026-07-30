package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A speculative race returns to the client on the winner and hands its still
// pending losers to a background race cleanup (finishRaceWhenPendingDone) that
// keeps refunding reserved cost and persisting loser signatures on
// context.Background() for up to SecondaryWaitAfterWinner. That cleanup holds no
// activeUserRequests slot, so the drain barrier must track it separately:
// settling (which reads the balance the cleanup is still mutating) and retiring
// (which closes the SQLite store the cleanup is still writing to) must both wait
// until the cleanup drains too.

// TestReleaseRuntimeDefersSettleWhilePendingRaceCleanup pins the money branch: a
// runtime with a pending race cleanup must NOT settle when its last foreground
// request drains — only once the cleanup completes as well.
func TestReleaseRuntimeDefersSettleWhilePendingRaceCleanup(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	g, _, settled := gatewayTestDepletionGateway(t, rt)

	g.reserveRuntime(rt, 1) // the one in-flight foreground request
	g.startRaceCleanup(rt)  // its background race cleanup, spawned before it returns
	rt.settlementReason = "low_balance"
	rt.settlementPending.Store(true)

	g.releaseRuntime(rt, 1) // foreground request returns; cleanup still in flight
	require.Never(t, func() bool { return settled.Load() > 0 }, 200*time.Millisecond, 20*time.Millisecond,
		"settlement must not fire while a background race cleanup is still refunding/persisting")

	g.releaseRaceCleanup(rt) // cleanup drains
	require.Eventually(t, func() bool { return settled.Load() == 1 }, time.Second, 10*time.Millisecond,
		"settlement must fire exactly once, after the cleanup drains")
}

// TestReleaseRuntimeDefersRetireWhilePendingRaceCleanup pins the durability
// branch: retirement (which closes the per-runtime SQLite store) must defer
// while a race cleanup is still writing loser signatures, and fire once it drains.
func TestReleaseRuntimeDefersRetireWhilePendingRaceCleanup(t *testing.T) {
	g, rt := newRetireTestGateway("12")
	g.reserveRuntime(rt, 0) // the one in-flight foreground request
	g.startRaceCleanup(rt)  // its background race cleanup
	rt.retireReason = "balance exhausted"
	rt.retirePending.Store(true)

	g.releaseRuntime(rt, 0) // foreground request returns; cleanup still in flight
	_, stillRegistered := g.runtimes["12"]
	require.True(t, stillRegistered, "retire must defer while a race cleanup is in flight")

	g.releaseRaceCleanup(rt) // cleanup drains
	_, stillRegistered = g.runtimes["12"]
	require.False(t, stillRegistered, "retire fires once the cleanup drains")
	require.Empty(t, g.runtimeOrder)
}

// TestGoTrackedRaceCleanupStartsBarrierSynchronously pins the ordering contract
// the fix depends on: the start hook must fire synchronously, before the cleanup
// goroutine is spawned, so a winning foreground handler that returns and drops
// activeUserRequests immediately afterwards can never observe the runtime as
// quiet while this cleanup is still in flight.
func TestGoTrackedRaceCleanupStartsBarrierSynchronously(t *testing.T) {
	var started, done atomic.Int64
	release := make(chan struct{})
	redundancy := &Redundancy{
		onRaceCleanupStart: func() { started.Add(1) },
		onRaceCleanupDone:  func() { done.Add(1) },
	}

	redundancy.goTrackedRaceCleanup(func() { <-release })

	require.Equal(t, int64(1), started.Load(), "start hook must fire before goTrackedRaceCleanup returns")
	require.Equal(t, int64(0), done.Load(), "done hook must not fire while the cleanup runs")

	close(release)
	require.Eventually(t, func() bool { return done.Load() == 1 }, time.Second, 10*time.Millisecond,
		"done hook must fire once the cleanup completes")
}

// TestConcurrentDrainSettlesExactlyOnce guards the two-counter drain: when the
// last foreground request and the last race cleanup reach zero concurrently,
// exactly one of the racing drain paths must settle — never zero (a lost
// wakeup) and, thanks to dedup, never twice.
func TestConcurrentDrainSettlesExactlyOnce(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	g, _, settled := gatewayTestDepletionGateway(t, rt)

	g.reserveRuntime(rt, 1)
	g.startRaceCleanup(rt)
	rt.settlementReason = "low_balance"
	rt.settlementPending.Store(true)

	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	go func() { ready.Done(); <-start; g.releaseRuntime(rt, 1) }()
	go func() { ready.Done(); <-start; g.releaseRaceCleanup(rt) }()
	ready.Wait()
	close(start) // release both drains as simultaneously as the scheduler allows

	require.Eventually(t, func() bool { return settled.Load() == 1 }, time.Second, 10*time.Millisecond,
		"a concurrent drain must settle exactly once")
	require.Never(t, func() bool { return settled.Load() > 1 }, 200*time.Millisecond, 20*time.Millisecond,
		"dedup must keep a double drain from settling twice")
}
