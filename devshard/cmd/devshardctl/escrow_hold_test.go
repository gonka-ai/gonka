package main

import (
	"context"
	"math"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"devshard/types"

	"github.com/stretchr/testify/require"
)

func setEscrowBalanceAndReservation(t *testing.T, runtime *devshardRuntime, balance, reserved uint64) {
	t.Helper()
	state := runtime.proxy.sm.ExportState()
	state.Balance = balance
	state.Inferences = map[uint64]*types.InferenceRecord{}
	if reserved > 0 {
		now := time.Now().Unix()
		state.Inferences[1] = &types.InferenceRecord{Status: types.StatusStarted, ReservedCost: reserved, StartedAt: now, ConfirmedAt: now}
	}
	require.NoError(t, runtime.proxy.sm.RestoreState(state))
}

// newHeldEscrowGateway returns a gateway whose escrow "12" is below the balance threshold while a started inference still reserves enough to bring it back.
func newHeldEscrowGateway(t *testing.T) (*Gateway, *devshardRuntime, func() int32, func() int32) {
	t.Helper()
	runtime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	setEscrowBalanceAndReservation(t, runtime, balanceMinimumThreshold-1, balanceMinimumThreshold)
	gateway, created, settled := gatewayTestDepletionGateway(t, runtime)
	return gateway, runtime, created.Load, settled.Load
}

type holdTopUpRecorder struct {
	mu    sync.Mutex
	roles []string
}

func (recorder *holdTopUpRecorder) createdRoles() []string {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return append([]string(nil), recorder.roles...)
}

// newHoldTopUpGateway registers heldCount escrows of model "m" that stay held through a balance tick and servingCount healthy ones, and records every escrow the gateway mints.
func newHoldTopUpGateway(t *testing.T, heldCount, servingCount, targetCount int, snapshot ChainPhaseSnapshot) (*Gateway, *holdTopUpRecorder) {
	t.Helper()
	runtimes := make([]*devshardRuntime, 0, heldCount+servingCount)
	for index := 0; index < heldCount+servingCount; index++ {
		runtime := gatewayTestRuntimeForLimits(t, strconv.Itoa(100+index), balanceMinimumThreshold, nonceDeactivationLimit-1)
		if index < heldCount {
			setEscrowBalanceAndReservation(t, runtime, balanceMinimumThreshold-1, balanceMinimumThreshold)
		}
		runtimes = append(runtimes, runtime)
	}
	gateway, _, _ := gatewayTestDepletionGateway(t, runtimes[0], func(settings *GatewaySettings) {
		settings.EscrowRotation.Models[0].TargetCount = targetCount
	})
	gateway.mu.Lock()
	for _, runtime := range runtimes[1:] {
		runtime.active.Store(true)
		gateway.runtimes[runtime.id] = runtime
		gateway.runtimeOrder = append(gateway.runtimeOrder, runtime)
	}
	gateway.mu.Unlock()
	for index, runtime := range runtimes {
		require.NoError(t, gateway.store.UpsertDevshard(GatewayDevshardState{
			RuntimeConfig: RuntimeConfig{ID: runtime.id, PrivateKeyHex: "secret", Model: "m"},
			Active:        true,
			RotationRole:  rotationRoleRegular,
			RotationEpoch: snapshot.EpochIndex,
		}))
		if index < heldCount {
			heldSince, isHeld, err := gateway.store.HoldDevshardIfActive(runtime.id, time.Now())
			require.NoError(t, err)
			require.True(t, isHeld)
			runtime.holdSince.Store(heldSince.UnixNano())
		}
	}
	gateway.phaseGate = &ChainPhaseGate{}
	gateway.phaseGate.storeSnapshot(snapshot)

	recorder := &holdTopUpRecorder{}
	saved := gatewayCreateRotationEscrow
	gatewayCreateRotationEscrow = func(_ *Gateway, _ context.Context, _ GatewaySettings, _ EscrowRotationModelSettings, role string, _ uint64) (*CreateDevshardEscrowResult, error) {
		recorder.mu.Lock()
		recorder.roles = append(recorder.roles, role)
		recorder.mu.Unlock()
		return &CreateDevshardEscrowResult{EscrowID: 900, TxHash: "TOPUP"}, nil
	}
	t.Cleanup(func() { gatewayCreateRotationEscrow = saved })
	return gateway, recorder
}

func regularEpochSnapshot(epoch uint64) ChainPhaseSnapshot {
	return ChainPhaseSnapshot{EpochIndex: epoch, BlockHeight: 100, epochSwitchBlockHeight: 100_000}
}

func waitForHoldTopUpIdle(t *testing.T, gateway *Gateway) {
	t.Helper()
	require.Eventually(t, func() bool {
		gateway.holdTopUpsInFlight.guard.Lock()
		defer gateway.holdTopUpsInFlight.guard.Unlock()
		return len(gateway.holdTopUpsInFlight.keys) == 0
	}, 2*time.Second, 10*time.Millisecond, "hold top-up did not finish")
}

func TestGatewayTopUpKeepsServingEscrowsAtHalfTheHeldOnes(t *testing.T) {
	for _, testCase := range []struct {
		name         string
		heldCount    int
		servingCount int
		targetCount  int
		snapshot     ChainPhaseSnapshot
		createdCount int
	}{
		{name: "four_held_one_serving_mints_one_for_half", heldCount: 4, servingCount: 1, targetCount: 1, snapshot: regularEpochSnapshot(1), createdCount: 1},
		{name: "four_held_two_serving_mints_nothing", heldCount: 4, servingCount: 2, targetCount: 1, snapshot: regularEpochSnapshot(1), createdCount: 0},
		{name: "three_held_none_serving_mints_two_for_half", heldCount: 3, servingCount: 0, targetCount: 1, snapshot: regularEpochSnapshot(1), createdCount: 2},
		{name: "one_held_fifteen_serving_refills_the_target_of_sixteen", heldCount: 1, servingCount: 15, targetCount: 16, snapshot: regularEpochSnapshot(1), createdCount: 1},
		{name: "two_held_six_serving_refills_the_target_of_eight", heldCount: 2, servingCount: 6, targetCount: 8, snapshot: regularEpochSnapshot(1), createdCount: 2},
		{name: "unknown_epoch_mints_nothing", heldCount: 4, servingCount: 1, targetCount: 8, snapshot: regularEpochSnapshot(0), createdCount: 0},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			gateway, recorder := newHoldTopUpGateway(t, testCase.heldCount, testCase.servingCount, testCase.targetCount, testCase.snapshot)

			require.NoError(t, gateway.topUpServingEscrows(t.Context(), "m"))

			require.Len(t, recorder.createdRoles(), testCase.createdCount)
		})
	}
}

func TestGatewayTopUpMintsATempEscrowInsideTheBridgeWindow(t *testing.T) {
	bridgeWindow := ChainPhaseSnapshot{EpochIndex: 1, BlockHeight: 100, epochSwitchBlockHeight: 150}
	gateway, recorder := newHoldTopUpGateway(t, 2, 0, 1, bridgeWindow)

	require.NoError(t, gateway.topUpServingEscrows(t.Context(), "m"))

	require.Equal(t, []string{rotationRoleTemp}, recorder.createdRoles(), "a regular escrow minted before PoC would be retired by the next rotation tick")
}

func TestGatewayCheckBalancesTopsUpServingEscrowsForHeldOnes(t *testing.T) {
	gateway, recorder := newHoldTopUpGateway(t, 4, 1, 1, regularEpochSnapshot(1))

	gateway.checkBalances()
	waitForHoldTopUpIdle(t, gateway)

	require.Equal(t, []string{rotationRoleRegular}, recorder.createdRoles())
}

func TestGatewayCheckBalancesReplacesADepletedEscrowWhenTheOthersAreOnlyHeld(t *testing.T) {
	runtime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	gateway, created, settled := gatewayTestDepletionGateway(t, runtime, func(settings *GatewaySettings) {
		settings.EscrowRotation.Models[0].TargetCount = 1
	})
	require.NoError(t, gateway.store.UpsertDevshard(GatewayDevshardState{
		RuntimeConfig: RuntimeConfig{ID: "13", PrivateKeyHex: "secret", Model: "m"},
		Active:        true,
		RotationRole:  rotationRoleRegular,
	}))
	_, isHeld, err := gateway.store.HoldDevshardIfActive("13", time.Now())
	require.NoError(t, err)
	require.True(t, isHeld)

	runBalanceTick(t, gateway, runtime.id)

	require.EqualValues(t, 1, created.Load(), "a held escrow serves nothing, so it must not count against the target")
	require.Eventually(t, func() bool { return settled.Load() == 1 }, time.Second, 10*time.Millisecond)
}

func TestGatewayCheckBalancesHoldsAnEscrowWhoseMoneyIsInDisputes(t *testing.T) {
	runtime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	state := runtime.proxy.sm.ExportState()
	state.Balance = balanceMinimumThreshold - 1
	state.Inferences = map[uint64]*types.InferenceRecord{1: {Status: types.StatusChallenged, ActualCost: balanceMinimumThreshold}}
	require.NoError(t, runtime.proxy.sm.RestoreState(state))
	gateway, created, _ := gatewayTestDepletionGateway(t, runtime)

	runBalanceTick(t, gateway, runtime.id)

	require.EqualValues(t, 0, created.Load(), "an escrow whose money is only held by a dispute was replaced")
	accepts, reason := runtime.acceptsNewInferences()
	require.False(t, accepts)
	require.Equal(t, "on_hold", reason)
}

func TestGatewayCheckBalancesKeepsAnEscrowHeldUntilItClearsTheReleaseMargin(t *testing.T) {
	gateway, runtime, created, _ := newHeldEscrowGateway(t)
	runBalanceTick(t, gateway, runtime.id)

	setEscrowBalanceAndReservation(t, runtime, balanceMinimumThreshold, balanceMinimumThreshold)
	runBalanceTick(t, gateway, runtime.id)

	accepts, reason := runtime.acceptsNewInferences()
	require.False(t, accepts, "released exactly at the threshold, the first request would put it back on hold with a fresh timer")
	require.Equal(t, "on_hold", reason)
	require.EqualValues(t, 0, created())
}

func TestGatewayCheckBalancesHoldsALowBalanceEscrowWhoseReservationCanRestoreIt(t *testing.T) {
	gateway, runtime, created, _ := newHeldEscrowGateway(t)

	runBalanceTick(t, gateway, runtime.id)

	require.EqualValues(t, 0, created(), "an escrow whose reservation can refund it back above the threshold was replaced")
	require.True(t, runtime.active.Load(), "a held escrow must stay active, it is expected back")
	accepts, reason := runtime.acceptsNewInferences()
	require.False(t, accepts, "a held escrow must not take new inferences")
	require.Equal(t, "on_hold", reason)
	require.NotEmpty(t, devshardIDs(t, gateway.store)[runtime.id].OnHoldSince, "the hold must be saved")
}

func TestGatewayHoldsAnExhaustedEscrowWhoseReservationCanRestoreIt(t *testing.T) {
	gateway, runtime, created, _ := newHeldEscrowGateway(t)

	gateway.holdOrReplaceExhaustedEscrow(runtime.id, runtime.model)
	waitForReplacementIdle(t, gateway, runtime.id)

	require.EqualValues(t, 0, created(), "an exhausted escrow whose reservation can refund it was replaced")
	accepts, reason := runtime.acceptsNewInferences()
	require.False(t, accepts)
	require.Equal(t, "on_hold", reason)
}

func TestGatewayCheckBalancesReleasesAHeldEscrowOnceItsBalanceRecovers(t *testing.T) {
	gateway, runtime, created, _ := newHeldEscrowGateway(t)
	runBalanceTick(t, gateway, runtime.id)

	setEscrowBalanceAndReservation(t, runtime, escrowHoldReleaseBalance(runtime.proxy.sm.Config()), 0)
	runBalanceTick(t, gateway, runtime.id)

	accepts, _ := runtime.acceptsNewInferences()
	require.True(t, accepts, "an escrow whose balance recovered must take inferences again")
	require.Empty(t, devshardIDs(t, gateway.store)[runtime.id].OnHoldSince, "a released hold must be cleared from the store")
	require.EqualValues(t, 0, created())
}

func TestGatewayCheckBalancesReplacesAHeldEscrowWhoseReservationCannotRestoreIt(t *testing.T) {
	gateway, runtime, created, settled := newHeldEscrowGateway(t)
	runBalanceTick(t, gateway, runtime.id)

	setEscrowBalanceAndReservation(t, runtime, balanceMinimumThreshold-1, 0)
	runBalanceTick(t, gateway, runtime.id)

	require.EqualValues(t, 1, created(), "a held escrow that can no longer recover must be replaced")
	require.False(t, runtime.active.Load())
	require.Empty(t, devshardIDs(t, gateway.store)[runtime.id].OnHoldSince, "a deactivated escrow is never on hold")
	require.Eventually(t, func() bool { return settled() == 1 }, time.Second, 10*time.Millisecond, "the replaced escrow must be settled")
}

const aDay = 24 * time.Hour

func moveOnlyReservation(t *testing.T, runtime *devshardRuntime, status types.InferenceStatus, startedAt, confirmedAt int64) {
	t.Helper()
	state := runtime.proxy.sm.ExportState()
	state.Inferences[1].Status = status
	state.Inferences[1].StartedAt = startedAt
	state.Inferences[1].ConfirmedAt = confirmedAt
	require.NoError(t, runtime.proxy.sm.RestoreState(state))
}

func aDayAgo() int64 {
	return time.Now().Add(-aDay).Unix()
}

// Test flow:
//  1. Put an escrow on hold whose only reservation is a Started inference confirmed a day ago, with the hold itself a day old.
//  2. The execution-timeout sweep can still return it before settlement, so the next tick must keep the hold however long it has lasted.
func TestGatewayCheckBalancesKeepsAHeldEscrowWhoseStartedReservationTheSweepCanReturn(t *testing.T) {
	gateway, runtime, created, _ := newHeldEscrowGateway(t)
	runBalanceTick(t, gateway, runtime.id)

	moveOnlyReservation(t, runtime, types.StatusStarted, aDayAgo(), aDayAgo())
	runtime.holdSince.Store(time.Now().Add(-aDay).UnixNano())
	runBalanceTick(t, gateway, runtime.id)

	require.EqualValues(t, 0, created(), "replacing it would let settlement pay the reservation to the host before the sweep returns it")
	accepts, reason := runtime.acceptsNewInferences()
	require.False(t, accepts)
	require.Equal(t, "on_hold", reason)
}

// Test flow:
//  1. Build one record per status, one Pending record long past its refusal deadline, one exactly at it, and one stamped in the absurd future.
//  2. Summarize them at a fixed moment.
//  3. Expect per-status counts and costs, the two overdue Pending records, and the latest Pending deadline never past now plus the window.
//  4. Started and disputed money always counts as recoverable; overdue Pending money counts only while a vote may still be running.
func TestSummarizeEscrowHoldInFlightCountsEachStatusAndItsDeadline(t *testing.T) {
	config := types.SessionConfig{RefusalTimeout: 60, ExecutionTimeout: 600}
	now := time.Unix(10_000, 0)
	window := escrowHoldPendingWindow(config)
	inferences := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusPending, ReservedCost: 10, StartedAt: 9_990},
		2: {Status: types.StatusPending, ReservedCost: 30, StartedAt: 1_000},
		3: {Status: types.StatusPending, ReservedCost: 100, StartedAt: now.Add(-window).Unix()},
		4: {Status: types.StatusPending, ReservedCost: 200, StartedAt: math.MaxInt64},
		5: {Status: types.StatusStarted, ReservedCost: 20, StartedAt: 1_000, ConfirmedAt: 1_000},
		6: {Status: types.StatusChallenged, ReservedCost: 1000, ActualCost: 40},
		7: {Status: types.StatusFinished, ReservedCost: 80, ActualCost: 80},
		8: {Status: types.StatusTimedOut, ReservedCost: 160},
	}

	summary := summarizeEscrowHoldInFlight(inferences, config, now)

	require.Equal(t, 4, summary.pendingCount)
	require.EqualValues(t, 340, summary.pendingCost)
	require.Equal(t, 1, summary.startedCount)
	require.EqualValues(t, 20, summary.startedCost)
	require.Equal(t, 1, summary.challengedCount)
	require.EqualValues(t, 40, summary.challengedCost)
	require.Equal(t, 2, summary.overduePendingCount, "a Pending record exactly at its deadline is overdue, one stamped in the future is not")
	require.EqualValues(t, 130, summary.overduePendingCost)
	require.EqualValues(t, 270, summary.recoverable(false), "an overdue Pending reservation no vote is working on will not come back")
	require.EqualValues(t, 400, summary.recoverable(true), "a running vote may still refund an overdue Pending reservation")
	require.Equal(t, now.Add(window), summary.latestPendingDeadline, "a start stamped in the future must count from now, not overflow into the past")
}

// Test flow:
//  1. Put an escrow on hold, then turn its only reservation into a Pending inference started a day ago that no vote is working on.
//  2. A refusal that far past its deadline will not come back, so the next tick must replace the escrow.
func TestGatewayCheckBalancesReplacesAHeldEscrowWhosePendingReservationIsPastItsRefusalDeadline(t *testing.T) {
	gateway, runtime, created, settled := newHeldEscrowGateway(t)
	runBalanceTick(t, gateway, runtime.id)

	moveOnlyReservation(t, runtime, types.StatusPending, aDayAgo(), 0)
	runBalanceTick(t, gateway, runtime.id)

	require.EqualValues(t, 1, created(), "a hold whose reservations can no longer come back must end at once")
	require.False(t, runtime.active.Load())
	require.Eventually(t, func() bool { return settled() == 1 }, time.Second, 10*time.Millisecond, "the replaced escrow must be settled")
}

// Test flow:
//  1. Put an escrow on hold whose only reservation is an overdue Pending inference while a race cleanup is still voting on the escrow.
//  2. The vote may still refund it, so the next tick must keep the hold.
func TestGatewayCheckBalancesKeepsAHeldEscrowWhileItsTimeoutVotesAreRunning(t *testing.T) {
	gateway, runtime, created, _ := newHeldEscrowGateway(t)
	runBalanceTick(t, gateway, runtime.id)

	moveOnlyReservation(t, runtime, types.StatusPending, aDayAgo(), 0)
	runtime.pendingRaceCleanup.Add(1)
	t.Cleanup(func() { runtime.pendingRaceCleanup.Add(-1) })
	runBalanceTick(t, gateway, runtime.id)

	require.EqualValues(t, 0, created(), "a running timeout vote can still refund the escrow")
	accepts, reason := runtime.acceptsNewInferences()
	require.False(t, accepts)
	require.Equal(t, "on_hold", reason)
}

// Test flow:
//  1. Drop an escrow below the balance threshold while its only reservation is a Pending inference long past its refusal deadline.
//  2. Nothing will refund that reservation, so the tick must replace the escrow instead of holding it.
func TestGatewayCheckBalancesReplacesALowBalanceEscrowWhoseOnlyReservationIsAnOverduePendingOne(t *testing.T) {
	gateway, runtime, created, _ := newHeldEscrowGateway(t)
	moveOnlyReservation(t, runtime, types.StatusPending, aDayAgo(), 0)

	runBalanceTick(t, gateway, runtime.id)

	require.EqualValues(t, 1, created(), "an escrow whose only reservation is overdue must be replaced at once")
	require.False(t, runtime.active.Load())
	require.Empty(t, devshardIDs(t, gateway.store)[runtime.id].OnHoldSince, "an escrow that cannot recover is never put on hold")
}

// Test flow:
//  1. Drop an escrow below the balance threshold while its only reservation is a Started inference confirmed a day ago.
//  2. The sweep can still return it, so the tick must hold the escrow rather than replace it.
func TestGatewayCheckBalancesHoldsALowBalanceEscrowWhoseStartedReservationIsOverdue(t *testing.T) {
	gateway, runtime, created, _ := newHeldEscrowGateway(t)
	moveOnlyReservation(t, runtime, types.StatusStarted, aDayAgo(), aDayAgo())

	runBalanceTick(t, gateway, runtime.id)

	require.EqualValues(t, 0, created(), "the sweep can still return a Started reservation")
	accepts, reason := runtime.acceptsNewInferences()
	require.False(t, accepts)
	require.Equal(t, "on_hold", reason)
}

// Test flow:
//  1. Put an escrow on hold whose money sits in a dispute, with the hold itself a day old.
//  2. A dispute has no deadline of its own, so the next tick must keep the hold however long it has lasted.
func TestGatewayCheckBalancesKeepsAHeldEscrowWithADisputeForAsLongAsItLasts(t *testing.T) {
	runtime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	state := runtime.proxy.sm.ExportState()
	state.Balance = balanceMinimumThreshold - 1
	state.Inferences = map[uint64]*types.InferenceRecord{1: {Status: types.StatusChallenged, ActualCost: balanceMinimumThreshold}}
	require.NoError(t, runtime.proxy.sm.RestoreState(state))
	gateway, created, _ := gatewayTestDepletionGateway(t, runtime)
	runBalanceTick(t, gateway, runtime.id)

	runtime.holdSince.Store(time.Now().Add(-aDay).UnixNano())
	runBalanceTick(t, gateway, runtime.id)

	require.EqualValues(t, 0, created.Load(), "a dispute can still refund the escrow, the hold must wait for it")
	accepts, reason := runtime.acceptsNewInferences()
	require.False(t, accepts)
	require.Equal(t, "on_hold", reason)
}

func TestGatewayRestoreEscrowHoldsReopensASavedHold(t *testing.T) {
	gateway, runtime, _, _ := newHeldEscrowGateway(t)
	_, isHeld, err := gateway.store.HoldDevshardIfActive(runtime.id, time.Now())
	require.NoError(t, err)
	require.True(t, isHeld)

	gateway.restoreEscrowHolds()

	accepts, reason := runtime.acceptsNewInferences()
	require.False(t, accepts, "a hold saved before a restart must still keep the escrow out of routing")
	require.Equal(t, "on_hold", reason)
}

func TestGatewayStoreKeepsAHoldAcrossUpsertAndClearsItOnDeactivation(t *testing.T) {
	store, err := NewGatewayStore(filepath.Join(t.TempDir(), "gateway.db"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, store.Close()) })
	escrow := GatewayDevshardState{RuntimeConfig: RuntimeConfig{ID: "12", PrivateKeyHex: "secret", Model: "m"}, Active: true}
	require.NoError(t, store.Initialize(GatewaySettings{DefaultModel: "m"}, []GatewayDevshardState{escrow}))
	firstHold := time.Date(2026, 9, 23, 0, 0, 0, 0, time.UTC)
	_, isHeld, err := store.HoldDevshardIfActive("12", firstHold)
	require.NoError(t, err)
	require.True(t, isHeld)

	heldSince, isHeld, err := store.HoldDevshardIfActive("12", firstHold.Add(time.Hour))
	require.NoError(t, err)
	require.True(t, isHeld)
	require.True(t, firstHold.Equal(heldSince), "a second hold must keep the first start time, not extend the hold")

	require.NoError(t, store.UpsertDevshard(escrow))
	require.Equal(t, "2026-09-23T00:00:00Z", devshardIDs(t, store)["12"].OnHoldSince, "an unrelated upsert must not clear a hold")

	deactivated, err := store.DeactivateDevshardIfActive("12", false)
	require.NoError(t, err)
	require.True(t, deactivated)
	require.Empty(t, devshardIDs(t, store)["12"].OnHoldSince, "a deactivated escrow is never on hold")

	_, isHeld, err = store.HoldDevshardIfActive("12", time.Now())
	require.NoError(t, err)
	require.False(t, isHeld, "an inactive escrow must not be put on hold")
	require.Empty(t, devshardIDs(t, store)["12"].OnHoldSince)
}

func TestGatewayCheckBalancesReleasesHoldsWhenRotationIsDisabled(t *testing.T) {
	gateway, runtime, created, _ := newHeldEscrowGateway(t)
	runBalanceTick(t, gateway, runtime.id)
	gateway.mu.Lock()
	gateway.settings.EscrowRotation.Enabled = false
	gateway.mu.Unlock()

	runBalanceTick(t, gateway, runtime.id)

	accepts, _ := runtime.acceptsNewInferences()
	require.True(t, accepts, "with rotation disabled nothing resolves a hold, so it must be released")
	require.Empty(t, devshardIDs(t, gateway.store)[runtime.id].OnHoldSince)
	require.EqualValues(t, 0, created())
}

func TestGatewayCheckBalancesDoesNotHoldAnEscrowItCannotReplace(t *testing.T) {
	runtime := gatewayTestRuntimeForLimits(t, "12", balanceMinimumThreshold-1, nonceDeactivationLimit-1)
	setEscrowBalanceAndReservation(t, runtime, balanceMinimumThreshold-1, balanceMinimumThreshold)
	gateway, _, _ := gatewayTestDepletionGateway(t, runtime, withoutReplacementModel)

	runBalanceTick(t, gateway, runtime.id)

	accepts, _ := runtime.acceptsNewInferences()
	require.True(t, accepts, "an escrow rotation cannot replace must keep serving instead of being held forever")
	require.Empty(t, devshardIDs(t, gateway.store)[runtime.id].OnHoldSince)
}
