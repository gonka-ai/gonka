package keeper_test

import (
	"context"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestMsgServer_FinishInference(t *testing.T) {
	const (
		epochId  = 1
		epochId2 = 2

		inferenceId = "inferenceId"
	)
	k, ms, ctx := setupMsgServer(t)
	k.SetEpochGroupData(ctx, types.EpochGroupData{EpochGroupId: epochId})

	MustAddParticipant(t, ms, ctx, testutil.Requester)
	MustAddParticipant(t, ms, ctx, testutil.Creator)
	MustAddParticipant(t, ms, ctx, testutil.Executor)

	_, err := ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   inferenceId,
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
		Model:         "model1",
	})
	require.NoError(t, err)
	savedInference, found := k.GetInference(ctx, inferenceId)

	ctx2 := sdk.UnwrapSDKContext(ctx)
	require.True(t, found)
	expectedInference := types.Inference{
		Index:               inferenceId,
		InferenceId:         inferenceId,
		PromptHash:          "promptHash",
		PromptPayload:       "promptPayload",
		RequestedBy:         testutil.Requester,
		Status:              types.InferenceStatus_STARTED,
		Model:               "model1",
		StartBlockTimestamp: ctx2.BlockTime().UnixMilli(),
		MaxTokens:           keeper.DefaultMaxTokens,
		EscrowAmount:        keeper.DefaultMaxTokens * keeper.PerTokenCost,
	}
	require.Equal(t, expectedInference, savedInference)

	devStat, found := k.DevelopersStatsGetByEpoch(ctx, testutil.Requester, epochId)
	require.True(t, found)
	/*	require.Equal(t, types.DeveloperStatsByEpoch{
		EpochId: epochId,
		Inferences: map[string]*types.InferenceStats{
			savedInference.InferenceId: {
				InferenceId:         expectedInference.InferenceId,
				EpochPocBlockHeight: savedInference.EpochGroupId,
				Status:              expectedInference.Status,
				AiTokensUsed:        savedInference.PromptTokenCount + savedInference.CompletionTokenCount,
				Model:               expectedInference.Model,
				ActualConstInCoins:  savedInference.ActualCost,
			},
		},
	}, devStat)*/

	require.Equal(t, types.DeveloperStatsByEpoch{
		EpochId:      epochId,
		InferenceIds: []string{expectedInference.InferenceId},
	}, devStat)
	k.SetEffectiveEpochGroupId(ctx, epochId2)
	k.SetEpochGroupData(ctx, types.EpochGroupData{EpochGroupId: epochId2, PocStartBlockHeight: epochId2})

	_, err = ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId:          inferenceId,
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           testutil.Executor,
	})
	require.NoError(t, err)
	savedInference, found = k.GetInference(ctx, inferenceId)
	require.True(t, found)

	expectedInference2 := types.Inference{
		Index:                inferenceId,
		InferenceId:          inferenceId,
		PromptHash:           "promptHash",
		PromptPayload:        "promptPayload",
		RequestedBy:          testutil.Requester,
		Status:               types.InferenceStatus_FINISHED,
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		EpochGroupId:         epochId2,
		ExecutedBy:           testutil.Executor,
		Model:                "model1",
		StartBlockTimestamp:  ctx2.BlockTime().UnixMilli(),
		EndBlockTimestamp:    ctx2.BlockTime().UnixMilli(),
		MaxTokens:            keeper.DefaultMaxTokens,
		EscrowAmount:         keeper.DefaultMaxTokens * keeper.PerTokenCost,
		ActualCost:           30 * keeper.PerTokenCost,
	}
	require.Equal(t, expectedInference2, savedInference)

	participantState, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	require.Equal(t, types.Participant{
		Index:             testutil.Executor,
		Address:           testutil.Executor,
		Weight:            -1,
		JoinTime:          ctx2.BlockTime().UnixMilli(),
		JoinHeight:        ctx2.BlockHeight(),
		LastInferenceTime: ctx2.BlockTime().UnixMilli(),
		InferenceUrl:      "url",
		Status:            types.ParticipantStatus_ACTIVE,
		CoinBalance:       30 * keeper.PerTokenCost,
		CurrentEpochStats: &types.CurrentEpochStats{
			InferenceCount: 1,
			EarnedCoins:    30 * keeper.PerTokenCost,
		},
	}, participantState)

	devStat, found = k.DevelopersStatsGetByEpoch(ctx, testutil.Requester, epochId2)
	require.True(t, found)
	require.Equal(t, 1, len(devStat.InferenceIds))

	devStatUpdated, found := k.DevelopersStatsGetByEpoch(ctx, testutil.Requester, epochId2)
	require.True(t, found)
	/*	require.Equal(t, types.DeveloperStatsByEpoch{
		EpochId: epochId2,
		Inferences: map[string]*types.InferenceStats{
			savedInference.InferenceId: {
				InferenceId:         expectedInference2.InferenceId,
				EpochPocBlockHeight: expectedInference2.EpochGroupId,
				Status:              savedInference.Status,
				AiTokensUsed:        savedInference.PromptTokenCount + savedInference.CompletionTokenCount,
				Model:               expectedInference2.Model,
				ActualConstInCoins:  expectedInference2.ActualCost,
			},
		},
	}, devStatUpdated)*/

	require.Equal(t, types.DeveloperStatsByEpoch{
		EpochId:      epochId2,
		InferenceIds: []string{expectedInference2.InferenceId}}, devStatUpdated)
}

func MustAddParticipant(t *testing.T, ms types.MsgServer, ctx context.Context, address string) {
	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: address,
		Url:     "url",
	})
	require.NoError(t, err)
}

func TestMsgServer_FinishInference_InferenceNotFound(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	_, err := ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId:          "inferenceId",
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     1,
		CompletionTokenCount: 1,
		ExecutedBy:           testutil.Executor,
	})
	require.Error(t, err)
	_, found := k.GetInference(ctx, "inferenceId")
	require.False(t, found)
}
