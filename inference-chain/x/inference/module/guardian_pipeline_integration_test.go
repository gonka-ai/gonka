package inference_test

import (
	"encoding/base64"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	stakingkeeper "github.com/cosmos/cosmos-sdk/x/staking/keeper"
	stakingtypes "github.com/cosmos/cosmos-sdk/x/staking/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
)

func TestEndBlockCapsBeforeGuardianEnhancement(t *testing.T) {
	k, ctx, mocks := keepertest.InferenceKeeperReturningMocks(t)
	ctx = ctx.WithBlockHeight(101)

	guardianAccount := sdk.MustAccAddressFromBech32(testutil.Validator2)
	guardianOperator := sdk.ValAddress(guardianAccount).String()

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.GenesisGuardianParams = &types.GenesisGuardianParams{
		NetworkMaturityThreshold: 500,
		NetworkMaturityMinHeight: 0,
		GuardianAddresses:        []string{guardianOperator},
	}
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, k.SetGenesisOnlyParams(ctx, &types.GenesisOnlyParams{
		TotalSupply:               1_000_000_000,
		OriginatorSupply:          160_000_000,
		PreProgrammedSaleAmount:   120_000_000,
		SupplyDenom:               "gonka",
		GenesisGuardianMultiplier: types.DecimalFromFloat(0.52),
		GenesisGuardianEnabled:    true,
	}))

	const epoch = uint64(1)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, epoch))
	require.NoError(t, k.SetEpoch(ctx, &types.Epoch{Index: epoch, PocStartBlockHeight: 100}))
	k.SetEpochGroupData(ctx, types.EpochGroupData{EpochIndex: epoch, EpochGroupId: epoch})
	require.NoError(t, k.SetActiveParticipants(ctx, types.ActiveParticipants{
		EpochId:          epoch,
		EpochGroupId:     epoch,
		CapWeightApplied: true,
		Participants: []*types.ActiveParticipant{
			{Index: testutil.Validator, Weight: 1_000, CapWeight: 90},
			{Index: testutil.Validator2, Weight: 1_000, CapWeight: 10},
		},
	}))

	validatorKey := base64.StdEncoding.EncodeToString(make([]byte, 32))
	mocks.GroupKeeper.EXPECT().
		GroupInfo(gomock.Any(), gomock.Any()).
		Return(&group.QueryGroupInfoResponse{Info: &group.GroupInfo{Id: epoch, Metadata: "changed"}}, nil)
	mocks.GroupKeeper.EXPECT().
		GroupMembers(gomock.Any(), gomock.Any()).
		Return(&group.QueryGroupMembersResponse{Members: []*group.GroupMember{
			{Member: &group.Member{Address: testutil.Validator, Weight: "1000", Metadata: validatorKey}},
			{Member: &group.Member{Address: testutil.Validator2, Weight: "1000", Metadata: validatorKey}},
		}}, nil)
	mocks.GroupKeeper.EXPECT().
		UpdateGroupMetadata(gomock.Any(), gomock.Any()).
		Return(&group.MsgUpdateGroupMetadataResponse{}, nil)

	mocks.StakingKeeper.EXPECT().
		SetComputeValidators(gomock.Any(), gomock.Any(), gomock.Any()).
		DoAndReturn(func(_ sdk.Context, results []stakingkeeper.ComputeResult, _ bool) ([]stakingtypes.Validator, error) {
			powerByOperator := make(map[string]int64, len(results))
			for _, result := range results {
				powerByOperator[result.OperatorAddress] = result.Power
			}
			require.Equal(t, int64(90), powerByOperator[sdk.ValAddress(sdk.MustAccAddressFromBech32(testutil.Validator)).String()])
			require.Equal(t, int64(46), powerByOperator[guardianOperator])
			return nil, nil
		})

	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)
	require.NoError(t, am.EndBlock(ctx))
}
