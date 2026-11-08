package main

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
)

// Test flow:
//  1. Restart the gateway with one active escrow while one of the two participants of its group is absent on chain.
//  2. Build the runtimes through buildRuntime with a bridge that answers GetHostInfo for that participant with ErrParticipantNotFound.
//  3. Expect the escrow loaded, active, not skipped and chosen for the model, and only the absent host blocked for chat,
//     so one host off chain plus a gateway restart does not stop the model (v6 RC1 load test: 502 for the whole absence).
func TestGatewayRestartKeepsServingAnEscrowWhileAParticipantIsAbsent(t *testing.T) {
	const hostA, hostB = "host-present", "host-absent"
	store, err := NewSQLiteGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.Initialize(context.Background(), GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "Qwen/Test",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "0000000000000000000000000000000000000000000000000000000000000001", Model: "Qwen/Test", RoutePrefix: "/devshard/dev"},
		Active:        true,
	}}))
	state, ok, err := store.LoadState(context.Background())
	require.NoError(t, err)
	require.True(t, ok)

	absent := &oneHostDownBridge{down: hostB, lookupErr: bridge.ErrParticipantNotFound}
	absent.getEscrow = func(escrowID string) (*bridge.EscrowInfo, error) {
		return &bridge.EscrowInfo{
			EscrowID: escrowID, Amount: 1_000_000, CreatorAddress: "creator",
			Slots: []string{hostA, hostB}, TokenPrice: 1, EpochID: 7, ModelID: "Qwen/Test",
		}, nil
	}
	savedBuilder := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, deps runtimeBuildDeps) (*devshardRuntime, error) {
		deps.bridge = absent
		return buildRuntime(cfg, deps)
	}
	t.Cleanup(func() { gatewayRuntimeBuilder = savedBuilder })

	runtimes, skipped, err := buildGatewayRuntimes(context.Background(), store, &state, t.TempDir(), NewPerfTracker(nil), dialTestChainGRPC(t))
	require.NoError(t, err)
	require.Empty(t, skipped)
	require.Len(t, runtimes, 1)
	t.Cleanup(func() { runtimes[0].close() })
	require.True(t, runtimes[0].active.Load())
	require.True(t, state.Devshards[0].Active)

	gateway := NewGateway(runtimes, NewGatewayLimiter(0, 0), "Qwen/Test")
	chosen, err := gateway.reserveRuntimeForModel("Qwen/Test", chatRequestCost{promptTokens: 1}, nil)
	require.NoError(t, err, "the escrow must serve while the participant is absent")
	require.Equal(t, "12", chosen.id)
	gateway.releaseRuntime(chosen, chatRequestCost{promptTokens: 1})

	picker := chosen.proxy.redundancy.picker
	require.False(t, picker.throttleBlocked(hostA), "the present host takes the requests")
	require.True(t, picker.throttleBlocked(hostB), "the absent host is skipped until the chain lists it again")
}
