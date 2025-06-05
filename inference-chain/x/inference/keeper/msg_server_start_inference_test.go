package keeper_test

import (
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestMsgServer_StartInferenceWithUnregesteredParticipant(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)
	_, err := ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
	})
	require.Error(t, err)
}

func TestMsgServer_StartInference(t *testing.T) {
	const epochId = 1
	k, ms, ctx, mocks := setupKeeperWithMocks(t)
	k.SetEffectiveEpochGroupId(ctx, epochId)

	sdkCtx := sdk.UnwrapSDKContext(ctx)
	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: testutil.Creator,
		Url:     "url",
	})
	require.NoError(t, err)
	_, err = ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: testutil.Requester,
		Url:     "url",
	})
	mocks.BankKeeper.ExpectPay(sdkCtx, testutil.Requester, keeper.DefaultMaxTokens*keeper.PerTokenCost)
	require.NoError(t, err)
	_, err = ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
	})
	require.NoError(t, err)
	savedInference, found := k.GetInference(ctx, "inferenceId")
	require.True(t, found)
	ctx2 := sdk.UnwrapSDKContext(ctx)
	require.Equal(t, types.Inference{
		Index:               "inferenceId",
		InferenceId:         "inferenceId",
		PromptHash:          "promptHash",
		PromptPayload:       "promptPayload",
		RequestedBy:         testutil.Requester,
		Status:              types.InferenceStatus_STARTED,
		StartBlockHeight:    0,
		StartBlockTimestamp: ctx2.BlockTime().UnixMilli(),
		MaxTokens:           keeper.DefaultMaxTokens,
		EscrowAmount:        keeper.DefaultMaxTokens * keeper.PerTokenCost,
	}, savedInference)

	devStat, found := k.DevelopersStatsGetByEpoch(ctx2, savedInference.RequestedBy, epochId)
	require.True(t, found)

	require.Equal(t, types.DeveloperStatsByEpoch{
		EpochId: epochId,
		Inferences: map[string]*types.InferenceStats{
			savedInference.InferenceId: {
				Status:       savedInference.Status,
				AiTokensUsed: 0,
			},
		},
	}, devStat)
}

// TODO: Need a way to test that blockheight is set to newer values, but can't figure out how to change the
// test value of the blockheight
