package calculations

import (
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

func CalculateMinimumValidationAverage(recentRequestCount int64, validationParams *types.ValidationParams) decimal.Decimal {
	if recentRequestCount >= validationParams.FullValidationTrafficCutoff {
		return decimal.NewFromFloat(validationParams.MinValidationAverage)
	}
	halfwaySize := validationParams.FullValidationTrafficCutoff / 2
	if recentRequestCount >= halfwaySize {
		remainingSize := validationParams.FullValidationTrafficCutoff - recentRequestCount
		minValidationAverage := decimal.NewFromFloat(validationParams.MinValidationAverage)
		minValidationHalfway := decimal.NewFromFloat(validationParams.MinValidationHalfway)
		remainingFraction := decimal.NewFromInt(remainingSize).Div(decimal.NewFromInt(halfwaySize))
		averageRange := minValidationHalfway.Sub(minValidationAverage)
		remainingRange := averageRange.Mul(remainingFraction)
		return remainingRange.Add(minValidationAverage)
	}
	if recentRequestCount > validationParams.MinValidationTrafficCutoff {
		distanceFromHalfway := halfwaySize - recentRequestCount
		bottomHalfRange := halfwaySize - validationParams.MinValidationTrafficCutoff
		percentageToMinimum := decimal.NewFromInt(distanceFromHalfway).Div(decimal.NewFromInt(bottomHalfRange))
		averageRange := decimal.NewFromFloat(validationParams.MaxValidationAverage - validationParams.MinValidationHalfway)
		return decimal.NewFromFloat(validationParams.MinValidationHalfway).Add(averageRange.Mul(percentageToMinimum))
	}
	return decimal.NewFromFloat(validationParams.MaxValidationAverage)
}
