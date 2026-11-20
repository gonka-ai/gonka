package keeper_test

import (
	"context"
	"strconv"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// Mainnet epoch 418 (group 1661), read from node2.gonka.ai on 2026-10-12:
// epoch_group_data/418 validation_weights (23 members, TotalWeight 841326),
// excluded_participants/418 (7 x failed_confirmation_poc) and the 12 voters
// recorded on ETH 26149258:181 / 26149505:257.
var epoch418Members = []struct {
	addr   string
	weight int64
}{
	{"gonka10eg5wakkmc0tee3llwj2qwa67knzk48cht38c5", 34362},
	{"gonka147qfckm9hpdzd9dgfvfj2r5rsv0eeunj4gpvz8", 82656},
	{"gonka168rtjfkszuhcggg4dfyse4yh7xn9zwfglnkns2", 111460},
	{"gonka1gyk0aahvr3qeju4zx0nplfreej6cy4jjk8svc5", 954},
	{"gonka1rqm2u7lxzn7r68y0drcyhag6xnckpsnfmgd53t", 7432},
	{"gonka1scskt6wpnjnumsah6kjphmdu87vjgvcxmn4rxv", 120823},
	{"gonka1u60wtn9dhadweeuq2gywcpx34fpfnuhmu7w0qq", 10872},
	{"gonka1ym3np7guxart483yfdxnlztuazx22cjt0e4a2p", 5462},
	{"gonka133xdfvcyj2ccv38jrl0qjp970u9lz5plptfwuh", 53462},
	{"gonka1346p2h8dn4kp98c5e93k5q64g0h7vxjxnd55fh", 1633},
	{"gonka16dgkvx7mh609ntkzknckwaskgq9lcdp86j0skk", 52900},
	{"gonka18x6wprs8c4x7awjgvn5t0e04fscrrash8rvmw4", 28070},
	{"gonka1a5dgp5cul6tn0jrdzs6usrm3jtg7w63nqaygd8", 2938},
	{"gonka1dkl4mah5erqggvhqkpc8j3qs5tyuetgdy552cp", 3311},
	{"gonka1gvrrhjmy4w4mayvs2s5l23edj8ertcmtd2v4zr", 171409},
	{"gonka1kvmerzu64094dt9t62ea0cp75larh39ulzldum", 49091},
	{"gonka1kx9mca3xm8u8ypzfuhmxey66u0ufxhs7nm6wc5", 10867},
	{"gonka1nurzpppvn0lxg20v2c3gf8zfltpdpz599rggst", 83062},
	{"gonka1p2lhgng7tcqju7emk989s5fpdr7k2c3ek6h26m", 1754},
	{"gonka1qwfrtz9c7kcrfkrrlne2pkcye74mj6ce33xdkl", 2876},
	{"gonka1tlvg4kjx7ljd5thgd5fkgh39q6lu8cmxupktgg", 3068},
	{"gonka1w29nvdy6caqtrw30whz9h6ghl0xszwh3egndah", 397},
	{"gonka1y2a9p56kv044327uycmqdexl7zs82fs5ryv5le", 2467},
}

// Excluded at h6451720 (5) and h6457153 (2).
var epoch418RemovedFirst = []string{
	"gonka18x6wprs8c4x7awjgvn5t0e04fscrrash8rvmw4",
	"gonka1gvrrhjmy4w4mayvs2s5l23edj8ertcmtd2v4zr",
	"gonka10eg5wakkmc0tee3llwj2qwa67knzk48cht38c5",
	"gonka1scskt6wpnjnumsah6kjphmdu87vjgvcxmn4rxv",
	"gonka1u60wtn9dhadweeuq2gywcpx34fpfnuhmu7w0qq",
}
var epoch418RemovedSecond = []string{
	"gonka1y2a9p56kv044327uycmqdexl7zs82fs5ryv5le",
	"gonka133xdfvcyj2ccv38jrl0qjp970u9lz5plptfwuh",
}

// The 12 members that voted on every epoch-418 record and are still in the group.
var epoch418LiveVoters = []string{
	"gonka1346p2h8dn4kp98c5e93k5q64g0h7vxjxnd55fh",
	"gonka147qfckm9hpdzd9dgfvfj2r5rsv0eeunj4gpvz8",
	"gonka168rtjfkszuhcggg4dfyse4yh7xn9zwfglnkns2",
	"gonka16dgkvx7mh609ntkzknckwaskgq9lcdp86j0skk",
	"gonka1a5dgp5cul6tn0jrdzs6usrm3jtg7w63nqaygd8",
	"gonka1dkl4mah5erqggvhqkpc8j3qs5tyuetgdy552cp",
	"gonka1gyk0aahvr3qeju4zx0nplfreej6cy4jjk8svc5",
	"gonka1kx9mca3xm8u8ypzfuhmxey66u0ufxhs7nm6wc5",
	"gonka1nurzpppvn0lxg20v2c3gf8zfltpdpz599rggst",
	"gonka1p2lhgng7tcqju7emk989s5fpdr7k2c3ek6h26m",
	"gonka1qwfrtz9c7kcrfkrrlne2pkcye74mj6ce33xdkl",
	"gonka1tlvg4kjx7ljd5thgd5fkgh39q6lu8cmxupktgg",
}

// Records that already hold the live majority when members are removed after
// voting (mainnet epoch 418 numbers, scenario from zpoken's review of #1958):
// no live member can vote again, so the EndBlock re-check must complete them.
func TestBridgeRecheck_Epoch418_RemovalAfterVotesCompletesInEndBlock(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	epochIndex := uint64(418)
	var total int64
	vws := make([]*types.ValidationWeight, 0, len(epoch418Members))
	parts := make([]*types.ActiveParticipant, 0, len(epoch418Members))
	for _, m := range epoch418Members {
		total += m.weight
		vws = append(vws, &types.ValidationWeight{MemberAddress: m.addr, Weight: m.weight})
		parts = append(parts, &types.ActiveParticipant{Index: m.addr})
		mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), sdk.MustAccAddressFromBech32(m.addr)).Return(true).AnyTimes()
	}
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epochIndex))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: epochIndex, EpochGroupId: 1661, TotalWeight: total, ValidationWeights: vws,
	})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{EpochId: epochIndex, Participants: parts}))

	removed := map[string]bool{}
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).DoAndReturn(
		func(context.Context, *group.QueryGroupMembersRequest) (*group.QueryGroupMembersResponse, error) {
			var out []*group.GroupMember
			for _, m := range epoch418Members {
				if removed[m.addr] {
					continue
				}
				out = append(out, &group.GroupMember{GroupId: 1661, Member: &group.Member{Address: m.addr, Weight: strconv.FormatInt(m.weight, 10)}})
			}
			return &group.QueryGroupMembersResponse{Members: out}, nil
		}).AnyTimes()
	mocks.GroupKeeper.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any()).Return(&group.MsgUpdateGroupMembersResponse{}, nil).AnyTimes()
	mocks.GroupKeeper.EXPECT().UpdateGroupMetadata(gomock.Any(), gomock.Any()).Return(&group.MsgUpdateGroupMetadataResponse{}, nil).AnyTimes()

	recipient := sdk.AccAddress([]byte("bridge_recipient____")).String()
	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{Id: "1", ChainId: "ethereum", Address: "0x972a7a92d92796a98801a8818bcf91f1648f2f68"})
	vote := func(v string) error {
		_, err := ms.BridgeExchange(ctx, &types.MsgBridgeExchange{
			OriginChain: "ethereum", ContractAddress: "0x972a7a92d92796a98801a8818bcf91f1648f2f68", OwnerAddress: recipient,
			Amount: "4000000000000", BlockNumber: "26149258", ReceiptIndex: "181", Validator: v,
		})
		return err
	}
	record := func() *types.BridgeTransaction {
		tx, found := k.GetBridgeTransactionByContent(ctx, &types.BridgeTransaction{
			ChainId: "ethereum", ContractAddress: "0x972a7a92d92796a98801a8818bcf91f1648f2f68", OwnerAddress: recipient,
			Amount: "4000000000000", BlockNumber: "26149258", ReceiptIndex: "181",
		})
		require.True(t, found)
		return tx
	}
	pending := func() int {
		it, err := k.BridgePendingByEpoch.Iterate(ctx, collections.NewPrefixedPairRange[uint64, string](epochIndex))
		require.NoError(t, err)
		defer it.Close()
		keys, err := it.Keys()
		require.NoError(t, err)
		return len(keys)
	}

	for _, v := range epoch418LiveVoters {
		require.NoError(t, vote(v))
	}
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_PENDING, record().Status)
	require.Equal(t, 1, pending())

	// Nothing to do before the group changes.
	k.RecheckPendingBridgeTransactions(ctx)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_PENDING, record().Status)

	eg, err := k.GetEpochGroup(ctx, epochIndex, "")
	require.NoError(t, err)
	for _, a := range append(append([]string{}, epoch418RemovedFirst...), epoch418RemovedSecond...) {
		require.NoError(t, eg.RemoveMember(ctx, &types.Participant{Address: a}))
		removed[a] = true
	}
	for _, v := range epoch418LiveVoters {
		require.ErrorIs(t, vote(v), types.ErrBridgeAlreadyValidated)
	}

	escrow := sdk.AccAddress([]byte("bridge_escrow_______"))
	mocks.AccountKeeper.EXPECT().GetModuleAddress(types.BridgeEscrowAccName).Return(escrow).AnyTimes()
	mocks.BankViewKeeper.EXPECT().SpendableCoin(gomock.Any(), escrow, types.BaseCoin).Return(sdk.NewInt64Coin(types.BaseCoin, 9_124_775_000_000_000)).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName,
		sdk.MustAccAddressFromBech32(recipient), sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 4_000_000_000_000)), "bridge_release").Return(nil).Times(1)

	k.MarkBridgeEpochForRecheck(ctx, epochIndex) // what removeFromEpochGroups does after RemoveMember
	k.RecheckPendingBridgeTransactions(ctx)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_COMPLETED, record().Status)
	require.Equal(t, 0, pending())
	has, err := k.BridgeRecheckEpochs.Has(ctx, epochIndex)
	require.NoError(t, err)
	require.False(t, has)

	// A second pass releases nothing again.
	k.MarkBridgeEpochForRecheck(ctx, epochIndex)
	k.RecheckPendingBridgeTransactions(ctx)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_COMPLETED, record().Status)
}
