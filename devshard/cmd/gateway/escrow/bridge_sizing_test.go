package escrow

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/store"
	"devshard/signing"
)

const (
	bridgeSizingModel      = "model-a"
	bridgeSizingTempCount  = 4
	bridgeSizingNeed       = 17
	bridgeSizingWalletPays = 6
	bridgeSizingReserves   = 1
	bridgeSizingMaxHeld    = 10

	bridgeSizingRoomyMaxUnsettled = 64
)

type createFn = func(context.Context, *signing.Secp256k1Signer, uint64, string, func(string) error) (chain.CreateEscrowResult, error)

func bridgeSizingManager(t *testing.T, need int, createEscrowFn createFn) (*Manager, *fakeStore, []store.DevshardRecord) {
	t.Helper()
	testStore := newFakeStore()
	regularIDs := []string{"reg-1", "reg-2"}
	devshards := make([]store.DevshardRecord, 0, len(regularIDs))
	for _, escrowID := range regularIDs {
		record := store.DevshardRecord{EscrowID: escrowID, Model: bridgeSizingModel, Active: true, RotationRole: roleRegular, PrivateKeyEnv: "MODEL_A_KEY"}
		testStore.devshards[escrowID] = record
		devshards = append(devshards, record)
	}
	manager := newRotationManager(t, testStore, &fakeTxClient{createEscrowFn: createEscrowFn}, false)
	manager.planner = fundingPlanner{models: map[string]*modelFunding{bridgeSizingModel: {needEscrows: need}}}
	return manager, testStore, devshards
}

func bridgeSizingModels(maxUnsettled int) []ModelConfig {
	return []ModelConfig{{ModelID: bridgeSizingModel, TempCount: bridgeSizingTempCount, TargetCount: bridgeSizingTempCount, ReserveCount: bridgeSizingReserves, Amount: 1000, MaxUnsettled: maxUnsettled, PrivateKeyEnv: "MODEL_A_KEY"}}
}

func countTemps(testStore *fakeStore) int {
	temps := 0
	for _, record := range testStore.devshards {
		if record.Model == bridgeSizingModel && record.RotationRole == roleTemp && record.Active {
			temps++
		}
	}
	return temps
}

func creatingUntilFailure(limit int, failure func(amount uint64) error) createFn {
	fundedCreate := succeedingCreateEscrowFn(200)
	creates := 0
	return func(ctx context.Context, signer *signing.Secp256k1Signer, amount uint64, modelID string, onPrepared func(string) error) (chain.CreateEscrowResult, error) {
		if creates == limit {
			return chain.CreateEscrowResult{}, failure(amount)
		}
		creates++
		return fundedCreate(ctx, signer, amount, modelID, onPrepared)
	}
}

func walletRefusal(amount uint64) error {
	return &chain.WalletUnderfundedError{Address: "gonka1wallet", Have: 0, Need: amount}
}

// Test flow:
//  1. Seed the planner with a model whose need is seventeen escrows against a temp count of four, with two active regulars and a wallet that pays for every create.
//  2. Call `prepareBridge`.
//  3. Assert it returns nil, seventeen temps exist and both regulars are parked.
func TestTheBridgeCreatesAsManyTempsAsTheNeedCallsFor(t *testing.T) {
	manager, testStore, devshards := bridgeSizingManager(t, bridgeSizingNeed, succeedingCreateEscrowFn(200))

	require.NoError(t, manager.prepareBridge(context.Background(), servedSnapshot(9, 500, bridgeSizingModel), bridgeSizingModels(bridgeSizingRoomyMaxUnsettled), devshards))

	require.Equal(t, bridgeSizingNeed, countTemps(testStore), "temps")
	assertParked(t, testStore, "reg-1")
	assertParked(t, testStore, "reg-2")
}

// Test flow:
//  1. Seed the planner with a model whose need is smaller than its temp count of four.
//  2. Call `prepareBridge`.
//  3. Assert exactly four temps exist: the need only ever raises the target.
func TestTheBridgeNeverCreatesFewerTempsThanTheTempCount(t *testing.T) {
	manager, testStore, devshards := bridgeSizingManager(t, 1, succeedingCreateEscrowFn(200))

	require.NoError(t, manager.prepareBridge(context.Background(), servedSnapshot(9, 500, bridgeSizingModel), bridgeSizingModels(bridgeSizingRoomyMaxUnsettled), devshards))

	require.Equal(t, bridgeSizingTempCount, countTemps(testStore), "temps")
}

// Test flow:
//  1. Seed the planner with a model whose need is seventeen escrows with a wallet that pays for six creates and then refuses as underfunded.
//  2. Call `prepareBridge`.
//  3. Assert it returns nil, six temps exist, both regulars are parked rather than promoted, and the saved status is completed.
func TestAWalletThatFundsMoreThanTheTempCountCapsTheBridgeWithoutDegrading(t *testing.T) {
	manager, testStore, devshards := bridgeSizingManager(t, bridgeSizingNeed, creatingUntilFailure(bridgeSizingWalletPays, walletRefusal))

	require.NoError(t, manager.prepareBridge(context.Background(), servedSnapshot(9, 500, bridgeSizingModel), bridgeSizingModels(bridgeSizingRoomyMaxUnsettled), devshards))

	require.Equal(t, bridgeSizingWalletPays, countTemps(testStore), "temps")
	assertParked(t, testStore, "reg-1")
	assertParked(t, testStore, "reg-2")
	require.True(t, testStore.rotationStatuses[bridgeSizingModel+"|"+string(roleTemp)].Completed, "rotation status completed")
}

// Test flow:
//  1. Seed the planner with a model whose need is seventeen escrows with a wallet that pays for two creates, fewer than its temp count of four.
//  2. Call `prepareBridge`.
//  3. Assert the regulars are kept and promoted to temp, and the saved status is not completed: the degrade path still runs below the temp count.
func TestAWalletShortOfTheTempCountStillPromotesTheRegulars(t *testing.T) {
	manager, testStore, devshards := bridgeSizingManager(t, bridgeSizingNeed, creatingUntilFailure(2, walletRefusal))

	_ = manager.prepareBridge(context.Background(), servedSnapshot(9, 500, bridgeSizingModel), bridgeSizingModels(bridgeSizingRoomyMaxUnsettled), devshards)

	for _, escrowID := range []string{"reg-1", "reg-2"} {
		require.True(t, testStore.devshards[escrowID].Active, "%s active", escrowID)
		require.Equal(t, roleTemp, testStore.devshards[escrowID].RotationRole, "%s role", escrowID)
	}
	require.False(t, testStore.rotationStatuses[bridgeSizingModel+"|"+string(roleTemp)].Completed, "rotation status completed")
}

// Test flow:
//  1. Seed the planner with a model whose need is seventeen escrows with a cap of ten unsettled escrows, a target of four regulars and one reserve.
//  2. Call `prepareBridge`.
//  3. Assert five temps exist, the cap less the target and the reserve, so the next epoch's escrows still fit, and both regulars are parked.
func TestTheBridgeLeavesRoomForTheNextEpochsTargetAndReserve(t *testing.T) {
	manager, testStore, devshards := bridgeSizingManager(t, bridgeSizingNeed, succeedingCreateEscrowFn(200))

	require.NoError(t, manager.prepareBridge(context.Background(), servedSnapshot(9, 500, bridgeSizingModel), bridgeSizingModels(bridgeSizingMaxHeld), devshards))

	require.Equal(t, bridgeSizingMaxHeld-bridgeSizingTempCount-bridgeSizingReserves, countTemps(testStore), "temps")
	assertParked(t, testStore, "reg-1")
	assertParked(t, testStore, "reg-2")
}

// Test flow:
//  1. Seed the planner with a model whose need is seventeen escrows with a chain client whose fifth create fails after broadcast.
//  2. Call `prepareBridge`.
//  3. Assert the failure is returned, four temps exist, and both regulars are parked, since the temp count was met.
func TestABroadcastFailureBeyondTheTempCountIsReportedAndStillRetiresTheRegulars(t *testing.T) {
	manager, testStore, devshards := bridgeSizingManager(t, bridgeSizingNeed, creatingUntilFailure(bridgeSizingTempCount, func(uint64) error { return errors.New("broadcast rejected") }))

	err := manager.prepareBridge(context.Background(), servedSnapshot(9, 500, bridgeSizingModel), bridgeSizingModels(bridgeSizingRoomyMaxUnsettled), devshards)

	require.ErrorContains(t, err, "broadcast rejected")
	require.Equal(t, bridgeSizingTempCount, countTemps(testStore), "temps")
	assertParked(t, testStore, "reg-1")
	assertParked(t, testStore, "reg-2")
}

// Test flow:
//  1. Decide one scheduled tick of a model whose demand peak needs several whole escrows.
//  2. Read the need the bridge sizes from.
//  3. Assert it is the decision's escrow count, above one.
func TestTheBridgeReadsTheEscrowCountTheLastDecisionStored(t *testing.T) {
	planner := fundingPlanner{models: map[string]*modelFunding{}, escrows: map[string]*escrowFunding{}}
	reading := guaranteeReading(true, false)
	reading.reserved = 3_000_000

	_, decision, _ := planner.decide("qwen", reading, true, time.Date(2026, 11, 21, 12, 0, 0, 0, time.UTC))

	require.Greater(t, decision.NeedEscrows, 1, "a need of several escrows")
	require.Equal(t, decision.NeedEscrows, planner.bridgeNeed("qwen"), "the stored need")
}
