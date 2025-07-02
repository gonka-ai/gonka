package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

var modelsMockData = []types.Model{
	{
		ProposedBy: "mock_data",
		Id:         "DeepSeek-R1",
	},
	{
		ProposedBy: "mock_data",
		Id:         "Gemma-3-27B",
	},
	{
		ProposedBy: "mock_data",
		Id:         "DeepSeek-V3",
	},
	{
		ProposedBy:   "mock_data",
		Id:           "QwQ-32B",
		Quantization: "fp8",
	},
	{
		ProposedBy: "mock_data",
		Id:         "Llama-3-405B",
	},
	{
		ProposedBy: "mock_data",
		Id:         "Llama-3-70B",
	},
	{
		ProposedBy:   "mock_data",
		Id:           "Qwen2.5-7B-Instruct",
		Quantization: "fp8",
	},
}

// TODO after nodes would be able deploy only models which network voited for, return real data
/*func (k Keeper) ModelsAll(goCtx context.Context, req *types.QueryModelsAllRequest) (*types.QueryModelsAllResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	models, err := k.GetAllModels(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	modelsValues, err := PointersToValues(models)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	k.LogInfo("Retrieved models", types.Inferences, "len(models)", len(modelsValues), "models", modelsValues)

	return &types.QueryModelsAllResponse{
		Model: modelsValues,
	}, nil
}
*/
func (k Keeper) ModelsAll(goCtx context.Context, req *types.QueryModelsAllRequest) (*types.QueryModelsAllResponse, error) {
	modelsValues := modelsMockData
	k.LogInfo("Retrieved models", types.Inferences, "len(models)", len(modelsValues), "models", modelsValues)

	return &types.QueryModelsAllResponse{
		Model: modelsValues,
	}, nil
}
