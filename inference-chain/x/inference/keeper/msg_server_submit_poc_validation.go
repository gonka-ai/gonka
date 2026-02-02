package keeper

import (
	"context"

	sdkerrors "cosmossdk.io/errors"
	"github.com/productscience/inference/x/inference/types"
)

const PocFailureTag = "[PoC Failure]"

func (k msgServer) SubmitPocValidation(goCtx context.Context, msg *types.MsgSubmitPocValidation) (*types.MsgSubmitPocValidationResponse, error) {
	// V1 dispatch: route to V1 handler when poc_v2_enabled=false
	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, err
	}
	if !params.PocParams.PocV2Enabled {
		return k.submitPocValidationV1(goCtx, msg)
	}

	k.logger.Info("SubmitPocValidation", "poc_v2_enabled", params.PocParams.PocV2Enabled)

	// V2 mode: this message type is deprecated
	return nil, sdkerrors.Wrap(types.ErrDeprecated, "MsgSubmitPocValidation is deprecated when poc_v2_enabled=true, use MsgSubmitPocValidationsV2 instead")
}
