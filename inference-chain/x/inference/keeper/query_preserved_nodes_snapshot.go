package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) PreservedNodesSnapshot(ctx context.Context, req *types.QueryPreservedNodesSnapshotRequest) (*types.QueryPreservedNodesSnapshotResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	snapshot, found, err := k.GetPreservedNodesSnapshot(ctx)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	resp := &types.QueryPreservedNodesSnapshotResponse{Found: found}
	if found {
		resp.Snapshot = &snapshot
	}
	return resp, nil
}
