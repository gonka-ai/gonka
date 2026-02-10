package keeper

import (
	"context"
	"strconv"

	"cosmossdk.io/collections"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

const (
	TokenCost = 1_000
)

func (k msgServer) Validation(goCtx context.Context, msg *types.MsgValidation) (*types.MsgValidationResponse, error) {
	k.LogInfo("Received MsgValidation", types.Validation,
		"msg.Creator", msg.Creator,
		"inferenceId", msg.InferenceId)

	ctx := sdk.UnwrapSDKContext(goCtx)

	if msg.ResponsePayload != "" {
		return nil, types.ErrValidationPayloadDeprecated
	}

	if msg.Revalidation {
		// Revalidation path: only participants that were deterministically selected
		// for this inference's revalidation vote are eligible to submit a vote.
		blockHeight := ctx.BlockHeight()
		if !k.IsParticipantEligibleToVoteOnRevalidation(blockHeight, msg.InferenceId, msg.Creator) {
			k.LogError("Participant not eligible to vote on revalidation", types.Validation,
				"participant", msg.Creator,
				"inferenceId", msg.InferenceId,
				"blockHeight", blockHeight)
			return nil, types.ErrNotDesignatedValidator
		}
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

	if inference.Status == types.InferenceStatus_INVALIDATED {
		k.LogInfo("Inference already invalidated", types.Validation, "inference", inference)
		return &types.MsgValidationResponse{}, nil
	}
	if inference.Status == types.InferenceStatus_STARTED {
		k.LogError("Inference not finished", types.Validation, "status", inference.Status, "inference", inference)
		return nil, types.ErrInferenceNotFinished
	}

	executor, found := k.GetParticipant(ctx, inference.ExecutedBy)
	if !found {
		k.LogError("Executor participant not found", types.Validation, "participantId", inference.ExecutedBy)
		return nil, types.ErrParticipantNotFound
	}

	if executor.Address == msg.Creator && !msg.Revalidation {
		k.LogError("Participant cannot validate own inference", types.Validation, "participant", msg.Creator, "inferenceId", msg.InferenceId)
		return nil, types.ErrParticipantCannotValidateOwnInference
	}

	model, err := k.GetEpochModelForEpoch(ctx, inference.EpochId, inference.Model)
	if err != nil {
		k.LogError("Failed to get epoch model", types.Validation,
			"model", inference.Model,
			"epochId", inference.EpochId,
			"inferenceId", msg.InferenceId,
			"error", err)
		return nil, err
	}
	passValue := model.ValidationThreshold.ToDecimal()
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

	currentEpochIndex, found := k.GetEffectiveEpochIndex(ctx)
	if !found {
		k.LogError("Failed to get current epoch", types.Validation, "error", err)
		return nil, types.ErrEffectiveEpochNotFound
	}

	if inference.EpochId != currentEpochIndex {
		k.LogInfo("Validation for different epoch", types.Validation, "inferenceEpoch", inference.EpochId, "currentEpochIndex", currentEpochIndex)
	}

	epochGroup, err := k.GetEpochGroup(ctx, inference.EpochId, "")
	if err != nil {
		k.LogError("Failed to get epoch group", types.Validation, "error", err, "epochIndex", inference.EpochId)
		return nil, err
	}

	groupData, found := k.GetEpochGroupData(ctx, epochGroup.GroupData.EpochIndex, inference.Model)
	if !found {
		k.LogError("Failed to get epoch group data", types.Validation, "epochIndex", epochGroup.GroupData.EpochIndex, "model", inference.Model)
		return nil, err
	}

	participant := groupData.ValidationWeight(msg.Creator)
	// Participants that have no confirmation weight are not eligible to validate
	if participant == nil || participant.ConfirmationWeight == 0 {
		k.LogError("Participant not found in epoch group data for the model", types.Validation, "participant", msg.Creator, "epochIndex", epochGroup.GroupData.EpochIndex, "model", inference.Model)
		return nil, types.ErrParticipantNotFound
	}

	// We check here if the sender is the designated validator for this inference
	// So we need to get the inference validation details and participant seed to execute calculations.ShouldValidate

	participantSeed, found := k.GetParticipantEpochSeed(ctx, inference.EpochId, msg.Creator)
	//After patch apply we can have no seeds stored because we should first wait for next epoch to start
	//So we need to skip the should validate check if the seed is not found
	//TODO: After patch applyed and seeds are stored we need to remove this skip and handle the error case
	skipTheShouldValidateCheck := false
	if !found {
		skipTheShouldValidateCheck = true
		//k.LogError("Sender random seed not found", types.Validation, "epochIndex", inference.EpochId, "participant", msg.Creator)
		//return nil, types.ErrRandomSeedNotFound
	}

	inferenceDetails, foundDetails := k.GetInferenceValidationDetails(ctx, inference.EpochId, inference.InferenceId)
	if !foundDetails {
		k.LogError("Inference validation details not found", types.Validation, "inferenceId", inference.InferenceId, "epochId", inference.EpochId)
		return nil, types.ErrInferenceValidationDetailsNotFound
	}
	params, err := k.GetParams(ctx)
	if err != nil {
		k.LogError("Failed to get params", types.Validation, "error", err)
		return nil, err
	}
	totalWeight := inferenceDetails.TotalPower
	validatorPower := participant.Weight
	executorPower := inferenceDetails.ExecutorPower

	if !skipTheShouldValidateCheck {
		shouldValidate, _ := calculations.ShouldValidate(participantSeed, &inferenceDetails, uint32(totalWeight), uint32(validatorPower), uint32(executorPower),
			params.ValidationParams, false)
		if !shouldValidate {
			k.LogError("Sender should not validate this inference", types.Validation, "epochIndex", inference.EpochId, "participant", msg.Creator)
			return nil, types.ErrNotDesignatedValidator
		}
	} else {
		k.LogInfo("Skipping should validate check", types.Validation, "epochIndex", inference.EpochId, "participant", msg.Creator)
	}

	// We only add it to the epoch group validations if all upper checks pass
	if !msg.Revalidation {
		// It not only creates new validation entry but also checks and throws error for validation duplicates
		err := k.addInferenceToEpochGroupValidations(ctx, msg, inference)
		if err != nil {
			k.LogError("Failed to add inference to epoch group validations", types.Validation, "inferenceId", msg.InferenceId, "error", err)
			return nil, err
		}
	}

	k.LogInfo("Validating inner loop", types.Validation, "inferenceId", inference.InferenceId, "validator", msg.Creator, "passed", passed, "revalidation", msg.Revalidation)
	if msg.Revalidation {
		// Use capped revalidation vote weight from cache when available; fall back to confirmation weight.
		voteWeight := int64(0)
		if w, ok := k.GetRevalidationVoteWeight(ctx.BlockHeight(), inference.InferenceId, msg.Creator); ok && w > 0 {
			voteWeight = w
		}
		passTotal, noPassTotal, thresholdReached, invalidateWon, err := k.AddRevalidationVoteAndCheckThreshold(goCtx, inference.InferenceId, passed, msg.Creator, voteWeight, ctx.BlockHeight())
		if err != nil {
			k.LogError("Failed to add revalidation vote", types.Validation, "error", err)
			return nil, err
		}
		k.LogInfo("Revalidation vote added", types.Validation, "inferenceId", inference.InferenceId,
			"participant", msg.Creator, "passed", passed, "weight", participant.ConfirmationWeight, "cappedWeight", voteWeight,
			"passTotal", passTotal, "noPassTotal", noPassTotal)
		if thresholdReached {
			if invalidateWon {
				k.applyInvalidation(ctx, inference)
			} else {
				k.applyRevalidation(ctx, inference)
			}
		}
		invalidator := k.GetRevalidationInvalidator(ctx, inference.InferenceId)
		invalidatorAddr, err := sdk.AccAddressFromBech32(invalidator)
		if err != nil {
			return nil, err
		}
		err = k.ActiveInvalidations.Remove(ctx, collections.Join(invalidatorAddr, inference.InferenceId))
		if err != nil {
			k.LogError("Failed to remove active invalidation", types.Validation, "error", err)
		}
		return &types.MsgValidationResponse{}, nil
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
		if k.MaximumInvalidationsReached(ctx, creatorAddr, groupData, participant) {
			k.LogWarn("Maximum invalidations reached.", types.Validation,
				"creator", msg.Creator,
				"model", inference.Model,
				"epochIndex", epochGroup.GroupData.EpochIndex,
			)
			return &types.MsgValidationResponse{}, nil
		}
		inference.Status = types.InferenceStatus_VOTING
		msgCreatorAddr, err := sdk.AccAddressFromBech32(msg.Creator)
		if err != nil {
			return nil, err
		}
		_ = k.ActiveInvalidations.Set(ctx, collections.Join(msgCreatorAddr, inference.InferenceId))
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
	err = k.SetInference(ctx, inference)
	if err != nil {
		k.LogError("Failed to set inference", types.Validation, "inferenceId", inference.InferenceId, "error", err)
		return nil, err
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

func getValidationValue(msg *types.MsgValidation) decimal.Decimal {
	if msg.ValueDecimal != nil {
		return msg.ValueDecimal.ToDecimal()
	}
	return decimal.NewFromFloat(msg.Value)
}

func (k msgServer) MaximumInvalidationsReached(ctx sdk.Context, creator sdk.AccAddress, data types.EpochGroupData, participant *types.ValidationWeight) bool {
	// TODO: caching: CountInvalidations should be cached
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
	blockTime := sdk.UnwrapSDKContext(ctx).BlockTime()
	currentTimeMillis := blockTime.UnixMilli()                                             // Current time in milliseconds
	windowDurationSeconds := int64(params.BandwidthLimitsParams.InvalidationsSamplePeriod) // Window duration in seconds (e.g., 60)
	windowDurationMillis := windowDurationSeconds * 1000                                   // Convert to milliseconds for time queries
	timeWindowStartMillis := currentTimeMillis - windowDurationMillis                      // Start time in milliseconds

	// TODO: caching: GetSummaryByModelAndTime possibly could be optimized with caches
	recentInferencesMap := k.GetSummaryByModelAndTime(ctx, timeWindowStartMillis, currentTimeMillis)
	inferencesForModel, found := recentInferencesMap[data.ModelId]
	if !found {
		// InferenceCount will be zero here... that's fine, it will return the default value of 1
		k.LogInfo("No inferences for model", types.Validation, "model", data.ModelId, "error", err)
	}

	//If we already got the participant and shouldn't iterate through all participants again
	if participant == nil {
		p := data.ValidationWeight(creator.String())
		if p == nil {
			k.LogError("No participant for model", types.Validation, "model", data.ModelId, "error", err)
			return true
		}
		participant = p
	}
	participantWeightPercent := decimal.NewFromInt(participant.Weight).Div(decimal.NewFromInt(data.TotalWeight))
	maxValidations := calculations.CalculateInvalidations(
		int64(inferencesForModel.InferenceCount),
		participantWeightPercent,
		participant.Reputation,
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
	epochGroupValidations, validationsFound := k.GetEpochGroupValidations(ctx, msg.Creator, inference.EpochId)
	if !validationsFound {
		epochGroupValidations = types.EpochGroupValidations{
			Participant:         msg.Creator,
			EpochIndex:          inference.EpochId,
			ValidatedInferences: []string{msg.InferenceId},
		}
	} else {
		// Use helper to both check for duplicates and keep the slice sorted.
		updated, found := UpsertStringIntoSortedSlice(epochGroupValidations.ValidatedInferences, msg.InferenceId)
		if found {
			k.LogInfo("Inference already validated", types.Validation, "inferenceId", msg.InferenceId)
			return types.ErrDuplicateValidation
		}
		epochGroupValidations.ValidatedInferences = updated
	}
	k.LogInfo("Adding inference to epoch group validations", types.Validation, "inferenceId", msg.InferenceId, "validator", msg.Creator, "height", inference.EpochPocStartBlockHeight)
	return k.SetEpochGroupValidations(ctx, epochGroupValidations)
}

// applyInvalidation applies the outcome of an ephemeral revalidation vote (invalidate won).
// Sets inference status to INVALIDATED, updates executor stats, and refunds if before claims set.
func (k msgServer) applyInvalidation(ctx sdk.Context, inference types.Inference) {
	if inference.Status == types.InferenceStatus_INVALIDATED {
		return
	}
	executor, found := k.GetParticipant(ctx, inference.ExecutedBy)
	if !found {
		k.LogError("applyInvalidation: executor not found", types.Validation, "inferenceId", inference.InferenceId)
		return
	}
	inference.Status = types.InferenceStatus_INVALIDATED
	executor.CurrentEpochStats.InvalidatedInferences++
	executor.ConsecutiveInvalidInferences++
	epochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err == nil {
		shouldRefund, _ := k.inferenceIsBeforeClaimsSet(ctx, inference, epochGroup.GroupData.EpochIndex)
		if shouldRefund {
			_ = k.refundInvalidatedInference(&executor, &inference, ctx)
		}
	}
	_ = k.SetParticipant(ctx, executor)
	_ = k.SetInference(ctx, inference)
	k.LogInfo("Ephemeral revalidation: inference invalidated", types.Validation, "inferenceId", inference.InferenceId)
}

// applyRevalidation applies the outcome of an ephemeral revalidation vote (revalidate won).
func (k msgServer) applyRevalidation(ctx sdk.Context, inference types.Inference) {
	if inference.Status == types.InferenceStatus_VALIDATED {
		return
	}
	executor, found := k.GetParticipant(ctx, inference.ExecutedBy)
	if !found {
		k.LogError("applyRevalidation: executor not found", types.Validation, "inferenceId", inference.InferenceId)
		return
	}
	inference.Status = types.InferenceStatus_VALIDATED
	executor.ConsecutiveInvalidInferences = 0
	executor.CurrentEpochStats.ValidatedInferences++
	_ = k.SetParticipant(ctx, executor)
	_ = k.SetInference(ctx, inference)
	k.LogInfo("Ephemeral revalidation: inference revalidated", types.Validation, "inferenceId", inference.InferenceId)
}
