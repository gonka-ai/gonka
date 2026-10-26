package keeper_test

import (
	"context"
	"encoding/hex"
	"fmt"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func devshardCtxWithAppHash(ctx sdk.Context, appHash []byte) sdk.Context {
	info := ctx.HeaderInfo()
	info.AppHash = appHash
	return ctx.WithHeaderInfo(info).WithEventManager(sdk.NewEventManager())
}

func devshardTestHash(seed byte) []byte {
	h := make([]byte, 32)
	for i := range h {
		h[i] = seed + byte(i)
	}
	return h
}

func createPendingDevshardEscrow(t *testing.T, ms types.MsgServer, ctx sdk.Context, expectLock func(), creator sdk.AccAddress) uint64 {
	t.Helper()
	expectLock()
	resp, err := ms.CreateDevshardEscrow(ctx, &types.MsgCreateDevshardEscrow{
		Creator: creator.String(),
		Amount:  7_000_000_000,
		ModelId: testDevshardModelID,
	})
	require.NoError(t, err)
	return resp.EscrowId
}

func setupDevshardSlotAssignment(t *testing.T) (keeper.Keeper, types.MsgServer, sdk.Context, func(), sdk.AccAddress, []string) {
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	addrs := makeDevshardAddrs(1, 20)
	setupEpochGroupForDevshard(ctx, k, 5, testDevshardModelID, addrs)
	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF
	lock := func() {
		mocks.BankKeeper.EXPECT().
			SendCoinsFromAccountToModule(gomock.Any(), creator, types.ModuleName, gomock.Any(), gomock.Any()).
			Return(nil)
	}
	return k, ms, ctx, lock, creator, addrs
}

func expectedDevshardSlots(addrs []string, appHash []byte, escrowID uint64) []string {
	weights := make(map[string]int64, len(addrs))
	for _, addr := range addrs {
		weights[addr] = 100
	}
	sorted, total := calculations.PrepareSortedEntries(weights)
	return calculations.GetSlotsFromSorted(
		hex.EncodeToString(appHash),
		fmt.Sprintf("devshard_escrow:%d", escrowID),
		testDevshardModelID,
		sorted,
		total,
		keeper.DevshardGroupSize,
	)
}

func TestCreateDevshardEscrow_PendingUntilNextBlock(t *testing.T) {
	k, ms, ctx, lock, creator, _ := setupDevshardSlotAssignment(t)
	ctx = devshardCtxWithAppHash(ctx, devshardTestHash(0x10))

	id := createPendingDevshardEscrow(t, ms, ctx, lock, creator)

	escrow, found := k.GetDevshardEscrow(ctx, id)
	require.True(t, found)
	require.Empty(t, escrow.Slots)
	require.Empty(t, escrow.AppHash)
	require.True(t, k.IsDevshardEscrowPendingSlots(ctx, id))

	resp, err := k.DevshardEscrow(ctx, &types.QueryGetDevshardEscrowRequest{Id: id})
	require.NoError(t, err)
	require.False(t, resp.Found)
	require.Nil(t, resp.Escrow)

	_, err = ms.SettleDevshardEscrow(ctx, &types.MsgSettleDevshardEscrow{
		Settler:  creator.String(),
		EscrowId: id,
	})
	require.ErrorContains(t, err, "pending slot assignment")
}

func TestAssignPendingDevshardSlots_UsesNextBlockAppHash(t *testing.T) {
	k, ms, ctx, lock, creator, addrs := setupDevshardSlotAssignment(t)
	createCtx := devshardCtxWithAppHash(ctx, devshardTestHash(0x10))
	id := createPendingDevshardEscrow(t, ms, createCtx, lock, creator)

	nextHash := devshardTestHash(0x20)
	nextCtx := devshardCtxWithAppHash(ctx, nextHash)
	require.NoError(t, k.AssignPendingDevshardSlots(nextCtx))

	escrow, found := k.GetDevshardEscrow(nextCtx, id)
	require.True(t, found)
	require.Equal(t, expectedDevshardSlots(addrs, nextHash, id), escrow.Slots)
	require.Equal(t, hex.EncodeToString(nextHash), escrow.AppHash)
	require.False(t, k.IsDevshardEscrowPendingSlots(nextCtx, id))

	resp, err := k.DevshardEscrow(nextCtx, &types.QueryGetDevshardEscrowRequest{Id: id})
	require.NoError(t, err)
	require.True(t, resp.Found)
	require.Equal(t, escrow.Slots, resp.Escrow.Slots)

	var assigned bool
	for _, ev := range nextCtx.EventManager().Events() {
		if ev.Type == "devshard_escrow_slots_assigned" {
			assigned = true
		}
	}
	require.True(t, assigned)
}

func TestAssignPendingDevshardSlots_CreateBlockAppHashDoesNotDecideCommittee(t *testing.T) {
	k, ms, ctx, lock, creator, addrs := setupDevshardSlotAssignment(t)
	nextHash := devshardTestHash(0x40)

	committees := make([][]string, 0, 2)
	for _, createHash := range [][]byte{devshardTestHash(0x10), devshardTestHash(0x30)} {
		branch, _ := ctx.CacheContext()
		id := createPendingDevshardEscrow(t, ms, devshardCtxWithAppHash(branch, createHash), lock, creator)
		nextCtx := devshardCtxWithAppHash(branch, nextHash)
		require.NoError(t, k.AssignPendingDevshardSlots(nextCtx))
		escrow, found := k.GetDevshardEscrow(nextCtx, id)
		require.True(t, found)
		require.NotEqual(t, expectedDevshardSlots(addrs, createHash, id), escrow.Slots)
		committees = append(committees, escrow.Slots)
	}
	require.Equal(t, committees[0], committees[1])

	branch, _ := ctx.CacheContext()
	id := createPendingDevshardEscrow(t, ms, devshardCtxWithAppHash(branch, devshardTestHash(0x10)), lock, creator)
	otherNext := devshardCtxWithAppHash(branch, devshardTestHash(0x50))
	require.NoError(t, k.AssignPendingDevshardSlots(otherNext))
	escrow, found := k.GetDevshardEscrow(otherNext, id)
	require.True(t, found)
	require.NotEqual(t, committees[0], escrow.Slots)
}

func TestAssignPendingDevshardSlots_AssignsEveryEscrowInBlock(t *testing.T) {
	k, ms, ctx, lock, creator, addrs := setupDevshardSlotAssignment(t)
	createCtx := devshardCtxWithAppHash(ctx, devshardTestHash(0x10))
	first := createPendingDevshardEscrow(t, ms, createCtx, lock, creator)
	second := createPendingDevshardEscrow(t, ms, createCtx, lock, creator)

	nextHash := devshardTestHash(0x20)
	nextCtx := devshardCtxWithAppHash(ctx, nextHash)
	require.NoError(t, k.AssignPendingDevshardSlots(nextCtx))

	for _, id := range []uint64{first, second} {
		escrow, found := k.GetDevshardEscrow(nextCtx, id)
		require.True(t, found)
		require.Equal(t, expectedDevshardSlots(addrs, nextHash, id), escrow.Slots)
		require.False(t, k.IsDevshardEscrowPendingSlots(nextCtx, id))
	}
}

func TestAssignPendingDevshardSlots_RefundsWhenWeightsGone(t *testing.T) {
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	setupEpochGroupForDevshard(ctx, k, 5, testDevshardModelID, makeDevshardAddrs(1, 20))
	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF
	amount := uint64(7_000_000_000)

	mocks.BankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), creator, types.ModuleName, gomock.Any(), gomock.Any()).
		Return(nil)
	resp, err := ms.CreateDevshardEscrow(devshardCtxWithAppHash(ctx, devshardTestHash(0x10)), &types.MsgCreateDevshardEscrow{
		Creator: creator.String(),
		Amount:  amount,
		ModelId: testDevshardModelID,
	})
	require.NoError(t, err)

	require.NoError(t, k.EpochGroupDataMap.Remove(ctx, collections.Join(uint64(5), testDevshardModelID)))

	coins, err := types.GetCoins(int64(amount))
	require.NoError(t, err)
	mocks.BankKeeper.EXPECT().
		SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, coins, "devshard_escrow_unassigned_refund").
		Return(nil)

	nextCtx := devshardCtxWithAppHash(ctx, devshardTestHash(0x20))
	require.NoError(t, k.AssignPendingDevshardSlots(nextCtx))

	escrow, found := k.GetDevshardEscrow(nextCtx, resp.EscrowId)
	require.True(t, found)
	require.True(t, escrow.Settled)
	require.Empty(t, escrow.Slots)
	require.False(t, k.IsDevshardEscrowPendingSlots(nextCtx, resp.EscrowId))
}

func TestAssignPendingDevshardSlots_NoPendingIsNoop(t *testing.T) {
	k, _, ctx, _ := setupDevshardEscrowTest(t)
	ctx = devshardCtxWithAppHash(ctx, devshardTestHash(0x10))
	require.NoError(t, k.AssignPendingDevshardSlots(ctx))
	require.Empty(t, ctx.EventManager().Events())
}

func TestAssignPendingDevshardSlots_UsesCreateTimeGroupSize(t *testing.T) {
	k, ms, ctx, lock, creator, _ := setupDevshardSlotAssignment(t)
	id := createPendingDevshardEscrow(t, ms, devshardCtxWithAppHash(ctx, devshardTestHash(0x10)), lock, creator)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DevshardEscrowParams.GroupSize = 8
	require.NoError(t, k.SetParams(ctx, params))

	nextCtx := devshardCtxWithAppHash(ctx, devshardTestHash(0x20))
	require.NoError(t, k.AssignPendingDevshardSlots(nextCtx))

	escrow, found := k.GetDevshardEscrow(nextCtx, id)
	require.True(t, found)
	require.Len(t, escrow.Slots, keeper.DevshardGroupSize)
}

func TestAssignPendingDevshardSlots_DoesNotReassign(t *testing.T) {
	k, ms, ctx, lock, creator, _ := setupDevshardSlotAssignment(t)
	id := createPendingDevshardEscrow(t, ms, devshardCtxWithAppHash(ctx, devshardTestHash(0x10)), lock, creator)

	firstCtx := devshardCtxWithAppHash(ctx, devshardTestHash(0x20))
	require.NoError(t, k.AssignPendingDevshardSlots(firstCtx))
	first, found := k.GetDevshardEscrow(firstCtx, id)
	require.True(t, found)

	require.NoError(t, k.DevshardEscrowsPendingSlots.Set(ctx, id, uint32(keeper.DevshardGroupSize)))
	laterCtx := devshardCtxWithAppHash(ctx, devshardTestHash(0x50))
	require.NoError(t, k.AssignPendingDevshardSlots(laterCtx))

	later, found := k.GetDevshardEscrow(laterCtx, id)
	require.True(t, found)
	require.Equal(t, first.Slots, later.Slots)
	require.Equal(t, first.AppHash, later.AppHash)
	require.False(t, k.IsDevshardEscrowPendingSlots(laterCtx, id))
}

func TestAssignPendingDevshardSlots_FailedRefundRollsBack(t *testing.T) {
	k, ms, ctx, mocks := setupDevshardEscrowTest(t)
	setupEpochGroupForDevshard(ctx, k, 5, testDevshardModelID, makeDevshardAddrs(1, 20))
	creator := sdk.AccAddress(make([]byte, 20))
	creator[0] = 0xFF

	mocks.BankKeeper.EXPECT().
		SendCoinsFromAccountToModule(gomock.Any(), creator, types.ModuleName, gomock.Any(), gomock.Any()).
		Return(nil)
	resp, err := ms.CreateDevshardEscrow(devshardCtxWithAppHash(ctx, devshardTestHash(0x10)), &types.MsgCreateDevshardEscrow{
		Creator: creator.String(),
		Amount:  7_000_000_000,
		ModelId: testDevshardModelID,
	})
	require.NoError(t, err)
	require.NoError(t, k.EpochGroupDataMap.Remove(ctx, collections.Join(uint64(5), testDevshardModelID)))

	gomock.InOrder(
		mocks.BankKeeper.EXPECT().
			SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), "devshard_escrow_unassigned_refund").
			DoAndReturn(func(c context.Context, _ string, _ sdk.AccAddress, _ sdk.Coins, _ string) error {
				sdk.UnwrapSDKContext(c).EventManager().EmitEvent(sdk.NewEvent("partial_refund"))
				escrow, found := k.GetDevshardEscrow(c, resp.EscrowId)
				require.True(t, found)
				escrow.Settled = true
				require.NoError(t, k.SetDevshardEscrow(c, escrow))
				require.NoError(t, k.DevshardEscrowsPendingSlots.Remove(c, resp.EscrowId))
				return fmt.Errorf("bank unavailable")
			}),
		mocks.BankKeeper.EXPECT().
			SendCoinsFromModuleToAccount(gomock.Any(), types.ModuleName, creator, gomock.Any(), "devshard_escrow_unassigned_refund").
			Return(nil),
	)

	failCtx := devshardCtxWithAppHash(ctx, devshardTestHash(0x20))
	require.NoError(t, k.AssignPendingDevshardSlots(failCtx))
	escrow, found := k.GetDevshardEscrow(failCtx, resp.EscrowId)
	require.True(t, found)
	require.False(t, escrow.Settled)
	require.True(t, k.IsDevshardEscrowPendingSlots(failCtx, resp.EscrowId))
	require.Empty(t, failCtx.EventManager().Events())

	retryCtx := devshardCtxWithAppHash(ctx, devshardTestHash(0x30))
	require.NoError(t, k.AssignPendingDevshardSlots(retryCtx))
	escrow, found = k.GetDevshardEscrow(retryCtx, resp.EscrowId)
	require.True(t, found)
	require.True(t, escrow.Settled)
	require.False(t, k.IsDevshardEscrowPendingSlots(retryCtx, resp.EscrowId))
	require.NoError(t, k.AssignPendingDevshardSlots(devshardCtxWithAppHash(ctx, devshardTestHash(0x40))))
}
