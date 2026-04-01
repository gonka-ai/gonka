package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) CancelMaintenance(goCtx context.Context, msg *types.MsgCancelMaintenance) (*types.MsgCancelMaintenanceResponse, error) {
	// TODO: implement in Task 2.2
	return nil, types.ErrMaintenanceNotImplemented
}
