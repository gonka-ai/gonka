package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) ListConfirmationPoCEvents(goCtx context.Context, req *types.QueryConfirmationPoCEventsRequest) (*types.QueryConfirmationPoCEventsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	events, err := k.GetAllConfirmationPoCEventsForEpoch(goCtx, req.EpochIndex)
	if err != nil {
		k.LogError("Error getting confirmation PoC events", types.PoC, "epochIndex", req.EpochIndex, "error", err)
		return nil, status.Error(codes.Internal, "failed to query confirmation PoC events")
	}

	// Convert to pointer slice for proto response
	eventPtrs := make([]*types.ConfirmationPoCEvent, len(events))
	for i := range events {
		eventPtrs[i] = &events[i]
	}

	return &types.QueryConfirmationPoCEventsResponse{Events: eventPtrs}, nil
}
