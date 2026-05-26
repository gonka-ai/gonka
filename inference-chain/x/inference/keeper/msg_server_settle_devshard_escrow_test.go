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

func setParticipantForDevshardTest(t *testing.T, k keeper.Keeper, ctx sdk.Context, addr string) {
	t.Helper()
	err := k.SetParticipant(ctx, types.Participant{
		Index:             addr,
		Address:           addr,
		Status:            types.ParticipantStatus_ACTIVE,
		CurrentEpochStats: types.NewCurrentEpochStats(),
	})
	require.NoError(t, err)
}

func setActiveParticipantsForDevshardTest(t *testing.T, k keeper.Keeper, ctx sdk.Context, epoch uint64, addrs ...string) {
	t.Helper()
	participants := make([]*types.ActiveParticipant, 0, len(addrs))
	for _, addr := range addrs {
		participants = append(participants, &types.ActiveParticipant{Index: addr})
	}
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epoch,
		Participants: participants,
	}))
}

func TestSettleDevshardEscrow_FeesSplitBySlotCount(t *testing.T) {
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
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
	setParticipantForDevshardTest(t, k, ctx, addrH1)
	setParticipantForDevshardTest(t, k, ctx, addrH2)
	setParticipantForDevshardTest(t, k, ctx, addrH3)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	setActiveParticipantsForDevshardTest(t, k, ctx, 5, addrH1, addrH2, addrH3)

	initialAmount := uint64(1_000)
	fees := uint64(403)
	expectedUserRefund := initialAmount - fees

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x11
	escrow := types.DevshardEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     initialAmount,
		Slots:      []string{addrH1, addrH1, addrH2, addrH3},
		EpochIndex: 5,
		Settled:    false,
	}
	_, err = k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	hostStats := []*types.DevshardSettlementHostStats{
		{SlotId: 0, Cost: 0, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 1, Cost: 0, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 2, Cost: 0, RequiredValidations: 10, CompletedValidations: 9},
		{SlotId: 3, Cost: 0, RequiredValidations: 10, CompletedValidations: 9},
	}
	msg := buildSettlementTestData(t, escrow, []*dcrdsecp.PrivateKey{keyH1, keyH1, keyH2, keyH3}, hostStats, fees)

	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("devshard_escrow_refund")).
		DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
			require.Len(t, coins, 1)
			require.Equal(t, expectedUserRefund, coins[0].Amount.Uint64())
			return nil
		})

	mocks.BankKeeper.EXPECT().
		LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	resp, err := ms.SettleDevshardEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	participantH1, found := k.GetParticipant(ctx, addrH1)
	require.True(t, found)
	participantH2, found := k.GetParticipant(ctx, addrH2)
	require.True(t, found)
	participantH3, found := k.GetParticipant(ctx, addrH3)
	require.True(t, found)

	// H1 owns two out of four slots, so it receives 2/4 of total fees = 200.
	// H2 and H3 each own one out of four slots, so they receive 1/4 of total fees = 100.
	// Remainder fees are distributed 1 coin per slot, starting from the first slot.
	require.Equal(t, int64(202), participantH1.CoinBalance)
	require.Equal(t, int64(101), participantH2.CoinBalance)
	require.Equal(t, int64(100), participantH3.CoinBalance)
	require.Equal(t, uint64(202), participantH1.CurrentEpochStats.EarnedCoins)
	require.Equal(t, uint64(101), participantH2.CurrentEpochStats.EarnedCoins)
	require.Equal(t, uint64(100), participantH3.CurrentEpochStats.EarnedCoins)
}

func TestSettleDevshardEscrow_HappyPath(t *testing.T) {
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys := make([]*dcrdsecp.PrivateKey, keeper.DevshardGroupSize)
	slots := make([]string, keeper.DevshardGroupSize)
	for i := 0; i < keeper.DevshardGroupSize; i++ {
		key, err := dcrdsecp.GeneratePrivateKey()
		require.NoError(t, err)
		keys[i] = key
		slots[i] = cosmosAddressFromDcrdKey(key).String()
		setParticipantForDevshardTest(t, k, ctx, slots[i])
	}
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	setActiveParticipantsForDevshardTest(t, k, ctx, 5, slots...)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xAA
	escrow := types.DevshardEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     7_000_000_000,
		Slots:      slots,
		EpochIndex: 5,
		Settled:    false,
	}
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	costPerSlot := uint64(100_000_000) // 0.1 GNK per slot
	hostStats := makeHostStats(keeper.DevshardGroupSize, costPerSlot)
	fees := uint64(200_000_000)
	msg := buildSettlementTestData(t, escrow, keys, hostStats, fees)

	// Expect refund to creator
	// Refund is reduced by fees; exact amount is verified in mock callback.
	expectedRefund := escrow.Amount - uint64(keeper.DevshardGroupSize)*100_000_000 - fees
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("devshard_escrow_refund")).
		DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
			require.Len(t, coins, 1)
			require.Equal(t, expectedRefund, coins[0].Amount.Uint64())
			return nil
		})
	mocks.BankKeeper.EXPECT().
		LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).AnyTimes()

	resp, err := ms.SettleDevshardEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Verify escrow is settled
	settled, found := k.GetDevshardEscrow(ctx, 1)
	require.True(t, found)
	require.True(t, settled.Settled)

	for _, addr := range slots {
		participant, found := k.GetParticipant(ctx, addr)
		require.True(t, found)
		require.Equal(t, int64(costPerSlot)+int64(fees/uint64(keeper.DevshardGroupSize)), participant.CoinBalance)
		require.Equal(t, costPerSlot+fees/uint64(keeper.DevshardGroupSize), participant.CurrentEpochStats.EarnedCoins)
	}
}

func TestSettleDevshardEscrow_AlreadySettled(t *testing.T) {
	k, ms, ctx, _ := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xBB
	escrow := types.DevshardEscrow{
		Id:      1,
		Creator: creator.String(),
		Settled: true,
		Slots:   make([]string, keeper.DevshardGroupSize),
	}
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	_, err = ms.SettleDevshardEscrow(ctx, &types.MsgSettleDevshardEscrow{
		Settler:  creator.String(),
		EscrowId: 1,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "already settled")
}

func TestSettleDevshardEscrow_WrongSettler(t *testing.T) {
	k, ms, ctx, _ := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xCC
	wrongSettler := sdk.AccAddress(make([]byte, 20))
	wrongSettler[0] = 0xDD
	escrow := types.DevshardEscrow{
		Id:      1,
		Creator: creator.String(),
		Slots:   make([]string, keeper.DevshardGroupSize),
	}
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	_, err = ms.SettleDevshardEscrow(ctx, &types.MsgSettleDevshardEscrow{
		Settler:  wrongSettler.String(),
		EscrowId: 1,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "not the escrow creator")
}

func TestSettleDevshardEscrow_ZeroCostSettlement(t *testing.T) {
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys := make([]*dcrdsecp.PrivateKey, keeper.DevshardGroupSize)
	slots := make([]string, keeper.DevshardGroupSize)
	for i := 0; i < keeper.DevshardGroupSize; i++ {
		key, err := dcrdsecp.GeneratePrivateKey()
		require.NoError(t, err)
		keys[i] = key
		slots[i] = cosmosAddressFromDcrdKey(key).String()
		setParticipantForDevshardTest(t, k, ctx, slots[i])
	}
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	setActiveParticipantsForDevshardTest(t, k, ctx, 5, slots...)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xBB
	escrow := types.DevshardEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     7_000_000_000,
		Slots:      slots,
		EpochIndex: 5,
		Settled:    false,
	}
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	hostStats := makeHostStats(keeper.DevshardGroupSize, 0) // all costs = 0
	msg := buildSettlementTestData(t, escrow, keys, hostStats, 0)

	// No validator payments expected (all costs are 0)
	// Full amount refunded to creator
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("devshard_escrow_refund")).
		Return(nil)

	resp, err := ms.SettleDevshardEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	settled, found := k.GetDevshardEscrow(ctx, 1)
	require.True(t, found)
	require.True(t, settled.Settled)
}

func TestSettleDevshardEscrow_AggregatesParticipantStats(t *testing.T) {
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keyH1, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	keyH2, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)

	addrH1 := cosmosAddressFromDcrdKey(keyH1).String()
	addrH2 := cosmosAddressFromDcrdKey(keyH2).String()
	setParticipantForDevshardTest(t, k, ctx, addrH1)
	setParticipantForDevshardTest(t, k, ctx, addrH2)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	setActiveParticipantsForDevshardTest(t, k, ctx, 5, addrH1, addrH2)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x21
	escrow := types.DevshardEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     5_000,
		Slots:      []string{addrH1, addrH1, addrH2, addrH2},
		EpochIndex: 5,
		Settled:    false,
	}
	_, err = k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	hostStats := []*types.DevshardSettlementHostStats{
		{SlotId: 0, Missed: 1, Invalid: 2, Cost: 10},
		{SlotId: 1, Missed: 0, Invalid: 1, Cost: 20},
		{SlotId: 2, Missed: 2, Invalid: 0, Cost: 30},
		{SlotId: 3, Missed: 1, Invalid: 1, Cost: 40},
	}
	msg := buildSettlementTestDataWithNonce(t, escrow, []*dcrdsecp.PrivateKey{keyH1, keyH1, keyH2, keyH2}, hostStats, 8, 20)

	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("devshard_escrow_refund")).
		Return(nil)
	mocks.BankKeeper.EXPECT().
		LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes()

	_, err = ms.SettleDevshardEscrow(ctx, msg)
	require.NoError(t, err)

	// assignedPerSlot = 20 / 4 = 5
	// H1: completed = (5-1) + (5-0) = 9, validated = (4-2) + (5-1) = 6
	participantH1, found := k.GetParticipant(ctx, addrH1)
	require.True(t, found)
	require.Equal(t, uint64(9), participantH1.CurrentEpochStats.InferenceCount)
	require.Equal(t, uint64(1), participantH1.CurrentEpochStats.MissedRequests)
	require.Equal(t, uint64(3), participantH1.CurrentEpochStats.InvalidatedInferences)
	require.Equal(t, uint64(6), participantH1.CurrentEpochStats.ValidatedInferences)

	// H2: completed = (5-2) + (5-1) = 7, validated = (3-0) + (4-1) = 6
	participantH2, found := k.GetParticipant(ctx, addrH2)
	require.True(t, found)
	require.Equal(t, uint64(7), participantH2.CurrentEpochStats.InferenceCount)
	require.Equal(t, uint64(3), participantH2.CurrentEpochStats.MissedRequests)
	require.Equal(t, uint64(1), participantH2.CurrentEpochStats.InvalidatedInferences)
	require.Equal(t, uint64(6), participantH2.CurrentEpochStats.ValidatedInferences)
}

func TestSettleDevshardEscrow_AggregatesParticipantStatsWithRemainderSlots(t *testing.T) {
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keyH1, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)
	keyH2, err := dcrdsecp.GeneratePrivateKey()
	require.NoError(t, err)

	addrH1 := cosmosAddressFromDcrdKey(keyH1).String()
	addrH2 := cosmosAddressFromDcrdKey(keyH2).String()
	setParticipantForDevshardTest(t, k, ctx, addrH1)
	setParticipantForDevshardTest(t, k, ctx, addrH2)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	setActiveParticipantsForDevshardTest(t, k, ctx, 5, addrH1, addrH2)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x22
	escrow := types.DevshardEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     5_000,
		Slots:      []string{addrH1, addrH2, addrH1, addrH2},
		EpochIndex: 5,
		Settled:    false,
	}
	_, err = k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	hostStats := []*types.DevshardSettlementHostStats{
		{SlotId: 0, Missed: 1, Invalid: 0, Cost: 10},
		{SlotId: 1, Missed: 0, Invalid: 1, Cost: 20},
		{SlotId: 2, Missed: 0, Invalid: 0, Cost: 30},
		{SlotId: 3, Missed: 1, Invalid: 0, Cost: 40},
	}
	msg := buildSettlementTestDataWithNonce(t, escrow, []*dcrdsecp.PrivateKey{keyH1, keyH2, keyH1, keyH2}, hostStats, 8, 6)

	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("devshard_escrow_refund")).
		Return(nil)
	mocks.BankKeeper.EXPECT().
		LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes()

	_, err = ms.SettleDevshardEscrow(ctx, msg)
	require.NoError(t, err)

	// nonce 6 with 4 slots gives slot upper bounds: slot 0 -> 1, slot 1 -> 2, slot 2 -> 2, slot 3 -> 1.
	participantH1, found := k.GetParticipant(ctx, addrH1)
	require.True(t, found)
	require.Equal(t, uint64(2), participantH1.CurrentEpochStats.InferenceCount)
	require.Equal(t, uint64(1), participantH1.CurrentEpochStats.MissedRequests)
	require.Equal(t, uint64(0), participantH1.CurrentEpochStats.InvalidatedInferences)
	require.Equal(t, uint64(2), participantH1.CurrentEpochStats.ValidatedInferences)

	participantH2, found := k.GetParticipant(ctx, addrH2)
	require.True(t, found)
	require.Equal(t, uint64(2), participantH2.CurrentEpochStats.InferenceCount)
	require.Equal(t, uint64(1), participantH2.CurrentEpochStats.MissedRequests)
	require.Equal(t, uint64(1), participantH2.CurrentEpochStats.InvalidatedInferences)
	require.Equal(t, uint64(1), participantH2.CurrentEpochStats.ValidatedInferences)
}

func TestSettleDevshardEscrow_PreviousEpochSettlementAllowedWithoutParticipantStats(t *testing.T) {
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	for _, addr := range slots {
		setParticipantForDevshardTest(t, k, ctx, addr)
	}
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 6))

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x31
	escrow := types.DevshardEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     7_000_000_000,
		Slots:      slots,
		EpochIndex: 5,
		Settled:    false,
	}
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	costPerSlot := uint64(100_000_000)
	fees := uint64(200_000_000)
	msg := buildSettlementTestData(t, escrow, keys, makeHostStats(keeper.DevshardGroupSize, costPerSlot), fees)

	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Eq("devshard_escrow_payment")).
		Return(nil).
		Times(keeper.DevshardGroupSize)
	expectedRefund := escrow.Amount - uint64(keeper.DevshardGroupSize)*costPerSlot - fees
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("devshard_escrow_refund")).
		DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
			require.Len(t, coins, 1)
			require.Equal(t, expectedRefund, coins[0].Amount.Uint64())
			return nil
		})

	resp, err := ms.SettleDevshardEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	for _, addr := range slots {
		participant, found := k.GetParticipant(ctx, addr)
		require.True(t, found)
		require.Equal(t, int64(0), participant.CoinBalance)
		require.Equal(t, uint64(0), participant.CurrentEpochStats.EarnedCoins)
		require.Equal(t, uint64(0), participant.CurrentEpochStats.InferenceCount)
		require.Equal(t, uint64(0), participant.CurrentEpochStats.MissedRequests)
		require.Equal(t, uint64(0), participant.CurrentEpochStats.InvalidatedInferences)
		require.Equal(t, uint64(0), participant.CurrentEpochStats.ValidatedInferences)
	}
}

func TestSettleDevshardEscrow_PreviousEpochSettlementDoesNotRollIntoCurrentEpochActiveParticipants(t *testing.T) {
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	for _, addr := range slots {
		setParticipantForDevshardTest(t, k, ctx, addr)
	}
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 6))
	setActiveParticipantsForDevshardTest(t, k, ctx, 6, slots...)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x32
	escrow := types.DevshardEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     7_000_000_000,
		Slots:      slots,
		EpochIndex: 5,
		Settled:    false,
	}
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	costPerSlot := uint64(100_000_000)
	fees := uint64(200_000_000)
	msg := buildSettlementTestData(t, escrow, keys, makeHostStats(keeper.DevshardGroupSize, costPerSlot), fees)

	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Eq("devshard_escrow_payment")).
		Return(nil).
		Times(keeper.DevshardGroupSize)
	expectedRefund := escrow.Amount - uint64(keeper.DevshardGroupSize)*costPerSlot - fees
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("devshard_escrow_refund")).
		DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
			require.Len(t, coins, 1)
			require.Equal(t, expectedRefund, coins[0].Amount.Uint64())
			return nil
		})

	resp, err := ms.SettleDevshardEscrow(ctx, msg)
	require.NoError(t, err)
	require.NotNil(t, resp)

	for _, addr := range slots {
		participant, found := k.GetParticipant(ctx, addr)
		require.True(t, found)
		require.Equal(t, int64(0), participant.CoinBalance)
		require.Equal(t, uint64(0), participant.CurrentEpochStats.EarnedCoins)
		require.Equal(t, uint64(0), participant.CurrentEpochStats.InferenceCount)
		require.Equal(t, uint64(0), participant.CurrentEpochStats.MissedRequests)
		require.Equal(t, uint64(0), participant.CurrentEpochStats.InvalidatedInferences)
		require.Equal(t, uint64(0), participant.CurrentEpochStats.ValidatedInferences)
	}
}

func TestSettleDevshardEscrow_CurrentEpochInactiveParticipantPaidImmediatelyWithoutParticipantStateChange(t *testing.T) {
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	for _, addr := range slots {
		setParticipantForDevshardTest(t, k, ctx, addr)
	}
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 5))
	setActiveParticipantsForDevshardTest(t, k, ctx, 5, slots[:keeper.DevshardGroupSize-1]...)

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x41
	escrow := types.DevshardEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     7_000_000_000,
		Slots:      slots,
		EpochIndex: 5,
		Settled:    false,
	}
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	costPerSlot := uint64(100_000_000)
	fees := uint64(200_000_000)
	msg := buildSettlementTestData(t, escrow, keys, makeHostStats(keeper.DevshardGroupSize, costPerSlot), fees)

	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, gomock.Any(), gomock.Any(), gomock.Eq("devshard_escrow_payment")).
		Return(nil).
		Times(1)
	expectedRefund := escrow.Amount - uint64(keeper.DevshardGroupSize)*costPerSlot - fees
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), gomock.Eq("devshard_escrow_refund")).
		DoAndReturn(func(_ context.Context, _ string, _ sdk.AccAddress, coins sdk.Coins, _ string) error {
			require.Len(t, coins, 1)
			require.Equal(t, expectedRefund, coins[0].Amount.Uint64())
			return nil
		})
	mocks.BankKeeper.EXPECT().
		LogSubAccountTransaction(gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any(), gomock.Any()).
		AnyTimes()

	_, err = ms.SettleDevshardEscrow(ctx, msg)
	require.NoError(t, err)

	activeParticipant, found := k.GetParticipant(ctx, slots[0])
	require.True(t, found)
	require.Equal(t, int64(costPerSlot)+int64(fees/uint64(keeper.DevshardGroupSize)), activeParticipant.CoinBalance)
	require.Equal(t, costPerSlot+fees/uint64(keeper.DevshardGroupSize), activeParticipant.CurrentEpochStats.EarnedCoins)

	inactiveParticipant, found := k.GetParticipant(ctx, slots[keeper.DevshardGroupSize-1])
	require.True(t, found)
	require.Equal(t, int64(0), inactiveParticipant.CoinBalance)
	require.Equal(t, uint64(0), inactiveParticipant.CurrentEpochStats.EarnedCoins)
	require.Equal(t, uint64(0), inactiveParticipant.CurrentEpochStats.InferenceCount)
	require.Equal(t, uint64(0), inactiveParticipant.CurrentEpochStats.MissedRequests)
	require.Equal(t, uint64(0), inactiveParticipant.CurrentEpochStats.InvalidatedInferences)
	require.Equal(t, uint64(0), inactiveParticipant.CurrentEpochStats.ValidatedInferences)
}

func TestSettleDevshardEscrow_OlderThanPreviousEpochRejected(t *testing.T) {
	k, ms, ctx, _ := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	keys, slots := generateDevshardKeys(t, keeper.DevshardGroupSize)
	for _, addr := range slots {
		setParticipantForDevshardTest(t, k, ctx, addr)
	}
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 7))

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0x31
	escrow := types.DevshardEscrow{
		Id:         1,
		Creator:    creator.String(),
		Amount:     7_000_000_000,
		Slots:      slots,
		EpochIndex: 5,
		Settled:    false,
	}
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	msg := buildSettlementTestData(t, escrow, keys, makeHostStats(keeper.DevshardGroupSize, 0), 0)

	_, err = ms.SettleDevshardEscrow(ctx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "current or previous epoch")
}

func TestSettleDevshardEscrow_AllowlistBlocks(t *testing.T) {
	k, ms, ctx, _ := setupDevshardEscrowTest(t)
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonka")

	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xCC
	escrow := types.DevshardEscrow{
		Id:      1,
		Creator: creator.String(),
		Amount:  7_000_000_000,
		Slots:   make([]string, keeper.DevshardGroupSize),
		Settled: false,
	}
	_, err := k.StoreDevshardEscrow(ctx, &escrow, 1)
	require.NoError(t, err)

	// Set params with allowlist NOT containing the escrow creator.
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DevshardEscrowParams = &types.DevshardEscrowParams{
		MinAmount:               types.DefaultDevshardEscrowMinAmount,
		MaxAmount:               types.DefaultDevshardEscrowMaxAmount,
		MaxEscrowsPerEpoch:      types.DefaultDevshardMaxEscrowsPerEpoch,
		GroupSize:               types.DefaultDevshardGroupSize,
		AllowedCreatorAddresses: []string{"gonka1someotheraddressxxxxxxxxxxxxxxxxxx"},
		TokenPrice:              types.DefaultDevshardTokenPrice,
		MaxNonce:                types.DefaultDevshardMaxNonce,
	}
	require.NoError(t, k.SetParams(ctx, params))

	_, err = ms.SettleDevshardEscrow(ctx, &types.MsgSettleDevshardEscrow{
		Settler:  creator.String(),
		EscrowId: 1,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "address is not allowed to create devshard escrows")
}
