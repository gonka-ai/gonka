package main

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func settlementEnabledSettings() GatewaySettings {
	return GatewaySettings{EscrowRotation: EscrowRotationSettings{SettlementEnabled: true}}
}

func stubSettleDevshardOnChain(t *testing.T, fn func(id string) (*SettleDevshardEscrowResult, error)) {
	t.Helper()
	saved := gatewaySettleDevshardOnChain
	t.Cleanup(func() { gatewaySettleDevshardOnChain = saved })
	gatewaySettleDevshardOnChain = func(g *Gateway, _ context.Context, id string, _ adminSettleEscrowRequest) (*SettleDevshardEscrowResult, error) {
		result, err := fn(id)
		if err == nil {
			g.clearSettlementRetry(id) // mirror the real settleDevshardOnChain, which clears on success
		}
		return result, err
	}
}

func TestSettlementRetry_RetriesUntilSuccessThenClears(t *testing.T) {
	attempts := 0
	stubSettleDevshardOnChain(t, func(string) (*SettleDevshardEscrowResult, error) {
		attempts++
		if attempts < 3 {
			return nil, errors.New("insufficient signatures")
		}
		return &SettleDevshardEscrowResult{}, nil
	})

	gateway := &Gateway{}
	gateway.registerSettlementRetry("escrow-1", 5)
	settings := settlementEnabledSettings()

	gateway.retrySettlements(context.Background(), 5, settings)
	require.Contains(t, gateway.pendingSettlementRetries(), "escrow-1", "still failing, kept for retry")

	gateway.retrySettlements(context.Background(), 5, settings)
	require.Contains(t, gateway.pendingSettlementRetries(), "escrow-1")

	gateway.retrySettlements(context.Background(), 5, settings)
	require.NotContains(t, gateway.pendingSettlementRetries(), "escrow-1", "cleared once settled")
	require.Equal(t, 3, attempts)
}

func TestSettlementRetry_ExpiresPastDeadlineWithoutRetrying(t *testing.T) {
	attempts := 0
	stubSettleDevshardOnChain(t, func(string) (*SettleDevshardEscrowResult, error) {
		attempts++
		return nil, errors.New("insufficient signatures")
	})

	gateway := &Gateway{}
	gateway.registerSettlementRetry("escrow-1", 5)

	gateway.retrySettlements(context.Background(), 6, settlementEnabledSettings())

	require.NotContains(t, gateway.pendingSettlementRetries(), "escrow-1", "dropped past deadline")
	require.Equal(t, 0, attempts, "no on-chain call past the deadline")
}

func TestSettlementRetry_NoopWhenSettlementDisabled(t *testing.T) {
	attempts := 0
	stubSettleDevshardOnChain(t, func(string) (*SettleDevshardEscrowResult, error) {
		attempts++
		return &SettleDevshardEscrowResult{}, nil
	})

	gateway := &Gateway{}
	gateway.registerSettlementRetry("escrow-1", 5)

	gateway.retrySettlements(context.Background(), 5, GatewaySettings{})

	require.Contains(t, gateway.pendingSettlementRetries(), "escrow-1", "left untouched while settlement disabled")
	require.Equal(t, 0, attempts)
}

func TestRegisterSettlementRetry_KeepsEarliestDeadline(t *testing.T) {
	gateway := &Gateway{}
	gateway.registerSettlementRetry("escrow-1", 5)
	gateway.registerSettlementRetry("escrow-1", 9)

	require.Equal(t, uint64(5), gateway.pendingSettlementRetries()["escrow-1"])
}
