package keeper

import (
	"context"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (s msgServer) SubmitValidatorsProof(goCtx context.Context, msg *types.MsgSubmitValidatorsProof) (*types.MsgSubmitValidatorsProofResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if err := s.Keeper.SetValidatorsSignatures(ctx, *msg.Proof); err != nil {
		return nil, err
	}

	return &types.MsgSubmitValidatorsProofResponse{}, nil
}
