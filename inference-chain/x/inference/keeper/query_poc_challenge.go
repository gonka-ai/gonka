package keeper

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (k Keeper) OpenPoCChallenges(ctx context.Context, req *types.QueryOpenPoCChallengesRequest) (*types.QueryOpenPoCChallengesResponse, error) {
	if req == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	list, err := k.ListPoCChallenges(ctx)
	if err != nil {
		return nil, err
	}
	resp := &types.QueryOpenPoCChallengesResponse{}
	for _, ch := range list {
		if ch.State != types.PoCChallengeState_POC_CHALLENGE_STATE_OPEN {
			continue
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
			Generating: k.IsUnderChallenge(ctx, ch.Target),
			Commits:    make([]*types.PoCV2StoreCommit, 0, len(commits)),
		}
		for i := range commits {
			commit := commits[i]
			item.Commits = append(item.Commits, &commit)
		}
		resp.Challenges = append(resp.Challenges, item)
	}
	return resp, nil
}
