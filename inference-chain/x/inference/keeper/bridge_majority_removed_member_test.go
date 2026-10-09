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
func TestBridgeExchange_MajorityUnreachableAfterMemberRemoval(t *testing.T) {
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

	vote := func(v string) error {
		_, err := ms.BridgeExchange(ctx, &types.MsgBridgeExchange{
			OriginChain: "ethereum", ContractAddress: "0x123", OwnerAddress: "0xabc",
			Amount: "100", BlockNumber: "1000", ReceiptIndex: "1", Validator: v,
		})
		return err
	}
	require.NoError(t, vote(a))
	require.ErrorIs(t, vote(removed), types.ErrBridgeValidatorNotInTxEpochGroup)

	// The last member still in the group votes: does that vote reach the majority?
	msgB := &types.MsgBridgeExchange{
		OriginChain: "ethereum", ContractAddress: "0x123", OwnerAddress: "0xabc",
		Amount: "100", BlockNumber: "1000", ReceiptIndex: "1", Validator: b,
	}
	validated, err := k.ValidateBridgeExchange(ctx, msgB)
	require.NoError(t, err)
	require.False(t, validated.IsCreate)
	power := validated.ExistingTx.TotalValidationPower + validated.ValidatorPower
	require.Equal(t, int64(40), power, "every member still in the group has voted")
	require.GreaterOrEqual(t, power, validated.TotalEpochPower/2+1,
		"all remaining weight voted, yet the record needs a majority of the epoch-start weight")
}
