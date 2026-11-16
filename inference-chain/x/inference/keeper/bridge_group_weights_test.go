package keeper_test

import (
	"errors"
	"strconv"
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const weightsEpoch = uint64(335)

func setupBridgeWeights(t *testing.T) (keeper.Keeper, sdk.Context, keepertest.InferenceMocks, []*group.GroupMember) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, weightsEpoch))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: weightsEpoch, PocStartBlockHeight: 1}))
	addrs := []string{testutil.Validator, testutil.Validator2, testutil.Creator}
	weights := []int64{40, 35, 25}
	data := types.EpochGroupData{EpochIndex: weightsEpoch, EpochGroupId: 1}
	var members []*group.GroupMember
	for i, a := range addrs {
		data.ValidationWeights = append(data.ValidationWeights, &types.ValidationWeight{MemberAddress: a, Weight: weights[i]})
		data.TotalWeight += weights[i]
		members = append(members, &group.GroupMember{GroupId: 1, Member: &group.Member{Address: a, Weight: strconv.FormatInt(weights[i], 10)}})
	}
	k.SetEpochGroupData(ctx, data)
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:      weightsEpoch,
		Participants: []*types.ActiveParticipant{{Index: testutil.Validator}, {Index: testutil.Validator2}, {Index: testutil.Creator}},
	}))
	mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), gomock.Any()).Return(true).AnyTimes()
	return k, ctx, mocks, members
}

func weightsMsg(voter string) *types.MsgBridgeExchange {
	return &types.MsgBridgeExchange{
		Validator: voter, OriginChain: "ethereum", ContractAddress: "0xabc", OwnerAddress: "gonka1owner",
		Amount: "100", BlockNumber: "1000", ReceiptIndex: "1", ReceiptsRoot: "0xroot",
	}
}

func weightsPendingTx(k keeper.Keeper, ctx sdk.Context) {
	k.SetBridgeTransaction(ctx, &types.BridgeTransaction{
		ChainId: "ethereum", ContractAddress: "0xabc", OwnerAddress: "gonka1owner", Amount: "100",
		BlockNumber: "1000", ReceiptIndex: "1", ReceiptsRoot: "0xroot", EpochIndex: weightsEpoch,
		Status: types.BridgeTransactionStatus_BRIDGE_PENDING, TotalValidationPower: 35,
	})
}

// validateBoth runs the same message with x/group consistent with the epoch data
// (no member list allowed) and with GroupInfo unavailable (member list path).
func validateBoth(t *testing.T, existing bool, voter string) (*keeper.ValidatedBridgeExchange, *keeper.ValidatedBridgeExchange) {
	k, ctx, mocks, members := setupBridgeWeights(t)
	if existing {
		weightsPendingTx(k, ctx)
	}
	mocks.GroupKeeper.EXPECT().GroupInfo(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupInfoResponse{Info: &group.GroupInfo{Id: 1, TotalWeight: "100"}}, nil).AnyTimes()
	fast, err := k.ValidateBridgeExchange(ctx, weightsMsg(voter))
	require.NoError(t, err)

	k2, ctx2, mocks2, _ := setupBridgeWeights(t)
	if existing {
		weightsPendingTx(k2, ctx2)
	}
	mocks2.GroupKeeper.EXPECT().GroupInfo(gomock.Any(), gomock.Any()).Return(nil, errors.New("unavailable")).AnyTimes()
	mocks2.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{Members: members}, nil).Times(1)
	slow, err := k2.ValidateBridgeExchange(ctx2, weightsMsg(voter))
	require.NoError(t, err)
	return fast, slow
}

func TestBridgeGroupWeights_VoteSkipsMemberListAndMatchesIt(t *testing.T) {
	fast, slow := validateBoth(t, true, testutil.Validator)
	require.Equal(t, slow, fast)
	require.Equal(t, int64(40), fast.ValidatorPower)
	require.Equal(t, int64(100), fast.TotalEpochPower)
	require.Equal(t, int64(75), fast.VotedPower)
	require.Equal(t, int64(51), fast.RequiredPower)
}

func TestBridgeGroupWeights_CreateSkipsMemberListAndMatchesIt(t *testing.T) {
	fast, slow := validateBoth(t, false, testutil.Validator2)
	require.Equal(t, slow, fast)
	require.True(t, fast.IsCreate)
	require.Equal(t, int64(35), fast.ValidatorPower)
	require.Equal(t, int64(100), fast.TotalEpochPower)
}

func TestBridgeGroupWeights_ExcludedVoterUsesMemberList(t *testing.T) {
	k, ctx, mocks, members := setupBridgeWeights(t)
	weightsPendingTx(k, ctx)
	addr := sdk.MustAccAddressFromBech32(testutil.Validator)
	require.NoError(t, k.ExcludedParticipantsMap.Set(ctx, collections.Join(weightsEpoch, addr),
		types.ExcludedParticipant{Address: testutil.Validator, EpochIndex: weightsEpoch}))
	mocks.GroupKeeper.EXPECT().GroupInfo(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupInfoResponse{Info: &group.GroupInfo{Id: 1, TotalWeight: "100"}}, nil).AnyTimes()
	// x/group no longer lists the voter.
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{Members: members[1:]}, nil).Times(1)
	_, err := k.ValidateBridgeExchange(ctx, weightsMsg(testutil.Validator))
	require.ErrorIs(t, err, types.ErrBridgeValidatorNotInTxEpochGroup)
}

func TestBridgeGroupWeights_MemberLeftUsesMemberList(t *testing.T) {
	k, ctx, mocks, members := setupBridgeWeights(t)
	weightsPendingTx(k, ctx)
	mocks.GroupKeeper.EXPECT().GroupInfo(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupInfoResponse{Info: &group.GroupInfo{Id: 1, TotalWeight: "75"}}, nil).AnyTimes()
	// Listed once, and the same list drops the removed member's vote.
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{Members: members[:2]}, nil).Times(1)
	validated, err := k.ValidateBridgeExchange(ctx, weightsMsg(testutil.Validator))
	require.NoError(t, err)
	require.Equal(t, int64(75), validated.TotalEpochPower)
	require.Equal(t, int64(40), validated.ValidatorPower)
}
