package keeper_test

import (
	"context"
	"strconv"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// Backfilled records are re-checked at most BridgeRecheckPerBlock per pass, resuming
// from the cursor; the epoch mark is dropped once its index is walked to the end.
func TestBridgeRecheck_BackfillAndBoundedPasses(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocks(t)

	const epochIndex = uint64(5)
	member := sdk.AccAddress([]byte("bridge_member_______")).String()
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: epochIndex, EpochGroupId: 7, TotalWeight: 100,
		ValidationWeights: []*types.ValidationWeight{{MemberAddress: member, Weight: 100}},
	})
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error) {
			return &group.QueryGroupMembersResponse{Members: []*group.GroupMember{
				{GroupId: 7, Member: &group.Member{Address: member, Weight: "100"}},
			}}, nil
		}).AnyTimes()

	contract := "0x972a7a92d92796a98801a8818bcf91f1648f2f68"
	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{Id: "1", ChainId: "ethereum", Address: contract})
	escrow := sdk.AccAddress([]byte("bridge_escrow_______"))
	mocks.AccountKeeper.EXPECT().GetModuleAddress(types.BridgeEscrowAccName).Return(escrow).AnyTimes()
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	mocks.BankViewKeeper.EXPECT().SpendableCoin(gomock.Any(), escrow, types.BaseCoin).Return(sdk.NewInt64Coin(types.BaseCoin, 1_000_000_000)).AnyTimes()

	const n = 2*keeper.BridgeRecheckPerBlock + 20
	recipient := sdk.AccAddress([]byte("bridge_recipient____")).String()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName,
		sdk.MustAccAddressFromBech32(recipient), sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 1)), "bridge_release").Return(nil).Times(n)
	for i := 0; i < n; i++ {
		k.SetBridgeTransaction(ctx, &types.BridgeTransaction{
			ChainId: "ethereum", ContractAddress: contract, OwnerAddress: recipient, Amount: "1",
			BlockNumber: "100", ReceiptIndex: strconv.Itoa(i), EpochIndex: epochIndex,
			Status: types.BridgeTransactionStatus_BRIDGE_PENDING, Validators: []string{member},
		})
	}
	// A completed record is not indexed.
	k.SetBridgeTransaction(ctx, &types.BridgeTransaction{
		ChainId: "ethereum", ContractAddress: contract, OwnerAddress: recipient, Amount: "1",
		BlockNumber: "101", ReceiptIndex: "0", EpochIndex: epochIndex,
		Status: types.BridgeTransactionStatus_BRIDGE_COMPLETED,
	})

	// Developer stats live in the same store under "stats/" keys; the recheck maps must not see them.
	require.NoError(t, k.SetDeveloperStats(ctx, types.Inference{InferenceId: "inf1", EpochId: 3, RequestedBy: recipient, Model: "m", StartBlockTimestamp: 1000}))
	indexed, err := k.BackfillBridgePendingIndex(ctx)
	require.NoError(t, err)
	require.Equal(t, n, indexed)

	completed := func() int {
		c := 0
		it, err := k.BridgeTransactionsMap.Iterate(ctx, nil)
		require.NoError(t, err)
		defer it.Close()
		vals, err := it.Values()
		require.NoError(t, err)
		for _, v := range vals {
			if v.BlockNumber == "100" && v.Status == types.BridgeTransactionStatus_BRIDGE_COMPLETED {
				c++
			}
		}
		return c
	}
	marked := func() bool {
		has, err := k.BridgeRecheckEpochs.Has(ctx, epochIndex)
		require.NoError(t, err)
		return has
	}

	k.RecheckPendingBridgeTransactions(ctx)
	require.Equal(t, keeper.BridgeRecheckPerBlock, completed())
	require.True(t, marked())
	k.RecheckPendingBridgeTransactions(ctx)
	require.Equal(t, 2*keeper.BridgeRecheckPerBlock, completed())
	require.True(t, marked())
	k.RecheckPendingBridgeTransactions(ctx)
	require.Equal(t, n, completed())
	require.False(t, marked())
}

// A mark that lands while a pass is under way keeps the cursor: records past it are
// still reached when members are removed every block, and records before it are
// re-checked by one more pass from the start.
func TestBridgeRecheck_MarkDuringPassKeepsCursorAndRestarts(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocks(t)

	const epochIndex = uint64(6)
	addr := func(s string) string { return sdk.AccAddress([]byte(s)).String() }
	a, b, c, d := addr("bridge_member_a_____"), addr("bridge_member_b_____"), addr("bridge_member_c_____"), addr("bridge_member_d_____")
	k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: epochIndex, EpochGroupId: 8, TotalWeight: 100})
	live := map[string]string{a: "40", b: "20", c: "20", d: "20"}
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error) {
			var members []*group.GroupMember
			for _, m := range []string{a, b, c, d} {
				if w, ok := live[m]; ok {
					members = append(members, &group.GroupMember{GroupId: 8, Member: &group.Member{Address: m, Weight: w}})
				}
			}
			return &group.QueryGroupMembersResponse{Members: members}, nil
		}).AnyTimes()

	contract := "0x972a7a92d92796a98801a8818bcf91f1648f2f68"
	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{Id: "1", ChainId: "ethereum", Address: contract})
	escrow := sdk.AccAddress([]byte("bridge_escrow_______"))
	mocks.AccountKeeper.EXPECT().GetModuleAddress(types.BridgeEscrowAccName).Return(escrow).AnyTimes()
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	mocks.BankViewKeeper.EXPECT().SpendableCoin(gomock.Any(), escrow, types.BaseCoin).Return(sdk.NewInt64Coin(types.BaseCoin, 1_000_000_000)).AnyTimes()
	recipient := sdk.AccAddress([]byte("bridge_recipient____")).String()

	const n = 2*keeper.BridgeRecheckPerBlock + 20
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName,
		sdk.MustAccAddressFromBech32(recipient), sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 1)), "bridge_release").Return(nil).Times(n)
	put := func(block string, i int, voters []string) {
		k.SetBridgeTransaction(ctx, &types.BridgeTransaction{
			ChainId: "ethereum", ContractAddress: contract, OwnerAddress: recipient, Amount: "1",
			BlockNumber: block, ReceiptIndex: strconv.Itoa(1000 + i), EpochIndex: epochIndex,
			Status: types.BridgeTransactionStatus_BRIDGE_PENDING, Validators: voters,
		})
	}
	// Records sort by id; block "100" ones come first. Each holds a's 40 votes.
	for i := 0; i < n; i++ {
		put("100", i, []string{a})
	}
	_, err := k.BackfillBridgePendingIndex(ctx)
	require.NoError(t, err)

	completed := func() int {
		cnt := 0
		it, err := k.BridgeTransactionsMap.Iterate(ctx, nil)
		require.NoError(t, err)
		defer it.Close()
		vals, err := it.Values()
		require.NoError(t, err)
		for _, v := range vals {
			if v.Status == types.BridgeTransactionStatus_BRIDGE_COMPLETED {
				cnt++
			}
		}
		return cnt
	}
	remove := func(m string) {
		delete(live, m)
		k.MarkBridgeEpochForRecheck(ctx, epochIndex)
	}

	// d leaves: live 80, required 41 > 40. The first pass walks 50 records, none complete.
	remove(d)
	k.RecheckPendingBridgeTransactions(ctx)
	require.Equal(t, 0, completed())

	// c leaves mid-pass: required 31. The pass goes on from its cursor.
	remove(c)
	k.RecheckPendingBridgeTransactions(ctx)
	require.Equal(t, keeper.BridgeRecheckPerBlock, completed())

	// A mark every block does not pull the cursor back to the first records.
	k.MarkBridgeEpochForRecheck(ctx, epochIndex)
	k.RecheckPendingBridgeTransactions(ctx)
	require.Equal(t, keeper.BridgeRecheckPerBlock+20, completed())

	// The restart pass completes the first 50, judged under the new required power.
	k.RecheckPendingBridgeTransactions(ctx)
	require.Equal(t, n, completed())
	k.RecheckPendingBridgeTransactions(ctx)
	has, err := k.BridgeRecheckEpochs.Has(ctx, epochIndex)
	require.NoError(t, err)
	require.False(t, has)
	has, err = k.BridgeRecheckRestart.Has(ctx, epochIndex)
	require.NoError(t, err)
	require.False(t, has)
}

// Pending records that never reach a majority sort ahead of one that does; with a
// member removed every block the pass still gets past them to the real one.
func TestBridgeRecheck_RemovalEveryBlockReachesRecordsPastStuckOnes(t *testing.T) {
	k, _, ctx, mocks := setupKeeperWithMocks(t)

	const epochIndex = uint64(7)
	a := sdk.AccAddress([]byte("bridge_member_a_____")).String()
	k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: epochIndex, EpochGroupId: 9, TotalWeight: 100})
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(&group.QueryGroupMembersResponse{
		Members: []*group.GroupMember{{GroupId: 9, Member: &group.Member{Address: a, Weight: "60"}}},
	}, nil).AnyTimes()

	contract := "0x972a7a92d92796a98801a8818bcf91f1648f2f68"
	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{Id: "1", ChainId: "ethereum", Address: contract})
	escrow := sdk.AccAddress([]byte("bridge_escrow_______"))
	mocks.AccountKeeper.EXPECT().GetModuleAddress(types.BridgeEscrowAccName).Return(escrow).AnyTimes()
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	mocks.BankViewKeeper.EXPECT().SpendableCoin(gomock.Any(), escrow, types.BaseCoin).Return(sdk.NewInt64Coin(types.BaseCoin, 1_000_000_000)).AnyTimes()
	recipient := sdk.AccAddress([]byte("bridge_recipient____")).String()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName,
		sdk.MustAccAddressFromBech32(recipient), sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 1)), "bridge_release").Return(nil).Times(1)

	put := func(block string, i int, voters []string) {
		k.SetBridgeTransaction(ctx, &types.BridgeTransaction{
			ChainId: "ethereum", ContractAddress: contract, OwnerAddress: recipient, Amount: "1",
			BlockNumber: block, ReceiptIndex: strconv.Itoa(i), EpochIndex: epochIndex,
			Status: types.BridgeTransactionStatus_BRIDGE_PENDING, Validators: voters,
		})
	}
	for i := 0; i < 2*keeper.BridgeRecheckPerBlock; i++ {
		put("100", i, nil)
	}
	put("200", 0, []string{a})
	_, err := k.BackfillBridgePendingIndex(ctx)
	require.NoError(t, err)

	realStatus := func() types.BridgeTransactionStatus {
		rng := collections.NewPrefixedTripleRange[string, string, string]("ethereum")
		it, err := k.BridgeTransactionsMap.Iterate(ctx, rng)
		require.NoError(t, err)
		defer it.Close()
		vals, err := it.Values()
		require.NoError(t, err)
		for _, v := range vals {
			if v.BlockNumber == "200" {
				return v.Status
			}
		}
		t.Fatal("record not found")
		return 0
	}
	for block := 0; block < 3; block++ {
		k.MarkBridgeEpochForRecheck(ctx, epochIndex)
		k.RecheckPendingBridgeTransactions(ctx)
	}
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_COMPLETED, realStatus())
}
