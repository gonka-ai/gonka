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
	EpochCount       decimal.Decimal
	ValidationParams *validationParamsDecimal
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
	return CalculateReputationFromMissSum(ctx.EpochCount, MissSumAboveCutoff(ctx.EpochMissPercentages, ctx.ValidationParams.MissPercentageCutoff.ToDecimal()), ctx.ValidationParams)
}

// CalculateReputationFromMissSum is CalculateReputation given the sum of the
// epoch miss rates above the cutoff (MissSumAboveCutoff).
func CalculateReputationFromMissSum(epochCount int64, missSum decimal.Decimal, params *types.ValidationParams) int64 {
	decimalCtx := reputationContextDecimal{
		EpochCount: decimal.NewFromInt(epochCount),
		ValidationParams: &validationParamsDecimal{
			EpochsToMax:          decimal.NewFromInt(params.EpochsToMax),
			MissPercentageCutoff: params.MissPercentageCutoff.ToDecimal(),
			MissRequestsPenalty:  params.MissRequestsPenalty.ToDecimal(),
		},
	}
	return calculateReputation(&decimalCtx, missSum).IntPart()
}

// EpochMissRate is MissedRequests / InferenceCount of one epoch, zero without inferences.
func EpochMissRate(inferenceCount, missedRequests uint64) decimal.Decimal {
	if inferenceCount == 0 {
		return decimal.Zero
	}
	return decimal.NewFromInt(int64(missedRequests)).Div(decimal.NewFromInt(int64(inferenceCount)))
}

// MissSumAboveCutoff adds the miss rates above cutoff; the sum can be extended
// epoch by epoch without changing the reputation it gives.
func MissSumAboveCutoff(missPercentages []decimal.Decimal, cutoff decimal.Decimal) decimal.Decimal {
	sum := decimal.Zero
	for _, missPercentage := range missPercentages {
		if missPercentage.GreaterThan(cutoff) {
			sum = sum.Add(missPercentage)
		}
	}
	return sum
}

func calculateReputation(ctx *reputationContextDecimal, missSum decimal.Decimal) decimal.Decimal {
	actualEpochCount := ctx.EpochCount.Sub(missCost(missSum, ctx.ValidationParams))
	if actualEpochCount.GreaterThan(ctx.ValidationParams.EpochsToMax) {
		return oneHundred
	}
	if actualEpochCount.LessThanOrEqual(decimal.Zero) {
		return decimal.Zero
	}
	return actualEpochCount.Div(ctx.ValidationParams.EpochsToMax).Truncate(2).Mul(oneHundred)
}

// missCost equals the per-epoch sum of missRate * (1/EpochsToMax) * penalty,
// times EpochsToMax: decimal Mul and Add are exact.
func missCost(missSum decimal.Decimal, params *validationParamsDecimal) decimal.Decimal {
	singleEpochValue := one.Div(params.EpochsToMax)
	return missSum.Mul(singleEpochValue).Mul(params.MissRequestsPenalty).Mul(params.EpochsToMax)
}
