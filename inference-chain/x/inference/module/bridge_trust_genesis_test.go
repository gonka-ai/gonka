package inference_test

import (
	"testing"

	"cosmossdk.io/math"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
)

func TestGenesisEpoch_BridgeVotesRejectedAndUpgradeStepsNoop(t *testing.T) {
	validators := []stakingtypes.Validator{
		{OperatorAddress: validatorOperatorAddress1, ConsensusPubkey: &codectypes.Any{}, Tokens: math.NewInt(100)},
		{OperatorAddress: validatorOperatorAddress2, ConsensusPubkey: &codectypes.Any{}, Tokens: math.NewInt(201)},
	}
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	mocks.StubForInitGenesisWithValidators(ctx, validators)
	inference.InitGenesis(ctx, k, mocks.StubGenesisState())

	members := make([]*group.GroupMember, 0, len(validators))
	voters := make([]sdk.AccAddress, 0, len(validators))
	for _, v := range validators {
		address, err := utils.OperatorAddressToAccAddress(v.OperatorAddress)
		require.NoError(t, err)
		members = append(members, &group.GroupMember{
			GroupId: 1,
			Member:  &group.Member{Address: address, Weight: v.Tokens.String()},
		})
		voter := sdk.MustAccAddressFromBech32(address)
		voters = append(voters, voter)
		mocks.AccountKeeper.EXPECT().HasAccount(gomock.Any(), voter).Return(true).AnyTimes()
	}
	mocks.GroupKeeper.EXPECT().GroupMembers(gomock.Any(), gomock.Any()).Return(
		&group.QueryGroupMembersResponse{Members: members}, nil,
	).AnyTimes()

	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	require.True(t, found)
	require.Equal(t, uint64(0), epochIndex)
	_, found = k.GetActiveParticipants(ctx, 0)
	require.False(t, found)

	_, err := k.GetEpochTrust(ctx, 0)
	require.ErrorIs(t, err, types.ErrActiveParticipantNotFound)

	ms := keeper.NewMsgServerImpl(k)
	for _, voter := range voters {
		msg := &types.MsgBridgeExchange{
			Validator:       voter.String(),
			OriginChain:     "ethereum",
			ContractAddress: "0x972a7a92d92796a98801a8818bcf91f1648f2f68",
			OwnerAddress:    testutil.Requester,
			Amount:          "100",
			BlockNumber:     "1000",
			ReceiptIndex:    "1",
			ReceiptsRoot:    "0xroot",
		}
		_, err := k.ValidateBridgeExchange(ctx, msg)
		require.ErrorIs(t, err, types.ErrActiveParticipantNotFound)
		_, err = ms.BridgeExchange(ctx, msg)
		require.ErrorIs(t, err, types.ErrActiveParticipantNotFound)
	}
	require.Empty(t, k.GetBridgeTransactionsByReceipt(ctx, "ethereum", "1000", "1"))

	filled, err := k.BackfillEpochTrustWeights(ctx)
	require.NoError(t, err)
	require.Equal(t, 0, filled)
	migrated, err := k.MigrateBridgeTrustPower(ctx)
	require.NoError(t, err)
	require.Equal(t, keeper.BridgeTrustMigrationResult{}, migrated)
}
