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

	snapshot, found, err := k.GetPreservedNodesSnapshot(ctx, req.EpisodeAnchorHeight)
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}

	if !found {
		return &types.QueryPreservedNodesSnapshotResponse{
			Snapshot: nil,
			Found:    false,
		}, nil
	}

	return &types.QueryPreservedNodesSnapshotResponse{
		Snapshot: &snapshot,
		Found:    true,
	}, nil
}
