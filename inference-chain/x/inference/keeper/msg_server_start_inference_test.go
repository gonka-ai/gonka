package keeper_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/x/inference/types"
)

func TestMsgServer_StartInference(t *testing.T) {
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
}
