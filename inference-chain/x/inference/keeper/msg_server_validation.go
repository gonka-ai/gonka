package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) Validation(goCtx context.Context, msg *types.MsgValidation) (*types.MsgValidationResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ActiveParticipantPermission, PreviousActiveParticipantPermission); err != nil {
		return nil, err
	}

	k.LogInfo("MsgValidation deprecated", types.Validation, "msg.Creator", msg.Creator, "inferenceId", msg.InferenceId)
	return nil, classicInferenceDeprecatedError()
}
