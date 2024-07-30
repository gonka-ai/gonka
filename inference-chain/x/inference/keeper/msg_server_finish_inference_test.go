package keeper_test

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestMsgServer_FinishInference(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: "receivedBy",
		Url:     "url",
		Models:  []string{"model1", "model2"},
	})
	require.NoError(t, err)
	_, err = ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		ReceivedBy:    "receivedBy",
		Creator:       "receivedBy",
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
		ReceivedBy:          "receivedBy",
		Status:              "STARTED",
		Model:               "model1",
		StartBlockTimestamp: ctx2.BlockTime().UnixMilli(),
	}, savedInference)
	// require that
	_, err = ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId:          "inferenceId",
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           "receivedBy",
	})
	require.NoError(t, err)
	savedInference, found = k.GetInference(ctx, "inferenceId")
	require.True(t, found)
	require.Equal(t, types.Inference{
		Index:                "inferenceId",
		InferenceId:          "inferenceId",
		PromptHash:           "promptHash",
		PromptPayload:        "promptPayload",
		ReceivedBy:           "receivedBy",
		Status:               "FINISHED",
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           "receivedBy",
		Model:                "model1",
		StartBlockTimestamp:  ctx2.BlockTime().UnixMilli(),
		EndBlockTimestamp:    ctx2.BlockTime().UnixMilli(),
	}, savedInference)

	participantState, found := k.GetParticipant(ctx, "receivedBy")
	require.True(t, found)
	require.Equal(t, types.Participant{
		Index:             "receivedBy",
		Address:           "receivedBy",
		Reputation:        1,
		Weight:            1,
		JoinTime:          ctx2.BlockTime().UnixMilli(),
		JoinHeight:        ctx2.BlockHeight(),
		LastInferenceTime: ctx2.BlockTime().UnixMilli(),
		InferenceUrl:      "url",
		Models:            []string{"model1", "model2"},
		Status:            types.ParticipantStatus_ACTIVE,
		PromptTokenCount: map[string]uint64{
			"model1": 10,
			"model2": 0,
		},
		CompletionTokenCount: map[string]uint64{
			"model1": 20,
			"model2": 0,
		},
	}, participantState)
}

func TestMsgServer_FinishInference_InferenceNotFound(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	_, err := ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId:          "inferenceId",
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     1,
		CompletionTokenCount: 1,
		ExecutedBy:           "executedBy",
	})
	require.Error(t, err)
	_, found := k.GetInference(ctx, "inferenceId")
	require.False(t, found)
}
