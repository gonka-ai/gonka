package keeper_test

import (
	"context"
	"log"
	"testing"
	"time"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keeper2 "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

const INFERENCE_ID = "inferenceId"
const MODEL_ID = "Qwen/QwQ-32B"

// Seeds that produce predictable ShouldValidate outcomes for fixed inference id (from calculations/reputation_test.go).
const (
	fiftyPercentSeed  = int64(6669939700021626378)
	ninetyPercentSeed = int64(5798067479865859744)
	defaultTrafficBasis = uint64(10_000)
)

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
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.9999),
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
	ms := inferenceHelper.MessageServer
	// Validation handler sets status to VOTING but does not call SubmitProposal or Vote (see msg_server_revalidate_inference_test.go).
	_, err = ms.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.80),
	})
	require.NoError(t, err)
	inference, found := k.GetInference(ctx, expected.InferenceId)
	log.Print(inference)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_VOTING, inference.Status)
	// Seed revalidation cache so Requester is eligible to vote (normally set by ProcessPendingRevalidationEvents in Precommiter).
	k.AddRevalidationEligibleParticipantForTest(ctx.BlockHeight(), expected.InferenceId, testutil.Validator, testutil.Requester, 100)

	_, err = ms.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Requester,
		ValueDecimal: types.DecimalFromFloat(0.80),
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
	addMembersToGroupDataWithWeights(k, ctx, 100, 100, 100, 100)
}

// addMembersToGroupDataWithWeights sets epoch group data with Validator and Requester; use confirmationWeightValidator 0 to test ineligibility.
func addMembersToGroupDataWithWeights(k keeper.Keeper, ctx sdk.Context, weightValidator, weightRequester, confirmationWeightValidator, confirmationWeightRequester int64) {
	groupData, _ := k.GetEpochGroupData(ctx, 0, MODEL_ID)
	groupData.ValidationWeights = []*types.ValidationWeight{
		{
			MemberAddress:      testutil.Validator,
			Weight:              weightValidator,
			Reputation:          50,
			ConfirmationWeight:  confirmationWeightValidator,
		},
		{
			MemberAddress:      testutil.Requester,
			Weight:             weightRequester,
			Reputation:         100,
			ConfirmationWeight: confirmationWeightRequester,
		},
	}
	var total int64 = 0
	for _, vw := range groupData.ValidationWeights {
		total += vw.Weight
	}
	groupData.TotalWeight = total
	k.SetEpochGroupData(ctx, groupData)
}

// TestMsgServer_Validation_ConfirmationWeightZero asserts that a participant with ConfirmationWeight 0 cannot validate (ErrParticipantNotFound).
func TestMsgServer_Validation_ConfirmationWeightZero(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	// Validator has ConfirmationWeight 0; Requester has 100 so group is valid but Validator cannot validate
	addMembersToGroupDataWithWeights(k, ctx, 100, 100, 0, 100)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)

	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.9999),
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrParticipantNotFound)
}

// TestMsgServer_Validation_ConfirmationWeightNonZero_NoSeed_Succeeds asserts that with ConfirmationWeight > 0 and no seed, validation is allowed (skip-the-check path).
func TestMsgServer_Validation_ConfirmationWeightNonZero_NoSeed_Succeeds(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupData(k, ctx)
	// No seed set for Validator -> skipTheShouldValidateCheck is true -> validation still succeeds

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)

	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.9999),
	})
	require.NoError(t, err)
	inference, found := k.GetInference(ctx, expected.InferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_VALIDATED, inference.Status)
}

func TestMsgServer_NoInference(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)
	createParticipants(t, ms, ctx)
	_, err := ms.Validation(ctx, &types.MsgValidation{
		InferenceId:  INFERENCE_ID,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.9999),
	})
	require.Error(t, err)
}

func TestMsgServer_NotFinished(t *testing.T) {
	inferenceHelper, _, ctx := NewMockInferenceHelper(t)
	requestTimestamp := time.Now().UnixNano()
	expected, err := inferenceHelper.StartInference("promptPayload", "model1", requestTimestamp, calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.9999),
	})
	require.Error(t, err)
}

func TestMsgServer_InvalidExecutor(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)
	mockValidator := NewMockAccount(testutil.Validator)
	MustAddParticipant(t, ms, ctx, *mockValidator)
	_, err := ms.Validation(ctx, &types.MsgValidation{
		InferenceId:  INFERENCE_ID,
		Creator:      testutil.Executor,
		ValueDecimal: types.DecimalFromFloat(0.9999),
	})
	require.Error(t, err)
}

func TestMsgServer_ValidatorCannotBeExecutor(t *testing.T) {
	_, ms, ctx := setupMsgServer(t)
	createParticipants(t, ms, ctx)
	_, err := ms.Validation(ctx, &types.MsgValidation{
		InferenceId:  INFERENCE_ID,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.9999),
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
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	if params.BandwidthLimitsParams == nil {
		params.BandwidthLimitsParams = &types.BandwidthLimitsParams{}
	}
	params.BandwidthLimitsParams.InvalidationsLimit = 1
	params.BandwidthLimitsParams.InvalidationsLimitCurve = 1
	params.BandwidthLimitsParams.InvalidationsSamplePeriod = 60
	k.SetParams(ctx, params)

	// Pre-populate one active invalidation for the validator so we hit the limit (>= 1)
	err = k.ActiveInvalidations.Set(ctx, collections.Join(sdk.MustAccAddressFromBech32(testutil.Validator), "prev-inference"))
	require.NoError(t, err)

	// Create and finish an inference
	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)

	// Attempt a failing validation; since limit reached, it should early-return without changing status
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.10), // below threshold so it would normally trigger invalidation
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
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.99),
	})
	require.NoError(t, err)

	// Second validation (same validator, same inference, not a revalidation) should return ErrDuplicateValidation
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.99),
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrDuplicateValidation)
}

// TestMsgServer_Validation_SeedAndShouldValidate_NotDesignated asserts that when the participant has a seed and ShouldValidate returns false, Validation returns ErrNotDesignatedValidator.
func TestMsgServer_Validation_SeedAndShouldValidate_NotDesignated(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	// Validator weight 50 so participant.Weight matches details; total 100 so TotalWeight 150 with Requester 100
	addMembersToGroupDataWithWeights(k, ctx, 50, 100, 100, 100)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)

	// Overwrite validation details so ShouldValidate(seed, details, ...) is false: executor rep 100, total 150, validator 50, executor 50 -> low ourProbability
	details := types.InferenceValidationDetails{
		InferenceId:        expected.InferenceId,
		EpochId:            0,
		ExecutorId:         testutil.Executor,
		ExecutorReputation:  100,
		TrafficBasis:        defaultTrafficBasis,
		ExecutorPower:      50,
		TotalPower:         150,
		Model:               model.Id,
	}
	k.SetInferenceValidationDetails(ctx, details)
	// Seed that yields randFloat such that randFloat >= ourProbability -> shouldValidate false
	err = k.SetRandomSeed(ctx, types.RandomSeed{Participant: testutil.Validator, EpochIndex: 0, Signature: "sig", Seed: fiftyPercentSeed})
	require.NoError(t, err)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	if params.ValidationParams == nil {
		params.ValidationParams = types.DefaultValidationParams()
	}
	params.ValidationParams.MinValidationAverage = types.DecimalFromFloat(0.1)
	params.ValidationParams.MaxValidationAverage = types.DecimalFromFloat(1.0)
	k.SetParams(ctx, params)

	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.9999),
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrNotDesignatedValidator)
}

// TestMsgServer_Validation_SeedAndShouldValidate_Designated_Succeeds asserts that when the participant has a seed and ShouldValidate returns true, Validation succeeds.
func TestMsgServer_Validation_SeedAndShouldValidate_Designated_Succeeds(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupDataWithWeights(k, ctx, 50, 50, 100, 100)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)

	// Details: total 100, executor 50, rep 0 -> ourProbability 1.0; ninetyPercentSeed still < 1.0 -> shouldValidate true
	details := types.InferenceValidationDetails{
		InferenceId:        expected.InferenceId,
		EpochId:            0,
		ExecutorId:         testutil.Executor,
		ExecutorReputation:  0,
		TrafficBasis:        defaultTrafficBasis,
		ExecutorPower:      50,
		TotalPower:         100,
		Model:               model.Id,
	}
	k.SetInferenceValidationDetails(ctx, details)
	err = k.SetRandomSeed(ctx, types.RandomSeed{Participant: testutil.Validator, EpochIndex: 0, Signature: "sig", Seed: ninetyPercentSeed})
	require.NoError(t, err)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	if params.ValidationParams == nil {
		params.ValidationParams = types.DefaultValidationParams()
	}
	params.ValidationParams.MinValidationAverage = types.DecimalFromFloat(0.5)
	params.ValidationParams.MaxValidationAverage = types.DecimalFromFloat(1.0)
	k.SetParams(ctx, params)

	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.9999),
	})
	require.NoError(t, err)
	inference, found := k.GetInference(ctx, expected.InferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_VALIDATED, inference.Status)
}

// TestMsgServer_Validation_TwoParticipants_OnlyDesignatedSucceeds asserts that with two participants, only the one for whom ShouldValidate is true can validate; the other gets ErrNotDesignatedValidator.
// Seeds are assigned based on the actual inference id so the test is deterministic regardless of inference id.
func TestMsgServer_Validation_TwoParticipants_OnlyDesignatedSucceeds(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)
	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupDataWithWeights(k, ctx, 50, 50, 100, 100)

	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)

	details := types.InferenceValidationDetails{
		InferenceId:        expected.InferenceId,
		EpochId:            0,
		ExecutorId:         testutil.Executor,
		ExecutorReputation: 50,
		TrafficBasis:       defaultTrafficBasis,
		ExecutorPower:      50,
		TotalPower:         100,
		Model:              model.Id,
	}
	k.SetInferenceValidationDetails(ctx, details)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	if params.ValidationParams == nil {
		params.ValidationParams = types.DefaultValidationParams()
	}
	params.ValidationParams.MinValidationAverage = types.DecimalFromFloat(0.1)
	params.ValidationParams.MaxValidationAverage = types.DecimalFromFloat(1.0)
	k.SetParams(ctx, params)

	// Assign seeds so Validator is designated and Requester is not (DeterministicFloat depends on inference id)
	totalPower := uint32(100)
	validatorPower := uint32(50)
	executorPower := uint32(50)
	shouldValFifty, _ := calculations.ShouldValidate(fiftyPercentSeed, &details, totalPower, validatorPower, executorPower, params.ValidationParams, false)
	shouldValNinety, _ := calculations.ShouldValidate(ninetyPercentSeed, &details, totalPower, validatorPower, executorPower, params.ValidationParams, false)
	require.NotEqual(t, shouldValFifty, shouldValNinety, "seeds must yield different designation for this test; adjust params if needed")
	validatorSeed, requesterSeed := fiftyPercentSeed, ninetyPercentSeed
	if !shouldValFifty {
		validatorSeed, requesterSeed = ninetyPercentSeed, fiftyPercentSeed
	}
	err = k.SetRandomSeed(ctx, types.RandomSeed{Participant: testutil.Validator, EpochIndex: 0, Signature: "sig", Seed: validatorSeed})
	require.NoError(t, err)
	err = k.SetRandomSeed(ctx, types.RandomSeed{Participant: testutil.Requester, EpochIndex: 0, Signature: "sig", Seed: requesterSeed})
	require.NoError(t, err)

	// Validator is designated -> succeeds
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.9999),
	})
	require.NoError(t, err)
	inference, found := k.GetInference(ctx, expected.InferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_VALIDATED, inference.Status)

	// Requester is not designated -> ErrNotDesignatedValidator (revalidation path still checks ShouldValidate)
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Requester,
		ValueDecimal: types.DecimalFromFloat(0.9999),
		Revalidation: true,
	})
	require.Error(t, err)
	require.ErrorIs(t, err, types.ErrNotDesignatedValidator)
}

// TestGetParticipantEpochSeed_ReturnsSetSeed asserts that after SetRandomSeed, GetParticipantEpochSeed returns the same Seed (hash/round-trip consistency).
func TestGetParticipantEpochSeed_ReturnsSetSeed(t *testing.T) {
	_, k, ctx := NewMockInferenceHelper(t)
	epoch := uint64(0)
	participant := testutil.Validator
	seedVal := int64(12345)
	err := k.SetRandomSeed(ctx, types.RandomSeed{Participant: participant, EpochIndex: epoch, Signature: "sig", Seed: seedVal})
	require.NoError(t, err)
	got, found := k.GetParticipantEpochSeed(ctx, epoch, participant)
	require.True(t, found)
	require.Equal(t, seedVal, got)
}
