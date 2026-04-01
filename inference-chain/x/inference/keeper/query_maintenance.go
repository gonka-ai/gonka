package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) MaintenanceCredit(ctx context.Context, req *types.QueryMaintenanceCreditRequest) (*types.QueryMaintenanceCreditResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	// TODO: implement in Task 5.1
	return &types.QueryMaintenanceCreditResponse{Found: false}, nil
}

func (k Keeper) MaintenanceScheduled(ctx context.Context, req *types.QueryMaintenanceScheduledRequest) (*types.QueryMaintenanceScheduledResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	// TODO: implement in Task 5.1
	return &types.QueryMaintenanceScheduledResponse{Found: false}, nil
}

func (k Keeper) MaintenanceActive(ctx context.Context, req *types.QueryMaintenanceActiveRequest) (*types.QueryMaintenanceActiveResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	// TODO: implement in Task 5.1
	return &types.QueryMaintenanceActiveResponse{}, nil
}

func (k Keeper) MaintenanceStatus(ctx context.Context, req *types.QueryMaintenanceStatusRequest) (*types.QueryMaintenanceStatusResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	// TODO: implement in Task 5.1
	return &types.QueryMaintenanceStatusResponse{Found: false}, nil
}

func (k Keeper) MaintenanceConcurrency(ctx context.Context, req *types.QueryMaintenanceConcurrencyRequest) (*types.QueryMaintenanceConcurrencyResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	// TODO: implement in Task 5.1
	return &types.QueryMaintenanceConcurrencyResponse{}, nil
}

func (k Keeper) MaintenanceSchedulability(ctx context.Context, req *types.QueryMaintenanceSchedulabilityRequest) (*types.QueryMaintenanceSchedulabilityResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	// TODO: implement in Task 2.3
	return &types.QueryMaintenanceSchedulabilityResponse{Schedulable: false, RejectionReason: "not yet implemented"}, nil
}
