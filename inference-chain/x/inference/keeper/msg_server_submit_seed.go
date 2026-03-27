package keeper

import (
	"context"

	errorsmod "cosmossdk.io/errors"
	sdkerrors "github.com/cosmos/cosmos-sdk/types/errors"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitSeed(ctx context.Context, msg *types.MsgSubmitSeed) (*types.MsgSubmitSeedResponse, error) {
	if err := k.CheckPermission(ctx, msg, ParticipantPermission); err != nil {
		return nil, err
	}
	if msg.Seed <= 0 {
		return nil, errorsmod.Wrap(sdkerrors.ErrInvalidRequest, "seed must be > 0")
	}

	seed := types.RandomSeed{
		Participant: msg.Creator,
		EpochIndex:  msg.EpochIndex,
		Signature:   msg.Signature,
		Seed:        msg.Seed,
	}

	if err := k.SetRandomSeed(ctx, seed); err != nil {
		return nil, err
	}

	return &types.MsgSubmitSeedResponse{}, nil
}
