package pocchallenge

import (
	"context"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

func SubmitValidations(ctx context.Context, chain Chain, store *Store, msg *types.MsgSubmitPoCChallengeValidations) (*types.MsgSubmitPoCChallengeValidationsResponse, error) {
	if chain.IsPoCParticipantBlocked(ctx, msg.Creator) {
		return nil, sdkerrors.Wrap(types.ErrParticipantBlocked, msg.Creator)
	}
	params, err := chain.GetParams(ctx)
	if err != nil {
		return nil, err
	}
	_, live, err := chain.GetRootGroupDataWithLiveMembers(ctx)
	if err != nil {
		return nil, err
	}
	if !live[msg.Creator] {
		return nil, sdkerrors.Wrap(types.ErrIllegalState, "voter is not a live epoch member")
	}
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()
	stored := 0
	for _, entry := range msg.Validations {
		if entry == nil || entry.ModelId == "" || entry.ParticipantAddress == "" {
			continue
		}
		if msg.Creator == entry.ParticipantAddress {
			continue
		}
		ch, found, err := store.Get(ctx, entry.ParticipantAddress)
		if err != nil {
			return nil, err
		}
		if !found || ch.FailReason != types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
			continue
		}
		seg := store.SegmentByStart(ch, msg.PocStageStartBlockHeight)
		if seg == nil || seg.SealHeight == 0 ||
			seg.Outcome != types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PENDING {
			continue
		}
		assigned := AssignedConfirmationModels(ctx, chain, ch.EpochIndex, ch.Target)
		if !containsString(assigned, entry.ModelId) {
			continue
		}
		if !chain.EligibleChallengeVoter(ctx, ch.EpochIndex, entry.ModelId, msg.Creator) {
			continue
		}
		counted := CountedSlices(seg.PocStageStartBlockHeight, seg.SealHeight, SliceBlocks(params))
		ready, err := HasRequiredCommits(ctx, store, ch.Target, seg.PocStageStartBlockHeight, counted, assigned)
		if err != nil {
			return nil, err
		}
		if !ready {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "counted slice commits are incomplete")
		}
		exists, err := store.HasValidation(ctx, entry.ParticipantAddress, msg.PocStageStartBlockHeight, entry.ModelId, msg.Creator)
		if err != nil {
			return nil, err
		}
		if exists {
			continue
		}
		if err := store.SetValidation(ctx, types.PoCChallengeValidation{
			Target:                   entry.ParticipantAddress,
			PocStageStartBlockHeight: msg.PocStageStartBlockHeight,
			ModelId:                  entry.ModelId,
			Validator:                msg.Creator,
			ValidatedWeight:          entry.ValidatedWeight,
		}); err != nil {
			return nil, err
		}
		if seg.FirstVoteHeight == 0 {
			seg.FirstVoteHeight = height
			store.ReplaceSegment(&ch, *seg)
			if err := store.Set(ctx, ch); err != nil {
				return nil, err
			}
		}
		stored++
	}
	chain.LogInfo("PoCChallenge validations stored", types.PoC,
		"validator", msg.Creator,
		"start", msg.PocStageStartBlockHeight,
		"stored", stored)
	return &types.MsgSubmitPoCChallengeValidationsResponse{}, nil
}

func ConfirmationWeightNodes(ctx context.Context, chain Chain, epochIndex uint64, target string) map[string][]*types.MLNodeInfo {
	out := make(map[string][]*types.MLNodeInfo)
	for _, group := range chain.GetAllEpochGroupData(ctx) {
		if group.EpochIndex != epochIndex || group.ModelId == "" {
			continue
		}
		for _, vw := range group.ValidationWeights {
			if vw != nil && vw.MemberAddress == target && len(vw.MlNodes) > 0 {
				out[group.ModelId] = vw.MlNodes
				break
			}
		}
	}
	return out
}

func AssignedConfirmationModels(ctx context.Context, chain Chain, epochIndex uint64, target string) []string {
	nodes := ConfirmationWeightNodes(ctx, chain, epochIndex, target)
	out := make([]string, 0, len(nodes))
	for id := range nodes {
		out = append(out, id)
	}
	return out
}

func ConfirmationPocWeight(nodes map[string][]*types.MLNodeInfo) int64 {
	var sum int64
	for _, list := range nodes {
		for _, n := range list {
			if n != nil {
				sum += n.PocWeight
			}
		}
	}
	return sum
}

func HasRequiredCommits(
	ctx context.Context,
	store *Store,
	target string,
	start int64,
	counted []SliceRange,
	assigned []string,
) (bool, error) {
	if len(counted) == 0 || len(assigned) == 0 {
		return false, nil
	}
	commits, err := store.ListCommitsForSegment(ctx, target, start)
	if err != nil {
		return false, err
	}
	have := make(map[string]map[uint32]bool)
	for _, c := range commits {
		if have[c.ModelId] == nil {
			have[c.ModelId] = make(map[uint32]bool)
		}
		have[c.ModelId][c.SliceIndex] = true
	}
	for _, model := range assigned {
		for _, sl := range counted {
			if !have[model][sl.Index] {
				return false, nil
			}
		}
	}
	return true, nil
}

func containsString(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}
