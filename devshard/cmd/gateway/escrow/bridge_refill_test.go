package escrow

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/store"
	"devshard/types"
)

func starvedBridge(t *testing.T) (*Manager, *fakeStore, chain.PhaseSnapshot, []ModelConfig) {
	t.Helper()
	manager, testStore, _, _ := planningManager(t)
	snapshot := planningSnapshot()
	snapshot.EpochSwitchBlockHeight = snapshot.BlockHeight + 100
	row := testStore.devshards["5"]
	row.RotationRole, row.RotationEpoch = roleTemp, int64(bridgeLabel(snapshot))
	row.PrivateKeyEnv, row.Amount = "K", 1_000_000
	testStore.devshards["5"] = row
	models := []ModelConfig{{ModelID: "qwen", TargetCount: 1, TempCount: 1, FullContextSlots: 1, Amount: 1_000_000, MaxUnsettled: 10, PrivateKeyEnv: "K"}}
	return manager, testStore, snapshot, models
}

// Test flow:
//  1. Keep one starved temp active at a bridge whose temp count and funding need are both one.
//  2. Run bridge preparation twice with the second tick seeing the newly created row before its session loads.
//  3. Assert a funded temp is added, the old temp stays active, and the second tick creates no duplicate.
func TestTheBridgeRefillsStarvedTempsEvenWhenTheirCountMeetsTheTarget(t *testing.T) {
	manager, testStore, snapshot, models := starvedBridge(t)

	require.NoError(t, manager.prepareBridge(t.Context(), snapshot, models, []store.DevshardRecord{testStore.devshards["5"]}))
	require.Len(t, testStore.devshards, 2)
	require.True(t, testStore.devshards["5"].Active)
	rows, err := testStore.ListDevshards(t.Context())
	require.NoError(t, err)
	for _, created := range rows {
		require.Equal(t, roleTemp, created.RotationRole)
		require.Equal(t, int64(bridgeLabel(snapshot)), created.RotationEpoch)
	}
	require.NoError(t, manager.prepareBridge(t.Context(), snapshot, models, rows))
	require.Len(t, testStore.devshards, 2)
}

// Test flow:
//  1. Make a bridge full, already awaiting a temp create, or unable to fit another unsettled escrow.
//  2. Run bridge preparation.
//  3. Assert none of these states spends money on another escrow.
func TestBridgeRefillsRespectAvailableMoneyPendingCreatesAndTheBudget(t *testing.T) {
	for _, name := range []string{"full", "pending", "budget", "unread", "unpriced", "blocked", "absent"} {
		t.Run(name, func(t *testing.T) {
			manager, testStore, snapshot, models := starvedBridge(t)
			switch name {
			case "full":
				money := manager.funds.(fakeFunds)["5"]
				money.Balance = 1_000_000
				manager.funds.(fakeFunds)["5"] = money
			case "pending":
				testStore.commitments["pending"] = store.Commitment{TxHash: "pending", Model: "qwen", Role: roleTemp, Epoch: bridgeLabel(snapshot)}
			case "budget":
				models[0].MaxUnsettled = 1
			case "unread":
				delete(manager.funds.(fakeFunds), "5")
				row := testStore.devshards["5"]
				row.Amount = 0
				testStore.devshards["5"] = row
			case "unpriced":
				snapshot.TokenPrice = 0
			case "blocked":
				snapshot.RequestsBlocked = true
				configuration := *manager.config.Load()
				configuration.Modes.PoCMode = config.PoCModeOff
				manager.config.Swap(&configuration)
			case "absent":
				snapshot.Models = map[string]chain.ModelParams{"other": {MaxModelLen: 8192}}
			}
			require.NoError(t, manager.prepareBridge(t.Context(), snapshot, models, []store.DevshardRecord{testStore.devshards["5"]}))
			require.Len(t, testStore.devshards, 1)
		})
	}
}

// Test flow:
//  1. Start with one starved temp and demand exceeding three fresh escrows.
//  2. Prepare twice at the same time, then advance one tick and prepare again.
//  3. Assert the refill creates only two initially, nothing on the immediate retry, and one after the tick.
func TestBridgeRefillsShareTheFundingCreateRateLimit(t *testing.T) {
	manager, testStore, snapshot, models := starvedBridge(t)
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }
	manager.planner.modelLocked("qwen", now).demand.Record(3_000_000)
	prepare := func() {
		t.Helper()
		rows, err := testStore.ListDevshards(t.Context())
		require.NoError(t, err)
		require.NoError(t, manager.prepareBridge(t.Context(), snapshot, models, rows))
	}
	prepare()
	require.Len(t, testStore.devshards, 3)
	prepare()
	require.Len(t, testStore.devshards, 3)
	now = now.Add(TickInterval)
	prepare()
	require.Len(t, testStore.devshards, 4)
}

// Test flow:
//  1. Keep a full temp whose free headroom cannot cover the recorded demand.
//  2. Give it enough returning reservations to cover that demand, or leave them absent.
//  3. Assert only the genuinely short pool is refilled, without retiring the old temp.
func TestBridgeRefillsCountReturningReservationsAgainstDemand(t *testing.T) {
	for _, returning := range []bool{false, true} {
		t.Run(map[bool]string{false: "spent", true: "returning"}[returning], func(t *testing.T) {
			manager, testStore, snapshot, models := starvedBridge(t)
			now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
			manager.now = func() time.Time { return now }
			manager.planner.modelLocked("qwen", now).demand.Record(1_000_000)
			money := manager.funds.(fakeFunds)["5"]
			money.Balance = 100_000
			if returning {
				money.Open = []types.InferenceRecord{{Status: types.StatusStarted, StartedAt: now.Unix(), InputLength: 3_000_000, ReservedCost: 3_000_000}}
			}
			manager.funds.(fakeFunds)["5"] = money
			require.NoError(t, manager.prepareBridge(t.Context(), snapshot, models, []store.DevshardRecord{testStore.devshards["5"]}))
			if returning {
				require.Len(t, testStore.devshards, 1)
			} else {
				require.Len(t, testStore.devshards, 3)
			}
			require.True(t, testStore.devshards["5"].Active)
		})
	}
}
