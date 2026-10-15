package main

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	promtestutil "github.com/prometheus/client_golang/prometheus/testutil"
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

// skippedEscrowGateway returns the depletion gateway with its one escrow active in the store, not resident, and waiting
// for a start after a failed startup build; the startup-skipped series for it is set, as the startup build sets it.
func skippedEscrowGateway(t *testing.T) *Gateway {
	t.Helper()
	gateway, _, _ := gatewayTestDepletionGateway(t, &devshardRuntime{id: "12", model: "m"})
	gateway.mu.Lock()
	delete(gateway.runtimes, "12")
	gateway.runtimeOrder = nil
	gateway.mu.Unlock()
	gateway.metrics = NewDevshardMetrics()
	gateway.metrics.RecordStartupSkippedEscrow("12", "m", startupSkipReasonRuntimeBuildFailed)
	gateway.awaitSkippedEscrows([]startupSkippedEscrow{{EscrowID: "12", Model: "m", Reason: startupSkipReasonRuntimeBuildFailed}})
	return gateway
}

func useRuntimeBuilder(t *testing.T, builder func(cfg RuntimeConfig, deps runtimeBuildDeps) (*devshardRuntime, error)) {
	t.Helper()
	saved := gatewayRuntimeBuilder
	gatewayRuntimeBuilder = builder
	t.Cleanup(func() { gatewayRuntimeBuilder = saved })
}

// Test flow:
//  1. Hold a skipped escrow whose build now succeeds, with a hold persisted in the store before the restart.
//  2. Retry.
//  3. Assert the registered runtime carries the persisted hold, as a runtime built at startup would, so routing and
//     rotation agree that the escrow is on hold.
func TestASkippedEscrowStartsWithItsPersistedHold(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	heldSince := time.Date(2026, 10, 15, 9, 30, 0, 0, time.UTC)
	_, held, err := gateway.store.HoldDevshardIfActive(context.Background(), "12", heldSince)
	require.NoError(t, err)
	require.True(t, held)
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	})

	gateway.retrySkippedEscrows(context.Background())

	runtime, registered := gateway.runtimes["12"]
	require.True(t, registered)
	require.Equal(t, heldSince.UnixNano(), runtime.holdSince.Load())
}

// Test flow:
//  1. Hold a skipped escrow. While the retry builds its runtime, a manual settlement claims the escrow, as
//     settleDevshardOnChain does for a non-resident escrow.
//  2. Let the build succeed.
//  3. Assert the retry drops its runtime instead of registering it, so no requests reach an escrow under settlement,
//     and the escrow no longer waits.
func TestASkippedEscrowIsNotRegisteredOnceSettlementClaimedItWhileBuilding(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		gateway.mu.Lock()
		gateway.claimAwaitingStart(cfg.ID)
		gateway.mu.Unlock()
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	})

	gateway.retrySkippedEscrows(context.Background())

	_, registered := gateway.runtimes["12"]
	require.False(t, registered)
	require.False(t, gateway.isAwaitingStart("12"))
}

// Test flow:
//  1. Hold a skipped escrow. While the retry builds its runtime, the escrow is deactivated in the store.
//  2. Let the build succeed.
//  3. Assert the retry drops its runtime and stops waiting, instead of serving an escrow that is inactive in the store.
func TestASkippedEscrowIsNotRegisteredOnceDeactivatedWhileBuilding(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		require.NoError(t, gateway.store.SetDevshardActive(context.Background(), cfg.ID, false))
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	})

	gateway.retrySkippedEscrows(context.Background())

	_, registered := gateway.runtimes["12"]
	require.False(t, registered)
	require.False(t, gateway.isAwaitingStart("12"))
}

// Test flow:
//  1. Hold a skipped escrow. Settle it manually while its runtime build still fails.
//  2. Assert the escrow does not wait while the settlement runs, so the retry cannot register a runtime under it.
//  3. Assert the settlement fails and the escrow waits again, active in the store, so the retry keeps looking after it
//     instead of leaving it active with no runtime and no retry.
func TestAFailedSettlementGivesASkippedEscrowBackToTheRetry(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	waitingDuringSettlement := true
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		waitingDuringSettlement = gateway.isAwaitingStart(cfg.ID)
		return nil, errors.New("recover session: database is locked")
	})

	_, err := gateway.settleDevshardOnChain(context.Background(), "12", adminSettleEscrowRequest{})

	require.Error(t, err)
	require.False(t, waitingDuringSettlement)
	require.True(t, gateway.isAwaitingStart("12"))
	require.True(t, devshardIDs(t, gateway.store)["12"].Active)
}

// Test flow:
//  1. Hold a skipped escrow, then activate it through the admin path before the retry gets to it.
//  2. Assert the runtime is registered, the escrow no longer waits, and the startup-skipped series for it is gone.
func TestAnAdminActivationEndsTheWaitOfASkippedEscrow(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	})
	record, found, err := gateway.store.GetDevshard(context.Background(), "12")
	require.NoError(t, err)
	require.True(t, found)

	_, err = gateway.addCreatedEscrowRuntime(context.Background(), record)

	require.NoError(t, err)
	_, registered := gateway.runtimes["12"]
	require.True(t, registered)
	require.False(t, gateway.isAwaitingStart("12"))
	require.Zero(t, promtestutil.CollectAndCount(gateway.metrics.startupSkippedEscrows))
}

// Test flow:
//  1. Hold a skipped escrow whose runtime another path registered while the retry was building.
//  2. Retry with a successful build.
//  3. Assert the retry keeps the registered runtime, stops waiting, and clears the startup-skipped series, so the
//     metric does not report a startup failure for an escrow that serves.
func TestARetryClearsTheSkippedSeriesOfAnEscrowAnotherPathStarted(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	started := &devshardRuntime{id: "12", model: "m"}
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		gateway.mu.Lock()
		gateway.runtimes[cfg.ID] = started
		gateway.mu.Unlock()
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	})

	gateway.retrySkippedEscrows(context.Background())

	require.Same(t, started, gateway.runtimes["12"])
	require.False(t, gateway.isAwaitingStart("12"))
	require.Zero(t, promtestutil.CollectAndCount(gateway.metrics.startupSkippedEscrows))
}

// Test flow:
//  1. Hold a skipped escrow whose build now succeeds, with the startup-skipped series set.
//  2. Retry.
//  3. Assert the series is gone once the runtime serves.
func TestARetryThatStartsASkippedEscrowClearsItsSeries(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	})

	gateway.retrySkippedEscrows(context.Background())

	_, registered := gateway.runtimes["12"]
	require.True(t, registered)
	require.Zero(t, promtestutil.CollectAndCount(gateway.metrics.startupSkippedEscrows))
}
