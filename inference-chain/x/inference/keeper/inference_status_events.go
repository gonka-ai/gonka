package keeper

import (
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const inferenceStatusUpdatedEventType = "inference_status_updated"

func emitInferenceStatusUpdatedEvent(ctx sdk.Context, inferenceID string, status types.InferenceStatus) {
	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			inferenceStatusUpdatedEventType,
			sdk.NewAttribute("inference_id", inferenceID),
			sdk.NewAttribute("status", status.String()),
		),
	)
}
