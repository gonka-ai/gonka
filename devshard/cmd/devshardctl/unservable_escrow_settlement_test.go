package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"devshard/user"

	"github.com/stretchr/testify/require"
)

func TestUnservableEscrowAtStartupMarksPendingAndSettlesViaHostRecovery(t *testing.T) {
	store, err := NewSQLiteGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })

	// Single-escrow gateway with EscrowRotation.SettlementEnabled=false (the exact #2039 repro).
	require.NoError(t, store.Initialize(context.Background(), GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "m",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
		MaxInputTokensInFlight:  200,
	}, []GatewayDevshardState{
		{RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "0000000000000000000000000000000000000000000000000000000000000001", Model: "m"}, Active: true},
	}))
	gwState, ok, err := store.LoadState(context.Background())
	require.NoError(t, err)
	require.True(t, ok)

	var sawHostRecoveryFlag atomic.Bool
	savedBuilder := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, deps runtimeBuildDeps) (*devshardRuntime, error) {
		if deps.allowHostStateRecovery {
			sawHostRecoveryFlag.Store(true)
			return gatewayTestRuntimeForLimits(t, cfg.ID, balanceMinimumThreshold, 5), nil
		}
		return nil, fmt.Errorf("runtime %s: create session: recover session: %w: missing nonce 2", cfg.ID, user.ErrLocalStateUnrecoverable)
	}
	t.Cleanup(func() { gatewayRuntimeBuilder = savedBuilder })

	runtimes, skipped, err := buildGatewayRuntimes(context.Background(), store, &gwState, t.TempDir(), NewPerfTracker(nil), dialTestChainGRPC(t))
	require.NoError(t, err)
	require.Empty(t, runtimes)
	require.Len(t, skipped, 1)
	require.Equal(t, "12", skipped[0].EscrowID)

	reloaded, ok, err := store.LoadState(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, reloaded.Devshards[0].Active)
	require.True(t, reloaded.Devshards[0].SettlementPending, "unservable escrow must be marked settlement_pending")

	var settled atomic.Int32
	oldSettle := gatewaySettleDevshardOnChain
	gatewaySettleDevshardOnChain = func(gw *Gateway, _ context.Context, id string, _ adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		deps := gw.runtimeBuildDepsFromSettings(gw.perf, gw.settings)
		deps.allowHostStateRecovery = true
		rt, buildErr := gatewayRuntimeBuilder(RuntimeConfig{ID: id, Model: "m"}, deps)
		if buildErr != nil {
			return nil, buildErr
		}
		_ = rt
		settled.Add(1)
		return &SettleDevshardEscrowResult{EscrowID: 12, TxHash: "SETTLED", Settler: "settler"}, nil
	}
	t.Cleanup(func() { gatewaySettleDevshardOnChain = oldSettle })

	g := NewGateway(nil, NewGatewayLimiter(0, 0), "m")
	g.store = store
	g.settings = reloaded.Settings
	g.chainClient = dialTestChainGRPC(t)

	g.scheduleUnservableEscrowSettlements(skipped)

	require.Eventually(t, func() bool {
		return settled.Load() == 1 && sawHostRecoveryFlag.Load()
	}, time.Second, 10*time.Millisecond)

	afterSettle, _, err := store.LoadState(context.Background())
	require.NoError(t, err)
	require.False(t, afterSettle.Devshards[0].SettlementPending, "settlement_pending cleared once settled")
}
