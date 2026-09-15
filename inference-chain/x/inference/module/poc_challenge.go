package inference

import (
	"context"
	"errors"
	"fmt"

	mathsdk "cosmossdk.io/math"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/keeper"
	"github.com/productscience/inference/x/inference/types"
	"github.com/productscience/inference/x/inference/utils"
)

var errChallengeEvaluation = errors.New("poc challenge evaluation")

func evaluationError(cause string) error {
	return fmt.Errorf("%w: %s", errChallengeEvaluation, cause)
}

func (am AppModule) decideLastChallengeSegments(ctx context.Context, epoch types.Epoch) error {
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.EpochParams == nil {
		return fmt.Errorf("epoch params not set")
	}
	epochContext := types.NewEpochContext(epoch, *params.EpochParams)
	finish := keeper.SafetyWindowHeight(epochContext.NextPoCStart(), params.EpochParams.ConfirmationPocSafetyWindow)
	upcoming, found := am.keeper.GetUpcomingEpoch(ctx)
	if !found || upcoming == nil {
		return fmt.Errorf("upcoming epoch not found")
	}
	return am.decideCurrentChallengeSegments(ctx, epoch.Index, finish, upcoming.PocStartBlockHeight, false)
}

func (am AppModule) decideCurrentChallengeSegments(ctx context.Context, epochIndex uint64, finish int64, snapshotHeight int64, rotate bool) error {
	list, err := am.keeper.ListPoCChallenges(ctx)
	if err != nil {
		return err
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	cacheCtx, writeFn := sdkCtx.CacheContext()
	for _, ch := range list {
		if ch.EpochIndex != epochIndex {
			continue
		}
		if ch.FailureKind != types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_UNSET {
			continue
		}
		if err := am.decideCurrentChallengeSegment(cacheCtx, ch, finish, snapshotHeight, rotate); err != nil {
			return err
		}
	}
	writeFn()
	return nil
}

func (am AppModule) decideCurrentChallengeSegment(
	ctx context.Context,
	ch types.PoCChallenge,
	finish int64,
	snapshotHeight int64,
	rotate bool,
) error {
	if finish <= ch.StartHeight {
		return nil
	}
	duration := finish - ch.StartHeight
	if duration < keeper.MinPunishableSegmentBlocks {
		return am.advanceAfterDecision(ctx, ch, rotate)
	}

	if err := am.evaluatePunishableChallengeSegment(ctx, ch, finish, snapshotHeight); err != nil {
		if errors.Is(err, errChallengeEvaluation) {
			return am.failChallengeEvaluation(ctx, ch, err.Error())
		}
		return err
	}
	updated, found, err := am.keeper.GetPoCChallenge(ctx, ch.Target)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if updated.FailureKind != types.PoCChallengeFailureKind_POC_CHALLENGE_FAILURE_KIND_UNSET {
		return am.keeper.DeleteChallengeSegmentData(ctx, ch.Target)
	}
	return am.advanceAfterDecision(ctx, updated, rotate)
}

func (am AppModule) canRotateChallenge(ctx context.Context, ch types.PoCChallenge) bool {
	safety, err := am.keeper.ChallengeSafetyFinish(ctx, ch)
	if err != nil {
		return false
	}
	return sdk.UnwrapSDKContext(ctx).BlockHeight() < safety
}

func (am AppModule) advanceAfterDecision(ctx context.Context, ch types.PoCChallenge, rotate bool) error {
	if err := am.keeper.DeleteChallengeSegmentData(ctx, ch.Target); err != nil {
		return err
	}
	if !rotate {
		return nil
	}
	if am.canRotateChallenge(ctx, ch) {
		return am.writeNextChallengeSegment(ctx, ch)
	}
	safety, err := am.keeper.ChallengeSafetyFinish(ctx, ch)
	if err != nil {
		return err
	}
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return am.keeper.RotateChallengeSegment(ctx, ch.Target, safety, sdkCtx.HeaderInfo().Hash)
}

func (am AppModule) writeNextChallengeSegment(ctx context.Context, ch types.PoCChallenge) error {
	sdkCtx := sdk.UnwrapSDKContext(ctx)
	return am.keeper.RotateChallengeSegment(ctx, ch.Target, sdkCtx.BlockHeight(), sdkCtx.HeaderInfo().Hash)
}

func (am AppModule) failChallengeEvaluation(ctx context.Context, ch types.PoCChallenge, cause string) error {
	if err := am.keeper.MarkChallengeRefundOnly(ctx, ch.Target, cause); err != nil {
		return err
	}
	return nil
}

func (am AppModule) evaluatePunishableChallengeSegment(
	ctx context.Context,
	ch types.PoCChallenge,
	finish int64,
	snapshotHeight int64,
) error {
	params, err := am.keeper.GetParams(ctx)
	if err != nil {
		return err
	}
	if params.EpochParams == nil || params.PocParams == nil {
		return evaluationError("missing epoch or poc params")
	}
	epochGroupData, found := am.keeper.GetEpochGroupData(ctx, ch.EpochIndex, "")
	if !found {
		return evaluationError(fmt.Sprintf("epoch group data not found for epoch %d", ch.EpochIndex))
	}
	scales := epochGroupData.GetConfirmationWeightScales()
	if len(scales) == 0 {
		return evaluationError("no confirmation weight scales")
	}
	snapshot, found, err := am.keeper.GetPoCValidationSnapshot(ctx, snapshotHeight)
	if err != nil {
		return err
	}
	if !found {
		return evaluationError(fmt.Sprintf("validation snapshot missing at %d", snapshotHeight))
	}
	presentScales := confirmationScalesInSnapshot(scales, snapshot.ModelVotingPowers)
	if len(presentScales) == 0 {
		return evaluationError("validation snapshot has no confirmation models")
	}

	participants, found := am.keeper.GetActiveParticipants(ctx, ch.EpochIndex)
	if !found {
		return evaluationError(fmt.Sprintf("active participants not found for epoch %d", ch.EpochIndex))
	}
	participant, found := am.keeper.GetParticipant(ctx, ch.Target)
	if !found {
		return evaluationError("target participant not found")
	}

	measured := map[string]int64{ch.Target: 0}
	calculatorResult, calcErr := am.challengeCalculatorResult(ctx, ch, finish, snapshot, params)
	if calcErr != nil {
		return calcErr
	}
	if len(calculatorResult) > 0 {
		fullMeasured := weightByParticipant(calculatorResult, presentScales)
		measured[ch.Target] = fullMeasured[ch.Target]
	}
	fullExpected := weightByParticipant(participants.Participants, presentScales)
	totalExpected := map[string]int64{ch.Target: fullExpected[ch.Target]}

	updated, ratios := foldEventReadings(&epochGroupData, measured, map[string]int64{}, totalExpected, nil)
	if updated {
		am.keeper.SetEpochGroupData(ctx, epochGroupData)
	}

	if participant.CurrentEpochStats == nil {
		participant.CurrentEpochStats = types.NewCurrentEpochStats()
	}
	if ratio, ok := ratios[ch.Target]; ok {
		participant.CurrentEpochStats.ConfirmationPoCRatio = ratio
	}
	if confirmationPoCFailed(participant.CurrentEpochStats, params.ConfirmationPocParams) {
		if err := am.keeper.MarkChallengeFailed(ctx, ch.Target); err != nil {
			return err
		}
	}
	return am.keeper.SetParticipant(ctx, participant)
}

func confirmationPoCFailed(stats *types.CurrentEpochStats, parameters *types.ConfirmationPoCParams) bool {
	if parameters == nil || parameters.AlphaThreshold == nil {
		return false
	}
	alpha := parameters.AlphaThreshold.ToDecimal()
	if alpha.IsZero() {
		return false
	}
	if stats == nil || stats.ConfirmationPoCRatio == nil {
		return false
	}
	return stats.ConfirmationPoCRatio.ToDecimal().LessThan(alpha)
}

func (am AppModule) challengeCalculatorResult(
	ctx context.Context,
	ch types.PoCChallenge,
	finish int64,
	snapshot types.PoCValidationSnapshot,
	params types.Params,
) ([]*types.ActiveParticipant, error) {
	commits, err := am.keeper.ListChallengeCommits(ctx, ch.Target)
	if err != nil {
		return nil, err
	}
	validations, err := am.keeper.ListChallengeValidations(ctx, ch.Target)
	if err != nil {
		return nil, err
	}

	storeCommits := make(map[types.PoCParticipantModelKey]types.PoCV2StoreCommit)
	distributions := make(map[types.PoCParticipantModelKey]types.MLNodeWeightDistribution)
	for _, commit := range commits {
		key := types.PoCParticipantModelKey{ParticipantAddress: ch.Target, ModelID: commit.ModelId}
		storeCommits[key] = commit
		distributions[key] = types.MLNodeWeightDistribution{
			ParticipantAddress:       ch.Target,
			PocStageStartBlockHeight: ch.StartHeight,
			ModelId:                  commit.ModelId,
			Weights: []*types.MLNodeWeight{{
				NodeId: "challenge",
				Weight: commit.Count,
			}},
		}
	}

	validationsV2 := make(map[types.PoCParticipantModelKey][]types.PoCValidationV2)
	for _, vote := range validations {
		key := types.PoCParticipantModelKey{ParticipantAddress: ch.Target, ModelID: vote.ModelId}
		validationsV2[key] = append(validationsV2[key], vote)
	}

	if len(storeCommits) == 0 && len(validationsV2) == 0 {
		return nil, nil
	}

	participant, found := am.keeper.GetParticipant(ctx, ch.Target)
	if !found {
		return nil, evaluationError("target participant not found")
	}
	participants := map[string]types.Participant{ch.Target: participant}
	seeds := make(map[string]types.RandomSeed)
	if seed, found := am.keeper.GetRandomSeed(ctx, ch.EpochIndex, ch.Target); found {
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
		guardianSet[accAddr] = true
	}

	var appHash string
	var validationSlots int
	if params.PocParams.ValidationSlots > 0 {
		appHash = snapshot.AppHash
		validationSlots = int(params.PocParams.ValidationSlots)
	}
	duration := finish - ch.StartHeight
	stageBlocks := params.EpochParams.PocStageDuration + params.EpochParams.PocExchangeDuration
	factor := mathsdk.LegacyNewDec(stageBlocks).Quo(mathsdk.LegacyNewDec(duration))

	modelVotingPowers := make(map[string]map[string]int64)
	for _, mvw := range snapshot.ModelVotingPowers {
		if mvw == nil {
			continue
		}
		modelVotingPowers[mvw.ModelId] = types.VotingPowerSliceToMap(mvw.VotingPowers)
	}

	calculator := NewPoCWeightCalculator(
		modelVotingPowers,
		snapshot.TotalNetworkWeight,
		storeCommits,
		distributions,
		validationsV2,
		params.PocParams,
		participants,
		seeds,
		ch.StartHeight,
		am,
		factor,
		guardianEnabled,
		guardianSet,
		appHash,
		validationSlots,
	)
	return calculator.Calculate(), nil
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
