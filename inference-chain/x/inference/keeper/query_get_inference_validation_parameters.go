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
		k.LogError("GetInferenceValidationParameters: Error getting current epoch group", types.Validation, "error", err)
		return nil, status.Error(codes.Internal, "error getting current epoch group")
	}

	previousEpochGroup, err := k.GetPreviousEpochGroup(ctx)
	if err != nil {
		k.LogWarn("No previous Epoch Group found", types.EpochGroup)
	}

	k.LogDebug("GetInferenceValidationParameters", types.Validation, "currentEpochGroup", currentEpochGroup.GroupData.EpochGroupId, "previousEpochGroup", previousEpochGroup.GroupData.EpochGroupId)
	validations := make([]*types.InferenceValidationDetails, 0)
	for _, id := range req.Ids {
		validation, found := k.GetInferenceValidationDetails(ctx, currentEpochGroup.GroupData.EpochIndex, id)
		if !found {
			if previousEpochGroup != nil {
				validation, found = k.GetInferenceValidationDetails(ctx, previousEpochGroup.GroupData.EpochIndex, id)
				if !found {
					k.LogError("GetInferenceValidationParameters: Inference validation details not found", types.Validation, "id", id)
				}
			}
		}
		if found {
			validations = append(validations, &validation)
		}
	}
	weights, err := currentEpochGroup.GetValidationWeights()
	if err != nil {
		k.LogError("GetInferenceValidationParameters: Error getting validator weights", types.Validation, "error", err)
		return nil, status.Error(codes.Internal, "error getting validator weights")
	}
	validatorWeight := weights.Members[req.Requester]

	return &types.QueryGetInferenceValidationParametersResponse{
		CurrentHeight:  uint64(blockHeight),
		Details:        validations,
		ValidatorPower: uint64(validatorWeight),
		Parameters:     currentEpochGroup.GroupData.ValidationParams,
	}, nil
}
