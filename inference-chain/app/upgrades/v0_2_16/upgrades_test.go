package v0_2_16

import (
	"context"
	"math"
	"testing"
	"time"

	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	sdk "github.com/cosmos/cosmos-sdk/types"
	authz "github.com/cosmos/cosmos-sdk/x/authz"
	keepertest "github.com/productscience/inference/testutil/keeper"
	coefficient "github.com/productscience/inference/x/inference/coefficients"
	"github.com/productscience/inference/x/inference/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.16", UpgradeName)
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
	require.Equal(t, inferencetypes.DefaultEpochParams().InferencePruningMax, migrated.EpochParams.InferencePruningMax)
	require.Equal(t, inferencetypes.DefaultEpochParams().PocPruningMax, migrated.EpochParams.PocPruningMax)
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

func TestMigrateDynamicCoefficientParams(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{EpochIndex: 1})
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.DynamicCoefficientParams = nil
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{
		{ModelId: "model-c", WeightScaleFactor: inferencetypes.DecimalFromFloat(3)},
		{ModelId: "model-a", WeightScaleFactor: inferencetypes.DecimalFromFloat(1)},
		{ModelId: "model-b", WeightScaleFactor: inferencetypes.DecimalFromFloat(2)},
		{ModelId: "disabled", WeightScaleFactor: &inferencetypes.Decimal{Value: 0, Exponent: 0}},
	}
	params.DelegationParams.InitialModelId = "model-b"
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migrateDynamicCoefficientParams(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.PocParams.DynamicCoefficientParams)
	require.Equal(t, uint32(500), got.PocParams.DynamicCoefficientParams.TargetZoneBps)

	targets := make(map[string]uint32)
	expectedBounds := map[string][2]*inferencetypes.Decimal{
		"model-a": {{Value: 9, Exponent: -1}, {Value: 11, Exponent: -1}},
		"model-b": {{Value: 18, Exponent: -1}, {Value: 22, Exponent: -1}},
		"model-c": {{Value: 27, Exponent: -1}, {Value: 33, Exponent: -1}},
	}
	for _, model := range got.PocParams.Models {
		require.Nil(t, model.WeightScaleFactor)
		if model.ModelId == "disabled" {
			require.Nil(t, model.DynamicCoefficient)
			continue
		}
		require.NotNil(t, model.DynamicCoefficient)
		require.Equal(t, expectedBounds[model.ModelId][0], model.DynamicCoefficient.CoeffMin)
		require.Equal(t, expectedBounds[model.ModelId][1], model.DynamicCoefficient.CoeffMax)
		require.Equal(t, &inferencetypes.Decimal{Value: 1, Exponent: 0}, model.DynamicCoefficient.RelativeDifficulty)
		targets[model.ModelId] = model.DynamicCoefficient.TargetShareBps
	}
	require.Equal(t, uint32(3333), targets["model-a"])
	require.Equal(t, uint32(3334), targets["model-b"])
	require.Equal(t, uint32(3333), targets["model-c"])
	require.NoError(t, got.Validate())

	seeded, found := k.GetEpochGroupData(ctx, 1, "")
	require.True(t, found)
	require.Len(t, seeded.ConfirmationWeightScales, 3)
	frozen, err := coefficient.Freeze(got.PocParams)
	require.NoError(t, err)
	totals := map[string]int64{"model-a": 100, "model-b": 100, "model-c": 100}
	result, err := coefficient.Calculate(frozen.Params, frozen.Scales, seeded.ConfirmationWeightScales, totals, totals, nil, true)
	require.NoError(t, err)
	for i, scale := range result.Scales {
		current := &inferencetypes.Decimal{Value: int64(i + 1), Exponent: 0}
		require.Equal(t, current, seeded.ConfirmationWeightScales[i].BaseCoefficient)
		require.Equal(t, &inferencetypes.Decimal{}, seeded.ConfirmationWeightScales[i].EffectiveCoefficient)
		require.True(t, seeded.ConfirmationWeightScales[i].ExcludeFromConfirmation)
		require.Equal(t, current, scale.BaseCoefficient)
		require.Equal(t, &inferencetypes.Decimal{Value: 25, Exponent: -3}, scale.AdaptiveStep)
	}

	// The migration is idempotent once the global block exists.
	require.NoError(t, migrateDynamicCoefficientParams(ctx, k))
	again, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, got.PocParams, again.PocParams)
	againState, found := k.GetEpochGroupData(ctx, 1, "")
	require.True(t, found)
	require.Equal(t, seeded, againState)
}

func TestMigrateDynamicCoefficientParamsPreservesLegacyPrecision(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{EpochIndex: 1})
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.DynamicCoefficientParams = nil
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{{
		ModelId:           "model-a",
		WeightScaleFactor: &inferencetypes.Decimal{Value: 1234567890123, Exponent: -13},
	}}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migrateDynamicCoefficientParams(ctx, k))

	got, getErr := k.GetParams(ctx)
	require.NoError(t, getErr)
	require.NotNil(t, got.PocParams.DynamicCoefficientParams)
	require.Nil(t, got.PocParams.Models[0].WeightScaleFactor)
	require.Equal(t,
		&inferencetypes.Decimal{Value: 11111111011107, Exponent: -14},
		got.PocParams.Models[0].DynamicCoefficient.CoeffMin,
	)
	require.Equal(t,
		&inferencetypes.Decimal{Value: 13580246791353, Exponent: -14},
		got.PocParams.Models[0].DynamicCoefficient.CoeffMax,
	)
}

func TestMigrateDynamicCoefficientParamsMeasuredModels(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{EpochIndex: 1})
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.DynamicCoefficientParams = nil
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{
		{ModelId: "MiniMaxAI/MiniMax-M2.7", WeightScaleFactor: &inferencetypes.Decimal{Value: 3024, Exponent: -4}},
		{ModelId: "zai-org/GLM-5.3-Flash", WeightScaleFactor: &inferencetypes.Decimal{Value: 62, Exponent: -2}},
		{ModelId: "deepseek-ai/DeepSeek-V4-Flash-0731", WeightScaleFactor: &inferencetypes.Decimal{Value: 246, Exponent: -3}},
	}
	params.DelegationParams.InitialModelId = "MiniMaxAI/MiniMax-M2.7"
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, migrateDynamicCoefficientParams(ctx, k))
	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NoError(t, got.Validate())

	expected := map[string][3]string{
		"MiniMaxAI/MiniMax-M2.7":             {"0.302400000000000000", "0.302400000000000000", "1.000000000000000000"},
		"zai-org/GLM-5.3-Flash":              {"0.558000000000000000", "0.682000000000000000", "2.668169014084000000"},
		"deepseek-ai/DeepSeek-V4-Flash-0731": {"0.221400000000000000", "0.270600000000000000", "1.541666666666000000"},
	}
	for _, model := range got.PocParams.Models {
		config := model.DynamicCoefficient
		require.NotNil(t, config)
		for i, value := range []*inferencetypes.Decimal{config.CoeffMin, config.CoeffMax, config.RelativeDifficulty} {
			dec, err := value.ToLegacyDec()
			require.NoError(t, err)
			require.Equal(t, expected[model.ModelId][i], dec.String())
		}
		target := uint32(3333)
		if model.ModelId == "MiniMaxAI/MiniMax-M2.7" {
			target = 3334
		}
		require.Equal(t, target, config.TargetShareBps)
	}

	// Seeded bases, rather than the lower bounds, drive the first epoch's adjustment.
	seeded, found := k.GetEpochGroupData(ctx, 1, "")
	require.True(t, found)
	frozen, err := coefficient.Freeze(got.PocParams)
	require.NoError(t, err)
	totals := map[string]int64{"MiniMaxAI/MiniMax-M2.7": 1000}
	result, err := coefficient.Calculate(frozen.Params, frozen.Scales, seeded.ConfirmationWeightScales, totals, totals, nil, true)
	require.NoError(t, err)
	expectedBases := map[string]*inferencetypes.Decimal{
		"MiniMaxAI/MiniMax-M2.7":             {Value: 3024, Exponent: -4},
		"zai-org/GLM-5.3-Flash":              {Value: 651, Exponent: -3},
		"deepseek-ai/DeepSeek-V4-Flash-0731": {Value: 2583, Exponent: -4},
	}
	for _, scale := range result.Scales {
		require.Equal(t, expectedBases[scale.ModelId], scale.BaseCoefficient)
	}
}

func TestInitialDynamicCoefficientConfigIntersectsBounds(t *testing.T) {
	for _, tc := range []struct {
		name, modelID     string
		current, min, max *inferencetypes.Decimal
	}{
		{"unknown", "testnet-model", &inferencetypes.Decimal{Value: 5, Exponent: -1}, &inferencetypes.Decimal{Value: 45, Exponent: -2}, &inferencetypes.Decimal{Value: 55, Exponent: -2}},
		{"benchmark floor", "zai-org/GLM-5.3-Flash", &inferencetypes.Decimal{Value: 55, Exponent: -2}, &inferencetypes.Decimal{Value: 508468965517, Exponent: -12}, &inferencetypes.Decimal{Value: 605, Exponent: -3}},
		{"benchmark ceiling", "deepseek-ai/DeepSeek-V4-Flash-0731", &inferencetypes.Decimal{Value: 46, Exponent: -2}, &inferencetypes.Decimal{Value: 414, Exponent: -3}, &inferencetypes.Decimal{Value: 48951, Exponent: -5}},
		{"no overlap", "MiniMaxAI/MiniMax-M2.7", &inferencetypes.Decimal{Value: 5, Exponent: -1}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, err := initialDynamicCoefficientConfig(&inferencetypes.PoCModelConfig{
				ModelId: tc.modelID, WeightScaleFactor: tc.current,
			})
			if tc.min == nil {
				require.ErrorContains(t, err, "does not overlap")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.min, config.CoeffMin)
			require.Equal(t, tc.max, config.CoeffMax)
		})
	}
}

func TestScaleMigrationDecimalRejectsOverflow(t *testing.T) {
	_, err := scaleMigrationDecimal(&inferencetypes.Decimal{Value: math.MaxInt64, Exponent: -18}, 11)
	require.ErrorContains(t, err, "does not fit int64")
}

func TestMigrateCurrentEffectiveCoefficients(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 7))
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 7,
		ConfirmationWeightScales: []*inferencetypes.ConfirmationWeightScale{{
			ModelId:           "model-a",
			WeightScaleFactor: inferencetypes.DecimalFromFloat(2),
		}},
	})

	require.NoError(t, migrateCurrentEffectiveCoefficients(ctx, k))

	data, found := k.GetEpochGroupData(ctx, 7, "")
	require.True(t, found)
	require.Nil(t, data.ConfirmationWeightScales[0].WeightScaleFactor)
	require.Equal(t, inferencetypes.DecimalFromFloat(2), data.ConfirmationWeightScales[0].EffectiveCoefficient)
}

func TestFreezeUpcomingCoefficientConfigDuringUpgrade(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	require.NoError(t, k.SetEpoch(ctx, &inferencetypes.Epoch{Index: 2, PocStartBlockHeight: 100}))
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{EpochIndex: 1})
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{EpochIndex: 2})
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.DynamicCoefficientParams = nil
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{{
		ModelId:           "model-a",
		WeightScaleFactor: inferencetypes.DecimalFromFloat(2),
	}}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migrateDynamicCoefficientParams(ctx, k))
	require.NoError(t, freezeUpcomingCoefficientConfig(ctx, k))

	data, found := k.GetEpochGroupData(ctx, 2, "")
	require.True(t, found)
	require.NotNil(t, data.DynamicCoefficientParams)
	require.Len(t, data.ConfirmationWeightScales, 1)
	require.Equal(t, &inferencetypes.Decimal{Value: 18, Exponent: -1}, data.ConfirmationWeightScales[0].Config.CoeffMin)
	require.Equal(t, &inferencetypes.Decimal{Value: 22, Exponent: -1}, data.ConfirmationWeightScales[0].Config.CoeffMax)
	require.Nil(t, data.ConfirmationWeightScales[0].BaseCoefficient)
}

func TestFreezeUpcomingCoefficientConfigMissingGroupData(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	require.NoError(t, k.SetEpoch(ctx, &inferencetypes.Epoch{Index: 2, PocStartBlockHeight: 100}))

	err := freezeUpcomingCoefficientConfig(ctx, k)
	require.Error(t, err)
	require.Contains(t, err.Error(), "upcoming epoch 2 has no root epoch group data")
}

func TestApplyFeeGroupUpgradeInfo_OmittedKeepsDefaultsEmptyDisables(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = inferencetypes.DefaultFeeParams()
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, applyFeeGroupUpgradeInfo(ctx, k, ""))
	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{inferencetypes.FeeGroupEpoch, inferencetypes.FeeGroupCosmos}, updated.FeeParams.EnabledFeeGroups)

	require.NoError(t, applyFeeGroupUpgradeInfo(ctx, k, `{"enabled_fee_groups":[]}`))
	updated, err = k.GetParams(ctx)
	require.NoError(t, err)
	require.Empty(t, updated.FeeParams.EnabledFeeGroups)
}

func TestApplyFeeGroupUpgradeInfo_BinariesOnlyKeepsDefaults(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = inferencetypes.DefaultFeeParams()
	require.NoError(t, k.SetParams(ctx, params))

	infoJSON := `{
		"binaries": {"linux/amd64": "https://example.com/inferenced.zip"},
		"api_binaries": {"linux/amd64": "https://example.com/decentralized-api.zip"}
	}`
	require.NoError(t, applyFeeGroupUpgradeInfo(ctx, k, infoJSON))
	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{inferencetypes.FeeGroupEpoch, inferencetypes.FeeGroupCosmos}, updated.FeeParams.EnabledFeeGroups)
}

func TestApplyFeeGroupUpgradeInfo_EnablesEpochAtPrice(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = inferencetypes.DefaultFeeParams()
	require.NoError(t, k.SetParams(ctx, params))

	infoJSON := `{
		"binaries": {"linux/amd64": "https://example.com/inferenced.zip"},
		"api_binaries": {"linux/amd64": "https://example.com/decentralized-api.zip"},
		"enabled_fee_groups": ["epoch"],
		"min_gas_prices": {"epoch": 10}
	}`
	require.NoError(t, applyFeeGroupUpgradeInfo(ctx, k, infoJSON))
	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{inferencetypes.FeeGroupEpoch}, updated.FeeParams.EnabledFeeGroups)
	epoch := updated.FeeParams.GroupByName(inferencetypes.FeeGroupEpoch)
	require.NotNil(t, epoch)
	require.Equal(t, uint64(10), epoch.MinGasPrice)
	require.Equal(t, uint64(0), updated.FeeParams.MinGasPriceNgonka)
}

func TestApplyFeeGroupUpgradeInfo_RejectsInvalid(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = inferencetypes.DefaultFeeParams()
	require.NoError(t, k.SetParams(ctx, params))

	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{"enabled_fee_groups":["epoch"]}`))
	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{"enabled_fee_groups":["epoch"],"min_gas_prices":{"epoch":0}}`))
	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{"enabled_fee_groups":["epoch"],"min_gas_prices":{"epoch":10,"bls":1}}`))
	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{"enabled_fee_groups":["epoc"],"min_gas_prices":{"epoc":10}}`))
	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{"min_gas_prices":{"epoch":1}}`))
	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{not json`))
}

func TestMigratePoCChallengeParams(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocChallengeParams = nil
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, migratePoCChallengeParams(ctx, k))
	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.DefaultPoCChallengeParams(), got.PocChallengeParams)
}

func TestMigrateDevshardApprovedVersions(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.DevshardEscrowParams.ApprovedVersions = []*inferencetypes.DevshardApprovedVersion{
		{
			Name:   "v2",
			Binary: "https://example.com/v2.zip",
			Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		},
		{
			Name:   "v1",
			Binary: "https://example.com/v1.zip",
			Sha256: "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789",
		},
	}
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migrateDevshardApprovedVersions(ctx, k))

	got, err := k.GetApprovedVersions(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2)
	require.Equal(t, "v1", got[0].Name)
	require.Equal(t, "v2", got[1].Name)

	after, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Empty(t, after.DevshardEscrowParams.ApprovedVersions)
}

func TestLeftoverApprovedVersionsDoNotBlockCoefficientMigrate(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{EpochIndex: 1})
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.DynamicCoefficientParams = nil
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{{
		ModelId:           "model-a",
		WeightScaleFactor: inferencetypes.DecimalFromFloat(1),
	}}
	params.DevshardEscrowParams.ApprovedVersions = []*inferencetypes.DevshardApprovedVersion{{
		Name:   "v1",
		Binary: "https://example.com/v1.zip",
		Sha256: "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}}
	require.NoError(t, k.SetParams(ctx, params))

	require.Error(t, migrateDynamicCoefficientParams(ctx, k))
	require.NoError(t, migrateDevshardApprovedVersions(ctx, k))
	require.NoError(t, migrateDynamicCoefficientParams(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.PocParams.DynamicCoefficientParams)
	require.Empty(t, got.DevshardEscrowParams.ApprovedVersions)
	stored, err := k.GetApprovedVersions(ctx)
	require.NoError(t, err)
	require.Len(t, stored, 1)
	require.Equal(t, "v1", stored[0].Name)
}

type testGrant struct {
	granter sdk.AccAddress
	grantee sdk.AccAddress
	grant   authz.Grant
}

type mockAuthzKeeper struct {
	grants       []testGrant
	existing     map[string]authz.Authorization
	saved        []authz.Authorization
	savedExpiry  *time.Time
	savedGranter sdk.AccAddress
	savedGrantee sdk.AccAddress
}

func (m *mockAuthzKeeper) IterateGrants(_ context.Context, handler func(sdk.AccAddress, sdk.AccAddress, authz.Grant) bool) {
	for _, grant := range m.grants {
		if handler(grant.granter, grant.grantee, grant.grant) {
			return
		}
	}
}

func (m *mockAuthzKeeper) GetAuthorization(_ context.Context, _, _ sdk.AccAddress, msgType string) (authz.Authorization, *time.Time) {
	if m.existing == nil {
		return nil, nil
	}
	return m.existing[msgType], nil
}

func (m *mockAuthzKeeper) SaveGrant(_ context.Context, grantee, granter sdk.AccAddress, authorization authz.Authorization, expiration *time.Time) error {
	m.saved = append(m.saved, authorization)
	m.savedExpiry = expiration
	m.savedGranter = granter
	m.savedGrantee = grantee
	return nil
}

func warmMarkerGrant(t *testing.T, granter, grantee sdk.AccAddress) testGrant {
	return warmKeyMarkerGrant(t, granter, grantee, nil)
}

func warmKeyMarkerGrant(t *testing.T, granter, grantee sdk.AccAddress, expiration *time.Time) testGrant {
	t.Helper()
	authorization := authz.NewGenericAuthorization(inferencetypes.WarmKeyGrantMarkerTypeURL)
	authorizationAny, err := codectypes.NewAnyWithValue(authorization)
	require.NoError(t, err)
	return testGrant{
		granter: granter,
		grantee: grantee,
		grant:   authz.Grant{Authorization: authorizationAny, Expiration: expiration},
	}
}

func TestGrantPoCChallengeAuthzCreatesMissingGrants(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	granter := sdk.AccAddress([]byte("granter_____________"))
	grantee := sdk.AccAddress([]byte("grantee_____________"))
	authzKeeper := &mockAuthzKeeper{
		grants: []testGrant{warmMarkerGrant(t, granter, grantee)},
	}
	require.NoError(t, grantPoCChallengeAuthz(ctx, authzKeeper, k))
	require.Len(t, authzKeeper.saved, 2)
}

func TestGrantPoCChallengeAuthzSkipsExisting(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	granter := sdk.AccAddress([]byte("granter_____________"))
	grantee := sdk.AccAddress([]byte("grantee_____________"))
	authzKeeper := &mockAuthzKeeper{
		grants: []testGrant{warmMarkerGrant(t, granter, grantee)},
		existing: map[string]authz.Authorization{
			sdk.MsgTypeURL(&inferencetypes.MsgPoCChallengeStoreCommit{}):       authz.NewGenericAuthorization(sdk.MsgTypeURL(&inferencetypes.MsgPoCChallengeStoreCommit{})),
			sdk.MsgTypeURL(&inferencetypes.MsgSubmitPoCChallengeValidations{}): authz.NewGenericAuthorization(sdk.MsgTypeURL(&inferencetypes.MsgSubmitPoCChallengeValidations{})),
		},
	}
	require.NoError(t, grantPoCChallengeAuthz(ctx, authzKeeper, k))
	require.Empty(t, authzKeeper.saved)
}

func TestGrantPoCChallengeAuthzSkipsExpiredMarker(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	granter := sdk.AccAddress([]byte("granter_____________"))
	grantee := sdk.AccAddress([]byte("grantee_____________"))
	expired := time.Unix(1, 0)
	grant := warmMarkerGrant(t, granter, grantee)
	grant.grant.Expiration = &expired
	authzKeeper := &mockAuthzKeeper{grants: []testGrant{grant}}
	require.NoError(t, grantPoCChallengeAuthz(ctx, authzKeeper, k))
	require.Empty(t, authzKeeper.saved)
}

func TestGrantPoCChallengeAuthzNoPairWithoutMarker(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	authzKeeper := &mockAuthzKeeper{}
	require.NoError(t, grantPoCChallengeAuthz(ctx, authzKeeper, k))
	require.Empty(t, authzKeeper.saved)
}

func TestGrantDeclarePoCIntentAuthzCreatesMissingGrant(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	granter := sdk.AccAddress([]byte("granter_____________"))
	grantee := sdk.AccAddress([]byte("grantee_____________"))
	expiration := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	authzKeeper := &mockAuthzKeeper{
		grants: []testGrant{warmKeyMarkerGrant(t, granter, grantee, &expiration)},
	}

	require.NoError(t, grantDeclarePoCIntentAuthz(ctx, authzKeeper, k))
	require.Len(t, authzKeeper.saved, 1)
	require.Equal(t, authz.NewGenericAuthorization(sdk.MsgTypeURL(&inferencetypes.MsgDeclarePoCIntent{})), authzKeeper.saved[0])
	require.Equal(t, &expiration, authzKeeper.savedExpiry)
	require.Equal(t, granter, authzKeeper.savedGranter)
	require.Equal(t, grantee, authzKeeper.savedGrantee)
}

func TestGrantDeclarePoCIntentAuthzSkipsExistingGrant(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	granter := sdk.AccAddress([]byte("granter_____________"))
	grantee := sdk.AccAddress([]byte("grantee_____________"))
	expiration := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	authzKeeper := &mockAuthzKeeper{
		grants: []testGrant{warmKeyMarkerGrant(t, granter, grantee, &expiration)},
		existing: map[string]authz.Authorization{
			sdk.MsgTypeURL(&inferencetypes.MsgDeclarePoCIntent{}): authz.NewGenericAuthorization(sdk.MsgTypeURL(&inferencetypes.MsgDeclarePoCIntent{})),
		},
	}

	require.NoError(t, grantDeclarePoCIntentAuthz(ctx, authzKeeper, k))
	require.Empty(t, authzKeeper.saved)
}

func TestApplyFeeGroupUpgradeInfo_CreatesMissingGroups(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = inferencetypes.DefaultFeeParams()
	params.FeeParams.Groups = params.FeeParams.Groups[:1]
	params.FeeParams.EnabledFeeGroups = []string{inferencetypes.FeeGroupEpoch}
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, applyFeeGroupUpgradeInfo(ctx, k, `{"enabled_fee_groups":["epoch","cosmos","governance"],"min_gas_prices":{"epoch":1,"cosmos":1,"governance":2}}`))
	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, []string{"epoch", "cosmos", "governance"}, updated.FeeParams.EnabledFeeGroups)
	require.Equal(t, uint64(1), updated.FeeParams.GroupByName("cosmos").MinGasPrice)
	require.Equal(t, uint64(2), updated.FeeParams.GroupByName("governance").MinGasPrice)
	require.Equal(t, params.FeeParams.Groups[0].Msgs, updated.FeeParams.Groups[0].Msgs)
}

func TestGovernanceUpdatesCoefficientsAfterMigration(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{EpochIndex: 1})
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.DynamicCoefficientParams = nil
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{
		{ModelId: "MiniMaxAI/MiniMax-M2.7", WeightScaleFactor: &inferencetypes.Decimal{Value: 3024, Exponent: -4}},
		{ModelId: "zai-org/GLM-5.3-Flash", WeightScaleFactor: &inferencetypes.Decimal{Value: 62, Exponent: -2}},
	}
	params.DelegationParams.InitialModelId = "MiniMaxAI/MiniMax-M2.7"
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, migrateDynamicCoefficientParams(ctx, k))
	params, err = k.GetParams(ctx)
	require.NoError(t, err)

	frozen, err := coefficient.Freeze(params.PocParams)
	require.NoError(t, err)
	totals := map[string]int64{"MiniMaxAI/MiniMax-M2.7": 100, "zai-org/GLM-5.3-Flash": 100}
	seeded, found := k.GetEpochGroupData(ctx, 1, "")
	require.True(t, found)
	first, err := coefficient.Calculate(frozen.Params, frozen.Scales, seeded.ConfirmationWeightScales, totals, totals, nil, true)
	require.NoError(t, err)
	previous := inferencetypes.EpochGroupData{
		EpochIndex: 2, DynamicCoefficientParams: frozen.Params, ConfirmationWeightScales: first.Scales,
	}
	upcoming := inferencetypes.EpochGroupData{
		EpochIndex: 3, DynamicCoefficientParams: frozen.Params, ConfirmationWeightScales: frozen.Scales,
	}
	k.SetEpochGroupData(ctx, previous)
	k.SetEpochGroupData(ctx, upcoming)

	// Governance changes every controller parameter and opens the formerly fixed model.
	params.PocParams.DynamicCoefficientParams = &inferencetypes.DynamicCoefficientParams{
		TargetZoneBps: 600, BootstrapShareBps: 200,
		StepMin:          &inferencetypes.Decimal{Value: 1, Exponent: -2},
		StepMax:          &inferencetypes.Decimal{Value: 4, Exponent: -2},
		BootstrapStepMax: &inferencetypes.Decimal{Value: 12, Exponent: -2},
	}
	params.PocParams.Models[0].DynamicCoefficient = &inferencetypes.DynamicCoefficientModelConfig{
		CoeffMin:           &inferencetypes.Decimal{Value: 1, Exponent: -1},
		CoeffMax:           &inferencetypes.Decimal{Value: 6, Exponent: -1},
		RelativeDifficulty: &inferencetypes.Decimal{Value: 2, Exponent: 0}, TargetShareBps: 4000,
	}
	params.PocParams.Models[1].DynamicCoefficient = &inferencetypes.DynamicCoefficientModelConfig{
		CoeffMin:           &inferencetypes.Decimal{Value: 5, Exponent: -1},
		CoeffMax:           &inferencetypes.Decimal{Value: 8, Exponent: -1},
		RelativeDifficulty: &inferencetypes.Decimal{Value: 3, Exponent: 0}, TargetShareBps: 6000,
	}
	ms := keeper.NewMsgServerImpl(k)
	_, err = ms.UpdateParams(ctx, &inferencetypes.MsgUpdateParams{Authority: k.GetAuthority(), Params: params})
	require.NoError(t, err)
	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, params.PocParams, updated.PocParams)

	// A proposal executed after PoC start cannot overwrite its frozen config or prior state.
	storedPrevious, found := k.GetEpochGroupData(ctx, 2, "")
	require.True(t, found)
	require.Equal(t, previous, storedPrevious)
	storedUpcoming, found := k.GetEpochGroupData(ctx, 3, "")
	require.True(t, found)
	require.Equal(t, upcoming, storedUpcoming)
	second, err := coefficient.Calculate(storedUpcoming.DynamicCoefficientParams,
		storedUpcoming.ConfirmationWeightScales, storedPrevious.ConfirmationWeightScales,
		totals, totals, nil, true)
	require.NoError(t, err)

	// The next snapshot adopts the proposal and carries the previous base coefficients.
	next, err := coefficient.Freeze(updated.PocParams)
	require.NoError(t, err)
	require.Equal(t, params.PocParams.DynamicCoefficientParams, next.Params)
	third, err := coefficient.Calculate(next.Params, next.Scales, second.Scales, totals, totals, nil, true)
	require.NoError(t, err)
	for i, scale := range third.Scales {
		require.Equal(t, params.PocParams.Models[i].DynamicCoefficient, scale.Config)
		require.Equal(t, second.Scales[i].BaseCoefficient, scale.BaseCoefficient)
		require.Equal(t, &inferencetypes.Decimal{Value: 2, Exponent: -2}, scale.AdaptiveStep)
	}
}

func TestMigrateCoefficientStatePreservesCurrentEffectiveWeight(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
	k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{
		EpochIndex: 1,
		ConfirmationWeightScales: []*inferencetypes.ConfirmationWeightScale{{
			ModelId: "testnet-model", WeightScaleFactor: &inferencetypes.Decimal{Value: 3},
		}},
	})
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.DynamicCoefficientParams = nil
	params.PocParams.Models = []*inferencetypes.PoCModelConfig{{
		ModelId: "testnet-model", WeightScaleFactor: &inferencetypes.Decimal{Value: 2},
	}}
	params.DelegationParams.InitialModelId = "testnet-model"
	require.NoError(t, k.SetParams(ctx, params))
	require.NoError(t, migrateDynamicCoefficientParams(ctx, k))
	require.NoError(t, migrateCurrentEffectiveCoefficients(ctx, k))
	data, found := k.GetEpochGroupData(ctx, 1, "")
	require.True(t, found)
	require.Len(t, data.ConfirmationWeightScales, 1)
	scale := data.ConfirmationWeightScales[0]
	require.Equal(t, &inferencetypes.Decimal{Value: 2}, scale.BaseCoefficient)
	require.Equal(t, &inferencetypes.Decimal{Value: 3}, scale.EffectiveCoefficient)
	require.Equal(t, &inferencetypes.Decimal{Value: 25, Exponent: -3}, scale.AdaptiveStep)
	require.Zero(t, scale.PrevSign)
	require.Nil(t, scale.WeightScaleFactor)
	require.False(t, scale.ExcludeFromConfirmation)
}

func TestMigrateDynamicCoefficientParamsRequiresCurrentEpoch(t *testing.T) {
	for _, tc := range []struct {
		name         string
		indexPresent bool
		errorMessage string
	}{
		{"missing index", false, "effective epoch index not found"},
		{"missing root", true, "current epoch 1 has no root epoch group data"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
			if tc.indexPresent {
				require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
			}
			params, err := k.GetParams(ctx)
			require.NoError(t, err)
			params.PocParams.DynamicCoefficientParams = nil
			params.PocParams.Models = []*inferencetypes.PoCModelConfig{{
				ModelId: "model-a", WeightScaleFactor: &inferencetypes.Decimal{Value: 2},
			}}
			params.DelegationParams.InitialModelId = "model-a"
			require.NoError(t, k.SetParams(ctx, params))

			require.ErrorContains(t, migrateDynamicCoefficientParams(ctx, k), tc.errorMessage)
			after, err := k.GetParams(ctx)
			require.NoError(t, err)
			require.Equal(t, params, after)

			// Once the epoch exists, retrying initializes the base from the original scale.
			require.NoError(t, k.SetEffectiveEpochIndex(ctx, 1))
			k.SetEpochGroupData(ctx, inferencetypes.EpochGroupData{EpochIndex: 1})
			require.NoError(t, migrateDynamicCoefficientParams(ctx, k))
			data, found := k.GetEpochGroupData(ctx, 1, "")
			require.True(t, found)
			require.Len(t, data.ConfirmationWeightScales, 1)
			require.Equal(t, &inferencetypes.Decimal{Value: 2}, data.ConfirmationWeightScales[0].BaseCoefficient)
		})
	}
}
