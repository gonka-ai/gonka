package keeper_test

import (
	"context"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/testutil"
	keeper2 "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
	"log"
	"testing"
)

const INFERENCE_ID = "inferenceId"
const MODEL_ID = "Qwen/QwQ-32B"

func TestMsgServer_Validation(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)

	model := &types.Model{Id: MODEL_ID}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, 10020220, keeper.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId: expected.InferenceId,
		Creator:     testutil.Validator,
		Value:       0.9999,
	})
	require.NoError(t, err)
	inference, found := k.GetInference(ctx, expected.InferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_VALIDATED, inference.Status)
}

func createParticipants(t *testing.T, ms types.MsgServer, ctx context.Context) {
	mockRequester := NewMockAccount(testutil.Requester)
	mockExecutor := NewMockAccount(testutil.Executor)
	mockValidator := NewMockAccount(testutil.Validator)
	mockCreator := NewMockAccount(testutil.Creator)
	MustAddParticipant(t, ms, ctx, *mockRequester)
	MustAddParticipant(t, ms, ctx, *mockExecutor)
	MustAddParticipant(t, ms, ctx, *mockValidator)
	MustAddParticipant(t, ms, ctx, *mockCreator)
}

func TestMsgServer_Validation_Invalidate(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)

	model := &types.Model{Id: MODEL_ID}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, 10020220, keeper.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)
	mocks := inferenceHelper.Mocks
	mocks.GroupKeeper.EXPECT().SubmitProposal(ctx, gomock.Any()).Return(&group.MsgSubmitProposalResponse{
		ProposalId: 1,
	}, nil)
	mocks.GroupKeeper.EXPECT().SubmitProposal(ctx, gomock.Any()).Return(&group.MsgSubmitProposalResponse{
		ProposalId: 2,
	}, nil)
	ms := inferenceHelper.MessageServer
	_, err = ms.Validation(ctx, &types.MsgValidation{
		InferenceId: expected.InferenceId,
		Creator:     testutil.Validator,
		Value:       0.80,
	})
	require.NoError(t, err)
	inference, found := k.GetInference(ctx, expected.InferenceId)
	log.Print(inference)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_VOTING, inference.Status)
	mocks.GroupKeeper.EXPECT().Vote(ctx, gomock.Eq(&group.MsgVote{
		ProposalId: 1,
		Voter:      testutil.Requester,
		Option:     group.VOTE_OPTION_YES,
		Metadata:   "Invalidate inference " + expected.InferenceId,
		Exec:       group.Exec_EXEC_TRY,
	}))
	mocks.GroupKeeper.EXPECT().Vote(ctx, gomock.Eq(&group.MsgVote{
		ProposalId: 2,
		Voter:      testutil.Requester,
		Option:     group.VOTE_OPTION_NO,
		Metadata:   "Revalidate inference " + expected.InferenceId,
		Exec:       group.Exec_EXEC_TRY,
	}))

	_, err = ms.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Requester,
		Value:        0.80,
		Revalidation: true,
	})
	inference, found = k.GetInference(ctx, expected.InferenceId)

	require.True(t, found)
	require.Equal(t, types.InferenceStatus_VOTING, inference.Status)
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
	inferenceHelper, _, ctx := NewMockInferenceHelper(t)
	requestTimestamp := int64(10020220)
	expected, err := inferenceHelper.StartInference("promptPayload", "model1", requestTimestamp, keeper.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId: expected.InferenceId,
		Creator:     testutil.Validator,
		Value:       0.9999,
	})
	require.Error(t, err)
}

func TestMsgServer_InvalidExecutor(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)
	mockValidator := NewMockAccount(testutil.Validator)
	MustAddParticipant(t, ms, ctx, *mockValidator)
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

func createCompletedInference(t *testing.T, ms types.MsgServer, ctx context.Context, mocks *keeper2.InferenceMocks) {
	_, err := ms.StartInference(ctx, &types.MsgStartInference{
		InferenceId:   "inferenceId",
		PromptHash:    "promptHash",
		PromptPayload: "promptPayload",
		RequestedBy:   testutil.Requester,
		Creator:       testutil.Creator,
		Model:         "Qwen/QwQ-32B",
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
