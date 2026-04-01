package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) CancelMaintenance(goCtx context.Context, msg *types.MsgCancelMaintenance) (*types.MsgCancelMaintenanceResponse, error) {
	if err := k.CheckPermission(goCtx, msg, AccountPermission); err != nil {
		return nil, err
	}
	// TODO: implement in Task 2.2
	return nil, types.ErrMaintenanceNotImplemented
}
