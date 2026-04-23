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

// pruneSubnet is a helper that runs only the subnet pruner via the Pruner framework.
func pruneSubnet(k keeper.Keeper, ctx sdk.Context, currentEpoch int64) error {
	params, err := k.GetParams(ctx)
	if err != nil {
		return err
	}
	return k.GetSubnetPruner(params).Prune(ctx, k, currentEpoch)
}

func TestPruneSubnetData_DeletesOldEscrows(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	mock.BankKeeper.ExpectAny(ctx)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	// Create escrow in epoch 3
	escrow := &types.SubnetEscrow{
		Creator:    "gonka1creator",
		Amount:     5_000_000_000,
		Slots:      make([]string, 16),
		EpochIndex: 3,
		Settled:    true,
	}
	id, err := k.StoreSubnetEscrow(ctx, escrow, 1)
	require.NoError(t, err)
	require.Equal(t, uint64(1), id)

	// Verify escrow exists
	_, found := k.GetSubnetEscrow(ctx, 1)
	require.True(t, found)

	// Prune at epoch 5 (threshold=2, so epoch 3 should be pruned)
	// First call removes the escrow, second call marks the epoch complete.
	require.NoError(t, pruneSubnet(k, ctx, 5))
	require.NoError(t, pruneSubnet(k, ctx, 5))

	// Escrow should be deleted
	_, found = k.GetSubnetEscrow(ctx, 1)
	require.False(t, found)
}

func TestPruneSubnetData_PreservesRecentEscrows(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	mock.BankKeeper.ExpectAny(ctx)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	// Create escrow in epoch 4
	escrow := &types.SubnetEscrow{
		Creator:    "gonka1creator",
		Amount:     5_000_000_000,
		Slots:      make([]string, 16),
		EpochIndex: 4,
		Settled:    true,
	}
	_, err := k.StoreSubnetEscrow(ctx, escrow, 1)
	require.NoError(t, err)

	// Prune at epoch 5 (threshold=2, so epoch 4 is not yet prunable)
	require.NoError(t, pruneSubnet(k, ctx, 5))

	// Escrow should still exist
	_, found := k.GetSubnetEscrow(ctx, 1)
	require.True(t, found)
}

func TestPruneSubnetData_HostStatsDeleted(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	mock.BankKeeper.ExpectAny(ctx)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	participant := sdk.AccAddress(make([]byte, 20))
	participant[0] = 0x01

	// Create escrow and stats for epoch 3
	escrow := &types.SubnetEscrow{
		Creator:    "gonka1creator",
		Amount:     5_000_000_000,
		Slots:      make([]string, 16),
		EpochIndex: 3,
		Settled:    true,
	}
	_, err := k.StoreSubnetEscrow(ctx, escrow, 1)
	require.NoError(t, err)

	_ = k.SubnetHostEpochStatsMap.Set(ctx, collections.Join(uint64(3), participant), types.SubnetHostEpochStats{
		Participant: participant.String(),
		EpochIndex:  3,
		Cost:        100,
		EscrowCount: 1,
	})

	// Prune at epoch 5 -- two passes: first removes escrows, second marks complete and runs PostPruneEpoch
	require.NoError(t, pruneSubnet(k, ctx, 5))
	require.NoError(t, pruneSubnet(k, ctx, 5))

	// Stats should be deleted
	_, found := k.GetSubnetHostEpochStats(ctx, 3, participant)
	require.False(t, found)

	// Epoch count should be deleted
	count := k.GetSubnetEscrowEpochCount(ctx, 3)
	require.Equal(t, uint64(0), count)
}

func TestPruneSubnetData_UnsettledEscrowDistributesFunds(t *testing.T) {
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

	slots := make([]string, keeper.SubnetGroupSize)
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

	escrow := &types.SubnetEscrow{
		Creator:    "gonka1creator",
		Amount:     8_000_000_000, // 8 GNK
		Slots:      slots,
		EpochIndex: 3,
		Settled:    false, // unsettled
	}
	_, err := k.StoreSubnetEscrow(ctx, escrow, 1)
	require.NoError(t, err)

	// Expect 4 payments of 2 GNK each (8 GNK / 4 unique validators)
	mock.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Eq("subnet_escrow_unsettled_distribution")).
		Return(nil).
		Times(4)

	require.NoError(t, pruneSubnet(k, ctx, 5))

	// Escrow should be deleted
	_, found := k.GetSubnetEscrow(ctx, 1)
	require.False(t, found)
}

func TestPruneSubnetData_UnsettledDistributionAmounts(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	// Create 4 unique validators in 16 slots (4 slots each)
	addrs := make([]sdk.AccAddress, 4)
	for i := range addrs {
		addrs[i] = sdk.AccAddress(make([]byte, 20))
		addrs[i][0] = byte(i + 1)
	}

	slots := make([]string, keeper.SubnetGroupSize)
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

	escrow := &types.SubnetEscrow{
		Creator:    "gonka1creator",
		Amount:     8_000_000_000, // 8 GNK
		Slots:      slots,
		EpochIndex: 3,
		Settled:    false,
	}
	_, err := k.StoreSubnetEscrow(ctx, escrow, 1)
	require.NoError(t, err)

	// Each of 4 validators should receive exactly 2 GNK (8 GNK / 4)
	expectedShare, err := types.GetCoins(2_000_000_000)
	require.NoError(t, err)

	for _, addr := range addrs {
		mock.BankKeeper.EXPECT().
			SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, addr, expectedShare, gomock.Eq("subnet_escrow_unsettled_distribution")).
			Return(nil)
	}

	require.NoError(t, pruneSubnet(k, ctx, 5))
}

func TestPruneSubnetData_TracksProgress(t *testing.T) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	mock.BankKeeper.ExpectAny(ctx)
	require.NoError(t, k.PruningState.Set(ctx, types.PruningState{}))

	// Create escrows in epochs 1, 2, 3
	for epoch := uint64(1); epoch <= 3; epoch++ {
		escrow := &types.SubnetEscrow{
			Creator:    "gonka1creator",
			Amount:     5_000_000_000,
			Slots:      make([]string, 16),
			EpochIndex: epoch,
			Settled:    true,
		}
		_, err := k.StoreSubnetEscrow(ctx, escrow, epoch)
		require.NoError(t, err)
	}

	// Prune at epoch 4 -> should prune epochs 1 and 2
	// First pass removes escrows, second pass marks epochs complete
	require.NoError(t, pruneSubnet(k, ctx, 4))
	require.NoError(t, pruneSubnet(k, ctx, 4))

	st, err := k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2), st.SubnetPrunedEpoch)

	// Epoch 3 escrow should still exist
	_, found := k.GetSubnetEscrow(ctx, 3)
	require.True(t, found)

	// Prune at epoch 5 -> should prune epoch 3
	require.NoError(t, pruneSubnet(k, ctx, 5))
	require.NoError(t, pruneSubnet(k, ctx, 5))

	st, err = k.PruningState.Get(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(3), st.SubnetPrunedEpoch)

	_, found = k.GetSubnetEscrow(ctx, 3)
	require.False(t, found)
}
