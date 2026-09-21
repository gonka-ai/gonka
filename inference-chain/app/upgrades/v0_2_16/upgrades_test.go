package v0_2_16

import (
	"testing"

	"cosmossdk.io/collections"
	keepertest "github.com/productscience/inference/testutil/keeper"
	inferencetypes "github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.16", UpgradeName)
}

func TestMigrateDynamicCoefficientParams(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
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
	expectedScales := map[string]*inferencetypes.Decimal{
		"model-a": inferencetypes.DecimalFromFloat(1),
		"model-b": inferencetypes.DecimalFromFloat(2),
		"model-c": inferencetypes.DecimalFromFloat(3),
	}
	for _, model := range got.PocParams.Models {
		require.Nil(t, model.WeightScaleFactor)
		if model.ModelId == "disabled" {
			require.Nil(t, model.DynamicCoefficient)
			continue
		}
		require.NotNil(t, model.DynamicCoefficient)
		require.Equal(t, expectedScales[model.ModelId], model.DynamicCoefficient.CoeffMin)
		require.Equal(t, expectedScales[model.ModelId], model.DynamicCoefficient.CoeffMax)
		require.Equal(t, &inferencetypes.Decimal{Value: 1, Exponent: 0}, model.DynamicCoefficient.RelativeDifficulty)
		targets[model.ModelId] = model.DynamicCoefficient.TargetShareBps
	}
	require.Equal(t, uint32(3333), targets["model-a"])
	require.Equal(t, uint32(3334), targets["model-b"])
	require.Equal(t, uint32(3333), targets["model-c"])
	require.NoError(t, got.Validate())

	// The migration is idempotent once the global block exists.
	require.NoError(t, migrateDynamicCoefficientParams(ctx, k))
	again, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, got.PocParams, again.PocParams)
}

func TestMigrateDynamicCoefficientParamsPreservesLegacyPrecision(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
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
		&inferencetypes.Decimal{Value: 1234567890123, Exponent: -13},
		got.PocParams.Models[0].DynamicCoefficient.CoeffMin,
	)
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
	require.Equal(t, inferencetypes.DecimalFromFloat(2), data.ConfirmationWeightScales[0].Config.CoeffMin)
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

func TestBackfillTrainingParamDefaults(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.TrainingParams = nil
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillTrainingParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NotNil(t, got.TrainingParams)
	require.Equal(t, inferencetypes.DefaultTrainingOptInTtlBlocks, got.TrainingParams.OptInTtlBlocks)
	require.Equal(t, inferencetypes.DefaultTrainingReleaseBufferBlocks, got.TrainingParams.ReleaseBufferBlocks)
}

func TestBackfillTrainingParamDefaults_RaisesLimitsToEpochLength(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.EpochParams.EpochLength = 2000
	params.TrainingParams = nil
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillTrainingParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(2000), got.TrainingParams.OptInTtlBlocks)
	require.Equal(t, 2*int64(2000)+got.TrainingParams.ReleaseBufferBlocks, got.TrainingParams.SettledShardRetentionBlocks)

	got.TrainingParams.TrainingEnabled = true
	require.NoError(t, got.Validate())
}

func TestBackfillTrainingParamDefaults_PreservesOverrides(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.TrainingParams.OptInTtlBlocks = 777
	params.TrainingParams.ReleaseBufferBlocks = 0
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, backfillTrainingParamDefaults(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, int64(777), got.TrainingParams.OptInTtlBlocks)
	require.Equal(t, inferencetypes.DefaultTrainingReleaseBufferBlocks, got.TrainingParams.ReleaseBufferBlocks)
}

func TestBackfillTrainshardNodeStatuses(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	require.NoError(t, k.Trainshards.Set(ctx, 1, inferencetypes.Trainshard{
		TrainshardId: 1,
		Status:       inferencetypes.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		Nodes:        []*inferencetypes.TrainshardReservedNode{{Participant: "host", NodeId: "node-a"}},
	}))
	require.NoError(t, k.Trainshards.Set(ctx, 2, inferencetypes.Trainshard{
		TrainshardId:   2,
		Status:         inferencetypes.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		ClosedAtHeight: 150,
		Nodes:          []*inferencetypes.TrainshardReservedNode{{Participant: "host", NodeId: "node-b"}},
	}))

	require.NoError(t, backfillTrainshardNodeStatuses(ctx, k))

	active, err := k.Trainshards.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE, active.Nodes[0].Status)

	closed, err := k.Trainshards.Get(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, inferencetypes.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE, closed.Nodes[0].Status)
	require.Equal(t, int64(150), closed.Nodes[0].ReleasedAtHeight)
	require.Equal(t, int64(150), closed.Nodes[0].ReservedUntilHeight)
}

func TestClearStaleTrainingOptIns(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	require.NoError(t, k.TrainingNodeOptIns.Set(ctx, collections.Join("host", "node-a"), int64(0)))
	require.NoError(t, k.TrainingNodeOptIns.Set(ctx, collections.Join("host", "node-b"), int64(500)))

	require.NoError(t, clearStaleTrainingOptIns(ctx, k))

	has, err := k.TrainingNodeOptIns.Has(ctx, collections.Join("host", "node-a"))
	require.NoError(t, err)
	require.False(t, has)

	expiresAt, err := k.TrainingNodeOptIns.Get(ctx, collections.Join("host", "node-b"))
	require.NoError(t, err)
	require.Equal(t, int64(500), expiresAt)
}

func TestApplyFeeGroupUpgradeInfo_EmptyKeepsDisabled(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = inferencetypes.DefaultFeeParams()
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, applyFeeGroupUpgradeInfo(ctx, k, ""))
	updated, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Empty(t, updated.FeeParams.EnabledFeeGroups)

	require.NoError(t, applyFeeGroupUpgradeInfo(ctx, k, `{"enabled_fee_groups":[]}`))
	updated, err = k.GetParams(ctx)
	require.NoError(t, err)
	require.Empty(t, updated.FeeParams.EnabledFeeGroups)
}

func TestApplyFeeGroupUpgradeInfo_BinariesOnlyKeepsDisabled(t *testing.T) {
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
	require.Empty(t, updated.FeeParams.EnabledFeeGroups)
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
	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{"enabled_fee_groups":["bls"],"min_gas_prices":{"bls":10}}`))
	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{not json`))
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
