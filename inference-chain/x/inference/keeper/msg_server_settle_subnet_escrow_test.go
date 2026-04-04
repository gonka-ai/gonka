package keeper_test

import (
	"math"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	dcrdsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestSettleSubnetEscrow_HappyPath(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys := make([]*dcrdsecp.PrivateKey, keeper.SubnetGroupSize)
	slots := make([]string, keeper.SubnetGroupSize)
	for i := 0; i < keeper.SubnetGroupSize; i++ {
		key, err := dcrdsecp.GeneratePrivateKey()
		require.NoError(t, err)
		keys[i] = key
		slots[i] = cosmosAddressFromDcrdKey(key).String()
	}

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xAA
	escrow := types.SubnetEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     7_000_000_000,
		Slots:      slots,
		EpochIndex: 5,
		Settled:    false,
	}
	_, err := k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	costPerSlot := uint64(100_000_000) // 0.1 GNK per slot
	hostStats := makeHostStats(keeper.SubnetGroupSize, costPerSlot)
	msg := buildSettlementTestData(t, escrow, keys, hostStats)

	// Expect payments to validators (deduplicated by address)
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Eq("subnet_escrow_payment")).
		Return(nil).
		Times(keeper.SubnetGroupSize) // 16 unique validators

	// Expect refund to creator
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("subnet_escrow_refund")).
		Return(nil)

	resp, err := ms.SettleSubnetEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify escrow is settled
	settled, found := k.GetSubnetEscrow(ctx, 1)
	require.True(t, found)
	require.True(t, settled.Settled)
}

func TestSettleSubnetEscrow_AlreadySettled(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xBB
	escrow := types.SubnetEscrow{
		Id:      1,
		Creator: creator.String(),
		Settled: true,
		Slots:   make([]string, keeper.SubnetGroupSize),
	}
	_, err := k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	_, err = ms.SettleSubnetEscrow(ctx, &types.MsgSettleSubnetEscrow{
		Settler:  creator.String(),
		EscrowId: 1,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "already settled")
}

func TestSettleSubnetEscrow_WrongSettler(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xCC
	wrongSettler := sdk.AccAddress(make([]byte, 20))
	wrongSettler[0] = 0xDD
	escrow := types.SubnetEscrow{
		Id:      1,
		Creator: creator.String(),
		Slots:   make([]string, keeper.SubnetGroupSize),
	}
	_, err := k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	_, err = ms.SettleSubnetEscrow(ctx, &types.MsgSettleSubnetEscrow{
		Settler:  wrongSettler.String(),
		EscrowId: 1,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not the escrow creator")
}

func TestSettleSubnetEscrow_ZeroCostSettlement(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys := make([]*dcrdsecp.PrivateKey, keeper.SubnetGroupSize)
	slots := make([]string, keeper.SubnetGroupSize)
	for i := 0; i < keeper.SubnetGroupSize; i++ {
		key, err := dcrdsecp.GeneratePrivateKey()
		require.NoError(t, err)
		keys[i] = key
		slots[i] = cosmosAddressFromDcrdKey(key).String()
	}

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xBB
	escrow := types.SubnetEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     7_000_000_000,
		Slots:      slots,
		EpochIndex: 5,
		Settled:    false,
	}
	_, err := k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	hostStats := makeHostStats(keeper.SubnetGroupSize, 0) // all costs = 0
	msg := buildSettlementTestData(t, escrow, keys, hostStats)

	// No validator payments expected (all costs are 0)
	// Full amount refunded to creator
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("subnet_escrow_refund")).
		Return(nil)

	resp, err := ms.SettleSubnetEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	settled, found := k.GetSubnetEscrow(ctx, 1)
	require.True(t, found)
	require.True(t, settled.Settled)
}

func TestSettleSubnetEscrow_AllowlistBlocks(t *testing.T) {
	k, ms, ctx, _ := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xCC
	escrow := types.SubnetEscrow{
		Id:      1,
		Creator: creator.String(),
		Amount:  7_000_000_000,
		Slots:   make([]string, keeper.SubnetGroupSize),
		Settled: false,
	}
	_, err := k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	// Set params with allowlist NOT containing the escrow creator.
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

	_, err = ms.SettleSubnetEscrow(ctx, &types.MsgSettleSubnetEscrow{
		Settler:  creator.String(),
		EscrowId: 1,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "address is not allowed to create subnet escrows")
}

func TestSettleSubnetEscrow_ValidatorCostOverflow(t *testing.T) {
	// Use 2-slot escrow so quorum=2 (both keys sign), keeping test setup minimal.
	k, ms, ctx, _ := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateSubnetKeys(t, 2)
	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xCC

	// Set group_size=2 in params to allow a 2-slot escrow
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.SubnetEscrowParams = &types.SubnetEscrowParams{
		MinAmount:               1,
		MaxAmount:               math.MaxInt64,
		MaxEscrowsPerEpoch:      100,
		GroupSize:               2,
		AllowedCreatorAddresses: []string{},
		TokenPrice:              types.DefaultSubnetTokenPrice,
	}
	require.NoError(t, k.SetParams(ctx, params))

	escrow := types.SubnetEscrow{
		Id: 1, Creator: creator.String(), Amount: math.MaxUint64, Slots: slots, Settled: false,
	}
	_, err = k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	// slot[0] cost = MaxUint64, slot[1] cost = 1 → validatorCosts[addr0] overflows
	hostStats := []*types.SubnetSettlementHostStats{
		{SlotId: 0, Cost: math.MaxUint64, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 1, Cost: 1, RequiredValidations: 10, CompletedValidations: 9},
	}
	msg := buildSettlementTestData(t, escrow, keys, hostStats)

	// VerifySubnetSettlement is called first and should catch the overflow,
	// so SettleSubnetEscrow returns an error before any bank operations.
	_, err = ms.SettleSubnetEscrow(ctx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "overflow")
}
