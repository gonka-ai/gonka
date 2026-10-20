package v0_2_17

import (
	"testing"

	"github.com/stretchr/testify/require"

	keepertest "github.com/productscience/inference/testutil/keeper"
	"github.com/productscience/inference/x/inference/types"
)

// TestUpgradeName pins the future on-chain proposal name. The governance
// proposal and UpgradeName must stay identical or the handler will not run.
func TestUpgradeName(t *testing.T) {
	require.Equal(t, "v0.2.17", UpgradeName)
}

func TestMigratePoCMinCommitCount(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)

	customStatTest := &types.PoCStatTestParams{
		DistThreshold:   types.DecimalFromFloat(0.44),
		PMismatch:       types.DecimalFromFloat(0.1),
		PValueThreshold: types.DecimalFromFloat(0.05),
	}
	preset := types.DefaultPoCStatTestParams()
	preset.MinCount = 64
	base := params.PocParams.Models[0]
	model := func(id string, statTest *types.PoCStatTestParams) *types.PoCModelConfig {
		m := *base
		m.ModelId = id
		m.StatTest = statTest
		dynamic := *base.DynamicCoefficient
		dynamic.TargetShareBps = 2500
		m.DynamicCoefficient = &dynamic
		return &m
	}
	params.PocParams.ValidationSampleSize = 200
	params.PocParams.Models = []*types.PoCModelConfig{
		model("deepseek-ai/DeepSeek-V4-Flash-0731", types.DefaultPoCStatTestParams()),
		model("zai-org/GLM-5.3-Flash", customStatTest),
		model("MiniMaxAI/MiniMax-M2.7", nil),
		model("preset", preset),
	}
	require.NoError(t, k.SetParams(ctx, params))
	before, err := k.GetParams(ctx)
	require.NoError(t, err)

	require.NoError(t, migratePoCMinCommitCount(ctx, k))

	firstRun, err := k.GetParams(ctx)
	require.NoError(t, err)
	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NoError(t, got.Validate())
	require.Equal(t, uint32(200), got.PocParams.MinCommitCount("deepseek-ai/DeepSeek-V4-Flash-0731"))
	require.Equal(t, uint32(200), got.PocParams.MinCommitCount("zai-org/GLM-5.3-Flash"))
	require.Equal(t, uint32(200), got.PocParams.MinCommitCount("MiniMaxAI/MiniMax-M2.7"))
	require.Equal(t, uint32(64), got.PocParams.MinCommitCount("preset"))

	glm := got.PocParams.Models[1].StatTest
	require.Equal(t, customStatTest.DistThreshold, glm.DistThreshold)
	require.Equal(t, customStatTest.PMismatch, glm.PMismatch)
	require.Equal(t, customStatTest.PValueThreshold, glm.PValueThreshold)
	require.Nil(t, got.PocParams.Models[2].StatTest.DistThreshold)

	for i := range got.PocParams.Models {
		gotModel := *got.PocParams.Models[i]
		beforeModel := *before.PocParams.Models[i]
		gotModel.StatTest, beforeModel.StatTest = nil, nil
		require.Equal(t, beforeModel, gotModel)
	}
	got.PocParams.Models, before.PocParams.Models = nil, nil
	require.Equal(t, before, got)

	require.NoError(t, migratePoCMinCommitCount(ctx, k))
	again, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.Equal(t, firstRun, again)
}

func TestMigratePoCMinCommitCountCapsAtSampleSize(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	params, err := k.GetParams(ctx)
	require.NoError(t, err)
	params.PocParams.ValidationSampleSize = 100
	params.PocParams.Models[0].ModelId = "model-a"
	params.PocParams.Models[0].StatTest = types.DefaultPoCStatTestParams()
	require.NoError(t, k.SetParams(ctx, params))

	require.NoError(t, migratePoCMinCommitCount(ctx, k))

	got, err := k.GetParams(ctx)
	require.NoError(t, err)
	require.NoError(t, got.Validate())
	require.Equal(t, uint32(100), got.PocParams.MinCommitCount("model-a"))
}
