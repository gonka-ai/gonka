package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A speculative race returns to the client on the winner and hands its still
// pending losers to a background finalizer that keeps refunding reserved cost
// and persisting loser signatures on context.Background() for up to
// SecondaryWaitAfterWinner. That finalizer holds no activeRequests slot, so the
// drain barrier must track it separately: settling (which reads the balance the
// finalizer is still mutating) and retiring (which closes the SQLite store the
// finalizer is still writing to) must both wait until the finalizer drains too.

// TestReleaseRuntimeDefersSettleWhilePendingFinalizer pins the money branch: a
// runtime with a pending finalizer must NOT settle when its last foreground
// request drains — only once the finalizer completes as well.
func TestReleaseRuntimeDefersSettleWhilePendingFinalizer(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	g, _, settled := gatewayTestDepletionGateway(t, rt)

	g.reserveRuntime(rt, 1) // the one in-flight foreground request
	g.startFinalizer(rt)    // its background race finalizer, spawned before it returns
	rt.settlementReason = "low_balance"
	rt.settlementPending.Store(true)

	g.releaseRuntime(rt, 1) // foreground request returns; finalizer still in flight
	require.Never(t, func() bool { return settled.Load() > 0 }, 200*time.Millisecond, 20*time.Millisecond,
		"settlement must not fire while a background finalizer is still refunding/persisting")

	g.releaseFinalizer(rt) // finalizer drains
	require.Eventually(t, func() bool { return settled.Load() == 1 }, time.Second, 10*time.Millisecond,
		"settlement must fire exactly once, after the finalizer drains")
}

// TestReleaseRuntimeDefersRetireWhilePendingFinalizer pins the durability
// branch: retirement (which closes the per-runtime SQLite store) must defer
// while a finalizer is still writing loser signatures, and fire once it drains.
func TestReleaseRuntimeDefersRetireWhilePendingFinalizer(t *testing.T) {
	g, rt := newRetireTestGateway("12")
	g.reserveRuntime(rt, 0) // the one in-flight foreground request
	g.startFinalizer(rt)    // its background race finalizer
	rt.retireReason = "balance exhausted"
	rt.retirePending.Store(true)

	g.releaseRuntime(rt, 0) // foreground request returns; finalizer still in flight
	_, stillRegistered := g.runtimes["12"]
	require.True(t, stillRegistered, "retire must defer while a finalizer is in flight")

	g.releaseFinalizer(rt) // finalizer drains
	_, stillRegistered = g.runtimes["12"]
	require.False(t, stillRegistered, "retire fires once the finalizer drains")
	require.Empty(t, g.runtimeOrder)
}

// TestGoTrackedFinalizerStartsBarrierSynchronously pins the ordering contract
// the fix depends on: the start hook must fire synchronously, before the
// finalizer goroutine is spawned, so a winning foreground handler that returns
// and drops activeRequests immediately afterwards can never observe the runtime
// as quiet while this finalizer is still in flight.
func TestGoTrackedFinalizerStartsBarrierSynchronously(t *testing.T) {
	var started, done atomic.Int64
	release := make(chan struct{})
	redundancy := &Redundancy{
		onBackgroundStart: func() { started.Add(1) },
		onBackgroundDone:  func() { done.Add(1) },
	}

	redundancy.goTrackedFinalizer(func() { <-release })

	require.Equal(t, int64(1), started.Load(), "start hook must fire before goTrackedFinalizer returns")
	require.Equal(t, int64(0), done.Load(), "done hook must not fire while the finalizer runs")

	close(release)
	require.Eventually(t, func() bool { return done.Load() == 1 }, time.Second, 10*time.Millisecond,
		"done hook must fire once the finalizer completes")
}

// TestConcurrentDrainSettlesExactlyOnce guards the two-counter drain: when the
// last foreground request and the last finalizer reach zero concurrently,
// exactly one of the racing drain paths must settle — never zero (a lost
// wakeup) and, thanks to dedup, never twice.
func TestConcurrentDrainSettlesExactlyOnce(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	g, _, settled := gatewayTestDepletionGateway(t, rt)

	g.reserveRuntime(rt, 1)
	g.startFinalizer(rt)
	rt.settlementReason = "low_balance"
	rt.settlementPending.Store(true)

	var ready sync.WaitGroup
	ready.Add(2)
	start := make(chan struct{})
	go func() { ready.Done(); <-start; g.releaseRuntime(rt, 1) }()
	go func() { ready.Done(); <-start; g.releaseFinalizer(rt) }()
	ready.Wait()
	close(start) // release both drains as simultaneously as the scheduler allows

	require.Eventually(t, func() bool { return settled.Load() == 1 }, time.Second, 10*time.Millisecond,
		"a concurrent drain must settle exactly once")
	require.Never(t, func() bool { return settled.Load() > 1 }, 200*time.Millisecond, 20*time.Millisecond,
		"dedup must keep a double drain from settling twice")
}
