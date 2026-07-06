package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) InvalidateInference(ctx context.Context, msg *types.MsgInvalidateInference) (*types.MsgInvalidateInferenceResponse, error) {
	if err := k.CheckPermission(ctx, msg, NoPermission); err != nil {
		return nil, err
	}

	k.LogInfo("MsgInvalidateInference deprecated", types.Validation, "inferenceId", msg.InferenceId, "creator", msg.Creator)
	return nil, classicInferenceDeprecatedError()
}
