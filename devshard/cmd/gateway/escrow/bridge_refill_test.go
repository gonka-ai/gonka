package escrow

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
	"devshard/types"
)

const (
	drainedBridgeModels = `[{"model_id":"qwen","temp_count":1,"target_count":1,"reserve_count":0,"amount":1000000,"private_key_env":"K","full_context_slots":1}]`
	cappedBridgeModels  = `[{"model_id":"qwen","temp_count":1,"target_count":1,"reserve_count":0,"amount":1000000,"private_key_env":"K","full_context_slots":1,"max_unsettled":3}]`
)

func drainedBridgeManager(t *testing.T) (*Manager, *fakeStore, *fakeTxClient, *recordingLifecycleNarrator) {
	t.Helper()
	testStore := newFakeStore()
	testStore.devshards["5"] = store.DevshardRecord{EscrowID: "5", Model: "qwen", Active: true, RotationRole: roleTemp, RotationEpoch: 8, PrivateKeyEnv: "K"}
	txClient := &fakeTxClient{createEscrowFn: succeedingCreateEscrowFn(100)}
	cfg := config.Defaults()
	cfg.Rotation.Enabled, cfg.Rotation.SettlementEnabled = true, false
	cfg.Rotation.ModelsJSON = drainedBridgeModels
	snapshot := planningSnapshot()
	snapshot.EpochSwitchBlockHeight = snapshot.BlockHeight + cfg.Rotation.PrePoCBlocks/2
	narrator := &recordingLifecycleNarrator{}
	deps := testManagerDeps(t, testStore, txClient, &fakeSnapshotSource{snapshot: snapshot}, &cfg)
	deps.Narrator = narrator
	deps.Funds = fakeFunds{"5": {Config: types.SessionConfig{RefusalTimeout: 60, ExecutionTimeout: 1800}, Balance: 40_000, TokenPrice: 1, FeePerNonce: 10}}
	return mustManager(t, deps), testStore, txClient, narrator
}

// Test flow:
//  1. Build a manager inside the bridge window whose one temp meets temp_count and is starved.
//  2. Run one tick.
//  3. Assert one escrow was created with the reason guard, stored as a temp at the bridge label, and the starved temp still serves.
func TestATickInsideTheWindowRefillsADrainedBridgeWithATemp(t *testing.T) {
	manager, testStore, txClient, narrator := drainedBridgeManager(t)

	require.NoError(t, manager.tick(context.Background()))

	require.Equal(t, 1, txClient.createCalls, "CreateEscrow calls")
	require.Equal(t, map[string]string{"100": "guard"}, narrator.createdReasons())
	created := testStore.devshards["100"]
	require.Equal(t, roleTemp, created.RotationRole, "the created row's role")
	require.Equal(t, int64(8), created.RotationEpoch, "the created row's label")
	require.True(t, testStore.devshards["5"].Active, "the starved temp stays serving")
}

// Test flow:
//  1. Table-driven: a manager inside the bridge window whose one temp is starved, with an open create commitment of each role at the bridge label.
//  2. Plan once.
//  3. Assert the temp commitment stands in for the refill and the regular one does not.
func TestAnOpenTempCommitmentStandsInForTheRefillInsideTheWindow(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		role        store.Role
		wantCreates int
	}{
		{name: "a temp commitment", role: roleTemp, wantCreates: 0},
		{name: "a regular commitment", role: roleRegular, wantCreates: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manager, testStore, txClient, _ := drainedBridgeManager(t)
			testStore.commitments["TX-OPEN"] = store.Commitment{TxHash: "TX-OPEN", Model: "qwen", Role: testCase.role, Epoch: 8}

			require.NoError(t, manager.planFunding(context.Background(), true))

			require.Equal(t, testCase.wantCreates, txClient.createCalls, "CreateEscrow calls")
		})
	}
}

// Test flow:
//  1. Table-driven: a manager inside the bridge window with max_unsettled 3, target_count 1 and temp_count 1 whose one temp is starved, alone or beside a second temp row: left out of the plan while active, its signing key missing or its amount unresolved, left out as no longer active, or live and starved under the current epoch's label instead of the bridge label.
//  2. Plan once.
//  3. Assert the lone temp is refilled, that beside a second active temp row nothing is created whatever its label, both temp rows and the next epoch's regular filling the budget, and that a temp row no longer active counts as a row only and leaves room for the refill.
func TestATempRowLeftOutOfThePlanCountsTowardTheWindowRoom(t *testing.T) {
	for _, testCase := range []struct {
		name        string
		second      func(row *store.DevshardRecord)
		secondLive  bool
		wantCreates int
	}{
		{name: "the starved temp alone", wantCreates: 1},
		{name: "a second temp whose key is missing", second: func(row *store.DevshardRecord) { row.PrivateKeyEnv = "GONE" }},
		{name: "a second temp whose amount is unresolved", second: func(row *store.DevshardRecord) { row.Amount = 0 }},
		{name: "a second temp no longer active", second: func(row *store.DevshardRecord) { row.Active = false }, wantCreates: 1},
		{name: "a second temp live and starved under the current epoch's label", second: func(row *store.DevshardRecord) { row.RotationEpoch = int64(planningSnapshot().EpochIndex) }, secondLive: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			manager, testStore, txClient, _ := drainedBridgeManager(t)
			manager.signer = &keyedSignerSource{fakeSignerSource: fakeSignerSource{signer: testSigner(t)}, missing: "GONE"}
			configured := *manager.config.Load()
			configured.Rotation.ModelsJSON = cappedBridgeModels
			manager.config.Swap(&configured)
			if testCase.second != nil {
				row := store.DevshardRecord{EscrowID: "6", Model: "qwen", Active: true, RotationRole: roleTemp, RotationEpoch: 8, PrivateKeyEnv: "K", Amount: 1_000_000}
				testCase.second(&row)
				testStore.devshards["6"] = row
			}
			if testCase.secondLive {
				funds := manager.funds.(fakeFunds)
				funds["6"] = funds["5"]
			}

			require.NoError(t, manager.planFunding(context.Background(), true))

			require.Equal(t, testCase.wantCreates, txClient.createCalls, "CreateEscrow calls")
		})
	}
}
