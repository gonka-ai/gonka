package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestScheduleDepletedEscrowReplacementBoundsConcurrency proves the escrowTxSem
// semaphore caps how many escrow-create chain transactions run at once. Without
// it, a mass-depletion event fans out one goroutine+tx per escrow simultaneously
// (per-id dedup does not bound the total), hammering the RPC node. We fire more
// jobs than slots and assert the observed concurrency never exceeds the cap.
func TestScheduleDepletedEscrowReplacementBoundsConcurrency(t *testing.T) {
	const slots = 2
	const jobs = 6

	var inFlight int32
	var maxSeen int32
	release := make(chan struct{})

	oldCreate := gatewayCreateDepletionEscrow
	gatewayCreateDepletionEscrow = func(_ *Gateway, _ context.Context, _ GatewaySettings, _ EscrowRotationModelSettings, _ string, _ uint64) (*CreateDevshardEscrowResult, error) {
		cur := atomic.AddInt32(&inFlight, 1)
		for {
			m := atomic.LoadInt32(&maxSeen)
			if cur <= m || atomic.CompareAndSwapInt32(&maxSeen, m, cur) {
				break
			}
		}
		<-release // hold the slot until the test lets go
		atomic.AddInt32(&inFlight, -1)
		return nil, fmt.Errorf("stub: hold slot then fail (no deactivate path)")
	}
	t.Cleanup(func() { gatewayCreateDepletionEscrow = oldCreate })

	g := &Gateway{
		settings: GatewaySettings{
			EscrowRotation: EscrowRotationSettings{
				Enabled: true,
				Models: []EscrowRotationModelSettings{
					{ModelID: "m", Amount: 1, PrivateKeyEnv: "KEY", TargetCount: 1},
				},
			},
		},
		escrowTxSem:           make(chan struct{}, slots),
		replenishmentInFlight: make(map[string]struct{}),
	}

	for i := 0; i < jobs; i++ {
		g.scheduleDepletedEscrowReplacement(fmt.Sprintf("id-%d", i), "m", "test")
	}

	// The pool fills to exactly `slots`; the rest block on acquireEscrowTxSlot.
	require.Eventually(t, func() bool { return atomic.LoadInt32(&inFlight) == slots },
		2*time.Second, 5*time.Millisecond, "pool should fill to the cap")
	time.Sleep(100 * time.Millisecond) // give any would-be extra worker time to (wrongly) start
	require.LessOrEqual(t, atomic.LoadInt32(&maxSeen), int32(slots),
		"semaphore must cap concurrent escrow-create txs")

	close(release) // drain the remaining jobs
	require.Eventually(t, func() bool { return atomic.LoadInt32(&inFlight) == 0 },
		3*time.Second, 5*time.Millisecond, "all jobs should complete")
	require.Equal(t, int32(slots), atomic.LoadInt32(&maxSeen), "concurrency should reach, but not exceed, the cap")
}

// TestEscrowTxSlotHelpersAreNilSafe guards the tests-and-literals path: a Gateway
// built without NewGateway has a nil escrowTxSem and must run unbounded, never panic.
func TestEscrowTxSlotHelpersAreNilSafe(t *testing.T) {
	var g *Gateway
	require.NotPanics(t, func() { g.acquireEscrowTxSlot(); g.releaseEscrowTxSlot() })

	g = &Gateway{}
	require.NotPanics(t, func() { g.acquireEscrowTxSlot(); g.releaseEscrowTxSlot() })
}
