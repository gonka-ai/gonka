package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) EpochInfo(goCtx context.Context, req *types.QueryEpochInfoRequest) (*types.QueryEpochInfoResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	latestEpoch, found := k.GetLatestEpoch(ctx)
	if !found {
		k.LogError("GetLatestEpoch returned false, no latest epoch found", types.EpochGroup)
		return nil, types.ErrLatestEpochNotFound
	}
	if latestEpoch == nil {
		k.LogError("GetLatestEpoch returned nil, no latest epoch found", types.EpochGroup)
		return nil, types.ErrLatestEpochNotFound
	}

	// Check for active confirmation PoC event
	activeEvent, isActive, err := k.GetActiveConfirmationPoCEvent(ctx)
	if err != nil {
		k.LogError("Error getting active confirmation PoC event", types.PoC, "error", err)
		// Continue without event rather than failing the query
		isActive = false
	}

	effectiveEpochIndex, _ := k.GetEffectiveEpochIndex(ctx)

	response := &types.QueryEpochInfoResponse{
		BlockHeight:             ctx.BlockHeight(),
		Params:                  params,
		LatestEpoch:             *latestEpoch,
		IsConfirmationPocActive: isActive,
		EffectiveEpochIndex:     effectiveEpochIndex,
	}

	if params.EpochParams != nil {
		epochContext := types.NewEpochContext(*latestEpoch, *params.EpochParams)
		nextEpochContext := epochContext.NextEpochContext()
		response.Phase = string(epochContext.GetCurrentPhase(ctx.BlockHeight()))
		response.EpochStages = epochContext.GetEpochStages()
		response.NextEpochStages = nextEpochContext.GetEpochStages()
	}

	if isActive && activeEvent != nil {
		response.ActiveConfirmationPocEvent = activeEvent
	}

	return response, nil
}
