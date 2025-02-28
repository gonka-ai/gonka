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

var one = decimal.NewFromInt(1)

func CalculateReputation(ctx *ReputationContext) int64 {
	actualEpochCount := decimal.NewFromInt(ctx.EpochCount).Sub(addMissCost(ctx.EpochMissPercentages, ctx.ValidationParams))
	if actualEpochCount.GreaterThan(decimal.NewFromInt(ctx.ValidationParams.EpochsToMax)) {
		return 100
	}
	if actualEpochCount.LessThanOrEqual(decimal.Zero) {
		return 0
	}
	truncate := actualEpochCount.Div(decimal.NewFromInt(ctx.ValidationParams.EpochsToMax)).Truncate(2)
	return truncate.Mul(decimal.NewFromInt(100)).IntPart()
}

func addMissCost(missPercentages []decimal.Decimal, params *types.ValidationParams) decimal.Decimal {
	epochsToMax := decimal.NewFromInt(params.EpochsToMax)
	singleEpochValue := one.Div(epochsToMax)
	missCost := decimal.NewFromFloat(0.0)
	for _, missPercentage := range missPercentages {
		if missPercentage.GreaterThan(decimal.NewFromFloat(params.MissPercentageCutoff)) {
			missCost = missCost.Add(missPercentage.Mul(singleEpochValue)).Mul(decimal.NewFromFloat(params.MissRequestsPenalty))
		}
	}
	return missCost.Mul(epochsToMax)
}
