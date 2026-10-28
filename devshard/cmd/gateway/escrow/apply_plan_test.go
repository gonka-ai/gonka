package escrow

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
	"devshard/signing"
	"devshard/types"
)

type testClock struct{ now time.Time }

func (clock *testClock) read() time.Time { return clock.now }

func onModels(fullContextSlots, reserveCount int) string {
	return fmt.Sprintf(`[{"model_id":"qwen","target_count":1,"reserve_count":%d,"amount":1000000,"private_key_env":"K","full_context_slots":%d}]`, reserveCount, fullContextSlots)
}

// onManager has a starved qwen escrow 5 holding 40 000 and a full one 6 holding 1 000 000, settlement off, under the given models list.
func onManager(t *testing.T, modelsJSON string) (*Manager, *fakeStore, *fakeTxClient, *recordingLifecycleNarrator, *testClock) {
	t.Helper()
	testStore := newFakeStore()
	for _, escrowID := range []string{"5", "6"} {
		testStore.devshards[escrowID] = store.DevshardRecord{EscrowID: escrowID, Model: "qwen", Active: true, RotationRole: roleRegular, RotationEpoch: 7, PrivateKeyEnv: "K"}
	}
	txClient := &fakeTxClient{createEscrowFn: succeedingCreateEscrowFn(100)}
	cfg := config.Defaults()
	cfg.Rotation.Enabled, cfg.Rotation.SettlementEnabled = true, false
	cfg.Rotation.ModelsJSON = modelsJSON
	clock := &testClock{now: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	narrator := &recordingLifecycleNarrator{}
	deps := testManagerDeps(t, testStore, txClient, &fakeSnapshotSource{snapshot: planningSnapshot()}, &cfg)
	deps.Narrator, deps.Now = narrator, clock.read
	idle := types.SessionConfig{RefusalTimeout: 60, ExecutionTimeout: 1800}
	deps.Funds = fakeFunds{
		"5": {Config: idle, Balance: 40_000, TokenPrice: 1, FeePerNonce: 10},
		"6": {Config: idle, Balance: 1_000_000, TokenPrice: 1, FeePerNonce: 10},
	}
	return mustManager(t, deps), testStore, txClient, narrator, clock
}

// Test flow:
//  1. Build an on-mode manager whose guarantee wants two full escrows and holds one.
//  2. Plan once.
//  3. Assert one escrow was created, narrated with the reason guard, and its row stored as a regular.
func TestThePlannerCreatesWhatItPlansWithItsReason(t *testing.T) {
	manager, testStore, txClient, narrator, _ := onManager(t, onModels(2, 0))

	require.NoError(t, manager.planFunding(context.Background(), true))

	require.Equal(t, 1, txClient.createCalls, "CreateEscrow calls")
	require.Equal(t, map[string]string{"100": "guard"}, narrator.createdReasons())
	require.Equal(t, roleRegular, testStore.devshards["100"].RotationRole, "the created row's role")
}

// Test flow:
//  1. Build an on-mode manager whose guarantee wants one full escrow and holds it, beside an idle starved escrow.
//  2. Plan once, move the clock eleven minutes, and plan again.
//  3. Assert nothing was created and the starved escrow was parked for settlement on the second pass.
func TestThePlannerRetiresAnIdleStarvedEscrow(t *testing.T) {
	manager, testStore, txClient, _, clock := onManager(t, onModels(1, 0))

	require.NoError(t, manager.planFunding(context.Background(), true))
	clock.now = clock.now.Add(11 * time.Minute)
	require.NoError(t, manager.planFunding(context.Background(), true))

	require.Zero(t, txClient.createCalls, "CreateEscrow calls")
	if row := testStore.devshards["5"]; row.Active || !row.SettlementPending {
		t.Fatalf("row 5 = %+v, want parked for settlement as idle starved", row)
	}
	require.True(t, testStore.devshards["6"].Active, "the full escrow stays serving")
}

// Test flow:
//  1. Build an on-mode manager short of one full escrow whose wallet refuses every create.
//  2. Plan twice.
//  3. Assert the first pass only tried the create and the second parked the starved escrow under budget pressure.
//  4. Let creates land, plan a third time, and assert the create landed and the wallet is no longer short.
func TestAWalletRefusalPressesThePlannerUntilACreateLands(t *testing.T) {
	manager, testStore, txClient, _, clock := onManager(t, onModels(2, 0))
	txClient.createEscrowFn = func(context.Context, *signing.Secp256k1Signer, uint64, string, func(string) error) (chain.CreateEscrowResult, error) {
		return chain.CreateEscrowResult{}, &chain.WalletUnderfundedError{Address: "gonka1creator", Have: 10, Need: 1_001_000}
	}

	require.NoError(t, manager.planFunding(context.Background(), true))
	require.True(t, testStore.devshards["5"].Active, "the first refusal parks nothing yet")
	clock.now = clock.now.Add(TickInterval)
	require.NoError(t, manager.planFunding(context.Background(), true))
	if row := testStore.devshards["5"]; row.Active || !row.SettlementPending {
		t.Fatalf("row 5 = %+v, want parked under budget pressure while the wallet is short", row)
	}
	txClient.createEscrowFn = succeedingCreateEscrowFn(100)
	clock.now = clock.now.Add(TickInterval)
	require.NoError(t, manager.planFunding(context.Background(), true))

	require.Equal(t, 3, txClient.createCalls, "CreateEscrow calls")
	require.False(t, manager.planner.models["qwen"].walletShort, "walletShort after a create landed")
}

// Test flow:
//  1. Build an on-mode manager short of one full escrow whose chain broadcast fails.
//  2. Plan three times, one tick apart.
//  3. Assert the create was tried on the first pass, the breaker held the second, and the third tried again.
func TestABrokenCreateCoolsTheBreakerForOnePass(t *testing.T) {
	manager, _, txClient, _, clock := onManager(t, onModels(2, 0))
	txClient.createEscrowFn = func(context.Context, *signing.Secp256k1Signer, uint64, string, func(string) error) (chain.CreateEscrowResult, error) {
		return chain.CreateEscrowResult{}, errors.New("broadcast failed")
	}
	calls := make([]int, 0, 3)

	for range 3 {
		_ = manager.planFunding(context.Background(), true)
		calls = append(calls, txClient.createCalls)
		clock.now = clock.now.Add(TickInterval)
	}

	require.Equal(t, []int{1, 1, 2}, calls, "CreateEscrow calls after each pass")
}

// Test flow:
//  1. Build an on-mode manager with two full escrows that wants one standby, and escalate the regular breaker with three failures.
//  2. Plan one tick apart until the standby create lands.
//  3. Assert the regular breaker was reset by the landed create: the reserve create's own reset never touched the regular key the planner gates on.
func TestALandedPlannedCreateResetsTheRegularBreaker(t *testing.T) {
	manager, _, txClient, _, clock := onManager(t, onModels(1, 1))
	manager.funds.(fakeFunds)["5"] = manager.funds.(fakeFunds)["6"]
	for range 3 {
		manager.breaker.recordFailure("qwen", roleRegular)
	}

	for pass := 0; txClient.createCalls == 0 && pass < 10; pass++ {
		require.NoError(t, manager.planFunding(context.Background(), true))
		clock.now = clock.now.Add(TickInterval)
	}

	require.Equal(t, 1, txClient.createCalls, "CreateEscrow calls")
	_, escalated := manager.breaker.states[createBreakerKey("qwen", roleRegular)]
	require.False(t, escalated, "the regular breaker after a landed planned create")
}

// Test flow:
//  1. Build an on-mode manager short of one full escrow whose wallet refuses the create, and plan once.
//  2. Make the starved escrow full, so no regular create is wanted, and plan again.
//  3. Assert the wallet is no longer marked short.
func TestAWalletShortMarkClearsWhenNoRegularCreateIsWanted(t *testing.T) {
	manager, _, txClient, _, clock := onManager(t, onModels(2, 0))
	txClient.createEscrowFn = func(context.Context, *signing.Secp256k1Signer, uint64, string, func(string) error) (chain.CreateEscrowResult, error) {
		return chain.CreateEscrowResult{}, &chain.WalletUnderfundedError{Address: "gonka1creator", Have: 10, Need: 1_001_000}
	}
	require.NoError(t, manager.planFunding(context.Background(), true))
	require.True(t, manager.planner.models["qwen"].walletShort, "walletShort after the refusal")
	manager.funds.(fakeFunds)["5"] = manager.funds.(fakeFunds)["6"]
	clock.now = clock.now.Add(TickInterval)

	require.NoError(t, manager.planFunding(context.Background(), true))

	require.False(t, manager.planner.models["qwen"].walletShort, "walletShort once no regular create is wanted")
}
