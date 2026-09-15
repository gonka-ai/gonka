package pocchallenge

import (
	"context"

	sdkerrors "cosmossdk.io/errors"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

type validationTarget struct {
	ch       types.PoCChallenge
	found    bool
	seg      *types.PoCChallengeSegment
	assigned []string
	ready    bool
}

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
	elig, err := chain.ChallengeVoterEligibility(ctx, msg.Creator)
	if err != nil {
		return nil, err
	}
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()
	targets := make(map[string]*validationTarget)
	written := make(map[string]struct{})
	stored := 0
	for _, entry := range msg.Validations {
		if entry == nil || entry.ModelId == "" || entry.ParticipantAddress == "" {
			continue
		}
		if msg.Creator == entry.ParticipantAddress {
			continue
		}
		tc, err := loadValidationTarget(ctx, chain, store, targets, entry.ParticipantAddress, msg.PocStageStartBlockHeight, params)
		if err != nil {
			return nil, err
		}
		if !tc.found || tc.ch.FailReason != types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
			continue
		}
		if tc.seg == nil || tc.seg.SealHeight == 0 ||
			tc.seg.Outcome != types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PENDING {
			continue
		}
		if !containsString(tc.assigned, entry.ModelId) {
			continue
		}
		if !elig.Allows(entry.ModelId) {
			continue
		}
		if !tc.ready {
			return nil, sdkerrors.Wrap(types.ErrIllegalState, "counted slice commits are incomplete")
		}
		dupKey := entry.ParticipantAddress + "/" + entry.ModelId
		if _, ok := written[dupKey]; ok {
			continue
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
		written[dupKey] = struct{}{}
		if tc.seg.FirstVoteHeight == 0 {
			tc.seg.FirstVoteHeight = height
			store.ReplaceSegment(&tc.ch, *tc.seg)
			if err := store.Set(ctx, tc.ch); err != nil {
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

func loadValidationTarget(
	ctx context.Context,
	chain Chain,
	store *Store,
	cache map[string]*validationTarget,
	target string,
	start int64,
	params types.Params,
) (*validationTarget, error) {
	if tc, ok := cache[target]; ok {
		return tc, nil
	}
	ch, found, err := store.Get(ctx, target)
	if err != nil {
		return nil, err
	}
	tc := &validationTarget{ch: ch, found: found}
	if found && ch.FailReason == types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
		tc.seg = store.SegmentByStart(ch, start)
		if tc.seg != nil && tc.seg.SealHeight != 0 &&
			tc.seg.Outcome == types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PENDING {
			tc.assigned = AssignedConfirmationModels(ctx, chain, ch.EpochIndex, ch.Target)
			counted := CountedSlices(tc.seg.PocStageStartBlockHeight, tc.seg.SealHeight, SliceBlocks(params))
			ready, err := HasRequiredCommits(ctx, store, ch.Target, tc.seg.PocStageStartBlockHeight, counted, tc.assigned)
			if err != nil {
				return nil, err
			}
			tc.ready = ready
		}
	}
	cache[target] = tc
	return tc, nil
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
