package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/bridge"
	"devshard/user"
)

// Test flow:
//  1. Hold one active regular escrow in the store and mark it as waiting for a participant record.
//  2. Count the active regular escrows for its model and epoch.
//  3. Assert it is not counted, so a slot that serves nothing does not take a rotation target.
func TestAnEscrowWaitingForAParticipantTakesNoRotationTarget(t *testing.T) {
	gateway, _, _ := gatewayTestDepletionGateway(t, &devshardRuntime{id: "12", model: "m"})
	before, err := gateway.activeRotationEscrowCount(context.Background(), rotationRoleRegular, 1, "m")
	require.NoError(t, err)

	gateway.awaitSkippedEscrows([]startupSkippedEscrow{{EscrowID: "12", Model: "m", Reason: startupSkipReasonParticipantMissing}})
	after, err := gateway.activeRotationEscrowCount(context.Background(), rotationRoleRegular, 1, "m")

	require.NoError(t, err)
	require.Equal(t, 1, before)
	require.Zero(t, after)
}

// Test flow:
//  1. Mark an active escrow as waiting for a participant record, with a builder that first finds no participant and then succeeds.
//  2. Retry twice.
//  3. Assert the first retry registers nothing and the second registers the runtime and stops waiting, without a process restart.
func TestAnEscrowWaitingForAParticipantStartsOnceTheRecordAppears(t *testing.T) {
	gateway, _, _ := gatewayTestDepletionGateway(t, &devshardRuntime{id: "12", model: "m"})
	gateway.mu.Lock()
	delete(gateway.runtimes, "12")
	gateway.runtimeOrder = nil
	gateway.mu.Unlock()
	saved := gatewayRuntimeBuilder
	attempts := 0
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		attempts++
		if attempts == 1 {
			return nil, fmt.Errorf("get host info: %w", bridge.ErrParticipantNotFound)
		}
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	}
	t.Cleanup(func() { gatewayRuntimeBuilder = saved })
	gateway.awaitSkippedEscrows([]startupSkippedEscrow{{EscrowID: "12", Model: "m", Reason: startupSkipReasonParticipantMissing}})

	gateway.retrySkippedEscrows(context.Background())
	_, registeredWhileMissing := gateway.runtimes["12"]
	gateway.retrySkippedEscrows(context.Background())
	_, registeredOnceFound := gateway.runtimes["12"]

	require.False(t, registeredWhileMissing)
	require.True(t, registeredOnceFound)
	require.False(t, gateway.isAwaitingStart("12"))
}

// Test flow:
//  1. Mark an active escrow as waiting for a participant record, with a builder that reports the escrow itself is gone from the chain.
//  2. Retry.
//  3. Assert the escrow is deactivated and no longer waits, as a start-up that finds it gone would do, instead of being retried every tick.
func TestAnEscrowWaitingForAParticipantStopsWaitingOnceTheEscrowIsGone(t *testing.T) {
	gateway, _, _ := gatewayTestDepletionGateway(t, &devshardRuntime{id: "12", model: "m"})
	gateway.mu.Lock()
	delete(gateway.runtimes, "12")
	gateway.runtimeOrder = nil
	gateway.mu.Unlock()
	saved := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(RuntimeConfig, runtimeBuildDeps) (*devshardRuntime, error) {
		return nil, fmt.Errorf("get escrow: %w", bridge.ErrEscrowNotFound)
	}
	t.Cleanup(func() { gatewayRuntimeBuilder = saved })
	gateway.awaitSkippedEscrows([]startupSkippedEscrow{{EscrowID: "12", Model: "m", Reason: startupSkipReasonParticipantMissing}})

	gateway.retrySkippedEscrows(context.Background())

	require.False(t, gateway.isAwaitingStart("12"))
	require.False(t, devshardIDs(t, gateway.store)["12"].Active)
}

// Test flow:
//  1. Mark an active escrow as skipped because its runtime build failed, with a builder that fails once for a reason
//     with no final meaning and then succeeds.
//  2. Retry twice.
//  3. Assert the first retry keeps the escrow waiting and active, and the second registers the runtime and stops waiting.
func TestASkippedEscrowStartsOnceItsBuildSucceeds(t *testing.T) {
	gateway, _, _ := gatewayTestDepletionGateway(t, &devshardRuntime{id: "12", model: "m"})
	gateway.mu.Lock()
	delete(gateway.runtimes, "12")
	gateway.runtimeOrder = nil
	gateway.mu.Unlock()
	saved := gatewayRuntimeBuilder
	attempts := 0
	gatewayRuntimeBuilder = func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		attempts++
		if attempts == 1 {
			return nil, errors.New("recover session: rebuild validation obs: database is locked")
		}
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	}
	t.Cleanup(func() { gatewayRuntimeBuilder = saved })
	gateway.awaitSkippedEscrows([]startupSkippedEscrow{{EscrowID: "12", Model: "m", Reason: startupSkipReasonRuntimeBuildFailed}})

	gateway.retrySkippedEscrows(context.Background())
	_, registeredAfterFailure := gateway.runtimes["12"]
	stillActive := devshardIDs(t, gateway.store)["12"].Active
	stillWaiting := gateway.isAwaitingStart("12")
	gateway.retrySkippedEscrows(context.Background())
	_, registeredOnceBuilt := gateway.runtimes["12"]

	require.False(t, registeredAfterFailure)
	require.True(t, stillActive)
	require.True(t, stillWaiting)
	require.True(t, registeredOnceBuilt)
	require.False(t, gateway.isAwaitingStart("12"))
}

// Test flow:
//  1. Mark an active escrow as skipped because its runtime build failed, with a builder that now reports its local state
//     unrecoverable.
//  2. Retry.
//  3. Assert the escrow is deactivated and no longer waits, as the startup build does for that reason.
func TestASkippedEscrowIsDeactivatedOnceItsLocalStateIsUnrecoverable(t *testing.T) {
	gateway, _, _ := gatewayTestDepletionGateway(t, &devshardRuntime{id: "12", model: "m"})
	gateway.mu.Lock()
	delete(gateway.runtimes, "12")
	gateway.runtimeOrder = nil
	gateway.mu.Unlock()
	saved := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = func(RuntimeConfig, runtimeBuildDeps) (*devshardRuntime, error) {
		return nil, fmt.Errorf("recover session: %w", user.ErrLocalStateUnrecoverable)
	}
	t.Cleanup(func() { gatewayRuntimeBuilder = saved })
	gateway.awaitSkippedEscrows([]startupSkippedEscrow{{EscrowID: "12", Model: "m", Reason: startupSkipReasonRuntimeBuildFailed}})

	gateway.retrySkippedEscrows(context.Background())

	require.False(t, gateway.isAwaitingStart("12"))
	require.False(t, devshardIDs(t, gateway.store)["12"].Active)
}
