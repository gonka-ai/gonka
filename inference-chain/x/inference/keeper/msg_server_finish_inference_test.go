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
	k, ms, ctx := setupMsgServer(t)
	MustAddParticipant(t, ms, ctx, testutil.Requester)
	MustAddParticipant(t, ms, ctx, testutil.Creator)
	MustAddParticipant(t, ms, ctx, testutil.Executor)
	_, err := ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
		Model:         "model1",
	})
	require.NoError(t, err)
	savedInference, found := k.GetInference(ctx, "inferenceId")
	ctx2 := sdk.UnwrapSDKContext(ctx)
	require.True(t, found)
	require.Equal(t, types.Inference{
		Index:               "inferenceId",
		InferenceId:         "inferenceId",
		PromptHash:          "promptHash",
		PromptPayload:       "promptPayload",
		RequestedBy:         testutil.Requester,
		Status:              types.InferenceStatus_STARTED,
		Model:               "model1",
		StartBlockTimestamp: ctx2.BlockTime().UnixMilli(),
		MaxTokens:           keeper.DefaultMaxTokens,
		EscrowAmount:        keeper.DefaultMaxTokens * keeper.PerTokenCost,
	}, savedInference)
	// require that
	_, err = ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId:          "inferenceId",
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           testutil.Executor,
	})
	require.NoError(t, err)
	savedInference, found = k.GetInference(ctx, "inferenceId")
	require.True(t, found)
	require.Equal(t, types.Inference{
		Index:                "inferenceId",
		InferenceId:          "inferenceId",
		PromptHash:           "promptHash",
		PromptPayload:        "promptPayload",
		RequestedBy:          testutil.Requester,
		Status:               types.InferenceStatus_FINISHED,
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           testutil.Executor,
		Model:                "model1",
		StartBlockTimestamp:  ctx2.BlockTime().UnixMilli(),
		EndBlockTimestamp:    ctx2.BlockTime().UnixMilli(),
		MaxTokens:            keeper.DefaultMaxTokens,
		EscrowAmount:         keeper.DefaultMaxTokens * keeper.PerTokenCost,
		ActualCost:           30 * keeper.PerTokenCost,
	}, savedInference)

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
		Models:            []string{"model1", "model2"},
		Status:            types.ParticipantStatus_ACTIVE,
		CoinBalance:       30 * keeper.PerTokenCost,
		InferenceCount:    uint64(1),
	}, participantState)
}

func MustAddParticipant(t *testing.T, ms types.MsgServer, ctx context.Context, address string) {
	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: address,
		Url:     "url",
		Models:  []string{"model1", "model2"},
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
