package calculations

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"math"
	"strconv"

	"github.com/productscience/inference/x/inference/types"
	"github.com/shopspring/decimal"
)

func ShouldValidate(
	seed int64,
	inferenceDetails *types.InferenceValidationDetails,
	totalPower uint32,
	validatorPower uint32,
	executorPower uint32,
	validationParams *types.ValidationParams,
	debug bool,
) (bool, string) {
	if totalPower <= executorPower {
		return false, "ShouldValidate:false totalPower <= executorPower"
	}
	// Creating with exponent vs dividing
	executorReputation := decimal.New(int64(inferenceDetails.ExecutorReputation), -2)
	maxValidationAverage := validationParams.MaxValidationAverage.ToDecimal()
	minValidationAverage := CalculateMinimumValidationAverage(int64(inferenceDetails.TrafficBasis), validationParams)
	rangeSize := maxValidationAverage.Sub(minValidationAverage)
	// algebraic simplification/removal of temp variables
	targetValidations := maxValidationAverage.Sub(rangeSize.Mul(executorReputation))
	// 100% rep will be minValidationAverage, 0% rep will be maxValidationAverage
	ourProbability := targetValidations.Mul(decimal.NewFromInt(int64(validatorPower))).Div(decimal.NewFromInt(int64(totalPower - executorPower)))
	if ourProbability.GreaterThan(one) {
		ourProbability = one
	}
	randFloat := DeterministicFloat(seed, inferenceDetails.InferenceId)
	shouldValidate := randFloat.LessThan(ourProbability)
	// The debug string was very expensive to create
	// But we shouldn't return "", empty strings can cause issues in logging
	if debug {
		return shouldValidate, fmt.Sprintf(
			"Should Validate: %v randFloat: %v ourProbability: %v, rangeSize: %v, targetValidations: %v",
			shouldValidate, randFloat, ourProbability, rangeSize, targetValidations,
		)
	} else if !shouldValidate {
		return shouldValidate, "ShouldValidate:false"
	}
	return shouldValidate, "ShouldValidate:true"
}

// DeterministicFloat generates a deterministic random float [0,1) from a seed and identifier.
// Instead of a real random number generator, we use a deterministic function that takes a seed and an identifier.
// This is more or less as random as using a seed in a deterministic random seed determined by this same hash, and has
// the advantage of being 100% deterministic regardless of platform and also faster to compute.
func DeterministicFloat(seed int64, identifier string) decimal.Decimal {
	// Build the exact same bytes as fmt.Sprintf("%d:%s", seed, identifier)
	// but without fmt. This must stay base-10 to preserve the legacy hash.
	b := make([]byte, 0, 21+1+len(identifier)) // 21 is enough for int64 in base10 with sign
	b = strconv.AppendInt(b, seed, 10)
	b = append(b, ':')
	b = append(b, identifier...)

	sum := sha256.Sum256(b)
	hashInt := binary.BigEndian.Uint64(sum[:8])

	hashDecimal := decimal.NewFromUint64(hashInt)
	return hashDecimal.Div(maxUint64Decimal)
}

var maxUint64Decimal = decimal.NewFromUint64(math.MaxUint64)
