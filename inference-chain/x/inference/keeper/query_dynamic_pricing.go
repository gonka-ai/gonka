package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// GetModelPerTokenPrice returns the current per-token price for a specific model
func (k Keeper) GetModelPerTokenPrice(goCtx context.Context, req *types.QueryGetModelPerTokenPriceRequest) (*types.QueryGetModelPerTokenPriceResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if req.ModelId == "" {
		return nil, status.Error(codes.InvalidArgument, "model_id cannot be empty")
	}

	price, err := k.GetModelCurrentPrice(goCtx, req.ModelId)
	if err != nil {
		k.LogError("Failed to get model price", types.Pricing, "modelId", req.ModelId, "error", err)
		return &types.QueryGetModelPerTokenPriceResponse{
			Price: 0,
			Found: false,
		}, nil
	}

	// If price is 0, it could be either not found or legitimately 0 (grace period)
	// We consider any successfully retrieved price as "found"
	return &types.QueryGetModelPerTokenPriceResponse{
		Price: price,
		Found: true,
	}, nil
}

// GetAllModelPerTokenPrices returns current per-token prices for all models that have prices set
func (k Keeper) GetAllModelPerTokenPrices(goCtx context.Context, req *types.QueryGetAllModelPerTokenPricesRequest) (*types.QueryGetAllModelPerTokenPricesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	// Get all model prices directly from our KV storage
	modelPricesMap, err := k.GetAllModelCurrentPrices(goCtx)
	if err != nil {
		k.LogError("Failed to get all model prices", types.Pricing, "error", err)
		return nil, status.Error(codes.Internal, "failed to retrieve model prices")
	}

	// Convert map to repeated ModelPrice for response
	var modelPrices []types.ModelPrice
	for modelId, price := range modelPricesMap {
		modelPrices = append(modelPrices, types.ModelPrice{
			ModelId: modelId,
			Price:   price,
		})
	}

	k.LogInfo("Retrieved all model prices", types.Pricing,
		"totalModels", len(modelPrices),
		"modelPrices", modelPrices)

	return &types.QueryGetAllModelPerTokenPricesResponse{
		ModelPrices: modelPrices,
	}, nil
}

// GetModelCapacity returns the cached capacity for a specific model
func (k Keeper) GetModelCapacity(goCtx context.Context, req *types.QueryGetModelCapacityRequest) (*types.QueryGetModelCapacityResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	if req.ModelId == "" {
		return nil, status.Error(codes.InvalidArgument, "model_id cannot be empty")
	}

	capacity, err := k.GetCachedModelCapacity(goCtx, req.ModelId)
	if err != nil {
		k.LogError("Failed to get model capacity", types.Pricing, "modelId", req.ModelId, "error", err)
		return &types.QueryGetModelCapacityResponse{
			Capacity: 0,
			Found:    false,
		}, nil
	}

	// If capacity is 0, it could be either not found or legitimately 0
	// We consider any successfully retrieved capacity as "found"
	return &types.QueryGetModelCapacityResponse{
		Capacity: uint64(capacity),
		Found:    true,
	}, nil
}

// GetAllModelCapacities returns cached capacities for all models that have capacity set
func (k Keeper) GetAllModelCapacities(goCtx context.Context, req *types.QueryGetAllModelCapacitiesRequest) (*types.QueryGetAllModelCapacitiesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	// Get all model capacities directly from our KV storage
	// We'll iterate through capacity storage to find all models with capacity set
	var modelCapacities []types.ModelCapacity

	// For now, get models from current epoch and check their capacities
	// TODO: This could be optimized by iterating KV prefix directly
	currentEpoch, found := k.GetEffectiveEpoch(goCtx)
	if !found {
		k.LogError("Failed to get current epoch for capacity query", types.Pricing)
		return &types.QueryGetAllModelCapacitiesResponse{
			ModelCapacities: modelCapacities,
		}, nil
	}

	mainEpochData, found := k.GetEpochGroupData(goCtx, uint64(currentEpoch.PocStartBlockHeight), "")
	if !found {
		k.LogError("Failed to get epoch group data for capacity query", types.Pricing)
		return &types.QueryGetAllModelCapacitiesResponse{
			ModelCapacities: modelCapacities,
		}, nil
	}

	// Get capacity for each model in current epoch
	for _, modelId := range mainEpochData.SubGroupModels {
		capacity, err := k.GetCachedModelCapacity(goCtx, modelId)
		if err != nil {
			k.LogWarn("Failed to get cached capacity for model", types.Pricing, "modelId", modelId, "error", err)
			continue
		}

		modelCapacities = append(modelCapacities, types.ModelCapacity{
			ModelId:  modelId,
			Capacity: uint64(capacity),
		})
	}

	k.LogInfo("Retrieved all model capacities", types.Pricing,
		"totalModels", len(modelCapacities))

	return &types.QueryGetAllModelCapacitiesResponse{
		ModelCapacities: modelCapacities,
	}, nil
}
