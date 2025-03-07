package calculations

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
	"math"
)

func ShouldValidate(
	seed int64,
	inferenceDetails *types.InferenceValidationDetails,
	totalPower uint32,
	validatorPower uint32,
	executorPower uint32,
	validationParams *types.ValidationParams,
) (bool, string) {
	executorReputation := decimal.NewFromInt32(inferenceDetails.ExecutorReputation).Div(decimal.NewFromInt32(100))
	maxValidationAverage := decimal.NewFromFloat(validationParams.MaxValidationAverage)
	minValidationAverage := CalculateMinimumValidationAverage(int64(inferenceDetails.TrafficBasis), validationParams)
	rangeSize := maxValidationAverage.Sub(minValidationAverage)
	executorAdjustment := rangeSize.Mul(one.Sub(executorReputation))
	// 100% rep will be 0, 0% rep will be rangeSize
	targetValidations := minValidationAverage.Add(executorAdjustment)
	ourProbability := targetValidations.Mul(decimal.NewFromInt(int64(validatorPower))).Div(decimal.NewFromInt(int64(totalPower - executorPower)))
	if ourProbability.GreaterThan(one) {
		ourProbability = one
	}
	randFloat := deterministicFloat(seed, inferenceDetails.InferenceId)
	shouldValidate := randFloat.LessThan(ourProbability)
	return shouldValidate, fmt.Sprintf(
		"Should Validate: %v randFloat: %v ourProbability: %v, rangeSize: %v, executorAdjustment: %v, targetValidations: %v",
		shouldValidate, randFloat, ourProbability, rangeSize, executorAdjustment, targetValidations,
	)
}

// In lieu of a real random number generator, we use a deterministic function that takes a seed and an inferenceId
// This is more or less as random as using a seed in a deterministic random seed determined by this same hash, and has
// the advantage of being 100% deterministic regardless of platform and also faster to compute.
func deterministicFloat(seed int64, inferenceId string) decimal.Decimal {
	// Concatenate the seed and inferenceId into a single string
	input := fmt.Sprintf("%d:%s", seed, inferenceId)

	// Use a cryptographic hash (e.g., SHA-256)
	h := sha256.New()
	h.Write([]byte(input))
	hash := h.Sum(nil)

	// Convert the first 8 bytes of the hash into a uint64
	hashInt := binary.BigEndian.Uint64(hash[:8])

	// Normalize the uint64 value to a decimal.Decimal in the range [0, 1)
	maxUint64 := decimal.NewFromUint64(math.MaxUint64) // ^uint64(0) gives max uint64
	hashDecimal := decimal.NewFromUint64(hashInt)
	return hashDecimal.Div(maxUint64)
}
