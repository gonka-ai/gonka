package v0_2_17

import (
	"testing"

	wasmtypes "github.com/CosmWasm/wasmd/x/wasm/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.17", UpgradeName)
}

func TestEnableWasmFeeGroup_AddsWasmBesideExistingGroups(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = types.DefaultFeeParams()
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, enableWasmFeeGroup(ctx, k))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{types.FeeGroupEpoch, types.FeeGroupCosmos, types.FeeGroupWasm}, updated.FeeParams.EnabledFeeGroups)
	require.Equal(t, uint64(1), updated.FeeParams.GroupByName(types.FeeGroupEpoch).MinGasPrice)
	require.Equal(t, uint64(1), updated.FeeParams.GroupByName(types.FeeGroupCosmos).MinGasPrice)
	require.Equal(t, uint64(wasmMinGasPriceNgonka), updated.FeeParams.GroupByName(types.FeeGroupWasm).MinGasPrice)
	require.NoError(t, updated.FeeParams.Validate())

	for _, msg := range []sdk.Msg{
		&wasmtypes.MsgStoreCode{},
		&wasmtypes.MsgInstantiateContract{},
		&wasmtypes.MsgExecuteContract{},
		&wasmtypes.MsgMigrateContract{},
	} {
		require.Equal(t, uint64(wasmMinGasPriceNgonka), updated.FeeParams.EnabledPayingPrice([]sdk.Msg{msg}, types.IsNetworkDuty))
	}
	require.Equal(t, uint64(1), updated.FeeParams.EnabledPayingPrice([]sdk.Msg{&types.MsgPoCV2StoreCommit{}}, types.IsNetworkDuty))
}

func TestEnableWasmFeeGroup_IdempotentAndKeepsExistingPrice(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	fp := types.DefaultFeeParams()
	fp.Groups = append(fp.Groups, &types.FeeGroup{Name: types.FeeGroupWasm, MinGasPrice: 4})
	fp.EnabledFeeGroups = append(fp.EnabledFeeGroups, types.FeeGroupWasm)
	params.FeeParams = fp
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, enableWasmFeeGroup(ctx, k))
	require.NoError(t, enableWasmFeeGroup(ctx, k))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{types.FeeGroupEpoch, types.FeeGroupCosmos, types.FeeGroupWasm}, updated.FeeParams.EnabledFeeGroups)
	require.Equal(t, uint64(4), updated.FeeParams.GroupByName(types.FeeGroupWasm).MinGasPrice)
}

func TestEnableWasmFeeGroup_NilParamsGetsDefaultsPlusWasm(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = nil
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, enableWasmFeeGroup(ctx, k))

	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{types.FeeGroupEpoch, types.FeeGroupCosmos, types.FeeGroupWasm}, updated.FeeParams.EnabledFeeGroups)
	require.Equal(t, uint64(wasmMinGasPriceNgonka), updated.FeeParams.GroupByName(types.FeeGroupWasm).MinGasPrice)
	require.NoError(t, updated.FeeParams.Validate())
}

func TestMigrateEpochValidationParams(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochValidationParams = nil
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migrateEpochValidationParams(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, types.DefaultEpochValidationParams(), got.EpochValidationParams)

	got.EpochValidationParams.MaxValidationsPerMessage = 64
	require.NoError(t, k.SetParams(ctx, got))
	require.NoError(t, migrateEpochValidationParams(ctx, k))
	again, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, uint32(64), again.EpochValidationParams.MaxValidationsPerMessage)
}

// A post-v0.2.16 model keeps seq_len and stat_test, stores the controller on
// the model, and has a cleared weight_scale_factor. v0.2.17 moves both onto
// one PREFILL block and leaves the scheme selector alone.
func TestMigratePocSchemeBlocksFromDynamicCoefficient(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	stat := &types.PoCStatTestParams{DistThreshold: types.DecimalFromFloat(0.41)}
	coefficient := &types.DynamicCoefficientModelConfig{
		CoeffMin:           &types.Decimal{Value: 15, Exponent: -1},
		CoeffMax:           &types.Decimal{Value: 15, Exponent: -1},
		RelativeDifficulty: &types.Decimal{Value: 1, Exponent: 0},
		TargetShareBps:     10000,
	}
	params.PocParams.PocScheme = types.PocScheme_POC_SCHEME_PREFILL
	params.PocParams.DynamicCoefficientParams = &types.DynamicCoefficientParams{
		TargetZoneBps:     500,
		StepMin:           types.DecimalFromFloat(0.005),
		StepMax:           types.DecimalFromFloat(0.05),
		BootstrapStepMax:  types.DecimalFromFloat(0.25),
		BootstrapShareBps: 100,
	}
	params.PocParams.Models = []*types.PoCModelConfig{{
		ModelId:            "model-a",
		SeqLen:             128,
		StatTest:           stat,
		PenaltyStartEpoch:  4,
		DynamicCoefficient: coefficient,
	}}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migratePocSchemeBlocks(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, types.PocScheme_POC_SCHEME_PREFILL, got.PocParams.PocScheme)
	model := got.PocParams.Models[0]
	require.Equal(t, int64(0), model.SeqLen)
	require.Nil(t, model.StatTest)
	require.Nil(t, model.WeightScaleFactor)
	require.Nil(t, model.DynamicCoefficient)
	require.Equal(t, uint64(4), model.PenaltyStartEpoch)
	require.Len(t, model.Schemes, 2)
	block := model.Schemes[0]
	require.Equal(t, types.PocScheme_POC_SCHEME_PREFILL, block.Scheme)
	require.Equal(t, int64(128), block.SeqLen)
	require.Equal(t, int64(0), block.DecodeSteps)
	require.Equal(t, stat.DistThreshold, block.StatTest.DistThreshold)
	require.Equal(t, "0.010000000000000000", decimalString(t, block.StatTest.PValueThreshold))
	require.Equal(t, coefficient, block.DynamicCoefficient)
	require.NoError(t, got.Validate())
}

// Flat pre-decode params have no dynamic controller. The recipe moves onto the
// PREFILL block and the static weight_scale_factor stays on the model.
func TestMigratePocSchemeBlocksFromFlatWeight(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	weight := types.DecimalFromFloat(1.5)
	params.PocParams.DynamicCoefficientParams = nil
	params.PocParams.Models = []*types.PoCModelConfig{{
		ModelId:           "model-a",
		SeqLen:            128,
		WeightScaleFactor: weight,
	}}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migratePocSchemeBlocks(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	model := got.PocParams.Models[0]
	require.Equal(t, weight, model.WeightScaleFactor)
	require.Nil(t, model.DynamicCoefficient)
	require.Equal(t, int64(0), model.SeqLen)
	require.Len(t, model.Schemes, 2)
	require.Nil(t, model.Schemes[0].DynamicCoefficient)
	require.Equal(t, int64(128), model.Schemes[0].SeqLen)
	require.NoError(t, got.Validate())
}

func TestMigratePocSchemeBlocksRejectsMissingRecipe(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.DynamicCoefficientParams = nil
	params.PocParams.Models = []*types.PoCModelConfig{{
		ModelId:           "model-a",
		WeightScaleFactor: types.DecimalFromFloat(1),
	}}
	require.NoError(t, k.SetParams(ctx, params))

	err = migratePocSchemeBlocks(ctx, k)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid prefill seq_len")

	got, getErr := k.GetParams(ctx)
	require.NoError(t, getErr)
	require.Empty(t, got.PocParams.Models[0].Schemes)
}

func TestPostV016ParamsRoundTripModelCoefficient(t *testing.T) {
	stat := &types.PoCStatTestParams{DistThreshold: types.DecimalFromFloat(0.41)}
	coefficient := &types.DynamicCoefficientModelConfig{
		CoeffMin:           &types.Decimal{Value: 15, Exponent: -1},
		CoeffMax:           &types.Decimal{Value: 15, Exponent: -1},
		RelativeDifficulty: &types.Decimal{Value: 1, Exponent: 0},
		TargetShareBps:     10000,
	}
	src := &types.PocParams{
		Models: []*types.PoCModelConfig{{
			ModelId:            "model-a",
			SeqLen:             128,
			StatTest:           stat,
			DynamicCoefficient: coefficient,
		}},
		DynamicCoefficientParams: &types.DynamicCoefficientParams{
			TargetZoneBps:     500,
			StepMin:           types.DecimalFromFloat(0.005),
			StepMax:           types.DecimalFromFloat(0.05),
			BootstrapStepMax:  types.DecimalFromFloat(0.25),
			BootstrapShareBps: 100,
		},
	}
	bz, err := src.Marshal()
	require.NoError(t, err)

	var decoded types.PocParams
	require.NoError(t, decoded.Unmarshal(bz))
	require.Equal(t, int64(128), decoded.Models[0].SeqLen)
	require.Equal(t, coefficient, decoded.Models[0].DynamicCoefficient)
	require.Empty(t, decoded.Models[0].Schemes)
	require.NotNil(t, decoded.DynamicCoefficientParams)
}

func TestMigratePocSchemeBlocksInstallsDecodeConfig(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	cases := []struct {
		id, minimum, maximum, difficulty, tau string
		share                                 uint32
	}{
		{"MiniMaxAI/MiniMax-M2.7", "0.302400000000000000", "0.302400000000000000", "1.000000000000000000", "0.039500000000000000", 3334},
		{"deepseek-ai/DeepSeek-V4-Flash-0731", "0.201246191645400000", "0.672672000000000000", "1.925925925925000000", "0.027000000000000000", 3333},
		{"zai-org/GLM-5.3-Flash", "0.810962376237000000", "1.911804631578600000", "5.473684210526000000", "0.017000000000000000", 3333},
	}
	params.PocParams.Models = nil
	params.PocParams.PocScheme = types.PocScheme_POC_SCHEME_PREFILL
	for _, tc := range cases {
		params.PocParams.Models = append(params.PocParams.Models, &types.PoCModelConfig{
			ModelId: tc.id, SeqLen: 1024, StatTest: types.DefaultPoCStatTestParams(),
			DynamicCoefficient: &types.DynamicCoefficientModelConfig{
				CoeffMin: types.DecimalFromFloat(1), CoeffMax: types.DecimalFromFloat(2),
				RelativeDifficulty: types.DecimalFromFloat(1), TargetShareBps: tc.share,
			},
		})
	}
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, migratePocSchemeBlocks(ctx, k))
	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NoError(t, got.Validate())
	require.Equal(t, types.PocScheme_POC_SCHEME_PREFILL, got.PocParams.PocScheme)
	for i, tc := range cases {
		model := got.PocParams.Models[i]
		prefill, ok := model.SchemeParams(types.PocScheme_POC_SCHEME_PREFILL)
		require.True(t, ok)
		require.Equal(t, params.PocParams.Models[i].DynamicCoefficient, prefill.DynamicCoefficient)
		require.Equal(t, params.PocParams.Models[i].StatTest.DistThreshold, prefill.StatTest.DistThreshold)
		require.Equal(t, params.PocParams.Models[i].StatTest.PMismatch, prefill.StatTest.PMismatch)
		require.Equal(t, "0.010000000000000000", decimalString(t, prefill.StatTest.PValueThreshold))
		decode, ok := model.SchemeParams(types.PocScheme_POC_SCHEME_DECODE)
		require.True(t, ok)
		require.Equal(t, int64(256), decode.SeqLen)
		require.Equal(t, int64(256), decode.DecodeSteps)
		for _, value := range []struct {
			got  *types.Decimal
			want string
		}{
			{decode.StatTest.DistThreshold, tc.tau},
			{decode.StatTest.PMismatch, "0.100000000000000000"},
			{decode.StatTest.PValueThreshold, "0.010000000000000000"},
			{decode.DynamicCoefficient.CoeffMin, tc.minimum},
			{decode.DynamicCoefficient.CoeffMax, tc.maximum},
			{decode.DynamicCoefficient.RelativeDifficulty, tc.difficulty},
		} {
			actual, err := value.got.ToLegacyDec()
			require.NoError(t, err)
			require.Equal(t, value.want, actual.String(), tc.id)
		}
		require.Equal(t, tc.share, decode.DynamicCoefficient.TargetShareBps)
	}
}

func TestMigrateLegacyPruningLimits(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams.InferencePruningMax = 0
	params.EpochParams.PocPruningMax = -1
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migrateLegacyPruningLimits(ctx, k))
	migrated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, types.DefaultEpochParams().InferencePruningMax, migrated.EpochParams.InferencePruningMax)
	require.Equal(t, types.DefaultEpochParams().PocPruningMax, migrated.EpochParams.PocPruningMax)
	require.NoError(t, migrated.Validate())

	// The migration is idempotent and preserves already-positive limits.
	migrated.EpochParams.InferencePruningMax = 123
	migrated.EpochParams.PocPruningMax = 456
	require.NoError(t, k.SetParams(ctx, migrated))
	require.NoError(t, migrateLegacyPruningLimits(ctx, k))
	again, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(123), again.EpochParams.InferencePruningMax)
	require.Equal(t, int64(456), again.EpochParams.PocPruningMax)
}

func decimalString(t *testing.T, value *types.Decimal) string {
	t.Helper()
	actual, err := value.ToLegacyDec()
	require.NoError(t, err)
	return actual.String()
}
