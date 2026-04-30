package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) PoCDelegation(ctx context.Context, req *types.QueryPoCDelegationRequest) (*types.QueryPoCDelegationResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	if req.Participant == "" {
		return nil, status.Error(codes.InvalidArgument, "participant required")
	}

	resp := &types.QueryPoCDelegationResponse{}

	if req.ModelId != "" {
		// Single model lookup
		if d, found := k.GetPoCDelegation(ctx, req.ModelId, req.Participant); found {
			resp.Delegations = append(resp.Delegations, &d)
		}
		if k.HasPoCRefusal(ctx, req.ModelId, req.Participant) {
			resp.Refusals = append(resp.Refusals, &types.PoCRefusal{
				ModelId:     req.ModelId,
				Participant: req.Participant,
			})
		}
		if k.HasPoCDirectIntent(ctx, req.ModelId, req.Participant) {
			resp.Intents = append(resp.Intents, &types.PoCDirectIntent{
				ModelId:     req.ModelId,
				Participant: req.Participant,
			})
		}
	} else {
		// All models for participant
		delegations, err := k.GetPoCDelegationsForParticipant(ctx, req.Participant)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		for i := range delegations {
			resp.Delegations = append(resp.Delegations, &delegations[i])
		}

		refusals, err := k.GetPoCRefusalsForParticipant(ctx, req.Participant)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		for i := range refusals {
			resp.Refusals = append(resp.Refusals, &refusals[i])
		}

		intents, err := k.GetPoCDirectIntentsForParticipant(ctx, req.Participant)
		if err != nil {
			return nil, status.Error(codes.Internal, err.Error())
		}
		for i := range intents {
			resp.Intents = append(resp.Intents, &intents[i])
		}
	}

	return resp, nil
}
