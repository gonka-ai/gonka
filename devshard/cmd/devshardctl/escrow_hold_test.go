package main

import (
	"context"
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
		state.Inferences[1] = &types.InferenceRecord{Status: types.StatusStarted, ReservedCost: reserved}
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

func TestRecoverableInFlightCountsReservationsAndDisputedCost(t *testing.T) {
	inferences := map[uint64]*types.InferenceRecord{
		1: {Status: types.StatusPending, ReservedCost: 10},
		2: {Status: types.StatusStarted, ReservedCost: 20},
		3: {Status: types.StatusChallenged, ReservedCost: 1000, ActualCost: 40},
		4: {Status: types.StatusFinished, ReservedCost: 80, ActualCost: 80},
		5: {Status: types.StatusTimedOut, ReservedCost: 160},
	}

	require.EqualValues(t, 70, recoverableInFlight(inferences))
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

func TestGatewayCheckBalancesReplacesAHeldEscrowPastItsDeadline(t *testing.T) {
	gateway, runtime, created, settled := newHeldEscrowGateway(t)
	runBalanceTick(t, gateway, runtime.id)

	runtime.holdSince.Store(time.Now().Add(-2 * maximumEscrowHoldDuration(runtime.proxy.sm.Config())).UnixNano())
	runBalanceTick(t, gateway, runtime.id)

	require.EqualValues(t, 1, created(), "a hold that outlived every reservation's timeout must end in a replacement")
	require.False(t, runtime.active.Load())
	require.Eventually(t, func() bool { return settled() == 1 }, time.Second, 10*time.Millisecond, "the replaced escrow must be settled")
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
