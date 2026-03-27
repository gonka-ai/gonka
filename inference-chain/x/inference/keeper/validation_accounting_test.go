package keeper_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

// TestValidationStatusGuard_PreventsDoubleShareWork verifies that once an inference
// is VALIDATED by one validator, a second validator calling Validation is rejected.
// Without the status guard, the second validator could overwrite VALIDATED to VOTING,
// leaving the shareWorkWithValidators redistribution irreversible if voting resolves
// to INVALIDATED - causing the executor to lose more than ActualCost.
func TestValidationStatusGuard_PreventsDoubleShareWork(t *testing.T) {
	inferenceHelper, k, ctx := NewMockInferenceHelper(t)
	createParticipants(t, inferenceHelper.MessageServer, ctx)

	model := &types.Model{Id: MODEL_ID, ValidationThreshold: &types.Decimal{Value: 85, Exponent: -2}}
	k.SetModel(ctx, model)
	StubModelSubgroup(t, ctx, k, inferenceHelper.Mocks, model)
	addMembersToGroupData(k, ctx)

	// Full inference lifecycle: Start -> Finish
	expected, err := inferenceHelper.StartInference("promptPayload", model.Id, time.Now().UnixNano(), calculations.DefaultMaxTokens)
	require.NoError(t, err)
	_, err = inferenceHelper.FinishInference()
	require.NoError(t, err)
	buildValidationCacheForTest(t, k, ctx)

	// Record executor CoinBalance after FinishInference
	executorBefore, found := k.GetParticipant(ctx, testutil.Executor)
	require.True(t, found)
	initialExecutorBalance := executorBefore.CoinBalance
	t.Logf("Executor CoinBalance after FinishInference: %d", initialExecutorBalance)

	inference, found := k.GetInference(ctx, expected.InferenceId)
	require.True(t, found)
	actualCost := inference.ActualCost
	require.Greater(t, actualCost, int64(0))

	validatorBefore, found := k.GetParticipant(ctx, testutil.Validator)
	require.True(t, found)
	initialValidatorBalance := validatorBefore.CoinBalance

	// === STEP 1: Validator passes the inference ===
	// shareWorkWithValidators runs, redistributing CoinBalance
	_, err = inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Validator,
		ValueDecimal: types.DecimalFromFloat(0.9999),
	})
	require.NoError(t, err)

	inf, found := k.GetInference(ctx, expected.InferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_VALIDATED, inf.Status)

	executorAfterShare, _ := k.GetParticipant(ctx, testutil.Executor)
	validatorAfterShare, _ := k.GetParticipant(ctx, testutil.Validator)

	shareDeducted := initialExecutorBalance - executorAfterShare.CoinBalance
	shareGiven := validatorAfterShare.CoinBalance - initialValidatorBalance
	t.Logf("After validation pass: executor lost %d, validator gained %d via shareWork", shareDeducted, shareGiven)
	require.Greater(t, shareGiven, int64(0))

	// === STEP 2: Second validator tries to fail the same inference ===
	// With the status guard, this should be rejected (no-op return)
	// Without the guard, it would overwrite VALIDATED->VOTING
	resp, err := inferenceHelper.MessageServer.Validation(ctx, &types.MsgValidation{
		InferenceId:  expected.InferenceId,
		Creator:      testutil.Requester,
		ValueDecimal: types.DecimalFromFloat(0.50), // fails threshold
	})
	require.NoError(t, err)
	require.NotNil(t, resp)

	// Status should still be VALIDATED, NOT overwritten to VOTING
	inf, found = k.GetInference(ctx, expected.InferenceId)
	require.True(t, found)
	require.Equal(t, types.InferenceStatus_VALIDATED, inf.Status,
		"Status guard should prevent overwriting VALIDATED to VOTING")

	// Executor and validator balances should be unchanged from after shareWork
	executorFinal, _ := k.GetParticipant(ctx, testutil.Executor)
	validatorFinal, _ := k.GetParticipant(ctx, testutil.Validator)

	require.Equal(t, executorAfterShare.CoinBalance, executorFinal.CoinBalance,
		"Executor CoinBalance should not change - second validation was rejected")
	require.Equal(t, validatorAfterShare.CoinBalance, validatorFinal.CoinBalance,
		"Validator CoinBalance should not change - second validation was rejected")

	t.Logf("Status guard working: second validation correctly rejected, no accounting damage")
}
