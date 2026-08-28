package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestARefusedSettleClosesAdmission(t *testing.T) {
	runtime := &devshardRuntime{id: "62147", model: "m"}
	runtime.active.Store(true)
	runtime.pendingRaceCleanup.Add(1)

	gateway := NewGateway([]*devshardRuntime{runtime}, NewGatewayLimiter(0, 0), "m")
	_, err := gateway.settleDevshardOnChain(t.Context(), "62147", adminSettleEscrowRequest{})

	require.ErrorIs(t, err, errDevshardBusy, "the settle cannot run while the barrier holds")
	require.False(t, runtime.active.Load(), "the refusal must stop new traffic, or the barrier never clears")
	require.True(t, runtime.settlementPending.Load(), "the drain hook has to know a settle is owed")
}

func TestRuntimeStatusReportsBothHalvesOfTheBarrier(t *testing.T) {
	runtime := &devshardRuntime{id: "62147", model: "m"}
	runtime.active.Store(true)
	runtime.activeUserRequests.Add(2)
	runtime.pendingRaceCleanup.Add(3)

	status := runtime.snapshot()
	require.EqualValues(t, 2, status.ActiveRequests)
	require.EqualValues(t, 3, status.PendingRaceCleanup,
		"the half that blocks settle must be visible to whoever waits on it")
}
