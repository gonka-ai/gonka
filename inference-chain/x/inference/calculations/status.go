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

const (
	// Keeping the log precision low keeps compute low and high precision is not needed
	LogPrecision = 12
)

// StatusCheckScope is a bitmask for which status checks to run in ComputeStatus.
// When 0 (RunAll), all checks are run. Otherwise only the set bits are evaluated in order.
type StatusCheckScope int

const (
	CheckConsecutiveFailures StatusCheckScope = 1 << iota
	CheckInvalidation
	CheckInactive
	CheckConfirmationPoC
	// RunAll (0) means run all checks; used when scope is not specified.
)

// ScopeFromReason maps SetParticipantReason to StatusCheckScope.
// None returns RunAll. Otherwise only the matching check(s) run.
func ScopeFromReason(reason types.SetParticipantReason) StatusCheckScope {
	switch reason {
	case types.SetParticipantReasonCompletedInference:
		return CheckInactive
	case types.SetParticipantReasonMissedInference:
		return CheckInactive
	case types.SetParticipantReasonInvalidation:
		return CheckConsecutiveFailures | CheckInvalidation
	case types.SetParticipantReasonConfirmationPoC:
		return CheckConfirmationPoC
	default:
		return 0 // RunAll
	}
}

// Note that newValue is passed in BY VALUE, so changes to newValue directly will not pass back.
// scope: when 0 (RunAll), all checks run; otherwise only the checks in the bitmask run.
func ComputeStatus(
	validationParameters *types.ValidationParams,
	confirmationPocParams *types.ConfirmationPoCParams,
	newValue types.Participant,
	oldStats types.CurrentEpochStats,
	scope StatusCheckScope,
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

	runAll := scope == 0

	if runAll || (scope&CheckConsecutiveFailures != 0) {
		// If we have consecutive failures with a likelihood of less than 1 in a million times, we're assuming bad
		falsePositiveRate := validationParameters.FalsePositiveRate.ToDecimal()
		consecutiveFailureCutoff := validationParameters.QuickFailureThreshold.ToDecimal()
		if probabilityOfConsecutiveFailures(falsePositiveRate, newValue.ConsecutiveInvalidInferences).LessThan(consecutiveFailureCutoff) {
			return types.ParticipantStatus_INVALID, ConsecutiveFailures, newStats
		}
	}

	if runAll || (scope&CheckInvalidation != 0) {
		invalidationDecision := getInvalidationStatus(&newStats, oldStats, validationParameters)
		switch invalidationDecision {
		case Fail:
			return types.ParticipantStatus_INVALID, StatisticalInvalidations, newStats
		case Error:
			return types.ParticipantStatus_ACTIVE, AlgorithmError, newStats
		}
	}

	if runAll || (scope&CheckInactive != 0) {
		inactiveDecision := getInactiveStatus(&newStats, oldStats, validationParameters)
		switch inactiveDecision {
		case Fail:
			return types.ParticipantStatus_INACTIVE, Downtime, newStats
		case Error:
			return types.ParticipantStatus_ACTIVE, AlgorithmError, newStats
		}
	}

	if runAll || (scope&CheckConfirmationPoC != 0) {
		failedConfirmationPoCDecision := getConfirmationPoCStatus(&newStats, confirmationPocParams)
		switch failedConfirmationPoCDecision {
		case Fail:
			return types.ParticipantStatus_INACTIVE, FailedConfirmationPoC, newStats
		case Error:
			return types.ParticipantStatus_ACTIVE, AlgorithmError, newStats
		}
	}

	return types.ParticipantStatus_ACTIVE, NoReason, newStats
}

func getInactiveStatus(newStats *types.CurrentEpochStats, oldStats types.CurrentEpochStats, parameters *types.ValidationParams) Decision {
	if parameters.DowntimeGoodPercentage == nil || parameters.DowntimeBadPercentage == nil || parameters.DowntimeHThreshold == nil {
		return Error
	}
	newInferences := int64(newStats.InferenceCount) - int64(oldStats.InferenceCount)
	newMissedInferences := int64(newStats.MissedRequests) - int64(oldStats.MissedRequests)
	inactiveSprt, err := NewSPRT(
		parameters.DowntimeGoodPercentage.ToDecimal(),
		parameters.DowntimeBadPercentage.ToDecimal(),
		parameters.DowntimeHThreshold.ToDecimal(),
		newStats.InactiveLLR.ToDecimal(),
		LogPrecision,
	)
	if err != nil {
		return Error
	}
	inactiveSprt.UpdateCounts(newMissedInferences, newInferences)
	newStats.InactiveLLR = types.DecimalFromDecimal(inactiveSprt.LLR)
	return inactiveSprt.Decision()
}

func getInvalidationStatus(newStats *types.CurrentEpochStats, oldStats types.CurrentEpochStats, parameters *types.ValidationParams) Decision {
	if parameters.BadParticipantInvalidationRate == nil || parameters.InvalidationHThreshold == nil {
		return Error
	}
	newValidations := int64(newStats.ValidatedInferences) - int64(oldStats.ValidatedInferences)
	newInvalidations := int64(newStats.InvalidatedInferences) - int64(oldStats.InvalidatedInferences)
	//newInferences := newValue.CurrentEpochStats.InferenceCount - oldValue.CurrentEpochStats.InferenceCount
	//newMissedInferences := newValue.CurrentEpochStats.MissedRequests - oldValue.CurrentEpochStats.MissedRequests

	invalidationSprt, err := NewSPRT(
		parameters.FalsePositiveRate.ToDecimal(),
		parameters.BadParticipantInvalidationRate.ToDecimal(),
		parameters.InvalidationHThreshold.ToDecimal(),
		newStats.InvalidLLR.ToDecimal(),
		LogPrecision,
	)
	if err != nil {
		return Error
	}
	invalidationSprt.UpdateCounts(newInvalidations, newValidations)
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
