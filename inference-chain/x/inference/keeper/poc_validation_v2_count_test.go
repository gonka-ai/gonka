package keeper_test

import (
	"testing"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func openRegularValidationWindow(t *testing.T, k keeper.Keeper, ctx sdk.Context) sdk.Context {
	t.Helper()
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams = &types.EpochParams{
		PocStageDuration:      50,
		PocExchangeDuration:   20,
		PocValidationDelay:    5,
		PocValidationDuration: 100,
	}
	require.NoError(t, k.SetParams(ctx, params))
	k.SetEffectiveEpochIndex(ctx, 0)
	k.SetEpoch(ctx, &types.Epoch{Index: 1, PocStartBlockHeight: 100})
	return ctx.WithBlockHeight(160)
}

func TestPocValidationV2Count_TracksSetsRebuildAndPrune(t *testing.T) {
	k, ctx := auditKeeper(t)
	participant := sdk.MustAccAddressFromBech32(testutil.Executor)
	validator := sdk.MustAccAddressFromBech32(testutil.Validator)
	other := sdk.MustAccAddressFromBech32(testutil.Executor2)

	require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    100,
		ModelId:                     "m",
		ValidatedWeight:             1,
	}))
	require.NoError(t, k.SetPocValidationV2(ctx, types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor2,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    100,
		ModelId:                     "m",
		ValidatedWeight:             1,
	}))
	count, err := k.CountPocValidationsV2ForValidator(ctx, 100, validator)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	legacyKey := collections.Join3(int64(4), participant, collections.Join("m", validator))
	require.NoError(t, k.PoCValidationsV2.Set(ctx, legacyKey, types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    4,
		ModelId:                     "m",
		ValidatedWeight:             1,
	}))
	legacyCount, err := k.CountPocValidationsV2ForValidator(ctx, 4, validator)
	require.NoError(t, err)
	require.Equal(t, 0, legacyCount)

	openCtx := openRegularValidationWindow(t, k, ctx)
	require.NoError(t, k.RebuildPocValidationV2Counts(openCtx))
	legacyCount, err = k.CountPocValidationsV2ForValidator(openCtx, 4, validator)
	require.NoError(t, err)
	require.Equal(t, 0, legacyCount)
	count, err = k.CountPocValidationsV2ForValidator(openCtx, 100, validator)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	require.NoError(t, k.RebuildPocValidationV2Counts(openCtx))
	count, err = k.CountPocValidationsV2ForValidator(ctx, 100, validator)
	require.NoError(t, err)
	require.Equal(t, 2, count)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	pruner := k.GetPoCValidationsV2Pruner(params)
	require.NoError(t, pruner.Remover(ctx, collections.Join3(int64(100), other, collections.Join("m", validator))))
	count, err = k.CountPocValidationsV2ForValidator(ctx, 100, validator)
	require.NoError(t, err)
	require.Equal(t, 1, count)
}

func TestPocValidationV2Count_RebuildSkipsFinishedConfirmationStages(t *testing.T) {
	k, ctx := auditKeeper(t)
	validator := sdk.MustAccAddressFromBech32(testutil.Validator)
	openCtx := openRegularValidationWindow(t, k, ctx)

	require.NoError(t, k.SetPocValidationV2(openCtx, types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    100,
		ModelId:                     "m",
		ValidatedWeight:             1,
	}))
	require.NoError(t, k.SetPocValidationV2(openCtx, types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    50,
		ModelId:                     "m",
		ValidatedWeight:             1,
	}))
	require.NoError(t, k.SetPocValidationV2(openCtx, types.PoCValidationV2{
		ParticipantAddress:          testutil.Executor,
		ValidatorParticipantAddress: testutil.Validator,
		PocStageStartBlockHeight:    77,
		ModelId:                     "m",
		ValidatedWeight:             1,
	}))
	require.NoError(t, k.SetConfirmationPoCEvent(openCtx, types.ConfirmationPoCEvent{
		EpochIndex: 1, EventSequence: 1, TriggerHeight: 50,
		Phase: types.ConfirmationPoCPhase_CONFIRMATION_POC_COMPLETED,
	}))
	active := types.ConfirmationPoCEvent{
		EpochIndex: 1, EventSequence: 2, TriggerHeight: 77,
		GenerationStartHeight: 86,
		Phase:                 types.ConfirmationPoCPhase_CONFIRMATION_POC_VALIDATION,
	}
	require.NoError(t, k.SetConfirmationPoCEvent(openCtx, active))
	require.NoError(t, k.SetActiveConfirmationPoCEvent(openCtx, active))

	require.NoError(t, k.RebuildPocValidationV2Counts(openCtx))

	regular, err := k.CountPocValidationsV2ForValidator(openCtx, 100, validator)
	require.NoError(t, err)
	require.Equal(t, 1, regular)
	finished, err := k.CountPocValidationsV2ForValidator(openCtx, 50, validator)
	require.NoError(t, err)
	require.Equal(t, 0, finished)
	inProgress, err := k.CountPocValidationsV2ForValidator(openCtx, 77, validator)
	require.NoError(t, err)
	require.Equal(t, 1, inProgress)
	hasValidation, err := k.HasPocValidationV2(openCtx, 50, testutil.Executor, "m", testutil.Validator)
	require.NoError(t, err)
	require.True(t, hasValidation)
}
