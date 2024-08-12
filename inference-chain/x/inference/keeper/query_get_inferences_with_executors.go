package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) GetInferencesWithExecutors(goCtx context.Context, req *types.QueryGetInferencesWithExecutorsRequest) (*types.QueryGetInferencesWithExecutorsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	inferences, found := k.GetInferences(ctx, req.Ids)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	var executorIds = make([]string, len(inferences))
	for i, inference := range inferences {
		executorIds[i] = inference.ExecutedBy
	}

	participants, found := k.GetParticipants(ctx, executorIds)
	if !found {
		return nil, status.Error(codes.NotFound, "not found")
	}

	var result = make([]types.InferenceWithExecutor, len(inferences))
	for i, inference := range inferences {
		executor := participants[i]

		if inference.ExecutedBy != executor.Index {
			return nil, status.Error(codes.Internal, "executor and inference do not match")
		}

		result[i] = types.InferenceWithExecutor{
			Inference: inference,
			Executor:  executor,
		}
	}

	numValidators := k.GetParticipantCounter(ctx)

	return &types.QueryGetInferencesWithExecutorsResponse{
		InferenceWithExecutor: result,
		NumValidators:         numValidators,
	}, nil
}
