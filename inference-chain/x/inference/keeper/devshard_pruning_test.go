package keeper_test

import (
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

func TestPruneDevshardData_UnsettledEscrowRefundsCreator(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xCC

	// Slot ownership is irrelevant: the full amount is refunded to the creator.
	slots := make([]string, keeper.DevshardGroupSize)
	for i := range slots {
		v := sdk.AccAddress(make([]byte, 20))
		v[0] = byte(i + 1)
		slots[i] = v.String()
	}

	escrow := &types.DevshardEscrow{
		Creator:    creator.String(),
		Amount:     8_000_000_000, // 8 GNK
		Slots:      slots,
		EpochIndex: 3,
		Settled:    false, // unsettled
	}
	_, err := k.StoreDevshardEscrow(ctx, escrow, 1)
	require.NoError(t, err)

	// Expect a single refund of the FULL amount to the creator -- no validator
	// distribution.
	refund, err := types.GetCoins(8_000_000_000)
	require.NoError(t, err)
	mock.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, refund, gomock.Eq("devshard_escrow_unsettled_refund")).
		Return(nil).
		Times(1)

	require.NoError(t, pruneDevshard(k, ctx, 5))

	// Escrow should be deleted
	_, found := k.GetDevshardEscrow(ctx, 1)
	require.False(t, found)
}

func TestPruneDevshardData_UnsettledRefundFullAmountNoStrandedRemainder(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xCC

	// Amount is intentionally NOT divisible by the validator count, so the old
	// equal-split would have stranded a remainder; the refund returns it all.
	slots := make([]string, keeper.DevshardGroupSize)
	for i := range slots {
		v := sdk.AccAddress(make([]byte, 20))
		v[0] = byte((i % 4) + 1)
		slots[i] = v.String()
	}

	const amount = uint64(8_000_000_003) // not divisible by 4
	escrow := &types.DevshardEscrow{
		Creator:    creator.String(),
		Amount:     amount,
		Slots:      slots,
		EpochIndex: 3,
		Settled:    false,
	}
	_, err := k.StoreDevshardEscrow(ctx, escrow, 1)
	require.NoError(t, err)

	// The entire locked amount is refunded to the creator -- nothing stranded.
	expectedRefund, err := types.GetCoins(int64(amount))
	require.NoError(t, err)
	mock.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, expectedRefund, gomock.Eq("devshard_escrow_unsettled_refund")).
		Return(nil).
		Times(1)

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
