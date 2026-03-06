package calculations

import (
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

type ParticipantStatusReason string

const (
	// ConsecutiveFailures indicates the participant has too many consecutive failures
	ConsecutiveFailures ParticipantStatusReason = "consecutive_failures"
	// Ramping indicates the participant is in ramp-up phase
	Ramping ParticipantStatusReason = "ramping"
	// StatisticalInvalidations indicates the participant has statistically significant invalidations
	StatisticalInvalidations ParticipantStatusReason = "statistical_invalidations"
	// NoReason indicates no reason for the status
	NoReason ParticipantStatusReason = ""
	// AlgorithmError Should NEVER happen unless we have bad algorithms or parameters
	AlgorithmError ParticipantStatusReason = "algorithm_error"
	// AlreadySet when we are already invalid or inactive
	AlreadySet ParticipantStatusReason = "already_set"
	// Downtime when missed inferences exceeds the threshold
	Downtime ParticipantStatusReason = "downtime"
	// Failed Confirmation PoC
	FailedConfirmationPoC ParticipantStatusReason = "failed_confirmation_poc"
)

func StatsHaveChanged(oldStats, newStats *types.CurrentEpochStats) bool {
	if oldStats == nil || newStats == nil {
		return true
	}
	return oldStats.InvalidatedInferences != newStats.InvalidatedInferences ||
		oldStats.ValidatedInferences != newStats.ValidatedInferences ||
		oldStats.InferenceCount != newStats.InferenceCount ||
		oldStats.MissedRequests != newStats.MissedRequests ||
		oldStats.ConfirmationPoCRatio != newStats.ConfirmationPoCRatio
}

// Note that newValue is passed in BY VALUE, so changes to newValue directly will not pass back
func ComputeStatus(
	validationParameters *types.ValidationParams,
	confirmationPocParams *types.ConfirmationPoCParams,
	newValue types.Participant,
	oldStats types.CurrentEpochStats,
	precomputed types.SPRTPrecomputedValues,
) (status types.ParticipantStatus, reason ParticipantStatusReason, stats types.CurrentEpochStats) {
	// Genesis only (for tests)
	newStats := getStats(&newValue)
	if validationParameters == nil || validationParameters.FalsePositiveRate == nil || validationParameters.QuickFailureThreshold == nil {
		return types.ParticipantStatus_ACTIVE, NoReason, newStats
	}

	// Once INVALID or INACTIVE, this can only be reset deliberately (at epoch start)
	if newValue.Status == types.ParticipantStatus_INVALID || newValue.Status == types.ParticipantStatus_INACTIVE {
		return newValue.Status, AlreadySet, newStats
	}

	// If we have consecutive failures with a likelihood of less than 1 in a million times, we're assuming bad
	falsePositiveRate := validationParameters.FalsePositiveRate.ToDecimal()
	consecutiveFailureCutoff := validationParameters.QuickFailureThreshold.ToDecimal()
	if probabilityOfConsecutiveFailures(falsePositiveRate, newValue.ConsecutiveInvalidInferences).LessThan(consecutiveFailureCutoff) {
		return types.ParticipantStatus_INVALID, ConsecutiveFailures, newStats
	}

	invalidationDecision := getInvalidationStatus(&newStats, oldStats, validationParameters, precomputed)
	if invalidationDecision == Fail {
		return types.ParticipantStatus_INVALID, StatisticalInvalidations, newStats
	} else if invalidationDecision == Error {
		return types.ParticipantStatus_ACTIVE, AlgorithmError, newStats
	}

	inactiveDecision := getInactiveStatus(&newStats, oldStats, validationParameters, precomputed)
	if inactiveDecision == Fail {
		return types.ParticipantStatus_INACTIVE, Downtime, newStats
	} else if inactiveDecision == Error {
		return types.ParticipantStatus_ACTIVE, AlgorithmError, newStats
	}

	failedConfirmationPoCDecision := getConfirmationPoCStatus(&newStats, confirmationPocParams)
	if failedConfirmationPoCDecision == Fail {
		return types.ParticipantStatus_INACTIVE, FailedConfirmationPoC, newStats
	} else if failedConfirmationPoCDecision == Error {
		return types.ParticipantStatus_ACTIVE, AlgorithmError, newStats
	}

	return types.ParticipantStatus_ACTIVE, NoReason, newStats
}

func getInactiveStatus(newStats *types.CurrentEpochStats, oldStats types.CurrentEpochStats, parameters *types.ValidationParams, precomputed types.SPRTPrecomputedValues) Decision {
	if parameters.DowntimeGoodPercentage == nil || parameters.DowntimeBadPercentage == nil || parameters.DowntimeHThreshold == nil {
		return Error
	}

	if precomputed.InactiveLogFail == nil || precomputed.InactiveLogPass == nil {
		return Error
	}

	newInferences := int64(newStats.InferenceCount) - int64(oldStats.InferenceCount)
	newMissedInferences := int64(newStats.MissedRequests) - int64(oldStats.MissedRequests)

	inactiveSprt := SPRT{
		H:       parameters.DowntimeHThreshold.ToDecimal(),
		LLR:     newStats.InactiveLLR.ToDecimal(),
		logFail: precomputed.InactiveLogFail.ToDecimal(),
		logPass: precomputed.InactiveLogPass.ToDecimal(),
	}

	inactiveSprt = inactiveSprt.UpdateCounts(newMissedInferences, newInferences)
	newStats.InactiveLLR = types.DecimalFromDecimal(inactiveSprt.LLR)
	return inactiveSprt.Decision()
}

func getInvalidationStatus(newStats *types.CurrentEpochStats, oldStats types.CurrentEpochStats, parameters *types.ValidationParams, precomputed types.SPRTPrecomputedValues) Decision {
	if parameters.FalsePositiveRate == nil || parameters.BadParticipantInvalidationRate == nil || parameters.InvalidationHThreshold == nil {
		return Error
	}

	if precomputed.InvalidationLogFail == nil || precomputed.InvalidationLogPass == nil {
		return Error
	}

	newValidations := int64(newStats.ValidatedInferences) - int64(oldStats.ValidatedInferences)
	newInvalidations := int64(newStats.InvalidatedInferences) - int64(oldStats.InvalidatedInferences)

	invalidationSprt := SPRT{
		H:       parameters.InvalidationHThreshold.ToDecimal(),
		LLR:     newStats.InvalidLLR.ToDecimal(),
		logFail: precomputed.InvalidationLogFail.ToDecimal(),
		logPass: precomputed.InvalidationLogPass.ToDecimal(),
	}

	invalidationSprt = invalidationSprt.UpdateCounts(newInvalidations, newValidations)
	newStats.InvalidLLR = types.DecimalFromDecimal(invalidationSprt.LLR)
	return invalidationSprt.Decision()
}

func getConfirmationPoCStatus(newStats *types.CurrentEpochStats, parameters *types.ConfirmationPoCParams) Decision {
	if parameters == nil || parameters.AlphaThreshold == nil || parameters.AlphaThreshold.ToDecimal().Equal(decimal.Zero) {
		return Pass
	}
	if newStats.ConfirmationPoCRatio == nil {
		return Pass
	}
	if newStats.ConfirmationPoCRatio.ToDecimal().LessThan(parameters.AlphaThreshold.ToDecimal()) {
		return Fail
	}
	return Pass
}

func getStats(newValue *types.Participant) types.CurrentEpochStats {
	var newStats types.CurrentEpochStats
	if newValue == nil || newValue.CurrentEpochStats == nil {
		newStats = types.CurrentEpochStats{}
	} else {
		newStats = *newValue.CurrentEpochStats
	}
	if newStats.InvalidLLR == nil {
		newStats.InvalidLLR = &types.Decimal{
			Value:    0,
			Exponent: 0,
		}
	}
	if newStats.InactiveLLR == nil {
		newStats.InactiveLLR = &types.Decimal{
			Value:    0,
			Exponent: 0,
		}
	}
	return newStats
}

// probabilityOfConsecutiveFailures returns P(F^N|G) = x^N
func probabilityOfConsecutiveFailures(expectedFailureRate decimal.Decimal, consecutiveFailures int64) decimal.Decimal {
	if expectedFailureRate.LessThan(decimal.Zero) || expectedFailureRate.GreaterThan(decimal.NewFromInt(1)) {
		// This won't happen
		return decimal.Zero
	}
	if consecutiveFailures < 0 {
		return decimal.Zero
	}

	return expectedFailureRate.Pow(decimal.NewFromInt(consecutiveFailures))
}
