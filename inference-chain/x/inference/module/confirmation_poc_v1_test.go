package inference_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inference "github.com/productscience/inference/x/inference/module"
	"github.com/productscience/inference/x/inference/types"
)

// TestUpdateConfirmationWeightsV1_BasicCalculation tests V1 confirmation weight calculation
func TestUpdateConfirmationWeightsV1_BasicCalculation(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")
	sdk.GetConfig().SetBech32PrefixForValidator("gonkavaloper", "gonkavaloperpub")

	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Setup params with weight scale factor
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		WeightScaleFactor: types.DecimalFromFloat(1.0),
	}
	require.NoError(t, k.SetParams(ctx, params))

	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	// Setup current validator weights
	currentValidatorWeights := map[string]int64{
		testutil.Validator: 100,
	}

	// Create V1 batches using trigger_height as key (180)
	batch := types.PoCBatch{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180, // Uses trigger_height
		ReceivedAtBlockHeight:    195,
		Nonces:                   []int64{1, 2, 3, 4, 5},
		NodeId:                   "node-1",
	}
	k.SetPocBatch(ctx, batch)

	// Create V1 validation
	validation := types.PoCValidation{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    180, // Uses trigger_height
		ValidatedAtBlockHeight:      200,
		FraudDetected:               false,
	}
	k.SetPoCValidation(ctx, validation)

	// Create participant
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:        testutil.Executor,
		Address:      testutil.Executor,
		ValidatorKey: "validatorKey",
		InferenceUrl: "http://example.com/",
	}))

	// Create seed
	k.SetRandomSeed(ctx, types.RandomSeed{
		Participant: testutil.Executor,
		EpochIndex:  2,
		Signature:   "sig",
	})

	// Create confirmation PoC event
	event := &types.ConfirmationPoCEvent{
		EpochIndex:            2,
		EventSequence:         0,
		TriggerHeight:         180,
		GenerationStartHeight: 190,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
	}

	weightScaleFactor := params.PocParams.GetWeightScaleFactorDec()

	// Call V1 confirmation weight calculation
	result := am.UpdateConfirmationWeightsV1(ctx, event, currentValidatorWeights, weightScaleFactor)

	// Should have 1 participant with weight = 5 (unique nonces)
	require.Len(t, result, 1)
	require.Equal(t, testutil.Executor, result[0].Index)
	require.Equal(t, int64(5), result[0].Weight)
}

// TestUpdateConfirmationWeightsV1_NoBatches tests V1 confirmation when participant has no batches
func TestUpdateConfirmationWeightsV1_NoBatches(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")

	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Setup params with weight scale factor
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		WeightScaleFactor: types.DecimalFromFloat(1.0),
	}
	require.NoError(t, k.SetParams(ctx, params))

	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	// No batches stored for trigger_height 180

	currentValidatorWeights := map[string]int64{
		testutil.Validator: 100,
	}

	event := &types.ConfirmationPoCEvent{
		EpochIndex:            2,
		EventSequence:         0,
		TriggerHeight:         180,
		GenerationStartHeight: 190,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
	}

	weightScaleFactor := params.PocParams.GetWeightScaleFactorDec()

	result := am.UpdateConfirmationWeightsV1(ctx, event, currentValidatorWeights, weightScaleFactor)

	// Should have 0 participants since no batches were submitted
	require.Len(t, result, 0)
}

// TestUpdateConfirmationWeightsV1_MultipleParticipants tests V1 confirmation with multiple participants
func TestUpdateConfirmationWeightsV1_MultipleParticipants(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")

	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Setup params with weight scale factor
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		WeightScaleFactor: types.DecimalFromFloat(1.0),
	}
	require.NoError(t, k.SetParams(ctx, params))

	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	// Setup current validator weights
	currentValidatorWeights := map[string]int64{
		testutil.Validator:  50,
		testutil.Validator2: 50,
	}

	// Create V1 batches for two participants
	batch1 := types.PoCBatch{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		Nonces:                   []int64{1, 2, 3}, // 3 unique
		NodeId:                   "node-1",
	}
	k.SetPocBatch(ctx, batch1)

	batch2 := types.PoCBatch{
		ParticipantAddress:       testutil.Executor2,
		PocStageStartBlockHeight: 180,
		Nonces:                   []int64{10, 20, 30, 40, 50}, // 5 unique
		NodeId:                   "node-2",
	}
	k.SetPocBatch(ctx, batch2)

	// Create V1 validations for both participants (need majority vote)
	k.SetPoCValidation(ctx, types.PoCValidation{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    180,
		FraudDetected:               false,
	})
	k.SetPoCValidation(ctx, types.PoCValidation{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator2,
		PocStageStartBlockHeight:    180,
		FraudDetected:               false,
	})

	k.SetPoCValidation(ctx, types.PoCValidation{
		ParticipantAddress:          testutil.Executor2,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    180,
		FraudDetected:               false,
	})
	k.SetPoCValidation(ctx, types.PoCValidation{
		ParticipantAddress:          testutil.Executor2,
		ValidatorParticipantAddress: testutil.Validator2,
		PocStageStartBlockHeight:    180,
		FraudDetected:               false,
	})

	// Create participants
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:        testutil.Executor,
		Address:      testutil.Executor,
		ValidatorKey: "validatorKey1",
	}))
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:        testutil.Executor2,
		Address:      testutil.Executor2,
		ValidatorKey: "validatorKey2",
	}))

	// Create seeds
	k.SetRandomSeed(ctx, types.RandomSeed{Participant: testutil.Executor, EpochIndex: 2, Signature: "sig1"})
	k.SetRandomSeed(ctx, types.RandomSeed{Participant: testutil.Executor2, EpochIndex: 2, Signature: "sig2"})

	event := &types.ConfirmationPoCEvent{
		EpochIndex:            2,
		EventSequence:         0,
		TriggerHeight:         180,
		GenerationStartHeight: 190,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
	}

	weightScaleFactor := params.PocParams.GetWeightScaleFactorDec()

	result := am.UpdateConfirmationWeightsV1(ctx, event, currentValidatorWeights, weightScaleFactor)

	// Should have 2 participants
	require.Len(t, result, 2)

	// Find each participant's result
	var exec1Result, exec2Result *types.ActiveParticipant
	for _, r := range result {
		if r.Index == testutil.Executor {
			exec1Result = r
		} else if r.Index == testutil.Executor2 {
			exec2Result = r
		}
	}

	require.NotNil(t, exec1Result)
	require.NotNil(t, exec2Result)
	require.Equal(t, int64(3), exec1Result.Weight)
	require.Equal(t, int64(5), exec2Result.Weight)
}

// TestUpdateConfirmationWeightsV1_FraudRejection tests V1 confirmation rejecting fraudulent participant
func TestUpdateConfirmationWeightsV1_FraudRejection(t *testing.T) {
	sdk.GetConfig().SetBech32PrefixForAccount("gonka", "gonkapub")

	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Setup params with weight scale factor
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{
		WeightScaleFactor: types.DecimalFromFloat(1.0),
	}
	require.NoError(t, k.SetParams(ctx, params))

	am := inference.NewAppModule(nil, k, nil, nil, nil, nil)

	// Setup current validator weights (validator2 has majority)
	currentValidatorWeights := map[string]int64{
		testutil.Validator:  10,
		testutil.Validator2: 90,
	}

	// Create V1 batch
	batch := types.PoCBatch{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180,
		Nonces:                   []int64{1, 2, 3},
		NodeId:                   "node-1",
	}
	k.SetPocBatch(ctx, batch)

	// Validator1 says valid, Validator2 (majority) says fraud
	k.SetPoCValidation(ctx, types.PoCValidation{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    180,
		FraudDetected:               false,
	})
	k.SetPoCValidation(ctx, types.PoCValidation{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator2,
		PocStageStartBlockHeight:    180,
		FraudDetected:               true, // Fraud detected by majority
	})

	// Create participant
	require.NoError(t, k.SetParticipant(ctx, types.Participant{
		Index:        testutil.Executor,
		Address:      testutil.Executor,
		ValidatorKey: "validatorKey",
	}))

	// Create seed
	k.SetRandomSeed(ctx, types.RandomSeed{Participant: testutil.Executor, EpochIndex: 2, Signature: "sig"})

	event := &types.ConfirmationPoCEvent{
		EpochIndex:            2,
		EventSequence:         0,
		TriggerHeight:         180,
		GenerationStartHeight: 190,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
	}

	weightScaleFactor := params.PocParams.GetWeightScaleFactorDec()

	result := am.UpdateConfirmationWeightsV1(ctx, event, currentValidatorWeights, weightScaleFactor)

	// Should be rejected due to fraud detection by majority
	require.Len(t, result, 0)
}
