package keeper

import (
	"context"

	sdkerrors "cosmossdk.io/errors"
	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SubmitNewUnfundedParticipant(goCtx context.Context, msg *types.MsgSubmitNewUnfundedParticipant) (*types.MsgSubmitNewUnfundedParticipantResponse, error) {
	if err := k.CheckPermission(goCtx, msg, OpenRegistrationPermission); err != nil {
		return nil, err
	}
	return nil, sdkerrors.Wrap(
		types.ErrDeprecated,
		"MsgSubmitNewUnfundedParticipant is deprecated; fund the account and register with MsgSubmitNewParticipant",
	)
}

