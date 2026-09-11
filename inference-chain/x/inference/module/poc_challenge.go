package inference

import (
	"context"

	mathsdk "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/keeper/pocchallenge"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
)

func (am AppModule) EvaluateSealedSegment(ctx context.Context, target string, startHeight int64, snapshot types.PoCValidationSnapshot) error {
	store := am.keeper.PoCChallenge
	ch, found, err := store.Get(ctx, target)
	if err != nil || !found {
		return err
	}
	if ch.FailReason != types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
		return nil
	}
	seg := store.SegmentByStart(ch, startHeight)
	if seg == nil || seg.SealHeight == 0 ||
		seg.Outcome != types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PENDING {
		return nil
	}

	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return err
	}
	sliceBlocks := pocchallenge.SliceBlocks(params)
	counted := pocchallenge.CountedSlices(seg.PocStageStartBlockHeight, seg.SealHeight, sliceBlocks)
	nodes := pocchallenge.ConfirmationWeightNodes(ctx, &am.keeper, ch.EpochIndex, target)
	assigned := make([]string, 0, len(nodes))
	for id := range nodes {
		assigned = append(assigned, id)
	}
	ready, err := pocchallenge.HasRequiredCommits(ctx, store, target, startHeight, counted, assigned)
	if err != nil {
		return err
	}
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()
	if !ready {
		return am.failSealedSegment(ctx, store, ch, seg, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_MISSING_COMMIT, height, 0, 1)
	}
	if seg.FirstVoteHeight == 0 {
		return nil
	}
	if snapshot.PocStageStartHeight == 0 {
		return nil
	}

	commits, err := store.ListCommitsForSegment(ctx, target, startHeight)
	if err != nil {
		return err
	}
	commitByModelSlice := make(map[string]map[uint32]types.PoCChallengeCommit)
	for _, c := range commits {
		if commitByModelSlice[c.ModelId] == nil {
			commitByModelSlice[c.ModelId] = make(map[uint32]types.PoCChallengeCommit)
		}
		commitByModelSlice[c.ModelId][c.SliceIndex] = c
	}
	votes, err := store.ListValidationsForSegment(ctx, target, startHeight)
	if err != nil {
		return err
	}
	inputs := am.challengeCalcInputs(ctx, ch, snapshot, params)

	pocWeight := pocchallenge.ConfirmationPocWeight(nodes)
	denom := pocDurationDenom(params.EpochParams)
	validatedSum, expectedSum, failReason := accumulateSegmentReading(counted, pocWeight, denom, params.ConfirmationPocParams, func(sl pocchallenge.SliceRange) (bool, int64, bool) {
		return am.evaluateCountedSlice(ch, sl, commitByModelSlice, votes, nodes, assigned, params, inputs)
	})

	if failReason != types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
		reading := validatedSum
		if failReason == types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_REJECTED {
			reading = 0
		}
		return am.failSealedSegment(ctx, store, ch, seg, failReason, height, reading, expectedSum)
	}

	return am.passSealedSegment(ctx, store, ch, seg, validatedSum, expectedSum)
}

type challengeCalcInputs struct {
	modelVP         map[string]map[string]int64
	totalWeight     int64
	participant     types.Participant
	seeds           map[string]types.RandomSeed
	guardianEnabled bool
	guardianSet     map[string]bool
	appHash         string
	slots           int
}

func challengeVotingPower(target string, snapshot types.PoCValidationSnapshot, rootWeight int64) (map[string]map[string]int64, int64) {
	modelVP := make(map[string]map[string]int64)
	for _, mvw := range snapshot.ModelVotingPowers {
		if mvw == nil {
			continue
		}
		vp := types.VotingPowerSliceToMap(mvw.VotingPowers)
		if _, ok := vp[target]; ok && len(vp) > 1 {
			delete(vp, target)
		}
		modelVP[mvw.ModelId] = vp
	}
	totalWeight := snapshot.TotalNetworkWeight - rootWeight
	if totalWeight < 0 {
		totalWeight = 0
	}
	return modelVP, totalWeight
}

func rootConsensusWeight(weights []*types.ValidationWeight, target string) int64 {
	for _, vw := range weights {
		if vw != nil && vw.MemberAddress == target {
			return vw.Weight
		}
	}
	return 0
}

func targetTrustWeight(target string, rootWeights []*types.ValidationWeight, participants []*types.ActiveParticipant, capApplied bool) int64 {
	fallback := rootConsensusWeight(rootWeights, target)
	if w, ok := resolveTrustWeights(participants, capApplied)[target]; ok {
		return w
	}
	return fallback
}

func (am AppModule) challengeCalcInputs(
	ctx context.Context,
	ch types.PoCChallenge,
	snapshot types.PoCValidationSnapshot,
	params types.Params,
) challengeCalcInputs {
	root, _ := am.keeper.GetEpochGroupData(ctx, ch.EpochIndex, "")
	var participants []*types.ActiveParticipant
	var capApplied bool
	if aps, found := am.keeper.GetActiveParticipants(ctx, ch.EpochIndex); found {
		participants = aps.Participants
		capApplied = aps.CapWeightApplied
	}
	modelVP, totalWeight := challengeVotingPower(ch.Target, snapshot, targetTrustWeight(ch.Target, root.ValidationWeights, participants, capApplied))

	participant, _ := am.keeper.GetParticipant(ctx, ch.Target)
	seed, seedFound := am.keeper.GetRandomSeed(ctx, ch.EpochIndex, ch.Target)
	seeds := map[string]types.RandomSeed{}
	if seedFound {
		seeds[ch.Target] = seed
	}

	guardianEnabled := am.keeper.GetGenesisGuardianEnabled(ctx)
	guardianAddrs := am.keeper.GetGenesisGuardianAddresses(ctx)
	guardianSet := make(map[string]bool, len(guardianAddrs))
	for _, addr := range guardianAddrs {
		accAddr, err := utils.OperatorAddressToAccAddress(addr)
		if err != nil {
			continue
		}
		if accAddr == ch.Target {
			continue
		}
		guardianSet[accAddr] = true
	}

	var appHash string
	var slots int
	if params.PocParams != nil && params.PocParams.ValidationSlots > 0 {
		appHash = snapshot.AppHash
		slots = int(params.PocParams.ValidationSlots)
	}
	return challengeCalcInputs{
		modelVP:         modelVP,
		totalWeight:     totalWeight,
		participant:     participant,
		seeds:           seeds,
		guardianEnabled: guardianEnabled,
		guardianSet:     guardianSet,
		appHash:         appHash,
		slots:           slots,
	}
}

func sliceFailsAlpha(validated, expected int64, params *types.ConfirmationPoCParams) bool {
	ratio := computeRatio(validated, expected)
	return calculations.ConfirmationPoCStatus(&types.CurrentEpochStats{ConfirmationPoCRatio: ratio}, params) == calculations.Fail
}

func accumulateSegmentReading(
	counted []pocchallenge.SliceRange,
	pocWeight, denom int64,
	alpha *types.ConfirmationPoCParams,
	eval func(sl pocchallenge.SliceRange) (ok bool, validated int64, rejected bool),
) (validatedSum, expectedSum int64, failReason types.PoCChallengeFailReason) {
	for _, sl := range counted {
		expected := sliceExpected(pocWeight, denom, sl.Length)
		expectedSum += expected
		if failReason == types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_REJECTED {
			continue
		}
		ok, validated, rejected := eval(sl)
		if rejected {
			failReason = types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_REJECTED
			validatedSum = 0
			continue
		}
		if ok {
			validatedSum += validated
		}
		if failReason == types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET &&
			sliceFailsAlpha(validated, expected, alpha) {
			failReason = types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_SEGMENT_UNDERWEIGHT
		}
	}
	return
}

func (am AppModule) evaluateCountedSlice(
	ch types.PoCChallenge,
	sl pocchallenge.SliceRange,
	commits map[string]map[uint32]types.PoCChallengeCommit,
	votes []types.PoCChallengeValidation,
	nodes map[string][]*types.MLNodeInfo,
	assigned []string,
	params types.Params,
	inputs challengeCalcInputs,
) (ok bool, validated int64, rejected bool) {
	storeCommits := make(map[types.PoCParticipantModelKey]types.PoCV2StoreCommit)
	dists := make(map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution)
	validations := make(map[types.PoCParticipantModelKey][]types.PoCValidationV2)
	for _, modelID := range assigned {
		c, found := commits[modelID][sl.Index]
		if !found {
			return false, 0, false
		}
		key := types.PoCParticipantModelKey{ParticipantAddress: ch.Target, ModelID: modelID}
		storeCommits[key] = types.PoCV2StoreCommit{
			ParticipantAddress:       ch.Target,
			PocStageStartBlockHeight: sl.Start,
			Count:                    c.Count,
			RootHash:                 c.RootHash,
			ModelId:                  modelID,
		}
		dists[key] = syntheticDistribution(ch.Target, modelID, sl.Start, c.Count, nodes[modelID])
	}
	if len(storeCommits) == 0 {
		return false, 0, false
	}
	for _, v := range votes {
		if v.Validator == ch.Target {
			continue
		}
		key := types.PoCParticipantModelKey{ParticipantAddress: ch.Target, ModelID: v.ModelId}
		validations[key] = append(validations[key], types.PoCValidationV2{
			ParticipantAddress:          ch.Target,
			ValidatorParticipantAddress: v.Validator,
			PocStageStartBlockHeight:    sl.Start,
			ValidatedWeight:             v.ValidatedWeight,
			ModelId:                     v.ModelId,
		})
	}
	if inputs.participant.Address == "" && inputs.participant.Index == "" {
		return false, 0, true
	}

	calc := NewPoCWeightCalculator(
		inputs.modelVP,
		inputs.totalWeight,
		storeCommits,
		dists,
		validations,
		params.PocParams,
		map[string]types.Participant{ch.Target: inputs.participant},
		inputs.seeds,
		sl.Start,
		am,
		mathsdk.LegacyOneDec(),
		inputs.guardianEnabled,
		inputs.guardianSet,
		inputs.appHash,
		inputs.slots,
	)
	result := calc.Calculate()
	if len(result) == 0 {
		return false, 0, true
	}
	return true, result[0].Weight, false
}

func syntheticDistribution(target, modelID string, start int64, count uint32, nodes []*types.MLNodeInfo) types.MLNodeWeightDistribution {
	weights := make([]*types.MLNodeWeight, 0, len(nodes))
	var sumPoc int64
	for _, n := range nodes {
		if n != nil {
			sumPoc += n.PocWeight
		}
	}
	var assigned uint32
	for i, n := range nodes {
		if n == nil {
			continue
		}
		var w uint32
		if i == len(nodes)-1 {
			w = count - assigned
		} else if sumPoc > 0 {
			w = uint32(int64(count) * n.PocWeight / sumPoc)
			assigned += w
		}
		weights = append(weights, &types.MLNodeWeight{NodeId: n.NodeId, Weight: w})
	}
	if len(weights) == 0 {
		weights = []*types.MLNodeWeight{{NodeId: "challenge", Weight: count}}
	}
	return types.MLNodeWeightDistribution{
		ParticipantAddress:       target,
		PocStageStartBlockHeight: start,
		Weights:                  weights,
		ModelId:                  modelID,
	}
}

func pocDurationDenom(epochParams *types.EpochParams) int64 {
	if epochParams == nil {
		return 1
	}
	denom := epochParams.PocStageDuration + epochParams.PocExchangeDuration
	if denom <= 0 {
		return 1
	}
	return denom
}

func sliceExpected(pocWeight, denom, length int64) int64 {
	if denom <= 0 {
		denom = 1
	}
	expected := pocWeight * length / denom
	if expected < 1 {
		return 1
	}
	return expected
}

func (am AppModule) failSealedSegment(
	ctx context.Context,
	store *pocchallenge.Store,
	ch types.PoCChallenge,
	seg *types.PoCChallengeSegment,
	reason types.PoCChallengeFailReason,
	height int64,
	validated, expected int64,
) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	cache, write := sdkCtx.CacheContext()
	seg.Outcome = types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_FAILED
	store.ReplaceSegment(&ch, *seg)
	if err := store.Set(cache, ch); err != nil {
		return err
	}
	if err := pocchallenge.FailChallenge(cache, store, ch.Target, reason, height); err != nil {
		return err
	}
	if err := am.applyChallengeReading(cache, ch.Target, ch.EpochIndex, validated, expected); err != nil {
		return err
	}
	write()
	return nil
}

func (am AppModule) passSealedSegment(
	ctx context.Context,
	store *pocchallenge.Store,
	ch types.PoCChallenge,
	seg *types.PoCChallengeSegment,
	validated, expected int64,
) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	cache, write := sdkCtx.CacheContext()
	seg.Outcome = types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PASSED
	store.ReplaceSegment(&ch, *seg)
	if err := store.Set(cache, ch); err != nil {
		return err
	}
	if err := am.applyChallengeReading(cache, ch.Target, ch.EpochIndex, validated, expected); err != nil {
		return err
	}
	write()
	return nil
}

func (am AppModule) applyChallengeReading(ctx context.Context, target string, epochIndex uint64, validated, expected int64) error {
	data, found := am.keeper.GetEpochGroupData(ctx, epochIndex, "")
	if !found {
		return nil
	}
	updated := false
	for i, vw := range data.ValidationWeights {
		if vw == nil || vw.MemberAddress != target {
			continue
		}
		reading := int64(0)
		if expected > 0 {
			reading = vw.ConfirmationWeight * validated / expected
		}
		if reading < vw.ConfirmationWeight {
			data.ValidationWeights[i].ConfirmationWeight = reading
			updated = true
		}
	}
	if updated {
		am.keeper.SetEpochGroupData(ctx, data)
	}
	participant, found := am.keeper.GetParticipant(ctx, target)
	if !found {
		return nil
	}
	if participant.CurrentEpochStats == nil {
		participant.CurrentEpochStats = &types.CurrentEpochStats{}
	}
	participant.CurrentEpochStats.ConfirmationPoCRatio = computeRatio(validated, expected)
	return am.keeper.SetParticipant(ctx, participant)
}

func (am AppModule) FinalizeOpenChallenges(ctx context.Context, epochIndex uint64) error {
	store := am.keeper.PoCChallenge
	list, err := store.ListOpen(ctx)
	if err != nil {
		return err
	}
	upcoming, hasUpcoming := am.keeper.GetUpcomingEpoch(ctx)
	var snapshot types.PoCValidationSnapshot
	var haveSnap bool
	if hasUpcoming && upcoming != nil {
		if snap, found, err := am.keeper.GetPoCValidationSnapshot(ctx, upcoming.PocStartBlockHeight); err == nil && found {
			snapshot = snap
			haveSnap = true
		}
	}
	height := sdk.UnwrapSDKContext(ctx).BlockHeight()

	for _, ch := range list {
		if ch.EpochIndex > epochIndex {
			continue
		}
		if ch.FailReason != types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
			continue
		}
		for _, seg := range append([]*types.PoCChallengeSegment(nil), ch.Segments...) {
			if seg == nil || seg.SealHeight == 0 ||
				seg.Outcome != types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PENDING {
				continue
			}
			if err := am.EvaluateSealedSegment(ctx, ch.Target, seg.PocStageStartBlockHeight, snapshot); err != nil {
				return err
			}
			updated, found, err := store.Get(ctx, ch.Target)
			if err != nil || !found {
				return err
			}
			ch = updated
			if ch.FailReason != types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
				break
			}
			seg = store.SegmentByStart(ch, seg.PocStageStartBlockHeight)
			if seg != nil && seg.FirstVoteHeight == 0 {
				if err := pocchallenge.FailChallenge(ctx, store, ch.Target, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_NO_VOTE, height); err != nil {
					return err
				}
				break
			}
			if !haveSnap {
				if err := pocchallenge.FailChallenge(ctx, store, ch.Target, types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_NO_VOTE, height); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

func (am AppModule) decideVotedChallengeSegments(ctx context.Context, triggerHeight int64) {
	store := am.keeper.PoCChallenge
	list, err := store.ListOpen(ctx)
	if err != nil {
		am.LogError("decideVotedChallengeSegments: list open failed", types.PoC, "error", err)
		return
	}
	snap, found, err := am.keeper.GetPoCValidationSnapshot(ctx, triggerHeight)
	if err != nil || !found {
		return
	}
	for _, ch := range list {
		if ch.FailReason != types.PoCChallengeFailReason_POC_CHALLENGE_FAIL_REASON_UNSET {
			continue
		}
		for _, seg := range append([]*types.PoCChallengeSegment(nil), ch.Segments...) {
			if seg == nil || seg.SealHeight == 0 ||
				seg.Outcome != types.PoCChallengeSegmentOutcome_POC_CHALLENGE_SEGMENT_OUTCOME_PENDING {
				continue
			}
			if err := am.EvaluateSealedSegment(ctx, ch.Target, seg.PocStageStartBlockHeight, snap); err != nil {
				am.LogError("decideVotedChallengeSegments: evaluate failed", types.PoC,
					"target", ch.Target, "start", seg.PocStageStartBlockHeight, "error", err)
			}
		}
	}
}

func (am AppModule) startNextChallengeSegments(ctx context.Context, nextStart, safetyHeight int64) {
	store := am.keeper.PoCChallenge
	if nextStart <= 0 || nextStart >= safetyHeight {
		return
	}
	list, err := store.ListOpen(ctx)
	if err != nil {
		am.LogError("startNextChallengeSegments: list open failed", types.PoC, "error", err)
		return
	}
	for _, ch := range list {
		if !store.IsChallengeGenerating(ctx, ch.Target) {
			continue
		}
		if err := pocchallenge.AppendNextSegment(ctx, store, ch.Target, nextStart); err != nil {
			am.LogError("startNextChallengeSegments: append failed", types.PoC,
				"target", ch.Target, "start", nextStart, "error", err)
		}
	}
}

func stripChallengeSkipFromPreserved(snapshot types.PreservedNodesSnapshot, skip map[string]struct{}) types.PreservedNodesSnapshot {
	if len(skip) == 0 {
		return snapshot
	}
	for _, model := range snapshot.ModelPreservedNodes {
		if model == nil {
			continue
		}
		kept := make([]*types.ParticipantPreservedNodes, 0, len(model.Participants))
		for _, p := range model.Participants {
			if p == nil {
				continue
			}
			if _, skipThis := skip[p.ParticipantId]; skipThis {
				continue
			}
			kept = append(kept, p)
		}
		model.Participants = kept
	}
	return snapshot
}
