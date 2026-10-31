package escrow

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
	"devshard/signing"
)

const reserveTestModels = `[{"model_id":"model-a","target_count":1,"amount":1000,"private_key_env":"MODEL_A_KEY"}]`

func reserveTestManager(t *testing.T, testStore *fakeStore, txClient *fakeTxClient, snapshot chain.PhaseSnapshot, modelsJSON string) *Manager {
	t.Helper()
	configuration := config.Defaults()
	configuration.Rotation.Enabled = true
	configuration.Rotation.ModelsJSON = modelsJSON
	return mustManager(t, testManagerDeps(t, testStore, txClient, &fakeSnapshotSource{snapshot: snapshot}, &configuration))
}

func servingRegular(escrowID string, epoch int64) store.DevshardRecord {
	return store.DevshardRecord{EscrowID: escrowID, Model: "model-a", Active: true, RotationRole: roleRegular, RotationEpoch: epoch, PrivateKeyEnv: "MODEL_A_KEY"}
}

func rowsInRole(testStore *fakeStore, role string) []store.DevshardRecord {
	testStore.mu.Lock()
	defer testStore.mu.Unlock()
	var rows []store.DevshardRecord
	for _, record := range testStore.devshards {
		if record.Active && record.RotationRole == role {
			rows = append(rows, record)
		}
	}
	return rows
}

// Test flow:
//  1. Build a manager and call OnReserveTaken twice for the same escrow.
//  2. Assert one wake-up is pending for the tick loop, and the take was narrated once.
func TestTakingAReserveWakesTheTick(t *testing.T) {
	narrator := &recordingLifecycleNarrator{}
	m := reserveTestManager(t, newFakeStore(), &fakeTxClient{}, servedSnapshot(9, 700, "model-a"), reserveTestModels)
	m.narrator = narrator

	m.OnReserveTaken("reserve-1")
	m.OnReserveTaken("reserve-1")

	require.Len(t, m.wakeup, 1)
	require.Equal(t, 1, countCalls(narrator.recorded(), "reserve taken reserve-1"))
}

func countCalls(calls []string, want string) int {
	count := 0
	for _, call := range calls {
		if call == want {
			count++
		}
	}
	return count
}

// Test flow:
//  1. Store a serving regular of epoch 9 and a reserve left over from epoch 8, as when the pre-PoC window was missed.
//  2. Run one tick at epoch 9.
//  3. Assert the leftover reserve is retired.
func TestAReserveLeftFromAnEarlierEpochIsRetired(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["regular-1"] = servingRegular("regular-1", 9)
	leftover := servingRegular("reserve-8", 8)
	leftover.RotationRole = RoleReserve
	testStore.devshards[leftover.EscrowID] = leftover
	txClient := &fakeTxClient{createEscrowFn: succeedingCreateEscrowFn(300)}
	m := reserveTestManager(t, testStore, txClient, servedSnapshot(9, 700, "model-a"), reserveTestModels)

	require.NoError(t, m.tick(t.Context()))

	assertParked(t, testStore, "reserve-8")
}

// Test flow:
//  1. Store an active regular and an active reserve for one model.
//  2. Call promoteRegularsToTemp, the bridge's degrade path when temps cannot be funded.
//  3. Assert only the regular is relabeled temp: the reserve keeps its role, so the registry's reserve flag and the row agree through the bridge.
func TestTheBridgeDegradePathLeavesTheReserveAReserve(t *testing.T) {
	testStore := newFakeStore()
	regular := servingRegular("regular-1", 9)
	reserve := servingRegular("reserve-1", 9)
	reserve.RotationRole = RoleReserve
	testStore.devshards[regular.EscrowID] = regular
	testStore.devshards[reserve.EscrowID] = reserve
	m := newRotationManager(t, testStore, &fakeTxClient{}, false)

	promoted, err := m.promoteRegularsToTemp(t.Context(), ModelConfig{ModelID: "model-a"}, []store.DevshardRecord{regular, reserve})

	require.NoError(t, err)
	require.Equal(t, 1, promoted)
	require.Equal(t, roleTemp, testStore.devshards["regular-1"].RotationRole)
	require.Equal(t, RoleReserve, testStore.devshards["reserve-1"].RotationRole)
}

// Test flow:
//  1. Store a serving regular and two reserves of this epoch for a model whose reserve_count was lowered to 1, and a reserve of a second model whose reserve_count is 0.
//  2. Run one tick.
//  3. Assert one reserve of the first model is retired and one kept, and the second model's reserve is retired: lowering the count frees the money at once, not at the next epoch.
func TestLoweringReserveCountRetiresTheExcessReserves(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["regular-1"] = servingRegular("regular-1", 9)
	for _, escrowID := range []string{"reserve-1", "reserve-2"} {
		reserve := servingRegular(escrowID, 9)
		reserve.RotationRole = RoleReserve
		testStore.devshards[escrowID] = reserve
	}
	offReserve := servingRegular("reserve-b", 9)
	offReserve.Model = "model-b"
	offReserve.RotationRole = RoleReserve
	testStore.devshards[offReserve.EscrowID] = offReserve
	snapshot := servedSnapshot(9, 700, "model-a")
	snapshot.FullWeightsByModel["model-b"] = map[string]float64{"participant": 1}
	m := reserveTestManager(t, testStore, &fakeTxClient{createEscrowFn: failOnCreate(t)}, snapshot,
		`[{"model_id":"model-a","target_count":1,"reserve_count":1,"amount":1000,"private_key_env":"MODEL_A_KEY"},{"model_id":"model-b","target_count":1,"reserve_count":0,"amount":1000,"private_key_env":"MODEL_A_KEY"}]`)

	require.NoError(t, m.tick(t.Context()))

	require.Len(t, rowsInRole(testStore, RoleReserve), 1)
	require.Equal(t, "model-a", rowsInRole(testStore, RoleReserve)[0].Model)
	assertParked(t, testStore, "reserve-b")
}

// Test flow:
//  1. Store a serving regular and a reserve, make the store refuse the reserve's role write, and report the reserve taken.
//  2. Run one tick; assert it reports the failed write and the row still reads reserve.
//  3. Let the store accept the write and run another tick with no second report; assert the row now reads regular.
func TestAFailedPromotionIsRetriedOnTheNextTick(t *testing.T) {
	testStore := newFakeStore()
	testStore.devshards["regular-1"] = servingRegular("regular-1", 9)
	reserve := servingRegular("reserve-1", 9)
	reserve.RotationRole = RoleReserve
	testStore.devshards[reserve.EscrowID] = reserve
	testStore.rotationRoleErrByID = map[string]error{"reserve-1": errors.New("store unavailable")}
	m := reserveTestManager(t, testStore, &fakeTxClient{createEscrowFn: succeedingCreateEscrowFn(300)}, servedSnapshot(9, 700, "model-a"), reserveTestModels)

	m.OnReserveTaken("reserve-1")
	require.Error(t, m.tick(t.Context()))
	require.Equal(t, RoleReserve, testStore.devshards["reserve-1"].RotationRole)

	testStore.rotationRoleErrByID = nil
	_ = m.tick(t.Context())

	require.Equal(t, roleRegular, testStore.devshards["reserve-1"].RotationRole)
}

// Test flow:
//  1. Store an active temp for a model short of its temps, with a wallet that cannot pay.
//  2. Run prepareBridge.
//  3. Assert it reports no error, since the refusal is already narrated once as underfunded.
func TestAnUnderfundedBridgeIsNotAFailedTick(t *testing.T) {
	testStore := newFakeStore()
	temp := store.DevshardRecord{EscrowID: "temp-1", Model: "model-a", Active: true, RotationRole: roleTemp, RotationEpoch: 9, PrivateKeyEnv: "MODEL_A_KEY"}
	testStore.devshards[temp.EscrowID] = temp
	txClient := &fakeTxClient{createEscrowFn: func(_ context.Context, _ *signing.Secp256k1Signer, amount uint64, _ string, _ func(string) error) (chain.CreateEscrowResult, error) {
		return chain.CreateEscrowResult{}, &chain.WalletUnderfundedError{Address: "gonka1wallet", Have: 0, Need: amount}
	}}
	m := newRotationManager(t, testStore, txClient, false)
	snapshot := servedSnapshot(9, 700, "model-a")

	require.NoError(t, m.prepareBridge(t.Context(), snapshot, []ModelConfig{{ModelID: "model-a", TempCount: 2, TargetCount: 1, Amount: 1000, PrivateKeyEnv: "MODEL_A_KEY"}}, []store.DevshardRecord{temp}))
}
