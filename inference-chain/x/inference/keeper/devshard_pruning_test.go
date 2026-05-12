package keeper_test

import (
	"fmt"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// pruneDevshard is a helper that runs only the devshard pruner via the Pruner framework.
func pruneDevshard(k keeper.Keeper, ctx sdk.Context, currentEpoch int64) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	return k.GetDevshardPruner(params).Prune(ctx, k, currentEpoch)
}

func TestPruneDevshardData_DeletesOldEscrows(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	mock.BankKeeper.ExpectAny(ctx)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	// Create escrow in epoch 3
	escrow := &types.DevshardEscrow{
		Creator:    "gonka1creator",
		Amount:     5_000_000_000,
		Slots:      make([]string, 16),
		EpochIndex: 3,
		Settled:    true,
	}
	id, err := k.StoreDevshardEscrow(ctx, escrow, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), id)

	// Verify escrow exists
	_, found := k.GetDevshardEscrow(ctx, 1)
	require.True(t, found)

	// Prune at epoch 5 (threshold=2, so epoch 3 should be pruned)
	// First call removes the escrow, second call marks the epoch complete.
	require.NoError(t, pruneDevshard(k, ctx, 5))
	require.NoError(t, pruneDevshard(k, ctx, 5))

	// Escrow should be deleted
	_, found = k.GetDevshardEscrow(ctx, 1)
	require.False(t, found)
}

func TestPruneDevshardData_PreservesRecentEscrows(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	mock.BankKeeper.ExpectAny(ctx)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	// Create escrow in epoch 4
	escrow := &types.DevshardEscrow{
		Creator:    "gonka1creator",
		Amount:     5_000_000_000,
		Slots:      make([]string, 16),
		EpochIndex: 4,
		Settled:    true,
	}
	_, err := k.StoreDevshardEscrow(ctx, escrow, 1)
	require.NoError(t, err)

	// Prune at epoch 5 (threshold=2, so epoch 4 is not yet prunable)
	require.NoError(t, pruneDevshard(k, ctx, 5))

	// Escrow should still exist
	_, found := k.GetDevshardEscrow(ctx, 1)
	require.True(t, found)
}

func TestPruneDevshardData_HostStatsDeleted(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	mock.BankKeeper.ExpectAny(ctx)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	participant := sdk.AccAddress(make([]byte, 20))
	participant[0] = 0x01

	// Create escrow and stats for epoch 3
	escrow := &types.DevshardEscrow{
		Creator:    "gonka1creator",
		Amount:     5_000_000_000,
		Slots:      make([]string, 16),
		EpochIndex: 3,
		Settled:    true,
	}
	_, err := k.StoreDevshardEscrow(ctx, escrow, 1)
	require.NoError(t, err)

	_ = k.DevshardHostEpochStatsMap.Set(ctx, collections.Join(uint64(3), participant), types.DevshardHostEpochStats{
		Participant: participant.String(),
		EpochIndex:  3,
		Cost:        100,
		EscrowCount: 1,
	})

	// Prune at epoch 5 -- two passes: first removes escrows, second marks complete and runs PostPruneEpoch
	require.NoError(t, pruneDevshard(k, ctx, 5))
	require.NoError(t, pruneDevshard(k, ctx, 5))

	// Stats should be deleted
	_, found := k.GetDevshardHostEpochStats(ctx, 3, participant)
	require.False(t, found)

	// Epoch count should be deleted
	count := k.GetDevshardEscrowEpochCount(ctx, 3)
	require.Equal(t, uint64(0), count)
}

func TestPruneDevshardData_UnsettledEscrowDistributesFunds(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	// Create 4 unique validators in 16 slots
	addr1 := sdk.AccAddress(make([]byte, 20))
	addr1[0] = 0x01
	addr2 := sdk.AccAddress(make([]byte, 20))
	addr2[0] = 0x02
	addr3 := sdk.AccAddress(make([]byte, 20))
	addr3[0] = 0x03
	addr4 := sdk.AccAddress(make([]byte, 20))
	addr4[0] = 0x04

	slots := make([]string, keeper.DevshardGroupSize)
	for i := 0; i < 4; i++ {
		slots[i] = addr1.String()
	}
	for i := 4; i < 8; i++ {
		slots[i] = addr2.String()
	}
	for i := 8; i < 12; i++ {
		slots[i] = addr3.String()
	}
	for i := 12; i < 16; i++ {
		slots[i] = addr4.String()
	}

	escrow := &types.DevshardEscrow{
		Creator:    "gonka1creator",
		Amount:     8_000_000_000, // 8 GNK
		Slots:      slots,
		EpochIndex: 3,
		Settled:    false, // unsettled
	}
	_, err := k.StoreDevshardEscrow(ctx, escrow, 1)
	require.NoError(t, err)

	// Expect 4 payments of 2 GNK each (8 GNK / 4 unique validators)
	mock.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Eq("devshard_escrow_unsettled_distribution")).
		Return(nil).
		Times(4)

	require.NoError(t, pruneDevshard(k, ctx, 5))

	// Escrow should be deleted
	_, found := k.GetDevshardEscrow(ctx, 1)
	require.False(t, found)
}

func TestPruneDevshardData_UnsettledDistributionAmounts(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	// Create 4 unique validators in 16 slots (4 slots each)
	addrs := make([]sdk.AccAddress, 4)
	for i := range addrs {
		addrs[i] = sdk.AccAddress(make([]byte, 20))
		addrs[i][0] = byte(i + 1)
	}

	slots := make([]string, keeper.DevshardGroupSize)
	for i := 0; i < 4; i++ {
		slots[i] = addrs[0].String()
	}
	for i := 4; i < 8; i++ {
		slots[i] = addrs[1].String()
	}
	for i := 8; i < 12; i++ {
		slots[i] = addrs[2].String()
	}
	for i := 12; i < 16; i++ {
		slots[i] = addrs[3].String()
	}

	escrow := &types.DevshardEscrow{
		Creator:    "gonka1creator",
		Amount:     8_000_000_000, // 8 GNK
		Slots:      slots,
		EpochIndex: 3,
		Settled:    false,
	}
	_, err := k.StoreDevshardEscrow(ctx, escrow, 1)
	require.NoError(t, err)

	// Each of 4 validators should receive exactly 2 GNK (8 GNK / 4)
	expectedShare, err := types.GetCoins(2_000_000_000)
	require.NoError(t, err)

	for _, addr := range addrs {
		mock.BankKeeper.EXPECT().
			SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, addr, expectedShare, gomock.Eq("devshard_escrow_unsettled_distribution")).
			Return(nil)
	}

	require.NoError(t, pruneDevshard(k, ctx, 5))
}

func TestPruneDevshardData_TracksProgress(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	mock.BankKeeper.ExpectAny(ctx)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	// Create escrows in epochs 1, 2, 3
	for epoch := uint64(1); epoch <= 3; epoch++ {
		escrow := &types.DevshardEscrow{
			Creator:    "gonka1creator",
			Amount:     5_000_000_000,
			Slots:      make([]string, 16),
			EpochIndex: epoch,
			Settled:    true,
		}
		_, err := k.StoreDevshardEscrow(ctx, escrow, epoch)
		require.NoError(t, err)
	}

	// Prune at epoch 4 -> should prune epochs 1 and 2
	// First pass removes escrows, second pass marks epochs complete
	require.NoError(t, pruneDevshard(k, ctx, 4))
	require.NoError(t, pruneDevshard(k, ctx, 4))

	st, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), st.DevshardPrunedEpoch)

	// Epoch 3 escrow should still exist
	_, found := k.GetDevshardEscrow(ctx, 3)
	require.True(t, found)

	// Prune at epoch 5 -> should prune epoch 3
	require.NoError(t, pruneDevshard(k, ctx, 5))
	require.NoError(t, pruneDevshard(k, ctx, 5))

	st, err = k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(3), st.DevshardPrunedEpoch)

	_, found = k.GetDevshardEscrow(ctx, 3)
	require.False(t, found)
}

func TestDistributeUnsettledEscrow_BankError_NoPartialPayment(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	addr1 := sdk.AccAddress(make([]byte, 20))
	addr1[0] = 0x01
	addr2 := sdk.AccAddress(make([]byte, 20))
	addr2[0] = 0x02

	slots := make([]string, 16)
	for i := 0; i < 8; i++ {
		slots[i] = addr1.String()
	}
	for i := 8; i < 16; i++ {
		slots[i] = addr2.String()
	}

	escrow := &types.DevshardEscrow{
		Creator:    "gonka1creator",
		Amount:     4_000_000_000,
		Slots:      slots,
		EpochIndex: 3,
		Settled:    false,
	}
	id, err := k.StoreDevshardEscrow(ctx, escrow, 1)
	require.NoError(t, err)

	// addr1 payment succeeds — capture coins to verify the CacheContext received the correct amount.
	// When addr2 fails, commit() is never called, so addr1's payment is discarded too.
	var addr1CoinsAttempted sdk.Coins
	mock.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, addr1, gomock.Any(), gomock.Eq("devshard_escrow_unsettled_distribution")).
		DoAndReturn(func(_ interface{}, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
			addr1CoinsAttempted = coins
			return nil
		}).Times(1)
	mock.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, addr2, gomock.Any(), gomock.Eq("devshard_escrow_unsettled_distribution")).
		Return(fmt.Errorf("bank: insufficient module balance")).Times(1)

	// Pruner.Prune uses continue on Remover errors and always returns nil.
	// With the fix, Remover returns err when distributeUnsettledEscrow fails,
	// so the escrow is preserved because Remove() is never called.
	err = pruneDevshard(k, ctx, 5)
	require.Error(t, err)

	// Escrow must NOT be deleted: Remover returned before Remove() was called.
	// CacheContext ensures addr1 partial payment is also rolled back (not committed).
	_, found := k.GetDevshardEscrow(ctx, id)
	require.True(t, found, "escrow must remain in state when distribution fails")

	// Balance check equivalent: verify addr1's payment was attempted with non-zero coins inside
	// the CacheContext. Since commit() was never called (addr2 failed), addr1's balance is
	// effectively unchanged — the escrow preservation above is the authoritative proof of rollback.
	require.True(t, addr1CoinsAttempted.IsAllPositive(),
		"addr1 payment must have been attempted inside CacheContext before addr2 failed")
}
