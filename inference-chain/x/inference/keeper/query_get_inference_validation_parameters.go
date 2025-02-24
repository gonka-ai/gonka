package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) GetInferenceValidationParameters(goCtx context.Context, req *types.QueryGetInferenceValidationParametersRequest) (*types.QueryGetInferenceValidationParametersResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	if len(req.Ids) == 0 {
		return nil, status.Error(codes.InvalidArgument, "ids cannot be empty")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	blockHeight := ctx.BlockHeight()

	currentEpochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("GetInferenceValidationParameters: Error getting current epoch group", "error", err)
		return nil, status.Error(codes.Internal, "error getting current epoch group")
	}

	previousEpochGroup, err := k.GetPreviousEpochGroup(ctx)
	if err != nil {
		k.LogWarn("No previous Epoch Group found")
	}

	validations := make([]types.InferenceValidationDetails, 0)
	for _, id := range req.Ids {
		validation, found := k.GetInferenceValidationDetails(ctx, currentEpochGroup.GroupData.EpochGroupId, id)
		if !found {
			if previousEpochGroup != nil {
				validation, found = k.GetInferenceValidationDetails(ctx, previousEpochGroup.GroupData.EpochGroupId, id)
				if !found {
					k.LogError("GetInferenceValidationParameters: Inference validation details not found", "id", id)
				}
			}
		}
		if found {
			validations = append(validations, validation)
		}
	}

	return &types.QueryGetInferenceValidationParametersResponse{
		TotalPower: currentEpochGroup.GroupData.TotalWeight,
		ValidatorPower: 
	}, nil
}
