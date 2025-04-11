package calculations

import (
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

type ReputationContext struct {
	EpochCount           int64
	EpochMissPercentages []decimal.Decimal
	ValidationParams     *types.ValidationParams
}

type reputationContextDecimal struct {
	EpochCount           decimal.Decimal
	EpochMissPercentages []decimal.Decimal
	ValidationParams     *validationParamsDecimal
}

type validationParamsDecimal struct {
	EpochsToMax                 decimal.Decimal
	MissPercentageCutoff        decimal.Decimal
	MissRequestsPenalty         decimal.Decimal
	FullValidationTrafficCutoff decimal.Decimal
	MinValidationAverage        decimal.Decimal
	MinValidationHalfway        decimal.Decimal
	MinValidationTrafficCutoff  decimal.Decimal
	MaxValidationAverage        decimal.Decimal
}

var one = decimal.NewFromInt(1)
var oneHundred = decimal.NewFromInt(100)

func CalculateReputation(ctx *ReputationContext) int64 {
	// For clarity, convert everything to decimal before we calculate
	decimalCtx := reputationContextDecimal{
		EpochCount:           decimal.NewFromInt(ctx.EpochCount),
		EpochMissPercentages: ctx.EpochMissPercentages,
		ValidationParams: &validationParamsDecimal{
			EpochsToMax:          decimal.NewFromInt(ctx.ValidationParams.EpochsToMax),
			MissPercentageCutoff: ctx.ValidationParams.MissPercentageCutoff.ToDecimal(),
			MissRequestsPenalty:  ctx.ValidationParams.MissRequestsPenalty.ToDecimal(),
		},
	}
	return calculateReputation(&decimalCtx).IntPart()
}

func calculateReputation(ctx *reputationContextDecimal) decimal.Decimal {
	actualEpochCount := ctx.EpochCount.Sub(addMissCost(ctx.EpochMissPercentages, ctx.ValidationParams))
	if actualEpochCount.GreaterThan(ctx.ValidationParams.EpochsToMax) {
		return oneHundred
	}
	if actualEpochCount.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	return actualEpochCount.Div(ctx.ValidationParams.EpochsToMax).Truncate(2).Mul(oneHundred)
}

func addMissCost(missPercentages []decimal.Decimal, params *validationParamsDecimal) decimal.Decimal {
	singleEpochValue := one.Div(params.EpochsToMax)
	missCost := decimal.Zero
	for _, missPercentage := range missPercentages {
		if missPercentage.GreaterThan(params.MissPercentageCutoff) {
			missCost = missCost.Add(missPercentage.Mul(singleEpochValue).Mul(params.MissRequestsPenalty))
		}
	}
	return missCost.Mul(params.EpochsToMax)
}
