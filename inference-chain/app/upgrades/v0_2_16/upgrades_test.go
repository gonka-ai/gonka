package v0_2_16

import (
	"testing"

	"cosmossdk.io/collections"
	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/stretchr/testify/require"
)

func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.16", UpgradeName)
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
	require.Equal(t, types.DefaultTrainingOptInTtlBlocks, got.TrainingParams.OptInTtlBlocks)
	require.Equal(t, types.DefaultTrainingReleaseBufferBlocks, got.TrainingParams.ReleaseBufferBlocks)
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
	require.Equal(t, types.DefaultTrainingReleaseBufferBlocks, got.TrainingParams.ReleaseBufferBlocks)
}

func TestBackfillTrainshardNodeStatuses(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)

	require.NoError(t, k.Trainshards.Set(ctx, 1, types.Trainshard{
		TrainshardId: 1,
		Status:       types.TrainshardStatus_TRAINSHARD_STATUS_ACTIVE,
		Nodes:        []*types.TrainshardReservedNode{{Participant: "host", NodeId: "node-a"}},
	}))
	require.NoError(t, k.Trainshards.Set(ctx, 2, types.Trainshard{
		TrainshardId:   2,
		Status:         types.TrainshardStatus_TRAINSHARD_STATUS_SETTLED,
		ClosedAtHeight: 150,
		Nodes:          []*types.TrainshardReservedNode{{Participant: "host", NodeId: "node-b"}},
	}))

	require.NoError(t, backfillTrainshardNodeStatuses(ctx, k))

	active, err := k.Trainshards.Get(ctx, 1)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_ACTIVE, active.Nodes[0].Status)

	closed, err := k.Trainshards.Get(ctx, 2)
	require.NoError(t, err)
	require.Equal(t, types.TrainshardNodeStatus_TRAINSHARD_NODE_STATUS_RELEASED_ON_CLOSE, closed.Nodes[0].Status)
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
	params.FeeParams = types.DefaultFeeParams()
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
	params.FeeParams = types.DefaultFeeParams()
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
	params.FeeParams = types.DefaultFeeParams()
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
	require.Equal(t, []string{types.FeeGroupEpoch}, updated.FeeParams.EnabledFeeGroups)
	epoch := updated.FeeParams.GroupByName(types.FeeGroupEpoch)
	require.NotNil(t, epoch)
	require.Equal(t, uint64(10), epoch.MinGasPrice)
	require.Equal(t, uint64(0), updated.FeeParams.MinGasPriceNgonka)
}

func TestApplyFeeGroupUpgradeInfo_RejectsInvalid(t *testing.T) {
	k, ctx := keepertest.InferenceKeeper(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.FeeParams = types.DefaultFeeParams()
	require.NoError(t, k.SetParams(ctx, params))

	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{"enabled_fee_groups":["epoch"]}`))
	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{"enabled_fee_groups":["epoch"],"min_gas_prices":{"epoch":0}}`))
	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{"enabled_fee_groups":["epoch"],"min_gas_prices":{"epoch":10,"bls":1}}`))
	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{"enabled_fee_groups":["epoc"],"min_gas_prices":{"epoc":10}}`))
	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{"enabled_fee_groups":["bls"],"min_gas_prices":{"bls":10}}`))
	require.Error(t, applyFeeGroupUpgradeInfo(ctx, k, `{not json`))
}
