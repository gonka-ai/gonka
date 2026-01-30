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

// Test SetPocValidationV2 error handling (no panic)
func TestSetPocValidationV2_InvalidAddress(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Test invalid participant address - should return error, not panic
	validation := types.PoCValidationV2{
		ParticipantAddress:          "invalid_address",
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    100,
		ValidatedWeight:             100,
	}
	err := k.SetPocValidationV2(sdkCtx, validation)
	require.Error(t, err)

	// Test invalid validator address - should return error, not panic
	validation2 := types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: "invalid_validator",
		PocStageStartBlockHeight:    100,
		ValidatedWeight:             100,
	}
	err = k.SetPocValidationV2(sdkCtx, validation2)
	require.Error(t, err)
}

// Test SetPoCV2StoreCommit error handling (no panic)
func TestSetPoCV2StoreCommit_InvalidAddress(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Test invalid address - should return error, not panic
	commit := types.PoCV2StoreCommit{
		ParticipantAddress:       "invalid_address",
		PocStageStartBlockHeight: 100,
		Count:                    10,
		RootHash:                 make([]byte, 32),
		CommitBlockHeight:        100,
	}
	err := k.SetPoCV2StoreCommit(sdkCtx, commit)
	require.Error(t, err)
}

// Test SetMLNodeWeightDistribution error handling (no panic)
func TestSetMLNodeWeightDistribution_InvalidAddress(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Test invalid address - should return error, not panic
	distribution := types.MLNodeWeightDistribution{
		ParticipantAddress:       "invalid_address",
		PocStageStartBlockHeight: 100,
		Weights: []*types.MLNodeWeight{
			{NodeId: "node-1", Weight: 10},
		},
	}
	err := k.SetMLNodeWeightDistribution(sdkCtx, distribution)
	require.Error(t, err)
}

// Test SubmitPocValidationsV2 duplicate handling (skip, don't fail)
func TestSubmitPocValidationsV2_DuplicateSkipped(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	// Block height must be in validation exchange window:
	// PocStart=100, EndOfGen=150, StartOfValidation=155, ValidationWindow=156-255
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(160)

	// Setup params with V2 enabled
	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{PocV2Enabled: true}
	params.EpochParams = &types.EpochParams{
		PocStageDuration:      50,
		PocExchangeDuration:   20,
		PocValidationDelay:    5,
		PocValidationDuration: 100,
	}
	require.NoError(t, k.SetParams(sdkCtx, params))

	// Setup epochs properly: GetUpcomingEpoch uses (effectiveIndex + 1)
	k.SetEffectiveEpochIndex(sdkCtx, 0)
	upcomingEpoch := &types.Epoch{
		Index:               1,
		PocStartBlockHeight: 100,
	}
	k.SetEpoch(sdkCtx, upcomingEpoch)

	msgServer := keeper.NewMsgServerImpl(k)

	// First submission should succeed
	msg := &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.PoCValidationPayloadV2{
			{
				ParticipantAddress: testutil.Executor,
				ValidatedWeight:    100,
			},
		},
	}
	_, err = msgServer.SubmitPocValidationsV2(sdkCtx, msg)
	require.NoError(t, err)

	// Second submission with same (stage, participant, validator) should succeed (skip duplicate)
	_, err = msgServer.SubmitPocValidationsV2(sdkCtx, msg)
	require.NoError(t, err) // No error - duplicate is skipped, not rejected

	// Verify only one validation exists
	exists, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testutil.Validator)
	require.NoError(t, err)
	require.True(t, exists)
}

// Test SubmitPocValidationsV2 partial success (valid + invalid in same batch)
func TestSubmitPocValidationsV2_PartialSuccess(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(160)

	// Setup params with V2 enabled
	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{PocV2Enabled: true}
	params.EpochParams = &types.EpochParams{
		PocStageDuration:      50,
		PocExchangeDuration:   20,
		PocValidationDelay:    5,
		PocValidationDuration: 100,
	}
	require.NoError(t, k.SetParams(sdkCtx, params))

	k.SetEffectiveEpochIndex(sdkCtx, 0)
	upcomingEpoch := &types.Epoch{
		Index:               1,
		PocStartBlockHeight: 100,
	}
	k.SetEpoch(sdkCtx, upcomingEpoch)

	msgServer := keeper.NewMsgServerImpl(k)

	// Batch with valid and invalid participant addresses
	msg := &types.MsgSubmitPocValidationsV2{
		Creator:                  testutil.Validator,
		PocStageStartBlockHeight: 100,
		Validations: []*types.PoCValidationPayloadV2{
			{
				ParticipantAddress: testutil.Executor, // valid
				ValidatedWeight:    100,
			},
			{
				ParticipantAddress: "invalid_address", // invalid - will be skipped
				ValidatedWeight:    50,
			},
			{
				ParticipantAddress: testutil.Executor2, // valid
				ValidatedWeight:    200,
			},
		},
	}
	_, err = msgServer.SubmitPocValidationsV2(sdkCtx, msg)
	require.NoError(t, err) // Message succeeds even with invalid entry

	// Verify valid ones were stored
	exists1, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testutil.Validator)
	require.NoError(t, err)
	require.True(t, exists1)

	exists2, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor2, testutil.Validator)
	require.NoError(t, err)
	require.True(t, exists2)
}

// Test HasPocValidationV2
func TestHasPocValidationV2(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Initially should not exist
	exists, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testutil.Validator)
	require.NoError(t, err)
	require.False(t, exists)

	// Store a validation
	validation := types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    100,
		ValidatedWeight:             100,
	}
	err = k.SetPocValidationV2(sdkCtx, validation)
	require.NoError(t, err)

	// Now should exist
	exists, err = k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testutil.Validator)
	require.NoError(t, err)
	require.True(t, exists)

	// Different validator should not exist
	exists, err = k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testutil.Validator2)
	require.NoError(t, err)
	require.False(t, exists)

	// Different participant should not exist
	exists, err = k.HasPocValidationV2(sdkCtx, 100, testutil.Executor2, testutil.Validator)
	require.NoError(t, err)
	require.False(t, exists)

	// Different stage should not exist
	exists, err = k.HasPocValidationV2(sdkCtx, 200, testutil.Executor, testutil.Validator)
	require.NoError(t, err)
	require.False(t, exists)
}

// Test PoCV2StoreCommit error handling in msg handler
func TestPoCV2StoreCommit_InvalidCreatorAddress(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(110)

	// Setup params with V2 enabled
	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{PocV2Enabled: true}
	params.EpochParams = &types.EpochParams{
		PocStageDuration:      50,
		PocExchangeDuration:   30,
		PocValidationDelay:    5,
		PocValidationDuration: 10,
	}
	require.NoError(t, k.SetParams(sdkCtx, params))

	// Setup epochs properly: GetUpcomingEpoch uses (effectiveIndex + 1)
	k.SetEffectiveEpochIndex(sdkCtx, 0)
	upcomingEpoch := &types.Epoch{
		Index:               1,
		PocStartBlockHeight: 100,
	}
	k.SetEpoch(sdkCtx, upcomingEpoch)

	msgServer := keeper.NewMsgServerImpl(k)

	// Test with invalid creator address
	msg := &types.MsgPoCV2StoreCommit{
		Creator:                  "invalid_address",
		PocStageStartBlockHeight: 100,
		Count:                    10,
		RootHash:                 make([]byte, 32),
	}
	_, err = msgServer.PoCV2StoreCommit(sdkCtx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid")
}
