package main

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReconcileHonorsCommittedAutoSettlementAfterModelDisabled(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	g, _, settled := gatewayTestDepletionGateway(t, rt)

	// Hold an active request so settlement is committed as pending+reason in the store.
	g.reserveRuntime(rt, chatRequestCost{promptTokens: 1})
	taken, err := g.deactivateDepletedEscrow(context.Background(), "12", "m", "low_balance", g.settings)
	require.NoError(t, err)
	require.True(t, taken)

	state, ok, err := g.store.LoadState(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, gatewayDevshardsByID(state.Devshards)["12"].SettlementPending)
	require.Equal(t, "low_balance", gatewayDevshardsByID(state.Devshards)["12"].SettlementReason)

	// Operator disables settlement for model "m" before the gateway restarts.
	updated := state.Settings
	updated.EscrowRotation.Models[0].SettlementEnabled = boolPtr(false)
	require.NoError(t, g.store.UpdateSettings(context.Background(), updated))

	// Simulate restart: no requests in flight; committed decision must still run.
	rt.activeUserRequests.Store(0)
	rt.reservedTokens.Store(0)
	g.reconcilePendingSettlements()

	require.Eventually(t, func() bool {
		return settled.Load() == 1 && !rt.settlementPending.Load()
	}, time.Second, 10*time.Millisecond)

	state, _, err = g.store.LoadState(context.Background())
	require.NoError(t, err)
	require.False(t, gatewayDevshardsByID(state.Devshards)["12"].SettlementPending)
	require.Empty(t, gatewayDevshardsByID(state.Devshards)["12"].SettlementReason)
}

func TestReconcileHonorsManualSettle409AcrossRestartWhenSettlementDisabled(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold, nonceDeactivationLimit-1)
	g, _, settled := gatewayTestDepletionGateway(t, rt, func(settings *GatewaySettings) {
		settings.EscrowRotation.SettlementEnabled = false
		settings.EscrowRotation.Models[0].SettlementEnabled = boolPtr(false)
	})

	// Manual settle arrives while a request is in flight -> returns errDevshardBusy (409)
	// and persists settleDrainingReason alongside settlement_pending.
	g.reserveRuntime(rt, chatRequestCost{promptTokens: 1})
	_, err := g.settleDevshardOnChain(context.Background(), "12", adminSettleEscrowRequest{})
	require.ErrorIs(t, err, errDevshardBusy)

	state, ok, err := g.store.LoadState(context.Background())
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, gatewayDevshardsByID(state.Devshards)["12"].SettlementPending)
	require.Equal(t, settleDrainingReason, gatewayDevshardsByID(state.Devshards)["12"].SettlementReason)

	// Simulate restart: in-flight work is gone, model settlement is still disabled,
	// yet the manual settlement decision must be honored and completed.
	rt.activeUserRequests.Store(0)
	rt.reservedTokens.Store(0)
	g.reconcilePendingSettlements()

	require.Eventually(t, func() bool {
		return settled.Load() == 1 && !rt.settlementPending.Load()
	}, time.Second, 10*time.Millisecond)

	state, _, err = g.store.LoadState(context.Background())
	require.NoError(t, err)
	require.False(t, gatewayDevshardsByID(state.Devshards)["12"].SettlementPending)
	require.Empty(t, gatewayDevshardsByID(state.Devshards)["12"].SettlementReason)
}

func TestReconcileHonorsModelOverrideSettlementAfterModelRemovedFromSettings(t *testing.T) {
	rt := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	g, _, settled := gatewayTestDepletionGateway(t, rt, func(settings *GatewaySettings) {
		settings.EscrowRotation.SettlementEnabled = false
		settings.EscrowRotation.Models[0].SettlementEnabled = boolPtr(true)
	})

	g.reserveRuntime(rt, chatRequestCost{promptTokens: 1})
	taken, err := g.deactivateDepletedEscrow(context.Background(), "12", "m", "low_balance", g.settings)
	require.NoError(t, err)
	require.True(t, taken)

	// Remove model override before restart so fallback would otherwise be global false.
	state, _, err := g.store.LoadState(context.Background())
	require.NoError(t, err)
	updated := state.Settings
	updated.EscrowRotation.Models = nil
	require.NoError(t, g.store.UpdateSettings(context.Background(), updated))

	rt.activeUserRequests.Store(0)
	rt.reservedTokens.Store(0)
	g.reconcilePendingSettlements()

	require.Eventually(t, func() bool {
		return settled.Load() == 1 && !rt.settlementPending.Load()
	}, time.Second, 10*time.Millisecond)
}
