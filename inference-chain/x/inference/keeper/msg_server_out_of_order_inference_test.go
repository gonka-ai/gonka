package keeper_test

import (
	"github.com/productscience/inference/x/inference/calculations"
	"testing"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestMsgServer_OutOfOrderInference(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)

	// Add participants directly
	_, err := ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: testutil.Requester,
		Url:     "url",
	})
	require.NoError(t, err)

	_, err = ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: testutil.Creator,
		Url:     "url",
	})
	require.NoError(t, err)

	_, err = ms.SubmitNewParticipant(ctx, &types.MsgSubmitNewParticipant{
		Creator: testutil.Executor,
		Url:     "url",
	})
	require.NoError(t, err)

	// First, try to finish an inference that hasn't been started yet
	// With our fix, this should now succeed
	_, err = ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId:          "inferenceId",
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           testutil.Executor,
	})
	require.NoError(t, err) // Now this should succeed

	// Verify the inference was created with FINISHED status
	savedInference, found := k.GetInference(ctx, "inferenceId")
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_FINISHED, savedInference.Status)
	require.Equal(t, "responseHash", savedInference.ResponseHash)
	require.Equal(t, "responsePayload", savedInference.ResponsePayload)
	require.Equal(t, uint64(10), savedInference.PromptTokenCount)
	require.Equal(t, uint64(20), savedInference.CompletionTokenCount)
	require.Equal(t, testutil.Executor, savedInference.ExecutedBy)

	// Now start the inference
	_, err = ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
		Model:         "model1",
	})
	require.NoError(t, err)

	// Verify the inference was updated correctly
	// It should still be in FINISHED state, but now have the start information as well
	savedInference, found = k.GetInference(ctx, "inferenceId")
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_FINISHED, savedInference.Status)
	require.Equal(t, "promptHash", savedInference.PromptHash)
	require.Equal(t, "promptPayload", savedInference.PromptPayload)
	require.Equal(t, testutil.Requester, savedInference.RequestedBy)
	require.Equal(t, "model1", savedInference.Model)

	// The finish information should still be there
	require.Equal(t, "responseHash", savedInference.ResponseHash)
	require.Equal(t, "responsePayload", savedInference.ResponsePayload)
	require.Equal(t, uint64(10), savedInference.PromptTokenCount)
	require.Equal(t, uint64(20), savedInference.CompletionTokenCount)
	require.Equal(t, testutil.Executor, savedInference.ExecutedBy)

	// Verify that the escrow amount is based on the actual token counts, not the MaxTokens
	// The actual cost should be (10 + 20) * PerTokenCost = 30 * PerTokenCost
	expectedActualCost := int64(30 * calculations.PerTokenCost)
	require.Equal(t, expectedActualCost, savedInference.ActualCost)

	// The escrow amount should be the same as the actual cost
	require.Equal(t, expectedActualCost, savedInference.EscrowAmount)
}
