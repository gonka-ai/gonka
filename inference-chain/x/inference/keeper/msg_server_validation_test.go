package keeper_test

import (
	"context"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"testing"
)

const INFERENCE_ID = "inferenceId"

func TestMsgServer_Validation(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	createParticipants(t, ms, ctx)
	createCompletedInference(t, ms, ctx)
	_, err := ms.Validation(ctx, &types.MsgValidation{
		InferenceId: INFERENCE_ID,
		Creator:     testutil.Validator,
		Value:       0.9999,
	})
	require.NoError(t, err)
	inference, found := k.GetInference(ctx, INFERENCE_ID)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_VALIDATED, inference.Status)
}

func createParticipants(t *testing.T, ms types.MsgServer, ctx context.Context) {
	MustAddParticipant(t, ms, ctx, testutil.Requester)
	MustAddParticipant(t, ms, ctx, testutil.Executor)
	MustAddParticipant(t, ms, ctx, testutil.Validator)
	MustAddParticipant(t, ms, ctx, testutil.Creator)
}

func TestMsgServer_Validation_Invalidate(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	createParticipants(t, ms, ctx)
	createCompletedInference(t, ms, ctx)
	_, err := ms.Validation(ctx, &types.MsgValidation{
		InferenceId: INFERENCE_ID,
		Creator:     testutil.Validator,
		Value:       0.80,
	})
	require.NoError(t, err)
	inference, found := k.GetInference(ctx, INFERENCE_ID)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_INVALIDATED, inference.Status)
}

func TestMsgServer_NoInference(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)
	createParticipants(t, ms, ctx)
	_, err := ms.Validation(ctx, &types.MsgValidation{
		InferenceId: INFERENCE_ID,
		Creator:     testutil.Validator,
		Value:       0.9999,
	})
	require.Error(t, err)
}

func TestMsgServer_NotFinished(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)
	createParticipants(t, ms, ctx)
	_, err := ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   INFERENCE_ID,
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		ReceivedBy:    testutil.Requester,
		Creator:       testutil.Creator,
		Model:         "model1",
	})
	require.NoError(t, err)
	_, err = ms.Validation(ctx, &types.MsgValidation{
		InferenceId: INFERENCE_ID,
		Creator:     testutil.Validator,
		Value:       0.9999,
	})
	require.Error(t, err)
}

func TestMsgServer_InvalidExecutor(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)
	MustAddParticipant(t, ms, ctx, testutil.Validator)
	_, err := ms.Validation(ctx, &types.MsgValidation{
		InferenceId: INFERENCE_ID,
		Creator:     testutil.Executor,
		Value:       0.9999,
	})
	require.Error(t, err)
}

func TestMsgServer_ValidatorCannotBeExecutor(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)
	createParticipants(t, ms, ctx)
	_, err := ms.Validation(ctx, &types.MsgValidation{
		InferenceId: INFERENCE_ID,
		Creator:     testutil.Validator,
		Value:       0.9999,
	})
	require.Error(t, err)
}

func createCompletedInference(t *testing.T, ms types.MsgServer, ctx context.Context) {
	_, err := ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		ReceivedBy:    testutil.Requester,
		Creator:       testutil.Creator,
		Model:         "model1",
	})
	require.NoError(t, err)
	_, err = ms.FinishInference(ctx, &types.MsgFinishInference{
		InferenceId:          "inferenceId",
		ResponseHash:         "responseHash",
		ResponsePayload:      "responsePayload",
		PromptTokenCount:     10,
		CompletionTokenCount: 20,
		ExecutedBy:           testutil.Executor,
	})
	require.NoError(t, err)
}

func TestZScoreCalculator(t *testing.T) {
	// Separately calculate values to confirm results
	equal := keeper.CalculateZScoreFromFPR(0.05, 95, 5)
	require.Equal(t, 0.0, equal)

	negative := keeper.CalculateZScoreFromFPR(0.05, 96, 4)
	require.InDelta(t, -0.458831, negative, 0.00001)

	positive := keeper.CalculateZScoreFromFPR(0.05, 94, 6)
	require.InDelta(t, 0.458831, positive, 0.00001)

	bigNegative := keeper.CalculateZScoreFromFPR(0.05, 960, 40)
	require.InDelta(t, -1.450953, bigNegative, 0.00001)

	bigPositive := keeper.CalculateZScoreFromFPR(0.05, 940, 60)
	require.InDelta(t, 1.450953, bigPositive, 0.00001)
}

func TestMeasurementsNeeded(t *testing.T) {
	require.Equal(t, uint64(53), keeper.MeasurementsNeeded(0.05, 100))
	require.Equal(t, uint64(27), keeper.MeasurementsNeeded(0.10, 100))
	require.Equal(t, uint64(262), keeper.MeasurementsNeeded(0.01, 300))
	require.Equal(t, uint64(100), keeper.MeasurementsNeeded(0.01, 100))
}
