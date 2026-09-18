package keeper

import (
	"context"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) OpenPoCChallenges(ctx context.Context, req *types.QueryOpenPoCChallengesRequest) (*types.QueryOpenPoCChallengesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	list, err := k.ListPoCChallenges(ctx)
	if err != nil {
		return nil, err
	}
	limit := uint32(0)
	if params.PocChallengeParams != nil {
		limit = params.PocChallengeParams.MaxActiveChallenges
	}
	resp := &types.QueryOpenPoCChallengesResponse{}
	for _, ch := range list {
		if limit > 0 && uint32(len(resp.Challenges)) >= limit {
			break
		}
		finish, err := k.ChallengeFinish(ctx, ch)
		if err != nil {
			return nil, err
		}
		commits, err := k.ListChallengeCommits(ctx, ch.Target)
		if err != nil {
			return nil, err
		}
		stored := ch
		item := &types.OpenPoCChallenge{
			Challenge:  &stored,
			Finish:     finish,
			Generating: k.IsChallengeGenerating(ctx, ch.Target),
			Commits:    make([]*types.PoCV2StoreCommit, 0, len(commits)),
		}
		for i := range commits {
			commit := commits[i]
			item.Commits = append(item.Commits, &commit)
		}
		resp.Challenges = append(resp.Challenges, item)
	}
	_ = sdk.UnwrapSDKContext(ctx)
	return resp, nil
}
