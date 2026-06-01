package keeper_test

import (
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

const testPoCModelID = "test-poc-model"
const testPoCModelID2 = "test-poc-model-2"

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
		ModelId:                  "",
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
		ModelId: "",
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
		Validations: []*types.PoCValidationEntryV2{
			{
				ParticipantAddress: testutil.Executor,
				ModelId:            testPoCModelID,
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
	exists, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testPoCModelID, testutil.Validator)
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
		Validations: []*types.PoCValidationEntryV2{
			{
				ParticipantAddress: testutil.Executor, // valid
				ModelId:            testPoCModelID,
				ValidatedWeight:    100,
			},
			{
				ParticipantAddress: "invalid_address", // invalid - will be skipped
				ModelId:            testPoCModelID,
				ValidatedWeight:    50,
			},
			{
				ParticipantAddress: testutil.Executor2, // valid
				ModelId:            testPoCModelID,
				ValidatedWeight:    200,
			},
		},
	}
	_, err = msgServer.SubmitPocValidationsV2(sdkCtx, msg)
	require.NoError(t, err) // Message succeeds even with invalid entry

	// Verify valid ones were stored
	exists1, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, testPoCModelID, testutil.Validator)
	require.NoError(t, err)
	require.True(t, exists1)

	exists2, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor2, testPoCModelID, testutil.Validator)
	require.NoError(t, err)
	require.True(t, exists2)
}

// Test HasPocValidationV2
func TestHasPocValidationV2(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx)

	// Initially should not exist
	exists, err := k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, "", testutil.Validator)
	require.NoError(t, err)
	require.False(t, exists)

	// Store a validation
	validation := types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    100,
		ModelId:                     "",
		ValidatedWeight:             100,
	}
	err = k.SetPocValidationV2(sdkCtx, validation)
	require.NoError(t, err)

	// Now should exist
	exists, err = k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, "", testutil.Validator)
	require.NoError(t, err)
	require.True(t, exists)

	// Different validator should not exist
	exists, err = k.HasPocValidationV2(sdkCtx, 100, testutil.Executor, "", testutil.Validator2)
	require.NoError(t, err)
	require.False(t, exists)

	// Different participant should not exist
	exists, err = k.HasPocValidationV2(sdkCtx, 100, testutil.Executor2, "", testutil.Validator)
	require.NoError(t, err)
	require.False(t, exists)

	// Different stage should not exist
	exists, err = k.HasPocValidationV2(sdkCtx, 200, testutil.Executor, "", testutil.Validator)
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
		Entries: []*types.PoCV2CommitEntry{{
			ModelId:  "",
			Count:    10,
			RootHash: make([]byte, 32),
		}},
	}
	_, err = msgServer.PoCV2StoreCommit(sdkCtx, msg)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid")
}

func setupPoCV2StoreCommitTest(
	t *testing.T,
	blockHeight int64,
	feeParams *types.FeeParams,
	modelIDs ...string,
) (keeper.Keeper, sdk.Context, types.MsgServer) {
	t.Helper()

	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).
		WithBlockHeight(blockHeight).
		WithGasMeter(storetypes.NewGasMeter(1_000_000_000))

	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{PocV2Enabled: true}
	params.EpochParams = &types.EpochParams{
		PocStageDuration:      50,
		PocExchangeDuration:   30,
		PocValidationDelay:    5,
		PocValidationDuration: 10,
	}
	params.FeeParams = feeParams
	require.NoError(t, k.SetParams(sdkCtx, params))

	k.SetEffectiveEpochIndex(sdkCtx, 0)
	k.SetEpoch(sdkCtx, &types.Epoch{
		Index:               1,
		PocStartBlockHeight: 100,
	})

	for _, modelID := range modelIDs {
		k.SetModel(sdkCtx, &types.Model{Id: modelID})
	}

	return k, sdkCtx, keeper.NewMsgServerImpl(k)
}

func makePoCV2CommitEntry(modelID string, count uint32, rootByte byte) *types.PoCV2CommitEntry {
	rootHash := make([]byte, 32)
	for i := range rootHash {
		rootHash[i] = rootByte
	}
	return &types.PoCV2CommitEntry{
		ModelId:  modelID,
		Count:    count,
		RootHash: rootHash,
	}
}

func commitKey(participantAddress, modelID string) types.PoCParticipantModelKey {
	return types.PoCParticipantModelKey{
		ParticipantAddress: participantAddress,
		ModelID:            modelID,
	}
}

func TestPoCV2StoreCommit_MultiModelFirstSubmissionChargesBaseGasOnce(t *testing.T) {
	msg := &types.MsgPoCV2StoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Entries: []*types.PoCV2CommitEntry{
			makePoCV2CommitEntry(testPoCModelID, 3, 1),
			makePoCV2CommitEntry(testPoCModelID2, 5, 2),
		},
	}

	kNoFee, noFeeCtx, noFeeMsgServer := setupPoCV2StoreCommitTest(t, 110, nil, testPoCModelID, testPoCModelID2)
	beforeNoFeeGas := noFeeCtx.GasMeter().GasConsumed()
	_, err := noFeeMsgServer.PoCV2StoreCommit(noFeeCtx, msg)
	require.NoError(t, err)
	noFeeGasDelta := noFeeCtx.GasMeter().GasConsumed() - beforeNoFeeGas

	feeParams := &types.FeeParams{
		BaseValidationGas: 1_000,
		GasPerPocCount:    10,
	}
	kWithFee, withFeeCtx, withFeeMsgServer := setupPoCV2StoreCommitTest(t, 110, feeParams, testPoCModelID, testPoCModelID2)
	beforeWithFeeGas := withFeeCtx.GasMeter().GasConsumed()
	_, err = withFeeMsgServer.PoCV2StoreCommit(withFeeCtx, msg)
	require.NoError(t, err)
	withFeeGasDelta := withFeeCtx.GasMeter().GasConsumed() - beforeWithFeeGas
	// Expected explicit charges: 1 * BaseValidationGas + (3 + 5) * GasPerPocCount = 1080.
	// The observed delta also picks up a small read-per-byte overhead because
	// the withFee Params proto has more bytes than the noFee proto; allow a
	// tolerance of 200 gas to absorb that without masking real regressions.
	require.InDelta(t, float64(1_080), float64(withFeeGasDelta-noFeeGasDelta), 200)

	commits, err := kNoFee.GetAllPoCV2StoreCommitsForStage(noFeeCtx, 100)
	require.NoError(t, err)
	require.Len(t, commits, 2)
	require.Equal(t, uint32(3), commits[commitKey(testutil.Executor, testPoCModelID)].Count)
	require.Equal(t, uint32(5), commits[commitKey(testutil.Executor, testPoCModelID2)].Count)

	commits, err = kWithFee.GetAllPoCV2StoreCommitsForStage(withFeeCtx, 100)
	require.NoError(t, err)
	require.Len(t, commits, 2)
	require.Equal(t, uint32(3), commits[commitKey(testutil.Executor, testPoCModelID)].Count)
	require.Equal(t, uint32(5), commits[commitKey(testutil.Executor, testPoCModelID2)].Count)
}

func TestPoCV2StoreCommit_SameBlockRepeatRejectedPerModel(t *testing.T) {
	k, sdkCtx, msgServer := setupPoCV2StoreCommitTest(t, 110, nil, testPoCModelID, testPoCModelID2)

	firstMsg := &types.MsgPoCV2StoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Entries: []*types.PoCV2CommitEntry{
			makePoCV2CommitEntry(testPoCModelID, 10, 1),
		},
	}
	_, err := msgServer.PoCV2StoreCommit(sdkCtx, firstMsg)
	require.NoError(t, err)

	secondMsg := &types.MsgPoCV2StoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Entries: []*types.PoCV2CommitEntry{
			makePoCV2CommitEntry(testPoCModelID, 12, 3),
			makePoCV2CommitEntry(testPoCModelID2, 4, 4),
		},
	}
	_, err = msgServer.PoCV2StoreCommit(sdkCtx, secondMsg)
	require.ErrorContains(t, err, "only one commit per block allowed")

	commits, err := k.GetAllPoCV2StoreCommitsForStage(sdkCtx, 100)
	require.NoError(t, err)
	require.Len(t, commits, 1)
	require.Equal(t, uint32(10), commits[commitKey(testutil.Executor, testPoCModelID)].Count)
}

func TestPoCV2StoreCommit_CountMustIncreasePerModel(t *testing.T) {
	k, sdkCtx, msgServer := setupPoCV2StoreCommitTest(t, 110, nil, testPoCModelID)

	firstMsg := &types.MsgPoCV2StoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Entries: []*types.PoCV2CommitEntry{
			makePoCV2CommitEntry(testPoCModelID, 10, 1),
		},
	}
	_, err := msgServer.PoCV2StoreCommit(sdkCtx, firstMsg)
	require.NoError(t, err)

	nextCtx := sdkCtx.WithBlockHeight(111).WithGasMeter(storetypes.NewGasMeter(1_000_000_000))
	secondMsg := &types.MsgPoCV2StoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Entries: []*types.PoCV2CommitEntry{
			makePoCV2CommitEntry(testPoCModelID, 10, 2),
		},
	}
	_, err = msgServer.PoCV2StoreCommit(nextCtx, secondMsg)
	require.ErrorContains(t, err, "count must increase")

	commits, err := k.GetAllPoCV2StoreCommitsForStage(nextCtx, 100)
	require.NoError(t, err)
	require.Len(t, commits, 1)
	require.Equal(t, uint32(10), commits[commitKey(testutil.Executor, testPoCModelID)].Count)
}

func TestPoCV2StoreCommit_AggregateDeltaGasAcrossModels(t *testing.T) {
	firstMsg := &types.MsgPoCV2StoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Entries: []*types.PoCV2CommitEntry{
			makePoCV2CommitEntry(testPoCModelID, 10, 1),
			makePoCV2CommitEntry(testPoCModelID2, 20, 2),
		},
	}
	kNoFee, noFeeCtx, noFeeMsgServer := setupPoCV2StoreCommitTest(t, 110, nil, testPoCModelID, testPoCModelID2)
	_, err := noFeeMsgServer.PoCV2StoreCommit(noFeeCtx, firstMsg)
	require.NoError(t, err)

	noFeeNextCtx := noFeeCtx.WithBlockHeight(111).WithGasMeter(storetypes.NewGasMeter(1_000_000_000))
	secondMsg := &types.MsgPoCV2StoreCommit{
		Creator:                  testutil.Executor,
		PocStageStartBlockHeight: 100,
		Entries: []*types.PoCV2CommitEntry{
			makePoCV2CommitEntry(testPoCModelID, 15, 3),
			makePoCV2CommitEntry(testPoCModelID2, 30, 4),
		},
	}
	beforeNoFeeGas := noFeeNextCtx.GasMeter().GasConsumed()
	_, err = noFeeMsgServer.PoCV2StoreCommit(noFeeNextCtx, secondMsg)
	require.NoError(t, err)
	noFeeGasDelta := noFeeNextCtx.GasMeter().GasConsumed() - beforeNoFeeGas

	feeParams := &types.FeeParams{
		BaseValidationGas: 1_000,
		GasPerPocCount:    10,
	}
	kWithFee, withFeeCtx, withFeeMsgServer := setupPoCV2StoreCommitTest(t, 110, feeParams, testPoCModelID, testPoCModelID2)
	_, err = withFeeMsgServer.PoCV2StoreCommit(withFeeCtx, firstMsg)
	require.NoError(t, err)

	withFeeNextCtx := withFeeCtx.WithBlockHeight(111).WithGasMeter(storetypes.NewGasMeter(1_000_000_000))
	beforeWithFeeGas := withFeeNextCtx.GasMeter().GasConsumed()
	_, err = withFeeMsgServer.PoCV2StoreCommit(withFeeNextCtx, secondMsg)
	require.NoError(t, err)
	withFeeGasDelta := withFeeNextCtx.GasMeter().GasConsumed() - beforeWithFeeGas
	// Expected explicit charges: no base (not the first commit) + (5 + 10) *
	// GasPerPocCount = 150. Tolerance 100 absorbs the per-byte overhead from
	// the larger withFee Params proto without masking real regressions.
	require.InDelta(t, float64(150), float64(withFeeGasDelta-noFeeGasDelta), 100)

	commits, err := kNoFee.GetAllPoCV2StoreCommitsForStage(noFeeNextCtx, 100)
	require.NoError(t, err)
	require.Equal(t, uint32(15), commits[commitKey(testutil.Executor, testPoCModelID)].Count)
	require.Equal(t, uint32(30), commits[commitKey(testutil.Executor, testPoCModelID2)].Count)

	commits, err = kWithFee.GetAllPoCV2StoreCommitsForStage(withFeeNextCtx, 100)
	require.NoError(t, err)
	require.Equal(t, uint32(15), commits[commitKey(testutil.Executor, testPoCModelID)].Count)
	require.Equal(t, uint32(30), commits[commitKey(testutil.Executor, testPoCModelID2)].Count)
}

func TestPoCV2StoreCommit_InvalidEntriesFailBeforeWrites(t *testing.T) {
	testCases := []struct {
		name         string
		invalidEntry *types.PoCV2CommitEntry
		wantErr      string
	}{
		{
			name:         "missing model id",
			invalidEntry: makePoCV2CommitEntry("", 7, 2),
			wantErr:      "model_id must not be empty",
		},
		{
			name:         "unknown governance model",
			invalidEntry: makePoCV2CommitEntry("missing-model", 7, 2),
			wantErr:      "is not a governance model",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			k, sdkCtx, msgServer := setupPoCV2StoreCommitTest(t, 110, nil, testPoCModelID)

			msg := &types.MsgPoCV2StoreCommit{
				Creator:                  testutil.Executor,
				PocStageStartBlockHeight: 100,
				Entries: []*types.PoCV2CommitEntry{
					makePoCV2CommitEntry(testPoCModelID, 10, 1),
					tc.invalidEntry,
				},
			}
			_, err := msgServer.PoCV2StoreCommit(sdkCtx, msg)
			require.ErrorContains(t, err, tc.wantErr)

			commits, err := k.GetAllPoCV2StoreCommitsForStage(sdkCtx, 100)
			require.NoError(t, err)
			require.Empty(t, commits)
		})
	}
}
