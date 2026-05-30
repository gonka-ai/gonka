package keeper

import (
	"context"
	"fmt"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) SetDevshardRequestsEnabled(goCtx context.Context, msg *types.MsgSetDevshardRequestsEnabled) (*types.MsgSetDevshardRequestsEnabledResponse, error) {
	if err := k.CheckPermission(goCtx, msg, GuardianPermission); err != nil {
		return nil, err
	}

	params, err := k.GetParams(goCtx)
	if err != nil {
		return nil, fmt.Errorf("failed to get params: %w", err)
	}
	if params.DevshardEscrowParams == nil {
		params.DevshardEscrowParams = types.DefaultDevshardEscrowParams()
	}
	params.DevshardEscrowParams.DevshardRequestsEnabled = msg.Enabled

	if err := k.SetParams(goCtx, params); err != nil {
		return nil, fmt.Errorf("failed to set params: %w", err)
	}
	return &types.MsgSetDevshardRequestsEnabledResponse{}, nil
}
