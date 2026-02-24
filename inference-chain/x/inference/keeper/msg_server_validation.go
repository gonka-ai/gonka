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

	creator, found := k.GetParticipant(ctx, msg.Creator)
	if !found {
		return nil, types.ErrParticipantNotFound
	}
	inference, found := k.GetInference(ctx, msg.InferenceId)
	if !found {
		k.LogError("Inference not found", types.Validation, "inferenceId", msg.InferenceId)
		return nil, types.ErrInferenceNotFound
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

	if groupData.ValidationWeight(msg.Creator) == nil {
		k.LogError("Participant not found in epoch group data for the model", types.Validation, "participant", msg.Creator, "epochIndex", epochGroup.GroupData.EpochIndex, "model", inference.Model)
		return nil, types.ErrParticipantNotFound
	}

	k.LogInfo("Validating inner loop", types.Validation, "inferenceId", inference.InferenceId, "validator", msg.Creator, "passed", passed, "revalidation", msg.Revalidation)
	if msg.Revalidation {
		return epochGroup.Revalidate(passed, inference, msg, ctx)
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
		if k.MaximumInvalidationsReached(ctx, creatorAddr, groupData) {
			k.LogWarn("Maximum invalidations reached.", types.Validation,
				"creator", msg.Creator,
				"model", inference.Model,
				"epochIndex", epochGroup.GroupData.EpochIndex,
			)
			return &types.MsgValidationResponse{}, nil
		}
		inference.Status = types.InferenceStatus_VOTING
		proposalDetails, err := epochGroup.StartValidationVote(ctx, &inference, msg.Creator)
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

func (k msgServer) MaximumInvalidationsReached(ctx sdk.Context, creator sdk.AccAddress, data types.EpochGroupData) bool {
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

	recentInferencesMap := k.GetSummaryByModelAndTime(ctx, timeWindowStartMillis, currentTimeMillis)
	inferencesForModel, found := recentInferencesMap[data.ModelId]
	if !found {
		// InferenceCount will be zero here... that's fine, it will return the default value of 1
		k.LogInfo("No inferences for model", types.Validation, "model", data.ModelId, "error", err)
	}

	participant := data.ValidationWeight(creator.String())
	if participant == nil {
		k.LogError("No participant for model", types.Validation, "model", data.ModelId, "error", err)
		return true
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
