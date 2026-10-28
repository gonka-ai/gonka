package keeper_test

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	storetypes "cosmossdk.io/store/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/testutil"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func setupPocValidationsReads(t *testing.T, models ...string) (keeper.Keeper, sdk.Context, []string) {
	t.Helper()
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	sdkCtx := sdk.UnwrapSDKContext(ctx).WithBlockHeight(160)
	params, err := k.GetParams(sdkCtx)
	require.NoError(t, err)
	params.PocParams = &types.PocParams{PocV2Enabled: true}
	params.EpochParams = &types.EpochParams{PocStageDuration: 50, PocExchangeDuration: 20, PocValidationDelay: 5, PocValidationDuration: 100}
	require.NoError(t, k.SetParams(sdkCtx, params))
	k.SetEffectiveEpochIndex(sdkCtx, 0)
	k.SetEpoch(sdkCtx, &types.Epoch{Index: 1, PocStartBlockHeight: 100})
	registerPoCParticipant(t, k, sdkCtx, testutil.Validator)
	targets := []string{testutil.Executor, testutil.Executor2}
	for _, m := range models {
		for _, target := range targets {
			seedPocValidationAdmission(t, k, sdkCtx, target, m, 100)
		}
	}
	return k, sdkCtx, targets
}

// Admission checks only that the target is a participant; its record is never read.
func TestSubmitPocValidationsV2DoesNotReadTargetParticipant(t *testing.T) {
	k, ctx, targets := setupPocValidationsReads(t, testPoCModelID)
	msg := &types.MsgSubmitPocValidationsV2{Creator: testutil.Validator, PocStageStartBlockHeight: 100}
	for _, target := range targets {
		msg.Validations = append(msg.Validations, &types.PoCValidationEntryV2{ParticipantAddress: target, ModelId: testPoCModelID, ValidatedWeight: 10})
	}

	var trace bytes.Buffer
	ctx.MultiStore().SetTracer(&trace)
	_, err := keeper.NewMsgServerImpl(k).SubmitPocValidationsV2(ctx, msg)
	ctx.MultiStore().SetTracer(nil)
	require.NoError(t, err)

	for _, target := range targets {
		key := append(append([]byte{}, types.ParticipantsPrefix...), sdk.MustAccAddressFromBech32(target)...)
		enc := base64.StdEncoding.EncodeToString(key)
		for _, line := range strings.Split(trace.String(), "\n") {
			require.False(t, strings.Contains(line, `"operation":"read"`) && strings.Contains(line, `"key":"`+enc+`"`), "participant %s read", target)
		}
		exists, err := k.HasPocValidationV2(ctx, 100, target, testPoCModelID, testutil.Validator)
		require.NoError(t, err)
		require.True(t, exists)
	}
}

// The governance-model lookup runs once per distinct model in a batch.
func TestSubmitPocValidationsV2ChecksEachModelOnce(t *testing.T) {
	gas := func(models ...string) uint64 {
		k, ctx, targets := setupPocValidationsReads(t, "model-a", "model-b")
		msg := &types.MsgSubmitPocValidationsV2{Creator: testutil.Validator, PocStageStartBlockHeight: 100}
		for i, target := range targets {
			msg.Validations = append(msg.Validations, &types.PoCValidationEntryV2{ParticipantAddress: target, ModelId: models[i], ValidatedWeight: 10})
		}
		ctx = ctx.WithGasMeter(storetypes.NewInfiniteGasMeter())
		_, err := keeper.NewMsgServerImpl(k).SubmitPocValidationsV2(ctx, msg)
		require.NoError(t, err)
		return ctx.GasMeter().GasConsumed()
	}
	sameModel := gas("model-a", "model-a")
	twoModels := gas("model-a", "model-b")
	require.Equal(t, storetypes.KVGasConfig().HasCost, twoModels-sameModel)
}
