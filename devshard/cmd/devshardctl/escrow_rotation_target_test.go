package main

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCountActiveRotationEscrowsFiltersByRoleEpochModelAndActive(t *testing.T) {
	devshards := []GatewayDevshardState{
		{RuntimeConfig: RuntimeConfig{ID: "1", Model: "m"}, Active: true, RotationRole: rotationRoleRegular, RotationEpoch: 1},
		{RuntimeConfig: RuntimeConfig{ID: "2", Model: "m"}, Active: true, RotationRole: rotationRoleRegular, RotationEpoch: 1},
		{RuntimeConfig: RuntimeConfig{ID: "3", Model: "m"}, Active: false, RotationRole: rotationRoleRegular, RotationEpoch: 1},
		{RuntimeConfig: RuntimeConfig{ID: "4", Model: "m"}, Active: true, RotationRole: rotationRoleTemp, RotationEpoch: 1},
		{RuntimeConfig: RuntimeConfig{ID: "5", Model: "m"}, Active: true, RotationRole: rotationRoleRegular, RotationEpoch: 2},
		{RuntimeConfig: RuntimeConfig{ID: "6", Model: "other"}, Active: true, RotationRole: rotationRoleRegular, RotationEpoch: 1},
		{RuntimeConfig: RuntimeConfig{ID: "7", Model: "m"}, Active: true, RotationRole: rotationRoleRegular, RotationEpoch: 1, OnHoldSince: "2026-09-23T00:00:00Z"},
	}

	require.Equal(t, 2, countActiveRotationEscrows(devshards, rotationRoleRegular, 1, "m"))
}

func TestGatewayActiveRotationEscrowCountReadsTheStore(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	require.NoError(t, store.Initialize(GatewaySettings{
		ChainREST:               "http://node:1317",
		PublicAPI:               "http://api:9000",
		DefaultModel:            "m",
		DefaultRequestMaxTokens: 1000,
		MaxConcurrentRequests:   2,
	}, []GatewayDevshardState{
		{RuntimeConfig: RuntimeConfig{ID: "1", PrivateKeyHex: "secret", Model: "m"}, Active: true, RotationRole: rotationRoleRegular, RotationEpoch: 1},
	}))
	g := &Gateway{store: store}

	count, err := g.activeRotationEscrowCount(rotationRoleRegular, 1, "m")

	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestKeyedMutexSerializesSameKeyButNotDifferentKeys(t *testing.T) {
	var locks keyedMutex

	unlockA := locks.lock(rotationTargetKey("m", rotationRoleRegular, 1))
	acquiredSameKey := make(chan struct{})
	go func() {
		unlock := locks.lock(rotationTargetKey("m", rotationRoleRegular, 1))
		close(acquiredSameKey)
		unlock()
	}()
	select {
	case <-acquiredSameKey:
		t.Fatal("a second lock for the same (model, role, epoch) must wait for the first to release")
	case <-time.After(150 * time.Millisecond):
	}

	acquiredOtherKey := make(chan struct{})
	go func() {
		unlock := locks.lock(rotationTargetKey("other-model", rotationRoleRegular, 1))
		close(acquiredOtherKey)
		unlock()
	}()
	select {
	case <-acquiredOtherKey:
	case <-time.After(2 * time.Second):
		t.Fatal("a lock for a different (model, role, epoch) must not wait behind an unrelated key")
	}

	unlockA()
	select {
	case <-acquiredSameKey:
	case <-time.After(2 * time.Second):
		t.Fatal("the second lock for the same key never acquired after the first released")
	}
}

func TestGatewayCheckBalancesDefersAReplacementUntilTheChainEpochIsKnown(t *testing.T) {
	runtime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	gateway, created, _ := gatewayTestDepletionGateway(t, runtime)
	gateway.phaseGate = &ChainPhaseGate{}

	runBalanceTick(t, gateway, runtime.id)

	require.EqualValues(t, 0, created.Load(), "a replacement minted before the epoch is known escapes the model's target")
	require.True(t, runtime.active.Load(), "the depleted escrow must keep serving until its replacement can be minted")
}

func TestGatewayCheckBalancesReplacesWithATempEscrowInsideTheBridgeWindow(t *testing.T) {
	runtime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	gateway, _, settled := gatewayTestDepletionGateway(t, runtime)
	gateway.phaseGate = &ChainPhaseGate{}
	gateway.phaseGate.storeSnapshot(ChainPhaseSnapshot{EpochIndex: 1, BlockHeight: 100, epochSwitchBlockHeight: 150})
	var rolesMutex sync.Mutex
	var roles []string
	saved := gatewayCreateDepletionEscrow
	gatewayCreateDepletionEscrow = func(_ *Gateway, _ context.Context, _ GatewaySettings, _ EscrowRotationModelSettings, role string, _ uint64) (*CreateDevshardEscrowResult, error) {
		rolesMutex.Lock()
		roles = append(roles, role)
		rolesMutex.Unlock()
		return &CreateDevshardEscrowResult{EscrowID: 99, TxHash: "OK"}, nil
	}
	t.Cleanup(func() { gatewayCreateDepletionEscrow = saved })

	runBalanceTick(t, gateway, runtime.id)

	require.Eventually(t, func() bool { return settled.Load() == 1 }, time.Second, 10*time.Millisecond, "the replaced escrow must be settled")
	rolesMutex.Lock()
	defer rolesMutex.Unlock()
	require.Equal(t, []string{rotationRoleTemp}, roles, "a regular escrow minted before PoC is retired by the next rotation tick")
}
