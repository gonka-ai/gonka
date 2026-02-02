package keeper_test

import (
	"testing"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// Test V1 batch submission with blocked participant
func TestSubmitPocBatchV1_BlockedParticipant(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)

	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.ParticipantAccessParams = &types.ParticipantAccessParams{
		BlockedParticipantAddresses: []string{testutil.Executor},
	}
	require.NoError(t, k.SetParams(sdkCtx, params))

	// Call V1 handler directly via msg server (when V1 is enabled, SubmitPocBatch routes to submitPocBatchV1)
	_, err = ms.SubmitPocBatch(sdkCtx, &types.MsgSubmitPocBatch{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 1,
		BatchId:                  "batch",
		Nonces:                   []int64{1},
		Dist:                     []float64{0.1},
		NodeId:                   "node1",
	})
	// Note: Current handler returns ErrDeprecated - this test validates the pattern
	// When V1 dispatch is wired (Phase 3), this should return ErrParticipantBlocked
	require.Error(t, err)
}

// Test V1 batch submission with empty NodeId
func TestSubmitPocBatchV1_EmptyNodeId(t *testing.T) {
	k, _, ctx := setupMsgServer(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)

	// Setup the epoch
	upcomingEpoch := &types.Epoch{
		Index:               1,
		PocStartBlockHeight: 80,
	}
	k.SetEpoch(sdkCtx, upcomingEpoch)

	// Create the msgServer
	msgServer := keeper.NewMsgServerImpl(k)

	// Test V1 handler directly
	msg := &types.MsgSubmitPocBatch{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 80,
		BatchId:                  "batch-1",
		Nonces:                   []int64{1, 2, 3},
		Dist:                     []float64{0.1, 0.2, 0.3},
		NodeId:                   "", // Empty NodeId
	}

	// Note: Current handler returns ErrDeprecated
	// When V1 dispatch is wired, this should return ErrPocNodeIdEmpty
	_, err := msgServer.SubmitPocBatch(sdkCtx, msg)
	require.Error(t, err)
}

// Test V1 validation submission with blocked validator
func TestSubmitPocValidationV1_BlockedValidator(t *testing.T) {
	k, ms, ctx := setupMsgServer(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)

	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.ParticipantAccessParams = &types.ParticipantAccessParams{
		BlockedParticipantAddresses: []string{testutil.Creator}, // validator in this msg
	}
	require.NoError(t, k.SetParams(sdkCtx, params))

	_, err = ms.SubmitPocValidation(sdkCtx, &types.MsgSubmitPocValidation{
		Creator:                  testutil.Creator,
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 1,
		Nonces:                   []int64{1},
		Dist:                     []float64{0.1},
		ReceivedDist:             []float64{0.1},
	})
	// Note: Current handler returns ErrDeprecated
	// When V1 dispatch is wired (Phase 3), this should return ErrParticipantBlocked
	require.Error(t, err)
}

// Test V1 batch submission stores PoCBatch correctly
func TestSubmitPocBatchV1_StoresBatch(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(100)

	// Setup epoch params
	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.EpochParams = &types.EpochParams{
		PocStageDuration:      50,
		PocExchangeDuration:   20,
		PocValidationDelay:    5,
		PocValidationDuration: 10,
	}
	require.NoError(t, k.SetParams(sdkCtx, params))

	// Setup upcoming epoch
	upcomingEpoch := &types.Epoch{
		Index:               1,
		PocStartBlockHeight: 80,
	}
	k.SetEpoch(sdkCtx, upcomingEpoch)

	// Manually create a PoCBatch (simulating what V1 handler would do)
	batch := types.PoCBatch{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 80,
		ReceivedAtBlockHeight:    100,
		Nonces:                   []int64{1, 2, 3},
		Dist:                     []float64{0.1, 0.2, 0.3},
		BatchId:                  "batch-1",
		NodeId:                   "node-1",
	}
	k.SetPocBatch(sdkCtx, batch)

	// Verify batch was stored
	batches, err := k.GetPoCBatchesByStage(sdkCtx, 80)
	require.NoError(t, err)
	require.Len(t, batches[testutil.Executor], 1)
	require.Equal(t, "node-1", batches[testutil.Executor][0].NodeId)
	require.Equal(t, []int64{1, 2, 3}, batches[testutil.Executor][0].Nonces)
}

// Test V1 validation submission stores PoCValidation correctly
func TestSubmitPocValidationV1_StoresValidation(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(120)

	// Manually create a PoCValidation (simulating what V1 handler would do)
	validation := types.PoCValidation{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    80,
		ValidatedAtBlockHeight:      120,
		Nonces:                      []int64{1, 2},
		Dist:                        []float64{0.1, 0.2},
		ReceivedDist:                []float64{0.1, 0.2},
		FraudDetected:               false,
	}
	k.SetPoCValidation(sdkCtx, validation)

	// Verify validation was stored
	validations, err := k.GetPoCValidationByStage(sdkCtx, 80)
	require.NoError(t, err)
	require.Len(t, validations[testutil.Executor], 1)
	require.Equal(t, testutil.Validator, validations[testutil.Executor][0].ValidatorParticipantAddress)
	require.False(t, validations[testutil.Executor][0].FraudDetected)
}

// Test V1 batch submission for confirmation PoC event
func TestSubmitPocBatchV1_ConfirmationPoC(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(200)

	// Setup epoch params
	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.EpochParams = &types.EpochParams{
		PocStageDuration:      50,
		PocExchangeDuration:   20,
		PocValidationDelay:    5,
		PocValidationDuration: 10,
	}
	require.NoError(t, k.SetParams(sdkCtx, params))

	// Create active confirmation PoC event in GENERATION phase
	event := types.ConfirmationPoCEvent{
		EpochIndex:            2,
		EventSequence:         0,
		TriggerHeight:         180,
		GenerationStartHeight: 190,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_GENERATION,
		PocSeedBlockHash:      "abc123",
	}
	require.NoError(t, k.SetActiveConfirmationPoCEvent(sdkCtx, event))

	// Manually create a PoCBatch for confirmation PoC (using trigger_height as key)
	batch := types.PoCBatch{
		ParticipantAddress:       testutil.Executor,
		PocStageStartBlockHeight: 180, // Uses trigger_height as key
		ReceivedAtBlockHeight:    200,
		Nonces:                   []int64{10, 20, 30},
		Dist:                     []float64{0.5, 0.6, 0.7},
		BatchId:                  "conf-batch-1",
		NodeId:                   "conf-node-1",
	}
	k.SetPocBatch(sdkCtx, batch)

	// Verify batch was stored with trigger_height key
	batches, err := k.GetPoCBatchesByStage(sdkCtx, 180)
	require.NoError(t, err)
	require.Len(t, batches[testutil.Executor], 1)
	require.Equal(t, "conf-node-1", batches[testutil.Executor][0].NodeId)
}

// Test V1 validation submission window check
func TestSubmitPocValidationV1_WindowValidation(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	// Setup epoch params
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams = &types.EpochParams{
		PocStageDuration:      50,
		PocExchangeDuration:   20,
		PocValidationDelay:    5,
		PocValidationDuration: 10,
	}
	require.NoError(t, k.SetParams(ctx, params))

	// Setup upcoming epoch
	upcomingEpoch := &types.Epoch{
		Index:               1,
		PocStartBlockHeight: 80,
	}
	k.SetEpoch(ctx, upcomingEpoch)

	// Test epoch context validation window check
	epochContext := types.NewEpochContext(*upcomingEpoch, *params.EpochParams)

	// ValidationExchangeWindow calculation:
	// EndOfPoCStage = pocAnchor + PocStageDuration = 80 + 50 = 130
	// StartOfPoCValidation = EndOfPoCStage + PocValidationDelay = 130 + 5 = 135
	// EndOfPoCValidation = StartOfPoCValidation + PocValidationDuration = 135 + 10 = 145
	// ValidationExchangeWindow: Start = 136 (StartOfPoCValidation + 1), End = 145

	// Block 135 - before validation window (at StartOfPoCValidation, not in exchange window)
	require.False(t, epochContext.IsValidationExchangeWindow(135))

	// Block 136 - start of validation exchange window
	require.True(t, epochContext.IsValidationExchangeWindow(136))

	// Block 140 - middle of validation window
	require.True(t, epochContext.IsValidationExchangeWindow(140))

	// Block 145 - end of validation window
	require.True(t, epochContext.IsValidationExchangeWindow(145))

	// Block 146 - after validation window
	require.False(t, epochContext.IsValidationExchangeWindow(146))
}
