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
	if len(req.Ids) == 0 {
		return nil, status.Error(codes.InvalidArgument, "ids cannot be empty")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)
	blockHeight := ctx.BlockHeight()

	inferences, found := k.GetInferences(ctx, req.Ids)
	if !found {
		k.LogError("GetInferencesWithExecutors: Inferences not found", "ids", req.Ids)
		return nil, status.Error(codes.NotFound, "inferences not found")
	}

	currentEpochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		k.LogError("GetInferencesWithExecutors: Error getting current epoch group", "error", err)
		return nil, status.Error(codes.Internal, "error getting current epoch group")
	}

	votingData, err := currentEpochGroup.GetVotingData(ctx)
	if err != nil {
		k.LogError("GetInferencesWithExecutors: Error getting voting data", "error", err)
		return nil, status.Error(codes.Internal, "error getting voting data")
	}

	var executorIds = make([]string, len(inferences))
	for i, inference := range inferences {
		if inference.ExecutedBy == "" {
			k.LogError("GetInferencesWithExecutors: Inference executed by cannot be empty", "inference", inference, "status", inference.Status)
			return nil, status.Error(codes.Internal, "inference executed by cannot be empty")
		}
		executorIds[i] = inference.ExecutedBy
	}

	participants, found := k.GetParticipants(ctx, executorIds)
	if !found {
		k.LogError("GetInferencesWithExecutors: Participants not found", "ids", executorIds)
		return nil, status.Error(codes.NotFound, "participant not found")
	}

	var result = make([]types.InferenceWithExecutor, len(inferences))
	for i, inference := range inferences {
		executor := participants[i]

		if inference.ExecutedBy != executor.Index {
			return nil, status.Error(codes.Internal, "executor and inference do not match")
		}

		weight, ok := votingData.Members[executor.Index]
		if !ok {
			k.LogWarn("GetInferencesWithExecutors: Error getting weight", "error", err)
			weight = 0
		}

		result[i] = types.InferenceWithExecutor{
			Inference:    inference,
			Executor:     executor,
			CurrentPower: uint32(weight),
		}
	}

	numValidators := k.GetParticipantCounter(ctx)
	validatorPower, ok := votingData.Members[req.Requester]
	if !ok {
		k.LogWarn("GetInferencesWithExecutors: Error getting validator power", "error", err)
		validatorPower = 0
	}

	return &types.QueryGetInferencesWithExecutorsResponse{
		InferenceWithExecutor: result,
		NumValidators:         numValidators,
		TotalPower:            uint32(votingData.TotalWeight),
		ValidatorPower:        uint32(validatorPower),
		CurrentHeight:         uint32(blockHeight),
	}, nil
}
