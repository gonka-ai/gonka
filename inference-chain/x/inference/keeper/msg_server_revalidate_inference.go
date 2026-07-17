package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) RevalidateInference(ctx context.Context, msg *types.MsgRevalidateInference) (*types.MsgRevalidateInferenceResponse, error) {
	if err := k.CheckPermission(ctx, msg, NoPermission); err != nil {
		return nil, err
	}

	k.LogInfo("MsgRevalidateInference deprecated", types.Validation, "inferenceId", msg.InferenceId, "creator", msg.Creator)
	return nil, classicInferenceDeprecatedError()
}
