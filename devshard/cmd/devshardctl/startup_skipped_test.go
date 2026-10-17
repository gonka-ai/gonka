package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
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

// An in-flight retry owns the build before it can start workers. Admin starts
// must wait for it to finish, then may start the escrow on a later request.
func TestAdminStartsDoNotBuildOverAnInFlightRetry(t *testing.T) {
	for _, action := range []string{"add", "activate"} {
		t.Run(action, func(t *testing.T) {
			gateway := skippedEscrowGateway(t)
			gateway.baseStorageDir = t.TempDir()
			builds := 0
			start := func() *httptest.ResponseRecorder {
				response := httptest.NewRecorder()
				if action == "add" {
					gateway.handleAdminAddDevshard(response, httptest.NewRequest(http.MethodPost,
						"/v1/admin/devshards", strings.NewReader(`{"id":"12"}`)))
				} else {
					gateway.handleAdminActivateDevshard(response, httptest.NewRequest(http.MethodPost,
						"/v1/admin/devshards/12/activate", nil), "12")
				}
				return response
			}
			useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
				builds++
				if builds == 1 {
					gateway.retrySkippedEscrows(context.Background())
					response := start()
					require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
					_, err := gateway.settleDevshardOnChain(context.Background(), cfg.ID, adminSettleEscrowRequest{})
					require.ErrorIs(t, err, errDevshardBusy)
					return nil, errors.New("temporary recovery failure")
				}
				return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
			})

			gateway.retrySkippedEscrows(context.Background())
			require.Equal(t, 1, builds)
			require.True(t, gateway.isAwaitingStart("12"))
			response := start()
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			require.Equal(t, 2, builds)
			require.Contains(t, gateway.runtimes, "12")
			require.True(t, devshardIDs(t, gateway.store)["12"].Active)
			require.False(t, gateway.isAwaitingStart("12"))
			require.Zero(t, promtestutil.CollectAndCount(gateway.metrics.startupSkippedEscrows))
		})
	}
}

func TestAnAdminAddEndsTheWaitAndCountsTheServingEscrowForRotation(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	gateway.baseStorageDir = t.TempDir()
	staleRecord := devshardIDs(t, gateway.store)["12"]
	builds := 0
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		builds++
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	})
	response := httptest.NewRecorder()
	gateway.handleAdminAddDevshard(response, httptest.NewRequest(http.MethodPost,
		"/v1/admin/devshards", strings.NewReader(`{"id":"12"}`)))

	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.False(t, gateway.isAwaitingStart("12"))
	require.Zero(t, promtestutil.CollectAndCount(gateway.metrics.startupSkippedEscrows))
	count, err := gateway.activeRotationEscrowCount(context.Background(), rotationRoleRegular, 1, "m")
	require.NoError(t, err)
	require.Equal(t, 1, count)
	gateway.retrySkippedEscrows(context.Background())
	gateway.retrySkippedEscrow(context.Background(), staleRecord, startupSkipReasonRuntimeBuildFailed, gateway.settings)
	require.Equal(t, 1, builds, "the admin-started escrow must not be rebuilt on the retry tick")
}

func TestSettlementExcludesAdminStartsUntilItsClaimIsReleased(t *testing.T) {
	for _, action := range []string{"add", "activate"} {
		t.Run(action, func(t *testing.T) {
			gateway := skippedEscrowGateway(t)
			gateway.mu.Lock()
			release := gateway.claimEscrowForSettlement("12")
			gateway.mu.Unlock()
			defer release()
			builds := 0
			useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
				builds++
				return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
			})
			for _, endedWait := range []bool{false, true} {
				if endedWait {
					gateway.stopAwaitingStart("12")
				}
				response := httptest.NewRecorder()
				if action == "add" {
					gateway.handleAdminAddDevshard(response, httptest.NewRequest(http.MethodPost,
						"/v1/admin/devshards", strings.NewReader(`{"id":"12"}`)))
				} else {
					gateway.handleAdminActivateDevshard(response, httptest.NewRequest(http.MethodPost,
						"/v1/admin/devshards/12/activate", nil), "12")
				}
				require.Equal(t, http.StatusConflict, response.Code, response.Body.String())
			}
			require.Zero(t, builds, "a conflicting builder must never start its workers")
			require.Empty(t, gateway.runtimes)
		})
	}
}

func TestAdminStartsRestoreThePersistedHoldOfASkippedEscrow(t *testing.T) {
	for _, action := range []string{"add", "activate"} {
		t.Run(action, func(t *testing.T) {
			gateway := skippedEscrowGateway(t)
			gateway.baseStorageDir = t.TempDir()
			heldSince := time.Now().UTC()
			_, held, err := gateway.store.HoldDevshardIfActive(context.Background(), "12", heldSince)
			require.NoError(t, err)
			require.True(t, held)
			useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
				rt := &devshardRuntime{id: cfg.ID, model: cfg.Model}
				rt.active.Store(true)
				return rt, nil
			})
			response := httptest.NewRecorder()
			if action == "add" {
				gateway.handleAdminAddDevshard(response, httptest.NewRequest(http.MethodPost,
					"/v1/admin/devshards", strings.NewReader(`{"id":"12"}`)))
			} else {
				gateway.handleAdminActivateDevshard(response, httptest.NewRequest(http.MethodPost,
					"/v1/admin/devshards/12/activate", nil), "12")
			}
			require.Equal(t, http.StatusOK, response.Code, response.Body.String())
			rt := gateway.runtimes["12"]
			require.Equal(t, heldSince.UnixNano(), rt.holdSince.Load())
			accepts, reason := rt.acceptsNewInferences()
			require.False(t, accepts)
			require.Equal(t, "on_hold", reason)
			count, err := gateway.activeRotationEscrowCount(context.Background(), rotationRoleRegular, 1, "m")
			require.NoError(t, err)
			require.Zero(t, count)
		})
	}
}

func TestAdminCanDeactivateASkippedEscrowDuringItsRetryBuild(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		response := httptest.NewRecorder()
		gateway.handleAdminDeactivateDevshard(response, httptest.NewRequest(http.MethodPost,
			"/v1/admin/devshards/12/deactivate", nil), cfg.ID)
		require.Equal(t, http.StatusOK, response.Code, response.Body.String())
		cleanup := httptest.NewRecorder()
		gateway.handleAdminCleanDevshard(cleanup, httptest.NewRequest(http.MethodPost,
			"/v1/admin/devshards/12/clean", nil), cfg.ID)
		require.Equal(t, http.StatusConflict, cleanup.Code, "cleanup must wait for the builder to stop")
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	})

	gateway.retrySkippedEscrows(context.Background())

	require.False(t, devshardIDs(t, gateway.store)["12"].Active)
	require.False(t, gateway.isAwaitingStart("12"))
	require.Empty(t, gateway.runtimes)
	require.Zero(t, promtestutil.CollectAndCount(gateway.metrics.startupSkippedEscrows))
	cleanup := httptest.NewRecorder()
	gateway.handleAdminCleanDevshard(cleanup, httptest.NewRequest(http.MethodPost,
		"/v1/admin/devshards/12/clean", nil), "12")
	require.Equal(t, http.StatusOK, cleanup.Code, cleanup.Body.String())
	require.NotContains(t, devshardIDs(t, gateway.store), "12")
}

func TestAFinalRetryErrorDoesNotDeactivateAnEscrowTakenAwayWhileBuilding(t *testing.T) {
	for _, action := range []string{"claimed", "no_longer_waiting", "resident"} {
		t.Run(action, func(t *testing.T) {
			gateway := skippedEscrowGateway(t)
			useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
				gateway.mu.Lock()
				defer gateway.mu.Unlock()
				if action == "claimed" {
					gateway.claimEscrowForSettlement(cfg.ID)
				} else if action == "resident" {
					gateway.runtimes[cfg.ID] = &devshardRuntime{id: cfg.ID, model: cfg.Model}
				} else {
					gateway.stopAwaitingStart(cfg.ID)
				}
				return nil, bridge.ErrEscrowNotFound
			})

			gateway.retrySkippedEscrows(context.Background())

			require.True(t, devshardIDs(t, gateway.store)["12"].Active)
			if action == "claimed" {
				require.True(t, gateway.isAwaitingStart("12"))
				require.False(t, gateway.awaitsRetry("12"))
				require.Equal(t, 1, promtestutil.CollectAndCount(gateway.metrics.startupSkippedEscrows))
			} else {
				require.False(t, gateway.isAwaitingStart("12"))
				require.Zero(t, promtestutil.CollectAndCount(gateway.metrics.startupSkippedEscrows))
			}
		})
	}
}

func TestEndingASkippedEscrowWaitClearsItsSeries(t *testing.T) {
	for _, action := range []string{"inactive_before_retry", "inactive_while_building", "final_error", "settlement"} {
		t.Run(action, func(t *testing.T) {
			gateway := skippedEscrowGateway(t)
			useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
				if action == "final_error" {
					return nil, bridge.ErrEscrowSettled
				}
				if action == "inactive_while_building" {
					require.NoError(t, gateway.store.SetDevshardActive(context.Background(), cfg.ID, false))
				}
				return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
			})
			if action == "inactive_before_retry" {
				require.NoError(t, gateway.store.SetDevshardActive(context.Background(), "12", false))
			}
			if action == "settlement" {
				// Persisting deactivation ends the wait even if settlement then fails
				// while loading an invalid key, before any chain broadcast.
				_, err := gateway.settleDevshardOnChain(context.Background(), "12", adminSettleEscrowRequest{PrivateKey: "invalid"})
				require.Error(t, err)
			} else {
				gateway.retrySkippedEscrows(context.Background())
			}

			require.False(t, devshardIDs(t, gateway.store)["12"].Active)
			require.False(t, gateway.isAwaitingStart("12"))
			require.Zero(t, promtestutil.CollectAndCount(gateway.metrics.startupSkippedEscrows))
		})
	}
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
//     and the escrow keeps waiting under the claim: it still has no runtime, so rotation must not count it.
func TestASkippedEscrowIsNotRegisteredOnceSettlementClaimedItWhileBuilding(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		gateway.mu.Lock()
		gateway.claimEscrowForSettlement(cfg.ID)
		gateway.mu.Unlock()
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	})

	gateway.retrySkippedEscrows(context.Background())

	_, registered := gateway.runtimes["12"]
	require.False(t, registered)
	require.True(t, gateway.isAwaitingStart("12"))
	require.False(t, gateway.awaitsRetry("12"))
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
//  2. Assert that while the settlement runs the retry may not register a runtime for the escrow, and the escrow
//     still waits, so rotation does not count it as serving.
//  3. Assert the settlement fails and the retry looks after the escrow again, active in the store, instead of leaving
//     it active with no runtime and no retry.
func TestAFailedSettlementGivesASkippedEscrowBackToTheRetry(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	retriableDuringSettlement, waitingDuringSettlement := true, false
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		retriableDuringSettlement = gateway.awaitsRetry(cfg.ID)
		waitingDuringSettlement = gateway.isAwaitingStart(cfg.ID)
		return nil, errors.New("recover session: database is locked")
	})

	_, err := gateway.settleDevshardOnChain(context.Background(), "12", adminSettleEscrowRequest{})

	require.Error(t, err)
	require.False(t, retriableDuringSettlement)
	require.True(t, waitingDuringSettlement)
	require.True(t, gateway.awaitsRetry("12"))
	require.True(t, devshardIDs(t, gateway.store)["12"].Active)
}

// A second settlement must return busy before building another transient
// session, while the first keeps ownership until its failed build returns.
func TestOverlappingSettlementsDoNotBuildASkippedEscrowTwice(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	retriableAfterInnerFailure := true
	builds := 0
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		builds++
		if builds == 1 {
			_, innerErr := gateway.settleDevshardOnChain(context.Background(), cfg.ID, adminSettleEscrowRequest{})
			require.ErrorIs(t, innerErr, errDevshardBusy)
			retriableAfterInnerFailure = gateway.awaitsRetry(cfg.ID)
		}
		return nil, errors.New("recover session: database is locked")
	})

	_, err := gateway.settleDevshardOnChain(context.Background(), "12", adminSettleEscrowRequest{})

	require.Error(t, err)
	require.Equal(t, 1, builds)
	require.False(t, retriableAfterInnerFailure)
	require.True(t, gateway.awaitsRetry("12"))
}

// Test flow:
//  1. Hold a skipped escrow and claim it for a settlement.
//  2. Count the active regular escrows for its model and epoch.
//  3. Assert the claimed escrow is still not counted: it has no runtime, so a slot that serves nothing must not take a
//     rotation target.
func TestAClaimedEscrowTakesNoRotationTarget(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	gateway.mu.Lock()
	gateway.claimEscrowForSettlement("12")
	gateway.mu.Unlock()

	count, err := gateway.activeRotationEscrowCount(context.Background(), rotationRoleRegular, 1, "m")

	require.NoError(t, err)
	require.Zero(t, count)
}

// Test flow:
//  1. Hold a skipped escrow whose build now returns a runtime with a running dispatcher, while a settlement claimed the
//     escrow during the build.
//  2. Retry.
//  3. Assert the dropped runtime's dispatcher is stopped, so rejected registrations do not leak a goroutine per tick.
func TestARejectedRetriedRuntimeStopsItsDispatcher(t *testing.T) {
	gateway := skippedEscrowGateway(t)
	env := setupTestProxy(t, 3, nil, true)
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		gateway.mu.Lock()
		gateway.claimEscrowForSettlement(cfg.ID)
		gateway.mu.Unlock()
		return &devshardRuntime{id: cfg.ID, model: cfg.Model, proxy: env.proxy}, nil
	})
	require.False(t, env.proxy.redundancy.picker.isStopped())

	gateway.retrySkippedEscrows(context.Background())

	_, registered := gateway.runtimes["12"]
	require.False(t, registered)
	require.True(t, env.proxy.redundancy.picker.isStopped())
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

// Test flow:
//  1. Build a managed gateway with one active escrow in the store that the startup build skipped, with a builder that
//     now succeeds.
//  2. Assert the first balance check, which runs as the gateway starts, registers the escrow at once. The waiting set
//     is filled before the workers start; filled afterwards, the first retry would find nothing and the escrow would
//     wait for the next tick, and a settlement started by a worker in between would not find it waiting.
func TestNewManagedGatewayWaitsForSkippedEscrowsBeforeItsWorkersStart(t *testing.T) {
	store, err := NewSQLiteGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	settings := GatewaySettings{DefaultModel: "m"}
	require.NoError(t, store.Initialize(context.Background(), settings, []GatewayDevshardState{{
		RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "m"},
		Active:        true,
	}}))
	useRuntimeBuilder(t, func(cfg RuntimeConfig, _ runtimeBuildDeps) (*devshardRuntime, error) {
		return &devshardRuntime{id: cfg.ID, model: cfg.Model}, nil
	})

	gateway := NewManagedGateway(nil, NewGatewayLimiter(0, 0), settings, t.TempDir(), store, nil, nil, nil, nil, nil,
		[]startupSkippedEscrow{{EscrowID: "12", Model: "m", Reason: startupSkipReasonRuntimeBuildFailed}})
	t.Cleanup(gateway.stopEscrowRotator)

	registered := func() bool {
		gateway.mu.Lock()
		defer gateway.mu.Unlock()
		_, ok := gateway.runtimes["12"]
		return ok
	}
	seriesCleared := func() bool { return promtestutil.CollectAndCount(gateway.metrics.startupSkippedEscrows) == 0 }
	require.Eventually(t, func() bool { return registered() && seriesCleared() },
		5*time.Second, 10*time.Millisecond, "the first balance check did not start the skipped escrow")
	require.False(t, gateway.isAwaitingStart("12"))
}
