package keeper_test

import (
	"math"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func setupSubnetEscrowTest(t testing.TB) (keeper.Keeper, types.MsgServer, sdk.Context, *keepertest.InferenceMocks) {
	k, ctx, mock := keepertest.InferenceKeeperReturningMocks(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")
	return k, keeper.NewMsgServerImpl(k), ctx, &mock
}

func setupEpochGroupForSubnet(ctx sdk.Context, k keeper.Keeper, epochIndex uint64) {
	_ = k.EffectiveEpochIndex.Set(ctx, epochIndex)

	epoch := types.Epoch{
		Index:               epochIndex,
		PocStartBlockHeight: int64(epochIndex * 100),
	}
	_ = k.Epochs.Set(ctx, epochIndex, epoch)

	weights := make([]*types.ValidationWeight, 20)
	for i := 0; i < 20; i++ {
		addr := sdk.AccAddress(make([]byte, 20))
		addr[0] = byte(i + 1)
		weights[i] = &types.ValidationWeight{
			MemberAddress: addr.String(),
			Weight:        100,
		}
	}

	groupData := types.EpochGroupData{
		PocStartBlockHeight: epochIndex * 100,
		EpochIndex:          epochIndex,
		ValidationWeights:   weights,
		TotalWeight:         2000,
	}
	_ = k.EpochGroupDataMap.Set(ctx, collections.Join(epochIndex, ""), groupData)
}

func TestCreateSubnetEscrow_HappyPath(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF
	amount := uint64(7_000_000_000) // 7 GNK

	mocks.BankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), creator, types.ModuleName, gomock.Any(), gomock.Any()).
		Return(nil)

	resp, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  amount,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)
	require.Equal(t, uint64(1), resp.EscrowId)

	escrow, found := k.GetSubnetEscrow(ctx, 1)
	require.True(t, found)
	require.Equal(t, creator.String(), escrow.Creator)
	require.Equal(t, amount, escrow.Amount)
	require.Len(t, escrow.Slots, keeper.SubnetGroupSize)
	require.Equal(t, uint64(5), escrow.EpochIndex)
	require.False(t, escrow.Settled)

	count := k.GetSubnetEscrowEpochCount(ctx, 5)
	require.Equal(t, uint64(1), count)
}

func TestCreateSubnetEscrow_AmountBelowMin(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	_, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  4_000_000_000,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "out of range")
}

func TestCreateSubnetEscrow_AmountAboveMax(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	_, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  11_000_000_000,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "out of range")
}

func TestCreateSubnetEscrow_EpochLimitReached(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5)
	_ = k.SubnetEscrowEpochCount.Set(ctx, 5, 100)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	_, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  5_000_000_000,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "max")
}

func TestCreateSubnetEscrow_InsufficientFunds(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	mocks.BankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), creator, types.ModuleName, gomock.Any(), gomock.Any()).
		Return(types.ErrNegativeCoinBalance)

	_, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  5_000_000_000,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "failed to lock funds")
}

func TestCreateSubnetEscrow_CounterOverflow(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5)

	// Set counter to max uint64
	err := k.SubnetEscrowCounter.Set(ctx, math.MaxUint64)
	require.NoError(t, err)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	_, err = ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  5_000_000_000,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "overflow")
}

func TestCreateSubnetEscrow_AllowlistBlocks(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	// Set params with allowlist that does NOT include the creator.
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.SubnetEscrowParams = &types.SubnetEscrowParams{
		MinAmount:               types.DefaultSubnetEscrowMinAmount,
		MaxAmount:               types.DefaultSubnetEscrowMaxAmount,
		MaxEscrowsPerEpoch:      types.DefaultSubnetMaxEscrowsPerEpoch,
		GroupSize:               types.DefaultSubnetGroupSize,
		AllowedCreatorAddresses: []string{"gonka1someotheraddressxxxxxxxxxxxxxxxxxx"},
		TokenPrice:              types.DefaultSubnetTokenPrice,
	}
	require.NoError(t, k.SetParams(ctx, params))

	_, err = ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  7_000_000_000,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "address is not allowed to create subnet escrows")
}

func TestCreateSubnetEscrow_ParamsOverrideDefaults(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)

	setupEpochGroupForSubnet(ctx, k, 5)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	// Set params with custom min=1000, max=2000, group_size=8.
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.SubnetEscrowParams = &types.SubnetEscrowParams{
		MinAmount:               1_000_000_000,
		MaxAmount:               2_000_000_000,
		MaxEscrowsPerEpoch:      types.DefaultSubnetMaxEscrowsPerEpoch,
		GroupSize:               8,
		AllowedCreatorAddresses: nil, // no restriction
		TokenPrice:              types.DefaultSubnetTokenPrice,
	}
	require.NoError(t, k.SetParams(ctx, params))

	mocks.BankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), creator, types.ModuleName, gomock.Any(), gomock.Any()).
		Return(nil)

	resp, err := ms.CreateSubnetEscrow(ctx, &types.MsgCreateSubnetEscrow{
		Creator: creator.String(),
		Amount:  1_500_000_000,
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	escrow, found := k.GetSubnetEscrow(ctx, resp.EscrowId)
	require.True(t, found)
	require.Len(t, escrow.Slots, 8)
}
