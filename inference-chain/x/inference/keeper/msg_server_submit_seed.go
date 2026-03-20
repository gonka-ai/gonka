package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitSeed(ctx context.Context, msg *types.MsgSubmitSeed) (*types.MsgSubmitSeedResponse, error) {
	if err := k.CheckPermission(ctx, msg, ParticipantPermission); err != nil {
		return nil, err
	}

	seed := types.RandomSeed{
		Participant: msg.Creator,
		EpochIndex:  msg.EpochIndex,
		Signature:   msg.Signature,
	}

	if err := k.SetRandomSeed(ctx, seed); err != nil {
		return nil, err
	}

	return &types.MsgSubmitSeedResponse{}, nil
}
