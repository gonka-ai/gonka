package v0_2_17

import (
	"context"
	"slices"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

const (
	glm53FlashModelID    = "zai-org/GLM-5.3-Flash"
	languageModelOnlyArg = "--language-model-only"
)

// addGlmLanguageModelOnly puts --language-model-only into the chain's model args
// of GLM-5.3-Flash: the decode PoC scheme needs GLM text-only. DAPI puts the
// chain's model args over a node's own, so this reaches every node whatever its
// node config. The effective and the upcoming epoch already hold a model
// snapshot, so they get the arg as well.
func addGlmLanguageModelOnly(ctx context.Context, k keeper.Keeper) {
	model, found := k.GetGovernanceModel(ctx, glm53FlashModelID)
	if !found || model == nil {
		return
	}
	if !slices.Contains(model.ModelArgs, languageModelOnlyArg) {
		model.ModelArgs = append(model.ModelArgs, languageModelOnlyArg)
		k.SetModel(ctx, model)
	}

	epochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		return
	}
	for _, index := range []uint64{epochIndex, epochIndex + 1} {
		data, found := k.GetEpochGroupData(ctx, index, glm53FlashModelID)
		if !found || data.ModelSnapshot == nil || slices.Contains(data.ModelSnapshot.ModelArgs, languageModelOnlyArg) {
			continue
		}
		data.ModelSnapshot.ModelArgs = append(data.ModelSnapshot.ModelArgs, languageModelOnlyArg)
		k.SetEpochGroupData(ctx, data)
	}
	k.LogInfo("added model arg", types.Upgrades, "model_id", glm53FlashModelID, "arg", languageModelOnlyArg)
}
