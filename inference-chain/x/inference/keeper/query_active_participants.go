package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ActiveParticipants returns the ActiveParticipants record for an epoch.
// epoch_index 0 resolves to the effective/current epoch (not latest).
// During main PoC, latest advances while effective/current stay put until
// set_new_validators; inference routing should query effective/current.
func (k Keeper) ActiveParticipants(ctx context.Context, req *types.QueryActiveParticipantsRequest) (*types.QueryActiveParticipantsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	epochIndex := req.EpochIndex
	if epochIndex == 0 {
		idx, ok := k.GetEffectiveEpochIndex(ctx)
		if !ok {
			return nil, status.Error(codes.NotFound, "no effective epoch found")
		}
		epochIndex = idx
	}

	participants, found := k.GetActiveParticipants(ctx, epochIndex)
	if !found {
		return nil, status.Errorf(codes.NotFound, "active participants not found for epoch %d", epochIndex)
	}

	return &types.QueryActiveParticipantsResponse{
		ActiveParticipants: participants,
		EpochIndex:         epochIndex,
	}, nil
}
