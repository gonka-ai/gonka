package keeper

import (
	"context"
	"errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func (s msgServer) SubmitParticipantsProof(goCtx context.Context, msg *types.MsgSubmitParticipantsProof) (*types.MsgSubmitParticipantsProofResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)
	if msg.BlockHeight == 0 {
		return nil, errors.New("block height must be set")
	}

	if msg.ValidatorsProof != nil {
		if err := s.Keeper.SetValidatorsSignatures(ctx, *msg.ValidatorsProof); err != nil {
			return nil, err
		}
	}

	if msg.ProofOpts != nil {
		s.Keeper.SetActiveParticipantsProof(ctx, *msg.ProofOpts, msg.BlockHeight)
	}
	return &types.MsgSubmitParticipantsProofResponse{}, nil
}
