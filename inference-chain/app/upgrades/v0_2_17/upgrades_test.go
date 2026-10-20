package v0_2_17

import (
	"slices"
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

func TestAddGlmLanguageModelOnly(t *testing.T) {
	k, ctx, _ := keepertest.InferenceKeeperReturningMocks(t)
	const minimaxModelID = "MiniMaxAI/MiniMax-M2.7"
	args := []string{"--max-model-len", "400000", "--kv-cache-dtype", "fp8"}
	want := append(slices.Clone(args), languageModelOnlyArg)

	k.SetModel(ctx, &types.Model{Id: glm53FlashModelID, ModelArgs: slices.Clone(args)})
	require.NoError(t, k.SetEffectiveEpochIndex(ctx, 7))
	for _, epoch := range []uint64{7, 8} {
		for _, id := range []string{glm53FlashModelID, minimaxModelID} {
			k.SetEpochGroupData(ctx, types.EpochGroupData{
				EpochIndex:    epoch,
				ModelId:       id,
				ModelSnapshot: &types.Model{Id: id, ModelArgs: slices.Clone(args)},
			})
		}
	}

	addGlmLanguageModelOnly(ctx, k)
	addGlmLanguageModelOnly(ctx, k)

	model, found := k.GetGovernanceModel(ctx, glm53FlashModelID)
	require.True(t, found)
	require.Equal(t, want, model.ModelArgs)
	for _, epoch := range []uint64{7, 8} {
		glm, found := k.GetEpochGroupData(ctx, epoch, glm53FlashModelID)
		require.True(t, found)
		require.Equal(t, want, glm.ModelSnapshot.ModelArgs)
		minimax, found := k.GetEpochGroupData(ctx, epoch, minimaxModelID)
		require.True(t, found)
		require.Equal(t, args, minimax.ModelSnapshot.ModelArgs)
	}
}
