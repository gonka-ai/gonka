package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestMsgServer_FinishInference(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	_, err := ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		ReceivedBy:    "receivedBy",
	})
	require.NoError(t, err)
	savedInference, found := k.GetInference(ctx, "inferenceId")
	require.True(t, found)
	require.Equal(t, types.Inference{
		Index:         "inferenceId",
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		ReceivedBy:    "receivedBy",
		Status:        "STARTED",
	}, savedInference)
	// require that
	_, err = ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId:          "inferenceId",
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     1,
		CompletionTokenCount: 1,
		ExecutedBy:           "executedBy",
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
		PromptTokenCount:     1,
		CompletionTokenCount: 1,
		ExecutedBy:           "executedBy",
	}, savedInference)
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
