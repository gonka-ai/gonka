package keeper

import (
	"context"
	"math"

	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/productscience/inference/x/inference/types"
)

const (
	FalsePositiveRate     = 0.05
	MinRampUpMeasurements = 10
	PassValue             = 0.99
)

func (k msgServer) Validation(goCtx context.Context, msg *types.MsgValidation) (*types.MsgValidationResponse, error) {
	ctx := sdk.UnwrapSDKContext(goCtx)

	inference, found := k.GetInference(ctx, msg.InferenceId)
	if !found {
		return nil, types.ErrInferenceNotFound
	}

	if inference.Status != types.InferenceStatus_FINISHED {
		return nil, types.ErrInferenceNotFinished
	}

	executor, found := k.GetParticipant(ctx, inference.ExecutedBy)
	if !found {
		return nil, types.ErrParticipantNotFound
	}

	if executor.Address == msg.Creator {
		return nil, types.ErrParticipantCannotValidateOwnInference
	}

	passed := msg.Value > PassValue

	executor.InferenceCount++
	if passed {
		inference.Status = types.InferenceStatus_VALIDATED
		executor.ValidatedInferences++
	} else {
		inference.Status = types.InferenceStatus_INVALIDATED
		executor.InvalidatedInferences++
	}
	// Where will we get this number? How much does it vary by model?

	executor.Status = calculateStatus(FalsePositiveRate, executor)
	k.SetParticipant(ctx, executor)
	k.SetInference(ctx, inference)

	return &types.MsgValidationResponse{}, nil
}

func calculateStatus(falsePositiveRate float64, participant types.Participant) (status types.ParticipantStatus) {
	// Why not use the p-value, you ask? (or should).
	// Frankly, it seemed like overkill. Z-Score is easy to explain, people get p-value wrong all the time and it's
	// a far more complicated algorithm (to understand and to calculate)
	zScore := CalculateZScoreFromFPR(falsePositiveRate, participant.ValidatedInferences, participant.InvalidatedInferences)
	measurementsNeeded := MeasurementsNeeded(falsePositiveRate, MinRampUpMeasurements)
	if participant.InferenceCount < measurementsNeeded {
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
