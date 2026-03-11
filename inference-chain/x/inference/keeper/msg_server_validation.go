package keeper

import (
	"context"
	"strconv"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/x/group"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

const (
	TokenCost = 1_000
)

func (k msgServer) Validation(goCtx context.Context, msg *types.MsgValidation) (*types.MsgValidationResponse, error) {
	if err := k.CheckPermission(goCtx, msg, ActiveParticipantPermission, PreviousActiveParticipantPermission); err != nil {
		return nil, err
	}

	ctx, err := k.Keeper.InjectParamsIntoContext(sdk.UnwrapSDKContext(goCtx))
	if err != nil {
		k.LogWarn("Validation: failed to inject params", types.Validation, "error", err)
	}

	k.LogInfo("Received MsgValidation", types.Validation,
		"msg.Creator", msg.Creator,
		"inferenceId", msg.InferenceId)

	if msg.ResponsePayload != "" {
		return nil, types.ErrValidationPayloadDeprecated
	}

	creator, found := k.GetParticipant(ctx, msg.Creator)
	if !found {
		return nil, types.ErrParticipantNotFound
	}
	inference, found := k.GetInference(ctx, msg.InferenceId)
	if !found {
		k.LogError("Inference not found", types.Validation, "inferenceId", msg.InferenceId)
		return nil, types.ErrInferenceNotFound
	}

	currentEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogError("Failed to get current epoch", types.Validation)
		return nil, types.ErrEffectiveEpochNotFound
	}

	// Ignore stale validations that arrive later than one epoch after inference epoch.
	if currentEpochIndex > inference.EpochId+1 {
		k.LogWarn(
			"Ignoring stale validation from old epoch",
			types.Validation,
			"inferenceId", inference.InferenceId,
			"inferenceEpoch", inference.EpochId,
			"currentEpoch", currentEpochIndex,
		)
		return &types.MsgValidationResponse{}, nil
	}

	if !msg.Revalidation {
		err := k.addInferenceToEpochGroupValidations(ctx, msg, inference)
		if err != nil {
			k.LogError("Failed to add inference to epoch group validations", types.Validation, "inferenceId", msg.InferenceId, "error", err)
			return nil, err
		}
	}

	if inference.Status == types.InferenceStatus_INVALIDATED {
		k.LogInfo("Inference already invalidated", types.Validation, "inference", inference)
		return &types.MsgValidationResponse{}, nil
	}
	if inference.Status == types.InferenceStatus_STARTED {
		k.LogError("Inference not finished", types.Validation, "status", inference.Status, "inference", inference)
		return nil, types.ErrInferenceNotFinished
	}
	previousStatus := inference.Status

	executor, found := k.GetParticipant(ctx, inference.ExecutedBy)
	if !found {
		k.LogError("Executor participant not found", types.Validation, "participantId", inference.ExecutedBy)
		return nil, types.ErrParticipantNotFound
	}

	if executor.Address == msg.Creator && !msg.Revalidation {
		k.LogError("Participant cannot validate own inference", types.Validation, "participant", msg.Creator, "inferenceId", msg.InferenceId)
		return nil, types.ErrParticipantCannotValidateOwnInference
	}

	if inference.EpochId != currentEpochIndex {
		k.LogInfo("Validation for different epoch", types.Validation, "inferenceEpoch", inference.EpochId, "currentEpochIndex", currentEpochIndex)
	}

	var (
		modelThreshold      *types.Decimal
		participantWeight   int64
		participantRepution int32
		totalWeight         int64
		modelEpochPolicy    string
	)
	cachedModelMeta, cacheFound, cacheErr := k.GetCachedEpochDataModelMeta(ctx, inference.EpochId, inference.Model)
	if cacheErr != nil {
		k.LogError("Validation: failed to load transient validation cache entry", types.Validation, "error", cacheErr, "model", inference.Model, "epochIndex", inference.EpochId)
		return nil, cacheErr
	}
	if !cacheFound {
		k.LogError("Validation: transient validation cache entry not found", types.Validation, "model", inference.Model, "epochIndex", inference.EpochId)
		return nil, types.ErrEpochGroupDataNotFound
	}
	modelThreshold = cachedModelMeta.ValidationThreshold
	modelEpochPolicy = cachedModelMeta.EpochPolicy
	totalWeight = cachedModelMeta.TotalWeight

	validatorMeta, weightFound, weightErr := k.GetCachedEpochDataModelWeight(ctx, inference.EpochId, inference.Model, msg.Creator)
	if weightErr != nil {
		k.LogError("Validation: failed to load transient validation weight entry", types.Validation, "error", weightErr, "participant", msg.Creator, "model", inference.Model, "epochIndex", inference.EpochId)
		return nil, weightErr
	}
	if !weightFound {
		k.LogError("Participant not found in transient validation cache for model", types.Validation, "participant", msg.Creator, "epochIndex", inference.EpochId, "model", inference.Model)
		return nil, types.ErrParticipantNotFound
	}
	participantWeight = validatorMeta.Weight
	participantRepution = validatorMeta.Reputation
	if modelThreshold == nil {
		k.LogError("Validation threshold missing", types.Validation, "model", inference.Model, "epochIndex", inference.EpochId)
		return nil, types.ErrModelSnapshotNotFound
	}

	passValue := modelThreshold.ToDecimal()
	messageValue := getValidationValue(msg)

	passed := messageValue.GreaterThan(passValue)
	k.LogInfo(
		"Validation details", types.Validation,
		"passValue", passValue,
		"passed", passed,
		"msgValue", messageValue,
		"model", inference.Model,
	)
	needsRevalidation := false

	k.LogInfo("Validating inner loop", types.Validation, "inferenceId", inference.InferenceId, "validator", msg.Creator, "passed", passed, "revalidation", msg.Revalidation)
	if msg.Revalidation {
		if inference.ProposalDetails == nil {
			k.LogError("Inference proposal details not set", types.Validation, "inference", inference)
			return nil, types.ErrInferenceNotFinished
		}
		return k.revalidateInferenceVote(ctx, passed, inference, msg.Creator)
	} else if passed {
		inference.Status = types.InferenceStatus_VALIDATED
		shouldShare, information := k.inferenceIsBeforeClaimsSet(ctx, inference, currentEpochIndex)
		k.LogInfo("Validation sharing decision", types.Validation, "inferenceId", inference.InferenceId, "validator", msg.Creator, "shouldShare", shouldShare, "information", information)
		if shouldShare {
			k.shareWorkWithValidators(ctx, inference, msg, &executor)
			inference.ValidatedBy = append(inference.ValidatedBy, msg.Creator)
		}
		executor.ConsecutiveInvalidInferences = 0
		executor.CurrentEpochStats.ValidatedInferences++
	} else if currentEpochIndex == inference.EpochId {
		// Only run invalidation voting if we're still in the same Epoch as the inference
		creatorAddr, err := sdk.AccAddressFromBech32(creator.Address)
		if err != nil {
			return nil, err
		}
		if k.MaximumInvalidationsReached(ctx, creatorAddr, inference.Model, participantWeight, participantRepution, totalWeight) {
			k.LogWarn("Maximum invalidations reached.", types.Validation,
				"creator", msg.Creator,
				"model", inference.Model,
				"epochIndex", inference.EpochId,
			)
			return &types.MsgValidationResponse{}, nil
		}
		inference.Status = types.InferenceStatus_VOTING
		proposalDetails, err := k.startValidationVoteWithPolicy(ctx, modelEpochPolicy, &inference, msg.Creator)
		if err != nil {
			return nil, err
		}
		msgCreatorAddr, err := sdk.AccAddressFromBech32(msg.Creator)
		if err != nil {
			return nil, err
		}
		err = k.ActiveInvalidations.Set(ctx, collections.Join(msgCreatorAddr, inference.InferenceId))
		if err != nil {
			k.LogError("Failed to set active invalidation", types.Validation, "error", err)
		}

		inference.ProposalDetails = proposalDetails
		needsRevalidation = true
	} else if currentEpochIndex != inference.EpochId {
		k.LogWarn("Ignoring invalidation submitted after epoch changeover", types.Validation, "inferenceId", inference.InferenceId, "inferenceEpoch", inference.EpochId, "currentEpoch", currentEpochIndex)
		inference.Status = types.InferenceStatus_FINISHED
	}

	err = k.SetParticipant(ctx, executor)
	if err != nil {
		k.LogError("Failed to set executor", types.Validation, "executor", executor.Address, "error", err)
		return nil, err
	}

	k.LogInfo("Saving inference", types.Validation, "inferenceId", inference.InferenceId, "status", inference.Status, "proposalDetails", inference.ProposalDetails)
	err = k.SetInferenceWithoutPruning(ctx, inference)
	if err != nil {
		k.LogError("Failed to set inference", types.Validation, "inferenceId", inference.InferenceId, "error", err)
		return nil, err
	}
	if inference.Status != previousStatus {
		emitInferenceStatusUpdatedEvent(ctx, inference.InferenceId, inference.Status)
	}

	ctx.EventManager().EmitEvent(
		sdk.NewEvent(
			"inference_validation",
			sdk.NewAttribute("inference_id", msg.InferenceId),
			sdk.NewAttribute("validator", msg.Creator),
			sdk.NewAttribute("needs_revalidation", strconv.FormatBool(needsRevalidation)),
			sdk.NewAttribute("passed", strconv.FormatBool(passed)),
		))
	return &types.MsgValidationResponse{}, nil
}

func (k msgServer) revalidateInferenceVote(
	ctx sdk.Context,
	passed bool,
	inference types.Inference,
	voter string,
) (*types.MsgValidationResponse, error) {
	invalidateOption := group.VOTE_OPTION_YES
	revalidationOption := group.VOTE_OPTION_NO
	if passed {
		invalidateOption = group.VOTE_OPTION_NO
		revalidationOption = group.VOTE_OPTION_YES
	}

	voteMsg := &group.MsgVote{
		ProposalId: inference.ProposalDetails.InvalidatePolicyId,
		Voter:      voter,
		Option:     invalidateOption,
		Metadata:   "Invalidate inference " + inference.InferenceId,
		Exec:       group.Exec_EXEC_TRY,
	}
	if err := k.voteValidationProposal(ctx, voteMsg); err != nil {
		return nil, err
	}

	voteMsg.ProposalId = inference.ProposalDetails.ReValidatePolicyId
	voteMsg.Option = revalidationOption
	voteMsg.Metadata = "Revalidate inference " + inference.InferenceId
	if err := k.voteValidationProposal(ctx, voteMsg); err != nil {
		return nil, err
	}
	return &types.MsgValidationResponse{}, nil
}

func (k msgServer) voteValidationProposal(ctx sdk.Context, vote *group.MsgVote) error {
	k.LogInfo("Voting", types.Validation, "vote", vote)
	_, err := k.group.Vote(ctx, vote)
	if err != nil {
		if err.Error() == "proposal not open for voting: invalid value" {
			k.LogInfo("Proposal already decided", types.Validation, "vote", vote)
			return nil
		}
		k.LogError("Error voting", types.Validation, "error", err, "vote", vote)
		return err
	}
	k.LogInfo("Voted on validation", types.Validation, "vote", vote)
	return nil
}

func (k msgServer) startValidationVoteWithPolicy(
	ctx sdk.Context,
	policyAddress string,
	inference *types.Inference,
	invalidator string,
) (*types.ProposalDetails, error) {
	invalidateResponse, revalidateResponse, err := k.submitValidationProposalsWithPolicy(ctx, policyAddress, inference.InferenceId, invalidator, inference.ExecutedBy)
	if err != nil {
		return nil, err
	}
	return &types.ProposalDetails{
		InvalidatePolicyId: invalidateResponse.ProposalId,
		ReValidatePolicyId: revalidateResponse.ProposalId,
		PolicyAddress:      policyAddress,
	}, nil
}

func (k msgServer) submitValidationProposalsWithPolicy(
	ctx sdk.Context,
	policyAddress string,
	inferenceID string,
	invalidator string,
	executor string,
) (*group.MsgSubmitProposalResponse, *group.MsgSubmitProposalResponse, error) {
	invalidateMessage := &types.MsgInvalidateInference{
		InferenceId: inferenceID,
		Creator:     policyAddress,
		Invalidator: invalidator,
	}
	revalidateMessage := &types.MsgRevalidateInference{
		InferenceId: inferenceID,
		Creator:     policyAddress,
		Invalidator: invalidator,
	}
	invalidateProposal := group.MsgSubmitProposal{
		GroupPolicyAddress: policyAddress,
		Proposers:          []string{invalidator},
		Metadata:           "Invalidation of inference " + inferenceID,
	}
	revalidateProposal := group.MsgSubmitProposal{
		GroupPolicyAddress: policyAddress,
		Proposers:          []string{executor},
		Metadata:           "Revalidation of inference " + inferenceID,
	}
	if err := invalidateProposal.SetMsgs([]sdk.Msg{invalidateMessage}); err != nil {
		return nil, nil, err
	}
	if err := revalidateProposal.SetMsgs([]sdk.Msg{revalidateMessage}); err != nil {
		return nil, nil, err
	}
	invalidateResponse, err := k.group.SubmitProposal(ctx, &invalidateProposal)
	if err != nil {
		return nil, nil, err
	}
	revalidateResponse, err := k.group.SubmitProposal(ctx, &revalidateProposal)
	if err != nil {
		return nil, nil, err
	}
	return invalidateResponse, revalidateResponse, nil
}

func getValidationValue(msg *types.MsgValidation) decimal.Decimal {
	if msg.ValueDecimal != nil {
		return msg.ValueDecimal.ToDecimal()
	}
	return decimal.NewFromFloat(msg.Value)
}

func (k msgServer) MaximumInvalidationsReached(
	ctx sdk.Context,
	creator sdk.AccAddress,
	modelID string,
	participantWeight int64,
	participantReputation int32,
	totalWeight int64,
) bool {
	currentInvalidations, err := k.CountInvalidations(ctx, creator)
	if err != nil {
		k.LogError("Failed to get current invalidations", types.Validation, "error", err)
		return false
	}
	// Quick return for the default case
	if currentInvalidations == 0 {
		return false
	}

	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Failed to get params", types.Validation, "error", err)
		return false
	}
	if params.BandwidthLimitsParams == nil {
		k.LogError("Failed to get bandwidth limits params", types.Validation)
		return false
	}

	windowBlocks := types.InvalidationsSamplePeriodToBlocks(params.BandwidthLimitsParams.InvalidationsSamplePeriod)
	inferencesForModel := int64(0)
	rollingInferenceCount, found, err := k.GetModelInferenceCountRollingSum(ctx, modelID, windowBlocks)
	if err != nil {
		k.LogError("Failed to get rolling inference count", types.Validation, "model", modelID, "error", err)
		return false
	}
	if found {
		inferencesForModel = int64(rollingInferenceCount)
	} else {
		// Default to zero when there is no model state yet.
		k.LogInfo("No rolling inference count for model", types.Validation, "model", modelID)
	}
	var participantWeightPercent = decimal.Zero
	if totalWeight != 0 {
		participantWeightPercent = decimal.NewFromInt(participantWeight).Div(decimal.NewFromInt(totalWeight))
	}
	maxValidations := calculations.CalculateInvalidations(
		inferencesForModel,
		participantWeightPercent,
		participantReputation,
		int64(params.BandwidthLimitsParams.InvalidationsLimit),
		int64(params.BandwidthLimitsParams.InvalidationsLimitCurve),
		int64(params.BandwidthLimitsParams.MinimumConcurrentInvalidations),
	)

	return currentInvalidations >= maxValidations
}

func (k msgServer) CountInvalidations(ctx sdk.Context, address sdk.AccAddress) (int64, error) {
	iter, err := k.ActiveInvalidations.Iterate(ctx, collections.NewPrefixedPairRange[sdk.AccAddress, string](address))
	if err != nil {
		return 0, err
	}
	defer iter.Close()
	count := int64(0)
	for ; iter.Valid(); iter.Next() {
		count++
	}
	return count, nil
}

func (k msgServer) inferenceIsBeforeClaimsSet(ctx context.Context, inference types.Inference, currentEpochIndex uint64) (bool, string) {
	// Submitted after epoch changeover (onSetNewValidatorsStage)
	if inference.EpochId < currentEpochIndex {
		return false, "Validation submitted in next epoch. InferenceEpoch: " + strconv.FormatUint(inference.EpochId, 10) + ", EpochGroupEpoch: " + strconv.FormatUint(currentEpochIndex, 10)
	}
	upcomingEpoch, found := k.GetUpcomingEpoch(ctx)
	// During regular inference time (majority case)
	if !found {
		// This would be before IsStartOfPocStage
		return true, "Validation during inference epoch"
	}
	// Somewhere inbetween StartOfPocStage and SetNewValidatorsStage
	// ActiveParticipants are set during EndOfPoCValidationStage, which is also when we set claims
	_, found = k.GetActiveParticipants(ctx, upcomingEpoch.Index)
	if found {
		// We're AFTER EndOfPocValidationStage
		return false, "Validation submitted after claims set but before next epoch starts"
	} else {
		// We're in between StartOfPocStage and EndOfPocValidationStage, before claims
		return true, "Validation submitted after PoC start but before claims set"
	}
}

func (k msgServer) shareWorkWithValidators(ctx sdk.Context, inference types.Inference, msg *types.MsgValidation, executor *types.Participant) {
	originalWorkers := append([]string{inference.ExecutedBy}, inference.ValidatedBy...)
	adjustments := calculations.ShareWork(originalWorkers, []string{msg.Creator}, inference.ActualCost)
	k.validateAdjustments(adjustments, msg)
	for _, adjustment := range adjustments {
		// A note about the bookkeeping here:
		// ShareWork will return negative adjustments for all existing shareholders, and a positive for the new (msg.Creator)
		// We account for this by adding a negative amount to the CoinBalance. BUT, we only register the NEGATIVE adjustments,
		// and we model them as moving money from the existing worker TO the positive
		if adjustment.ParticipantId == executor.Address {
			executor.CoinBalance += adjustment.WorkAdjustment
			k.LogInfo("Adjusting executor balance for validation", types.Validation, "executor", executor.Address, "adjustment", adjustment.WorkAdjustment)
			k.LogInfo("Adjusting executor CoinBalance for validation", types.Balances, "executor", executor.Address, "adjustment", adjustment.WorkAdjustment, "coin_balance", executor.CoinBalance)
			if adjustment.WorkAdjustment < 0 {
				k.SafeLogSubAccountTransaction(ctx, msg.Creator, adjustment.ParticipantId, types.OwedSubAccount, -adjustment.WorkAdjustment, "share_validation_executor:"+inference.InferenceId)
			}
		} else {
			worker, found := k.GetParticipant(ctx, adjustment.ParticipantId)
			if !found {
				k.LogError("Participant not found for redistribution", types.Validation, "participantId", adjustment.ParticipantId)
				continue
			}
			worker.CoinBalance += adjustment.WorkAdjustment
			k.LogInfo("Adjusting worker balance for validation", types.Validation, "worker", worker.Address, "adjustment", adjustment.WorkAdjustment)
			k.LogInfo("Adjusting worker CoinBalance for validation", types.Balances, "worker", worker.Address, "adjustment", adjustment.WorkAdjustment, "coin_balance", worker.CoinBalance)
			if adjustment.WorkAdjustment < 0 {
				k.SafeLogSubAccountTransaction(ctx, msg.Creator, adjustment.ParticipantId, types.OwedSubAccount, -adjustment.WorkAdjustment, "share_validation_worker:"+inference.InferenceId)
			}
			err := k.SetParticipant(ctx, worker)
			if err != nil {
				k.LogError("Unable to update participant to share work", types.Validation, "worker", worker.Address)
			}
		}
	}
}

func (k msgServer) validateAdjustments(adjustments []calculations.Adjustment, msg *types.MsgValidation) {
	positiveAdjustmentTotal := int64(0)
	negativeAdjustmentTotal := int64(0)
	for _, adjustment := range adjustments {
		if adjustment.ParticipantId == msg.Creator {
			if adjustment.WorkAdjustment < 0 {
				k.LogError("Validation adjustment for new validator cannot be negative", types.Validation, "adjustment", adjustment)
			} else {
				// must be a positive number or zero
				positiveAdjustmentTotal += adjustment.WorkAdjustment
			}
		} else {
			if adjustment.WorkAdjustment > 0 {
				k.LogError("Validation adjustment for existing validator cannot be positive", types.Validation, "adjustment", adjustment)
			} else {
				// must be a negative number or zero
				negativeAdjustmentTotal += -adjustment.WorkAdjustment
			}
		}
	}
	if positiveAdjustmentTotal != negativeAdjustmentTotal {
		k.LogError("Validation adjustment totals do not match", types.Validation, "positiveAdjustmentTotal", positiveAdjustmentTotal, "negativeAdjustmentTotal", negativeAdjustmentTotal)
	}
}

func (k msgServer) addInferenceToEpochGroupValidations(ctx sdk.Context, msg *types.MsgValidation, inference types.Inference) error {
	entryKey := collections.Join3(inference.EpochId, msg.Creator, msg.InferenceId)
	alreadyValidated, err := k.EpochGroupValidationEntry.Has(ctx, entryKey)
	if err != nil {
		return err
	}
	if alreadyValidated {
		k.LogInfo("Inference already validated", types.Validation, "inferenceId", msg.InferenceId)
		return types.ErrDuplicateValidation
	}
	k.LogInfo("Adding inference to epoch group validations", types.Validation, "inferenceId", msg.InferenceId, "validator", msg.Creator, "height", inference.EpochPocStartBlockHeight)
	return k.SetEpochGroupValidation(ctx, inference.EpochId, msg.Creator, msg.InferenceId)
}
