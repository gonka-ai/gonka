package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

// Mainnet epoch 418: TotalWeight 841326, 7 members removed by
// deactiveParticipant (failed_confirmation_poc), 16 left with 419861 < 420664.
// Here: TotalWeight 100, removed member held 60, the two left hold 20+20.
func TestBridgeExchange_CompletesAfterMajorityMemberRemoval(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	a := testutil.Validator
	b := testutil.Validator2
	removed := sdk.AccAddress([]byte("removed_member______")).String()
	epochIndex := uint64(1)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epochIndex))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: epochIndex, EpochGroupId: 1, TotalWeight: 100,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: a, Weight: 20}, {MemberAddress: b, Weight: 20}, {MemberAddress: removed, Weight: 60},
		},
	})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epochIndex,
		Participants: []*types.ActiveParticipant{{Index: a}, {Index: b}, {Index: removed}},
	}))
	for _, v := range []string{a, b, removed} {
		mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), sdk.MustAccAddressFromBech32(v)).Return(true).AnyTimes()
	}

	// The real RemoveMember path: x/group weight 0, metadata "changed".
	mocks.GroupKeeper.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any()).Return(&group.MsgUpdateGroupMembersResponse{}, nil).AnyTimes()
	mocks.GroupKeeper.EXPECT().UpdateGroupMetadata(gomock.Any(), gomock.Any()).Return(&group.MsgUpdateGroupMetadataResponse{}, nil).AnyTimes()
	eg, err := k.GetEpochGroup(ctx, epochIndex, "")
	require.NoError(t, err)
	require.NoError(t, eg.RemoveMember(ctx, &types.Participant{Address: removed}))
	after, found := k.GetEpochGroupData(ctx, epochIndex, "")
	require.True(t, found)
	require.Equal(t, int64(100), after.TotalWeight, "RemoveMember leaves TotalWeight unchanged")

	// x/group now lists only the two remaining members (weight-0 members are deleted).
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{Members: []*group.GroupMember{
			{GroupId: 1, Member: &group.Member{Address: a, Weight: "20"}},
			{GroupId: 1, Member: &group.Member{Address: b, Weight: "20"}},
		}}, nil,
	).AnyTimes()

	// A WGNK burn, as on mainnet: completion releases native coins from escrow.
	recipient := sdk.AccAddress([]byte("bridge_recipient____")).String()
	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{Id: "1", ChainId: "ethereum", Address: "0x123"})
	newMsg := func(v string) *types.MsgBridgeExchange {
		return &types.MsgBridgeExchange{
			OriginChain: "ethereum", ContractAddress: "0x123", OwnerAddress: recipient,
			Amount: "100", BlockNumber: "1000", ReceiptIndex: "1", Validator: v,
		}
	}
	vote := func(v string) error {
		_, err := ms.BridgeExchange(ctx, newMsg(v))
		return err
	}
	require.NoError(t, vote(a))
	require.ErrorIs(t, vote(removed), types.ErrBridgeValidatorNotInTxEpochGroup)

	// The last member still in the group votes; on the base its 40 of 100 never reach 51.
	validated, err := k.ValidateBridgeExchange(ctx, newMsg(b))
	require.NoError(t, err)
	require.Equal(t, int64(40), validated.VotedPower, "every member still in the group has voted")
	require.Equal(t, int64(34), validated.RequiredPower, "floor TotalWeight/3+1")

	escrow := sdk.AccAddress([]byte("bridge_escrow_______"))
	mocks.AccountKeeper.EXPECT().GetModuleAddress(types.BridgeEscrowAccName).Return(escrow).AnyTimes()
	mocks.BankViewKeeper.EXPECT().SpendableCoin(gomock.Any(), escrow, types.BaseCoin).Return(sdk.NewInt64Coin(types.BaseCoin, 1000)).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName,
		sdk.MustAccAddressFromBech32(recipient), sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 100)), "bridge_release").Return(nil).Times(1)
	require.NoError(t, vote(b))

	tx, found := k.GetBridgeTransactionByContent(ctx, validated.ExistingTx)
	require.True(t, found)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_COMPLETED, tx.Status)
}

// A member that voted and was then removed no longer counts: the record must
// not complete on its stored vote plus one small live vote.
func TestBridgeExchange_DropsVoteOfMemberRemovedAfterVoting(t *testing.T) {
	k, ms, ctx, mocks := setupKeeperWithMocks(t)

	a := testutil.Validator
	b := testutil.Validator2
	removed := sdk.AccAddress([]byte("removed_member______")).String()
	epochIndex := uint64(1)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epochIndex))
	k.SetEpochGroupData(ctx, types.EpochGroupData{
		EpochIndex: epochIndex, EpochGroupId: 1, TotalWeight: 100,
		ValidationWeights: []*types.ValidationWeight{
			{MemberAddress: a, Weight: 20}, {MemberAddress: b, Weight: 20}, {MemberAddress: removed, Weight: 60},
		},
	})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      epochIndex,
		Participants: []*types.ActiveParticipant{{Index: a}, {Index: b}, {Index: removed}},
	}))
	for _, v := range []string{a, b, removed} {
		mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), sdk.MustAccAddressFromBech32(v)).Return(true).AnyTimes()
	}

	members := []*group.GroupMember{
		{GroupId: 1, Member: &group.Member{Address: a, Weight: "20"}},
		{GroupId: 1, Member: &group.Member{Address: b, Weight: "20"}},
		{GroupId: 1, Member: &group.Member{Address: removed, Weight: "60"}},
	}
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).DoAndReturn(
		func(_ any, _ any) (*group.QueryGroupMembersResponse, error) {
			return &group.QueryGroupMembersResponse{Members: members}, nil
		},
	).AnyTimes()

	recipient := sdk.AccAddress([]byte("bridge_recipient____")).String()
	k.SetBridgeContractAddress(ctx, types.BridgeContractAddress{Id: "1", ChainId: "ethereum", Address: "0x123"})
	newMsg := func(v string) *types.MsgBridgeExchange {
		return &types.MsgBridgeExchange{
			OriginChain: "ethereum", ContractAddress: "0x123", OwnerAddress: recipient,
			Amount: "100", BlockNumber: "1000", ReceiptIndex: "1", Validator: v,
		}
	}
	vote := func(v string) error {
		_, err := ms.BridgeExchange(ctx, newMsg(v))
		return err
	}
	require.NoError(t, vote(removed))

	mocks.GroupKeeper.EXPECT().UpdateGroupMembers(gomock.Any(), gomock.Any()).Return(&group.MsgUpdateGroupMembersResponse{}, nil).AnyTimes()
	mocks.GroupKeeper.EXPECT().UpdateGroupMetadata(gomock.Any(), gomock.Any()).Return(&group.MsgUpdateGroupMetadataResponse{}, nil).AnyTimes()
	eg, err := k.GetEpochGroup(ctx, epochIndex, "")
	require.NoError(t, err)
	require.NoError(t, eg.RemoveMember(ctx, &types.Participant{Address: removed}))
	members = members[:2]

	validated, err := k.ValidateBridgeExchange(ctx, newMsg(a))
	require.NoError(t, err)
	require.Equal(t, int64(20), validated.VotedPower, "the removed member's 60 no longer counts")
	require.Equal(t, int64(34), validated.RequiredPower)
	require.NoError(t, vote(a))
	tx, found := k.GetBridgeTransactionByContent(ctx, validated.ExistingTx)
	require.True(t, found)
	require.NotEqual(t, types.BridgeTransactionStatus_BRIDGE_COMPLETED, tx.Status)

	escrow := sdk.AccAddress([]byte("bridge_escrow_______"))
	mocks.AccountKeeper.EXPECT().GetModuleAddress(types.BridgeEscrowAccName).Return(escrow).AnyTimes()
	mocks.BankViewKeeper.EXPECT().SpendableCoin(gomock.Any(), escrow, types.BaseCoin).Return(sdk.NewInt64Coin(types.BaseCoin, 1000)).AnyTimes()
	mocks.BankKeeper.EXPECT().SendCoinsFromModuleToAccount(gomock.Any(), types.BridgeEscrowAccName,
		sdk.MustAccAddressFromBech32(recipient), sdk.NewCoins(sdk.NewInt64Coin(types.BaseCoin, 100)), "bridge_release").Return(nil).Times(1)
	require.NoError(t, vote(b))
	tx, found = k.GetBridgeTransactionByContent(ctx, validated.ExistingTx)
	require.True(t, found)
	require.Equal(t, types.BridgeTransactionStatus_BRIDGE_COMPLETED, tx.Status)
}
