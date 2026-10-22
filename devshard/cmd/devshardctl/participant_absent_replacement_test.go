package main

import (
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
)

// Test flow:
//  1. Start a rotating gateway whose only active escrow was skipped at start-up because a participant of its group is absent on chain.
//  2. Keep the participant absent and run balance ticks.
//  3. Expect a replacement escrow to be created, so one host off chain plus a gateway restart does not stop the model until the host returns.
//
// Known issue: today nothing creates the replacement before the next epoch switch; the escrow only comes back when the participant does
// (load test of devshard-gateway v6 RC1: 502 "no devshard runtimes available" for the whole 5-minute absence). Skipped until fixed;
// DEVSHARD_KNOWN_ISSUES=1 runs it.
func TestGatewayReplacesAnEscrowWaitingForAnAbsentParticipant(t *testing.T) {
	if os.Getenv("DEVSHARD_KNOWN_ISSUES") == "" {
		t.Skip("known issue: no replacement escrow while a participant is absent after a gateway restart; set DEVSHARD_KNOWN_ISSUES=1 to run")
	}
	gateway, _, _ := gatewayTestDepletionGateway(t, &devshardRuntime{id: "12", model: "m"})
	savedDepletionCreate := gatewayCreateDepletionEscrow
	gatewayCreateDepletionEscrow = nil
	t.Cleanup(func() { gatewayCreateDepletionEscrow = savedDepletionCreate })
	broadcasts := stubCreateOnChain(t, "TXREPLACEMENT", 99)
	savedBuilder := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		if cfg.ID == "12" {
			return nil, fmt.Errorf("get host info: %w", bridge.ErrParticipantNotFound)
		}
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	}
	t.Cleanup(func() { gatewayRuntimeBuilder = savedBuilder })
	gateway.mu.Lock()
	delete(gateway.runtimes, "12")
	gateway.runtimeOrder = nil
	gateway.mu.Unlock()
	gateway.awaitParticipants([]startupSkippedEscrow{{EscrowID: "12", Model: "m", Reason: startupSkipReasonParticipantMissing}})

	require.Eventually(t, func() bool {
		gateway.checkBalances()
		return broadcasts.Load() > 0
	}, 3*time.Second, 50*time.Millisecond, "no replacement escrow was created while the only active escrow waits for an absent participant")
	require.True(t, gateway.isAwaitingParticipant("12"), "the participant is still absent, so the skipped escrow must keep waiting")
}
