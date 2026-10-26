package main

import (
	"context"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"devshard/bridge"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fastEscrowReady(t *testing.T) {
	t.Helper()
	savedTimeout := runtimeEscrowReadyTimeout
	savedPoll := bridge.EscrowReadyPollInterval
	runtimeEscrowReadyTimeout = time.Second
	bridge.EscrowReadyPollInterval = time.Millisecond
	t.Cleanup(func() {
		runtimeEscrowReadyTimeout = savedTimeout
		bridge.EscrowReadyPollInterval = savedPoll
	})
}

func stubPendingChainBridge(t *testing.T, readyAfter int32) *atomic.Int32 {
	t.Helper()
	var calls atomic.Int32
	saved := gatewayChainBridge
	gatewayChainBridge = func(*Gateway) bridge.MainnetBridge {
		return &stubMainnetBridge{getEscrow: func(id string) (*bridge.EscrowInfo, error) {
			if calls.Add(1) <= readyAfter {
				return nil, bridge.ErrEscrowNotFound
			}
			return &bridge.EscrowInfo{EscrowID: id}, nil
		}}
	}
	t.Cleanup(func() { gatewayChainBridge = saved })
	return &calls
}

func TestCreateRotationEscrowWaitsForPendingSlots(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	fastEscrowReady(t)
	stubCreateOnChain(t, "TXPEND", 901)
	calls := stubPendingChainBridge(t, 2)

	var queriesAtBuild int32
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		queriesAtBuild = calls.Load()
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	}

	model := normalizedEscrowRotationModels(settings)[0]
	_, err := g.createRotationEscrow(context.Background(), settings, model, rotationRoleTemp, 10)
	require.NoError(t, err)

	assert.Equal(t, int32(3), queriesAtBuild, "runtime built only after the escrow became visible")
	assert.Contains(t, devshardIDs(t, store), "901")
	commitments, _ := store.LoadCommitments(context.Background())
	assert.Empty(t, commitments)
}

func TestCreateRotationEscrowClearsCommitmentForRefundedEscrow(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	stubCreateOnChain(t, "TXREFUND", 902)
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		return nil, fmt.Errorf("runtime %s: %w", cfg.ID, bridge.ErrEscrowSettled)
	}

	model := normalizedEscrowRotationModels(settings)[0]
	_, err := g.createRotationEscrow(context.Background(), settings, model, rotationRoleTemp, 10)
	require.ErrorIs(t, err, bridge.ErrEscrowSettled)

	assert.NotContains(t, devshardIDs(t, store), "902")
	commitments, _ := store.LoadCommitments(context.Background())
	assert.Empty(t, commitments, "refunded escrow is a terminal outcome")
}

func TestReconcileCommitmentsClearsRefundedEscrow(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	require.NoError(t, store.SaveCommitment(context.Background(), GatewayEscrowCommitment{TxHash: "TXSET", Model: "Qwen/Test", Role: rotationRoleTemp}))
	stubQueryTxEscrowID(t, func(string) (uint64, bool, error) { return 903, true, nil })
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		return nil, fmt.Errorf("runtime %s: %w", cfg.ID, bridge.ErrEscrowSettled)
	}

	g.reconcileCommitments(context.Background(), settings)

	assert.NotContains(t, devshardIDs(t, store), "903")
	commitments, _ := store.LoadCommitments(context.Background())
	assert.Empty(t, commitments)
}

func TestReconcileCommitmentsKeepsFreshEscrowNotFound(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	require.NoError(t, store.SaveCommitment(context.Background(), GatewayEscrowCommitment{TxHash: "TXNF", Model: "Qwen/Test", Role: rotationRoleTemp}))
	stubQueryTxEscrowID(t, func(string) (uint64, bool, error) { return 904, true, nil })
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		return nil, fmt.Errorf("runtime %s: get escrow: %w", cfg.ID, bridge.ErrEscrowNotFound)
	}

	g.reconcileCommitments(context.Background(), settings)

	commitments, _ := store.LoadCommitments(context.Background())
	require.Len(t, commitments, 1)
	assert.Equal(t, "TXNF", commitments[0].TxHash)
}

func TestReconcileCommitmentsClearsExpiredEscrowNotFound(t *testing.T) {
	g, store, settings := newRecoveryGateway(t)
	require.NoError(t, store.SaveCommitment(context.Background(), GatewayEscrowCommitment{
		TxHash: "TXGONE", Model: "Qwen/Test", Role: rotationRoleTemp, CreatedAt: time.Now().UTC().Add(-time.Hour),
	}))
	stubQueryTxEscrowID(t, func(string) (uint64, bool, error) { return 905, true, nil })
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		return nil, fmt.Errorf("runtime %s: get escrow: %w", cfg.ID, bridge.ErrEscrowNotFound)
	}

	g.reconcileCommitments(context.Background(), settings)

	commitments, _ := store.LoadCommitments(context.Background())
	assert.Empty(t, commitments)
}

func TestBuildGatewayRuntimesRetriesPendingEscrow(t *testing.T) {
	fastEscrowReady(t)
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
	}, []GatewayDevshardState{
		{RuntimeConfig: RuntimeConfig{ID: "1", PrivateKeyHex: "secret", Model: "Qwen/Test"}, Active: true},
	}))
	state, ok, err := store.LoadState(context.Background())
	require.NoError(t, err)
	require.True(t, ok)

	var builds atomic.Int32
	savedBuilder := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, deps runtimeBuildDeps) (*devshardRuntime, error) {
		if builds.Add(1) == 1 {
			return nil, fmt.Errorf("runtime %s: get escrow: %w", cfg.ID, bridge.ErrEscrowNotFound)
		}
		return &devshardRuntime{id: cfg.ID, model: deps.defaultModel}, nil
	}
	t.Cleanup(func() { gatewayRuntimeBuilder = savedBuilder })

	runtimes, _, err := buildGatewayRuntimes(context.Background(), store, &state, t.TempDir(), NewPerfTracker(nil), dialTestChainGRPC(t))
	require.NoError(t, err)
	require.Len(t, runtimes, 1)
	require.Equal(t, int32(2), builds.Load())
	require.True(t, state.Devshards[0].Active)
}
