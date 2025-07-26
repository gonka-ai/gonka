package keeper_test

import (
	"testing"

	keepertest "github.com/productscience/inference/testutil/keeper"
	keeper2 "github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
)

func TestModels(t *testing.T) {
	keeper, ctx := keepertest.InferenceKeeper(t)

	keeper.SetModel(ctx, &types.Model{Id: "1", ProposedBy: "user1", UnitsOfComputePerToken: 1})
	models, err := keeper.GetGovernanceModels(ctx)
	println("Models: ", models, "Error: ", err)
	modelValues, err := keeper2.PointersToValues(models)
	println("ModelValues: ", modelValues, "Error: ", err)
}
