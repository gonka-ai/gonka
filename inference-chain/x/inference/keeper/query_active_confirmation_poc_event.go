package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) ActiveConfirmationPoCEvent(goCtx context.Context, req *types.QueryActiveConfirmationPoCEventRequest) (*types.QueryActiveConfirmationPoCEventResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	activeEvent, isActive, err := k.GetActiveConfirmationPoCEvent(goCtx)
	if err != nil {
		k.LogError("Error getting active confirmation PoC event", types.PoC, "error", err)
		return nil, status.Error(codes.Internal, "failed to query active confirmation PoC event")
	}

	response := &types.QueryActiveConfirmationPoCEventResponse{
		IsActive: isActive,
	}

	if isActive && activeEvent != nil {
		response.Event = activeEvent
	}

	return response, nil
}
