package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) Trainshard(ctx context.Context, req *types.QueryGetTrainshardRequest) (*types.QueryGetTrainshardResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	shard, err := k.Trainshards.Get(ctx, req.TrainshardId)
	if err != nil {
		return &types.QueryGetTrainshardResponse{Found: false}, nil
	}
	return &types.QueryGetTrainshardResponse{Trainshard: &shard, Found: true}, nil
}

func (k Keeper) ActiveTrainshards(ctx context.Context, req *types.QueryActiveTrainshardsRequest) (*types.QueryActiveTrainshardsResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	var shards []*types.Trainshard
	err := k.TrainshardActiveIndex.Walk(ctx, nil, func(id uint64) (bool, error) {
		shard, err := k.Trainshards.Get(ctx, id)
		if err != nil {
			return false, err
		}
		shards = append(shards, &shard)
		return false, nil
	})
	if err != nil {
		return nil, status.Error(codes.Internal, err.Error())
	}
	return &types.QueryActiveTrainshardsResponse{Trainshards: shards}, nil
}

func (k Keeper) TrainshardProposal(ctx context.Context, req *types.QueryGetTrainshardProposalRequest) (*types.QueryGetTrainshardProposalResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	proposal, err := k.TrainshardProposals.Get(ctx, req.ProposalId)
	if err != nil {
		return &types.QueryGetTrainshardProposalResponse{Found: false}, nil
	}
	return &types.QueryGetTrainshardProposalResponse{Proposal: &proposal, Found: true}, nil
}
