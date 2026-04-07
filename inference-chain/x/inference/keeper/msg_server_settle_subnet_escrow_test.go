package keeper_test

import (
	"context"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	dcrdsecp "github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestSettleSubnetEscrow_FeesSplitBySlotCount(t *testing.T) {
	k, ms, ctx, mocks := setupSubnetEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keyH1, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	keyH2, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	keyH3, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)

	addrH1 := cosmosAddressFromDcrdKey(keyH1).String()
	addrH2 := cosmosAddressFromDcrdKey(keyH2).String()
	addrH3 := cosmosAddressFromDcrdKey(keyH3).String()

	initialAmount := uint64(1_000)
	fees := uint64(403)
	expectedUserRefund := initialAmount - fees

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x11
	escrow := types.SubnetEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     initialAmount,
		Slots:      []string{addrH1, addrH1, addrH2, addrH3},
		EpochIndex: 5,
		Settled:    false,
	}
	_, err = k.StoreSubnetEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	hostStats := []*types.SubnetSettlementHostStats{
		{SlotId: 0, Cost: 0, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 1, Cost: 0, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 2, Cost: 0, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 3, Cost: 0, RequiredValidations: 10, CompletedValidations: 9},
	}
	msg := buildSettlementTestData(t, escrow, []*dcrdsecp.PrivateKey{keyH1, keyH1, keyH2, keyH3}, hostStats, fees)

	payouts := make(map[string]uint64)
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Eq("subnet_escrow_payment")).
		DoAndReturn(func(_ context.Context, _ string, recipient sdk.AccAddress, coins sdk.Coins, _ string) error {
			require.Len(t, coins, 1)
			payouts[recipient.String()] = coins[0].Amount.Uint64()
			return nil
		}).
		Times(3)

	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("subnet_escrow_refund")).
		DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
			require.Len(t, coins, 1)
			require.Equal(t, expectedUserRefund, coins[0].Amount.Uint64())
			return nil
		})

	resp, err := ms.SettleSubnetEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// H1 owns two out of four slots, so it receives 2/4 of total fees = 200
	// H2 and H3 each own one out of four slots, so they receive 1/4 of total fees = 100.
	//
	// Remainder fees are distributed 1 coin per slot, starting from the first slot.
	// H1 gets 2 remainder coins for its two slots, H2 gets 1 coin, and H3 gets 0 coins.
	require.Equal(t, uint64(202), payouts[addrH1])
	require.Equal(t, uint64(101), payouts[addrH2])
	require.Equal(t, uint64(100), payouts[addrH3])
}

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
	fees := uint64(200_000_000)
	msg := buildSettlementTestData(t, escrow, keys, hostStats, fees)

	// Expect payments to validators (deduplicated by address)
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Eq("subnet_escrow_payment")).
		Return(nil).
		Times(keeper.SubnetGroupSize) // 16 unique validators

	// Expect refund to creator
	// Refund is reduced by fees; exact amount is verified in mock callback.
	expectedRefund := escrow.Amount - uint64(keeper.SubnetGroupSize)*100_000_000 - fees
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("subnet_escrow_refund")).
		DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
			require.Len(t, coins, 1)
			require.Equal(t, expectedRefund, coins[0].Amount.Uint64())
			return nil
		})

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
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

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
