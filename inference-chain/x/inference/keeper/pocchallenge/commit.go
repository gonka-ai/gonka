package pocchallenge

import (
	"context"
	"fmt"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func StoreCommit(ctx context.Context, chain Chain, store *Store, msg *types.MsgPoCChallengeStoreCommit) (*types.MsgPoCChallengeStoreCommitResponse, error) {
	if chain.IsPoCParticipantBlocked(ctx, msg.Creator) {
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, msg.Creator)
	}
	if len(msg.Entries) == 0 {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "entries must not be empty")
	}
	ch, found, err := store.Get(ctx, msg.Creator)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "no open challenge for signer")
	}
	seg := store.SegmentByStart(ch, msg.PocStageStartBlockHeight)
	if seg == nil {
		return nil, sdkerrors.Wrap(types.ErrPocWrongStartBlockHeight, "unknown segment")
	}

	params, err := chain.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	if ch.FailReason != types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "challenge already failed")
	}
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()
	if err := checkCommitSlice(seg, height, SliceBlocks(params), msg.SliceIndex); err != nil {
		return nil, err
	}
	assigned := AssignedConfirmationModels(ctx, chain, ch.EpochIndex, ch.Target)

	for _, entry := range msg.Entries {
		if entry == nil || entry.ModelId == "" {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "model_id must not be empty")
		}
		if entry.Count == 0 {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "entry count must be greater than 0")
		}
		if len(entry.RootHash) != 32 {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, fmt.Sprintf("root_hash must be 32 bytes, got %d", len(entry.RootHash)))
		}
		if !containsString(assigned, entry.ModelId) {
			return nil, sdkerrors.Wrap(types.ErrInvalidModel, entry.ModelId)
		}
		existing, has, err := store.GetCommit(ctx, msg.Creator, msg.PocStageStartBlockHeight, entry.ModelId, msg.SliceIndex)
		if err != nil {
			return nil, err
		}
		if has {
			if existing.CommitBlockHeight == height {
				return nil, sdkerrors.Wrap(types.ErrIllegalState, "only one commit per block allowed")
			}
			if entry.Count <= existing.Count {
				return nil, sdkerrors.Wrap(types.ErrIllegalState, "count must increase")
			}
		}
		if err := store.SetCommit(ctx, types.PoCChallengeCommit{
			Target:                   msg.Creator,
			PocStageStartBlockHeight: msg.PocStageStartBlockHeight,
			ModelId:                  entry.ModelId,
			SliceIndex:               msg.SliceIndex,
			Count:                    entry.Count,
			RootHash:                 entry.RootHash,
			CommitBlockHeight:        height,
		}); err != nil {
			return nil, err
		}
	}
	return &types.MsgPoCChallengeStoreCommitResponse{}, nil
}

func checkCommitSlice(seg *types.PoCChallengeSegment, height, sliceBlocks int64, sliceIndex uint32) error {
	if seg.Outcome != types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PENDING {
		return sdkerrors.Wrap(types.ErrIllegalState, "segment is not pending")
	}
	if seg.FirstVoteHeight != 0 {
		return sdkerrors.Wrap(types.ErrPocTooLate, "votes have started")
	}
	if seg.SealHeight == 0 {
		if sliceIndex != CurrentSliceIndex(height, seg.PocStageStartBlockHeight, sliceBlocks) {
			return sdkerrors.Wrap(types.ErrIllegalState, "only the current slice is writable")
		}
		return nil
	}
	if sliceIndex != LastSliceIndex(seg.PocStageStartBlockHeight, seg.SealHeight, sliceBlocks) {
		return sdkerrors.Wrap(types.ErrIllegalState, "only the last slice is writable after seal")
	}
	return nil
}
