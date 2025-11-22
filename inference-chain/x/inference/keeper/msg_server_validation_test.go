package keeper_test

import (
	"context"
	"log"
	"testing"
	"time"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/testutil"
	keeper2 "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
	"go.uber.org/mock/gomock"
)

const INFERENCE_ID = "inferenceId"
const MODEL_ID = "Qwen/QwQ-32B"

func TestMsgServer_Validation(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)

	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupData(k, ctx)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
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
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)

	addMembersToGroupData(k, ctx)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
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

	has, err := k.ActiveInvalidations.Has(ctx, collections.Join(sdk.MustAccAddressFromBech32(testutil.Validator), expected.InferenceId))
	require.NoError(t, err)
	require.True(t, has)
}

func addMembersToGroupData(k keeper.Keeper, ctx sdk.Context) {
	groupData, _ := k.GetEpochGroupData(ctx, 0, MODEL_ID)
	groupData.ValidationWeights = []*types.ValidationWeight{
		{
			MemberAddress: testutil.Validator,
			Weight:        100,
			Reputation:    50,
		},
		{
			MemberAddress: testutil.Requester,
			Weight:        100,
			Reputation:    100,
		},
	}
	// Ensure TotalWeight is set to avoid division by zero in calculations
	var total int64 = 0
	for _, vw := range groupData.ValidationWeights {
		total += vw.Weight
	}
	groupData.TotalWeight = total
	k.SetEpochGroupData(ctx, groupData)
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
	requestTimestamp := time.Now().UnixNano()
	expected, err := inferenceHelper.StartInference("promptPayload", "model1", requestTimestamp, calculations.DefaultMaxTokens)
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

// New tests for invalidation limits and duplicate validations
func TestMsgServer_Validation_InvalidationsLimit_NoStatusChange_ButRecordsCredit(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)

	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupData(k, ctx)

	// Make the maximum allowed invalidations very small and deterministic
	params := k.GetParams(ctx)
	if params.BandwidthLimitsParams == nil {
		params.BandwidthLimitsParams = &types.BandwidthLimitsParams{}
	}
	params.BandwidthLimitsParams.InvalidationsLimit = 1
	params.BandwidthLimitsParams.InvalidationsLimitCurve = 1
	params.BandwidthLimitsParams.InvalidationsSamplePeriod = 60
	k.SetParams(ctx, params)

	// Pre-populate one active invalidation for the validator so we hit the limit (>= 1)
	err := k.ActiveInvalidations.Set(ctx, collections.Join(sdk.MustAccAddressFromBech32(testutil.Validator), "prev-inference"))
	require.NoError(t, err)

	// Create and finish an inference
	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)

	// Attempt a failing validation; since limit reached, it should early-return without changing status
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId: expected.InferenceId,
		Creator:     testutil.Validator,
		Value:       0.10, // below threshold so it would normally trigger invalidation
	})
	require.NoError(t, err)

	// Inference status should remain FINISHED (no transition to VOTING)
	saved, found := k.GetInference(ctx, expected.InferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_FINISHED, saved.Status)

	// Validator should still get credit for performing validation in EpochGroupValidations
	egv, ok := k.GetEpochGroupValidations(ctx, testutil.Validator, saved.EpochId)
	require.True(t, ok)
	// The recorded list should contain this inference id
	foundId := false
	for _, id := range egv.ValidatedInferences {
		if id == expected.InferenceId {
			foundId = true
			break
		}
	}
	require.True(t, foundId, "expected inference id to be recorded in epoch group validations")
}

func TestMsgServer_Validation_DuplicateValidation_ReturnsErrDuplicateValidation(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)

	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupData(k, ctx)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)

	// First validation should succeed
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId: expected.InferenceId,
		Creator:     testutil.Validator,
		Value:       0.99,
	})
	require.NoError(t, err)

	// Second validation (same validator, same inference, not a revalidation) should return ErrDuplicateValidation
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId: expected.InferenceId,
		Creator:     testutil.Validator,
		Value:       0.99,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrDuplicateValidation)
}
