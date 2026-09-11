package pocchallenge

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func GenerationState(ctx context.Context, store *Store, target string, height int64, sliceBlocks int64) (*types.QueryChallengeGenerationStateResponse, error) {
	ch, found, err := store.Get(ctx, target)
	if err != nil {
		return nil, err
	}
	resp := &types.QueryChallengeGenerationStateResponse{
		Generating: store.IsChallengeGenerating(ctx, target),
		Open:       found,
	}
	if !found {
		return resp, nil
	}
	open := store.OpenSegment(ch)
	if open != nil {
		resp.PocStageStartBlockHeight = open.PocStageStartBlockHeight
		resp.SeedHash = open.SeedHash
		resp.CurrentSliceIndex = CurrentSliceIndex(height, open.PocStageStartBlockHeight, sliceBlocks)
	} else if last := lastSealed(ch); last != nil {
		resp.PocStageStartBlockHeight = last.PocStageStartBlockHeight
		resp.SeedHash = last.SeedHash
		resp.CurrentSliceIndex = LastSliceIndex(last.PocStageStartBlockHeight, last.SealHeight, sliceBlocks)
	}
	if ch.FailReason == types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
		for _, seg := range ch.Segments {
			if seg != nil && seg.SealHeight != 0 &&
				seg.Outcome == types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PENDING {
				resp.SealedPendingSegments = append(resp.SealedPendingSegments, seg)
			}
		}
	}
	return resp, nil
}

func lastSealed(ch types.PoCChallenge) *types.PoCChallengeSegment {
	var last *types.PoCChallengeSegment
	for _, seg := range ch.Segments {
		if seg != nil && seg.SealHeight != 0 {
			last = seg
		}
	}
	return last
}

func OpenChallenges(ctx context.Context, store *Store, sliceBlocks int64) (*types.QueryOpenPoCChallengesResponse, error) {
	list, err := store.ListOpen(ctx)
	if err != nil {
		return nil, err
	}
	resp := &types.QueryOpenPoCChallengesResponse{}
	for i := range list {
		ch := list[i]
		if ch.FailReason != types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
			continue
		}
		item := &types.OpenPoCChallenge{Challenge: &ch}
		for _, seg := range ch.Segments {
			if seg == nil || seg.SealHeight == 0 ||
				seg.Outcome != types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PENDING {
				continue
			}
			counted := CountedSlices(seg.PocStageStartBlockHeight, seg.SealHeight, sliceBlocks)
			commits, err := store.ListCommitsForSegment(ctx, ch.Target, seg.PocStageStartBlockHeight)
			if err != nil {
				return nil, err
			}
			wanted := make(map[uint32]struct{}, len(counted))
			for _, sl := range counted {
				wanted[sl.Index] = struct{}{}
			}
			for _, c := range commits {
				if _, ok := wanted[c.SliceIndex]; !ok {
					continue
				}
				item.CountedCommits = append(item.CountedCommits, &types.CountedSliceCommit{
					PocStageStartBlockHeight: c.PocStageStartBlockHeight,
					SliceIndex:               c.SliceIndex,
					ModelId:                  c.ModelId,
					Count:                    c.Count,
					RootHash:                 c.RootHash,
				})
			}
		}
		resp.Challenges = append(resp.Challenges, item)
	}
	return resp, nil
}
