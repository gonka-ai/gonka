package keeper

import (
	"context"
	"errors"
	"math"
	"strconv"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/calculations"
	"github.com/productscience/inference/x/inference/types"
)

const (
	TokenCost = 1_000
)

var ModelToPassValue = map[string]float64{
	"Qwen/Qwen2.5-7B-Instruct": 0.950,
	"Qwen/QwQ-32B":             0.950,
}

func (k msgServer) Validation(goCtx context.Context, msg *types.MsgValidation) (*types.MsgValidationResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	params := k.Keeper.GetParams(ctx)
	inference, found := k.GetInference(ctx, msg.InferenceId)
	if !found {
		return nil, types.ErrInferenceNotFound
	}

	if !msg.Revalidation {
		k.addInferenceToEpochGroupValidations(ctx, msg, inference)
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
		return nil, types.ErrParticipantNotFound
	}

	if executor.Address == msg.Creator && !msg.Revalidation {
		return nil, types.ErrParticipantCannotValidateOwnInference
	}

	passValue, ok := ModelToPassValue[inference.Model]
	if !ok {
		k.LogError("Model not supported", types.Validation, "model", inference.Model)
		return nil, errors.New("Model " + inference.Model + " not supported")
	}

	passed := msg.Value > passValue
	needsRevalidation := false

	epochGroup, err := k.GetCurrentEpochGroup(ctx)
	if err != nil {
		return nil, err
	}

	k.LogInfo("Validating inner loop", types.Validation, "inferenceId", inference.InferenceId, "validator", msg.Creator, "passed", passed, "revalidation", msg.Revalidation)
	if msg.Revalidation {
		return epochGroup.Revalidate(passed, inference, msg, ctx)
	} else if passed {
		inference.Status = types.InferenceStatus_VALIDATED
		originalWorkers := append([]string{inference.ExecutedBy}, inference.ValidatedBy...)
		adjustments := calculations.ShareWork(originalWorkers, []string{msg.Creator}, inference.ActualCost)
		inference.ValidatedBy = append(inference.ValidatedBy, msg.Creator)
		for _, adjustment := range adjustments {
			if adjustment.ParticipantId == executor.Address {
				executor.CoinBalance += adjustment.WorkAdjustment
				k.LogInfo("Adjusting executor balance for validation", types.Validation, "executor", executor.Address, "adjustment", adjustment.WorkAdjustment)
				k.LogInfo("Adjusting executor CoinBalance for validation", types.Payments, "executor", executor.Address, "adjustment", adjustment.WorkAdjustment, "coin_balance", executor.CoinBalance)
			} else {
				worker, found := k.GetParticipant(ctx, adjustment.ParticipantId)
				if !found {
					k.LogError("Participant not found for redistribution", types.Validation, "participantId", adjustment.ParticipantId)
					continue
				}
				worker.CoinBalance += adjustment.WorkAdjustment
				k.LogInfo("Adjusting worker balance for validation", types.Validation, "worker", worker.Address, "adjustment", adjustment.WorkAdjustment)
				k.LogInfo("Adjusting worker CoinBalance for validation", types.Payments, "worker", worker.Address, "adjustment", adjustment.WorkAdjustment, "coin_balance", worker.CoinBalance)
				k.SetParticipant(ctx, worker)
			}
		}

		executor.ConsecutiveInvalidInferences = 0
		executor.CurrentEpochStats.ValidatedInferences++
	} else {
		inference.Status = types.InferenceStatus_VOTING
		proposalDetails, err := epochGroup.StartValidationVote(ctx, &inference, msg.Creator)
		if err != nil {
			return nil, err
		}
		inference.ProposalDetails = proposalDetails
		needsRevalidation = true
	}
	// Where will we get this number? How much does it vary by model?

	executor.Status = calculateStatus(params.ValidationParams, executor)
	k.SetParticipant(ctx, executor)

	k.LogInfo("Saving inference", types.Validation, "inferenceId", inference.InferenceId, "status", inference.Status, "proposalDetails", inference.ProposalDetails)
	k.SetInference(ctx, inference)

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

func (k msgServer) addInferenceToEpochGroupValidations(ctx sdk.Context, msg *types.MsgValidation, inference types.Inference) {
	epochGroupValidations, validationsFound := k.GetEpochGroupValidations(ctx, msg.Creator, inference.EpochGroupId)
	if !validationsFound {
		epochGroupValidations = types.EpochGroupValidations{
			Participant: msg.Creator, PocStartBlockHeight: inference.EpochGroupId,
		}
	}
	k.LogInfo("Adding inference to epoch group validations", types.Validation, "inferenceId", msg.InferenceId, "validator", msg.Creator, "height", inference.EpochGroupId)
	epochGroupValidations.ValidatedInferences = append(epochGroupValidations.ValidatedInferences, msg.InferenceId)
	k.SetEpochGroupValidations(ctx, epochGroupValidations)
}

func calculateStatus(validationParameters *types.ValidationParams, participant types.Participant) (status types.ParticipantStatus) {
	// Why not use the p-value, you ask? (or should).
	// Frankly, it seemed like overkill. Z-Score is easy to explain, people get p-value wrong all the time and it's
	// a far more complicated algorithm (to understand and to calculate)
	// If we have consecutive failures with a likelihood of less than 1 in a million times, we're assuming bad (for 5% FPR, that's 5 consecutive failures)
	falsePositiveRate := validationParameters.FalsePositiveRate.ToFloat()
	if ProbabilityOfConsecutiveFailures(falsePositiveRate, participant.ConsecutiveInvalidInferences) < 0.000001 {
		return types.ParticipantStatus_INVALID
	}
	zScore := CalculateZScoreFromFPR(falsePositiveRate, participant.CurrentEpochStats.ValidatedInferences, participant.CurrentEpochStats.InvalidatedInferences)
	measurementsNeeded := MeasurementsNeeded(falsePositiveRate, uint64(validationParameters.MinRampUpMeasurements))
	if participant.CurrentEpochStats.InferenceCount < measurementsNeeded {
		return types.ParticipantStatus_RAMPING
	}
	if zScore > 1 {
		return types.ParticipantStatus_INVALID
	}
	return types.ParticipantStatus_ACTIVE
}

// CalculateZScoreFromFPR - Positive values mean the failure rate is HIGHER than expected, thus bad
func CalculateZScoreFromFPR(expectedFailureRate float64, valid uint64, invalid uint64) float64 {
	total := valid + invalid
	observedFailureRate := float64(invalid) / float64(total)

	// Calculate the variance using the binomial distribution formula
	variance := expectedFailureRate * (1 - expectedFailureRate) / float64(total)

	// Calculate the standard deviation
	stdDev := math.Sqrt(variance)

	// Calculate the z-score (how many standard deviations the observed failure rate is from the expected failure rate)
	zScore := (observedFailureRate - expectedFailureRate) / stdDev

	return zScore
}

// MeasurementsNeeded calculates the number of measurements required
// for a single failure to be within one standard deviation of the expected distribution
func MeasurementsNeeded(p float64, max uint64) uint64 {
	if p <= 0 || p >= 1 {
		panic("Probability p must be between 0 and 1, exclusive")
	}

	// This value is derived from solving the inequality: |1 - np| <= sqrt(np(1 - p))
	// Which leads to the quadratic inequality: y^2 - 3y + 1 >= 0, where y = np
	// The solution to this inequality is np >= (3 + sqrt(5)) / 2
	requiredValue := (3 + math.Sqrt(5)) / 2

	// Calculate the number of measurements
	n := requiredValue / p

	// Round up to the nearest whole number since we need an integer count of measurements
	needed := uint64(math.Ceil(n))
	if needed > max {
		return max
	}
	return needed
}

// If we have consecutive failures, it is rapidly more likely that the executor is bad
func ProbabilityOfConsecutiveFailures(expectedFailureRate float64, consecutiveFailures int64) float64 {
	if expectedFailureRate < 0 || expectedFailureRate > 1 {
		panic("expectedFailureRate must be between 0 and 1")
	}
	if consecutiveFailures < 0 {
		panic("consecutiveFailures must be non-negative")
	}

	// P(F^N|G) = x^N
	return math.Pow(expectedFailureRate, float64(consecutiveFailures))
}
