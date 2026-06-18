package main

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// newRetireTestGateway builds a minimal Gateway holding a single active runtime
// registered in both the lookup map and the ordered slice, mirroring how the
// real registry is populated.
func newRetireTestGateway(id string) (*Gateway, *devshardRuntime) {
	rt := &devshardRuntime{id: id}
	rt.active.Store(true)
	g := &Gateway{
		runtimes:         map[string]*devshardRuntime{id: rt},
		runtimeOrder:     []*devshardRuntime{rt},
		rotationFailures: make(map[string]struct{}),
	}
	return g, rt
}

// TestRetireRuntimeRemovesRuntimeFromRegistry pins the core leak fix: retiring a
// runtime must drop it from both g.runtimes and g.runtimeOrder so its
// user.Session (and the per-runtime SQLite handles it owns) can be released.
func TestRetireRuntimeRemovesRuntimeFromRegistry(t *testing.T) {
	g, _ := newRetireTestGateway("12")

	require.True(t, g.retireRuntime("12", "test"))

	_, stillRegistered := g.runtimes["12"]
	require.False(t, stillRegistered, "runtime must be removed from g.runtimes")
	require.Empty(t, g.runtimeOrder, "runtime must be removed from g.runtimeOrder")

	// Idempotent: retiring an already-gone runtime is a no-op, not a panic.
	require.False(t, g.retireRuntime("12", "test"))
}

// TestRetireRuntimeDefersWhileRequestsInFlight guards against closing a SQLite
// store out from under an in-flight request: retirement must defer (and leave
// the runtime registered) until the request count drains to zero.
func TestRetireRuntimeDefersWhileRequestsInFlight(t *testing.T) {
	g, rt := newRetireTestGateway("12")
	rt.activeRequests.Store(1)

	require.False(t, g.retireRuntime("12", "busy"))
	_, stillRegistered := g.runtimes["12"]
	require.True(t, stillRegistered, "busy runtime must stay registered")

	rt.activeRequests.Store(0)
	require.True(t, g.retireRuntime("12", "drained"))
	_, stillRegistered = g.runtimes["12"]
	require.False(t, stillRegistered)
}

// TestRetireRotatedDevshardRetiresWithoutSettlement covers the no-settle
// terminal path: when settlement is disabled, the rotated-out runtime is
// deactivated AND retired in the same step.
func TestRetireRotatedDevshardRetiresWithoutSettlement(t *testing.T) {
	g, _ := newRetireTestGateway("12")
	settings := GatewaySettings{EscrowRotation: EscrowRotationSettings{SettlementEnabled: false}}

	settled, err := g.retireRotatedDevshard(context.Background(), "12", "rotated", settings)
	require.NoError(t, err)
	require.False(t, settled)

	_, stillRegistered := g.runtimes["12"]
	require.False(t, stillRegistered, "no-settle rotation must retire the runtime")
}

// TestRetireRotatedDevshardRetiresAfterSettlement covers the settle terminal
// path: the runtime stays alive through settlement (which reads its session)
// and is retired only once settlement succeeds.
func TestRetireRotatedDevshardRetiresAfterSettlement(t *testing.T) {
	g, _ := newRetireTestGateway("12")
	settings := GatewaySettings{EscrowRotation: EscrowRotationSettings{SettlementEnabled: true}}

	oldSettle := gatewaySettleDevshardOnChain
	gatewaySettleDevshardOnChain = func(g *Gateway, _ context.Context, id string, _ adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		// The session must still be reachable at settlement time.
		_, ok := g.runtimes[id]
		require.True(t, ok, "runtime must still be registered during settlement")
		return &SettleDevshardEscrowResult{TxHash: "OK"}, nil
	}
	t.Cleanup(func() { gatewaySettleDevshardOnChain = oldSettle })

	settled, err := g.retireRotatedDevshard(context.Background(), "12", "rotated", settings)
	require.NoError(t, err)
	require.True(t, settled)

	_, stillRegistered := g.runtimes["12"]
	require.False(t, stillRegistered, "settled rotation must retire the runtime")
}
