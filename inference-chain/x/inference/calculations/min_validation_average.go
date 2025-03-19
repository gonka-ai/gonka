package calculations

import (
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

func calculateMinimumValidationAverage(recentRequestCount decimal.Decimal, validationParams validationParamsDecimal) decimal.Decimal {
	if recentRequestCount.GreaterThanOrEqual(validationParams.FullValidationTrafficCutoff) {
		return validationParams.MinValidationAverage
	}
	halfwaySize := validationParams.FullValidationTrafficCutoff.Div(decimal.NewFromInt(2))
	if recentRequestCount.GreaterThanOrEqual(halfwaySize) {
		return betweenFullAndHalf(recentRequestCount, validationParams, halfwaySize)
	}
	if recentRequestCount.GreaterThan(validationParams.MinValidationTrafficCutoff) {
		return betweenHalfAndMin(recentRequestCount, validationParams, halfwaySize)
	}
	return validationParams.MaxValidationAverage
}

func betweenHalfAndMin(recentRequestCount decimal.Decimal, validationParams validationParamsDecimal, halfwaySize decimal.Decimal) decimal.Decimal {
	distanceFromHalfway := halfwaySize.Sub(recentRequestCount)
	bottomHalfRange := halfwaySize.Sub(validationParams.MinValidationTrafficCutoff)
	percentageToMinimum := distanceFromHalfway.Div(bottomHalfRange)
	averageRange := validationParams.MaxValidationAverage.Sub(validationParams.MinValidationHalfway)
	return validationParams.MinValidationHalfway.Add(averageRange.Mul(percentageToMinimum))
}

func betweenFullAndHalf(recentRequestCount decimal.Decimal, validationParams validationParamsDecimal, halfwaySize decimal.Decimal) decimal.Decimal {
	remainingSize := validationParams.FullValidationTrafficCutoff.Sub(recentRequestCount)
	remainingFraction := remainingSize.Div(halfwaySize)
	averageRange := validationParams.MinValidationHalfway.Sub(validationParams.MinValidationAverage)
	remainingRange := averageRange.Mul(remainingFraction)
	return remainingRange.Add(validationParams.MinValidationAverage)
}

func CalculateMinimumValidationAverage(recentRequestCount int64, validationParams *types.ValidationParams) decimal.Decimal {
	return calculateMinimumValidationAverage(decimal.NewFromInt(recentRequestCount), validationParamsDecimal{
		FullValidationTrafficCutoff: decimal.NewFromInt(validationParams.FullValidationTrafficCutoff),
		MinValidationAverage:        validationParams.MinValidationAverage.ToDecimal(),
		MinValidationHalfway:        validationParams.MinValidationHalfway.ToDecimal(),
		MinValidationTrafficCutoff:  decimal.NewFromInt(validationParams.MinValidationTrafficCutoff),
		MaxValidationAverage:        validationParams.MaxValidationAverage.ToDecimal(),
	})
}
