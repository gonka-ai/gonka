package keeper

import (
	"context"
	"errors"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// PoCV2StoreCommit returns the stored commit for a participant at a given PoC stage.
func (k Keeper) PoCV2StoreCommit(goCtx context.Context, req *types.QueryPoCV2StoreCommitRequest) (*types.QueryPoCV2StoreCommitResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	addr, err := sdk.AccAddressFromBech32(req.ParticipantAddress)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid participant address: %v", err)
	}

	pk := collections.Join(req.PocStageStartBlockHeight, addr)
	commit, err := k.PoCV2StoreCommits.Get(ctx, pk)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return &types.QueryPoCV2StoreCommitResponse{Found: false}, nil
		}
		return nil, status.Errorf(codes.Internal, "failed to get commit: %v", err)
	}

	return &types.QueryPoCV2StoreCommitResponse{
		Count:    commit.Count,
		RootHash: commit.RootHash,
		Found:    true,
	}, nil
}

// AllPoCV2StoreCommitsForStage returns all store commits for a given PoC stage.
func (k Keeper) AllPoCV2StoreCommitsForStage(goCtx context.Context, req *types.QueryAllPoCV2StoreCommitsForStageRequest) (*types.QueryAllPoCV2StoreCommitsForStageResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	var commits []*types.PoCV2StoreCommitWithAddress

	iter, err := k.PoCV2StoreCommits.Iterate(ctx, collections.NewPrefixedPairRange[int64, sdk.AccAddress](req.PocStageStartBlockHeight))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to iterate commits: %v", err)
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get key: %v", err)
		}
		value, err := iter.Value()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get value: %v", err)
		}

		addr := key.K2()
		acc := k.AccountKeeper.GetAccount(ctx, addr)
		if acc == nil {
			k.LogError("AllPoCV2StoreCommitsForStage. Account not found", types.PoC, "address", addr.String())
			continue
		}

		pubKey := acc.GetPubKey()
		if pubKey == nil {
			k.LogError("AllPoCV2StoreCommitsForStage. PubKey not found", types.PoC, "address", addr.String())
			continue
		}

		commits = append(commits, &types.PoCV2StoreCommitWithAddress{
			ParticipantAddress: addr.String(),
			Count:              value.Count,
			RootHash:           value.RootHash,
			HexPubKey:          utils.PubKeyToHexString(pubKey),
		})
	}

	return &types.QueryAllPoCV2StoreCommitsForStageResponse{
		Commits: commits,
	}, nil
}

// MLNodeWeightDistribution returns the stored weight distribution for a participant at a given PoC stage.
func (k Keeper) MLNodeWeightDistribution(goCtx context.Context, req *types.QueryMLNodeWeightDistributionRequest) (*types.QueryMLNodeWeightDistributionResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	addr, err := sdk.AccAddressFromBech32(req.ParticipantAddress)
	if err != nil {
		return nil, status.Errorf(codes.InvalidArgument, "invalid participant address: %v", err)
	}

	pk := collections.Join(req.PocStageStartBlockHeight, addr)
	distribution, err := k.MLNodeWeightDistributions.Get(ctx, pk)
	if err != nil {
		if errors.Is(err, collections.ErrNotFound) {
			return &types.QueryMLNodeWeightDistributionResponse{Found: false}, nil
		}
		return nil, status.Errorf(codes.Internal, "failed to get distribution: %v", err)
	}

	return &types.QueryMLNodeWeightDistributionResponse{
		Weights: distribution.Weights,
		Found:   true,
	}, nil
}

// AllMLNodeWeightDistributionsForStage returns all weight distributions for a given PoC stage.
func (k Keeper) AllMLNodeWeightDistributionsForStage(goCtx context.Context, req *types.QueryAllMLNodeWeightDistributionsForStageRequest) (*types.QueryAllMLNodeWeightDistributionsForStageResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}

	ctx := sdk.UnwrapSDKContext(goCtx)

	var distributions []*types.MLNodeWeightDistributionWithAddress

	iter, err := k.MLNodeWeightDistributions.Iterate(ctx, collections.NewPrefixedPairRange[int64, sdk.AccAddress](req.PocStageStartBlockHeight))
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to iterate distributions: %v", err)
	}
	defer iter.Close()

	for ; iter.Valid(); iter.Next() {
		key, err := iter.Key()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get key: %v", err)
		}
		value, err := iter.Value()
		if err != nil {
			return nil, status.Errorf(codes.Internal, "failed to get value: %v", err)
		}

		addr := key.K2()
		distributions = append(distributions, &types.MLNodeWeightDistributionWithAddress{
			ParticipantAddress: addr.String(),
			Weights:            value.Weights,
		})
	}

	return &types.QueryAllMLNodeWeightDistributionsForStageResponse{
		Distributions: distributions,
	}, nil
}
