package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitSeed(goCtx context.Context, msg *types.MsgSubmitSeed) (*types.MsgSubmitSeedResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

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
