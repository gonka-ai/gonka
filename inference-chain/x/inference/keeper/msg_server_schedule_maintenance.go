package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func (k msgServer) ScheduleMaintenance(goCtx context.Context, msg *types.MsgScheduleMaintenance) (*types.MsgScheduleMaintenanceResponse, error) {
	// TODO: implement in Task 2.1
	return nil, types.ErrMaintenanceNotImplemented
}
