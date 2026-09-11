package pocchallenge

import (
	"context"

	"github.com/productscience/inference/x/inference/types"
)

func SealOpenSegment(ctx context.Context, store *Store, target string, sealHeight, sliceBlocks int64) error {
	ch, found, err := store.Get(ctx, target)
	if err != nil || !found {
		return err
	}
	seg := store.OpenSegment(ch)
	if seg == nil || seg.SealHeight != 0 {
		return nil
	}
	seg.SealHeight = sealHeight
	if len(CountedSlices(seg.PocStageStartBlockHeight, sealHeight, sliceBlocks)) == 0 {
		seg.Outcome = types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_AUTO_PASSED
	}
	store.ReplaceSegment(&ch, *seg)
	return store.Set(ctx, ch)
}

func SealAllOpen(ctx context.Context, store *Store, sealHeight, sliceBlocks int64) error {
	list, err := store.ListOpen(ctx)
	if err != nil {
		return err
	}
	for _, ch := range list {
		if err := SealOpenSegment(ctx, store, ch.Target, sealHeight, sliceBlocks); err != nil {
			return err
		}
	}
	return nil
}

func LeaveGenerating(ctx context.Context, store *Store, target string, endHeight int64) error {
	ch, found, err := store.Get(ctx, target)
	if err != nil || !found {
		return err
	}
	if ch.GenerationEndHeight == 0 {
		ch.GenerationEndHeight = endHeight
	}
	return store.Set(ctx, ch)
}

func FailUnrelatedLeave(ctx context.Context, store *Store, target string, endHeight int64, effectiveEpoch uint64, haveEpoch bool) error {
	if !haveEpoch {
		return nil
	}
	ch, found, err := store.Get(ctx, target)
	if err != nil || !found {
		return err
	}
	if ch.EpochIndex != effectiveEpoch {
		return nil
	}
	if ReadyToPay(ch) {
		return nil
	}
	return FailChallenge(ctx, store, target, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNRELATED_REMOVAL, endHeight)
}

func FailChallenge(ctx context.Context, store *Store, target string, reason types.PoCChallengeFailReason, endHeight int64) error {
	ch, found, err := store.Get(ctx, target)
	if err != nil || !found {
		return err
	}
	if ch.FailReason != types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
		return nil
	}
	ch.FailReason = reason
	if ch.GenerationEndHeight == 0 {
		ch.GenerationEndHeight = endHeight
	}
	return store.Set(ctx, ch)
}

func AppendNextSegment(ctx context.Context, store *Store, target string, startHeight int64) error {
	ch, found, err := store.Get(ctx, target)
	if err != nil || !found {
		return err
	}
	if !IsGenerating(ch) {
		return nil
	}
	if store.SegmentByStart(ch, startHeight) != nil {
		return nil
	}
	ch.Segments = append(ch.Segments, &types.PoCChallengeSegment{
		PocStageStartBlockHeight: startHeight,
	})
	return store.Set(ctx, ch)
}

func SliceBlocks(params types.Params) int64 {
	if params.PocChallengeParams != nil && params.PocChallengeParams.SliceBlocks > 0 {
		return params.PocChallengeParams.SliceBlocks
	}
	return types.DefaultPoCChallengeSliceBlocks
}

func IsMissedRequestWaived(ctx context.Context, store *Store, addr string, height int64) bool {
	ch, found, err := store.Get(ctx, addr)
	if err != nil || !found {
		return false
	}
	if height < ch.ChallengeStartHeight {
		return false
	}
	if IsGenerating(ch) || height < ch.GenerationEndHeight {
		return true
	}
	return false
}

func WaiveDevshardMissesWhileGenerating(
	store *Store,
	ctx context.Context,
	escrow types.DevshardEscrow,
	hostStats types.DevshardSettlementHostStats,
	assignedToSlot uint64,
	settleHeight int64,
	host string,
) (types.DevshardSettlementHostStats, uint64) {
	ch, found, err := store.Get(ctx, host)
	if err != nil || !found {
		return hostStats, assignedToSlot
	}
	if escrow.CreateBlockHeight == 0 {
		return hostStats, assignedToSlot
	}
	openEnd := ch.GenerationEndHeight
	if IsGenerating(ch) {
		openEnd = settleHeight + 1
	}
	if escrow.CreateBlockHeight < openEnd && settleHeight >= ch.ChallengeStartHeight {
		original := uint64(hostStats.Missed)
		hostStats.Missed = 0
		if assignedToSlot >= original {
			assignedToSlot -= original
		} else {
			assignedToSlot = 0
		}
	}
	return hostStats, assignedToSlot
}
