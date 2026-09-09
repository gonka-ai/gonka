package calculations

import (
	"math"
	"math/bits"

	sdkerrors "cosmossdk.io/errors"
	"github.com/productscience/inference/x/inference/types"
)

const (
	DefaultMaxTokens = 5000
	PerTokenCost     = 1000 // Legacy fallback price
)

const maxInt64Uint64 = uint64(math.MaxInt64)

func CalculateCost(inference *types.Inference) (int64, error) {
	// Simply use the per-token price stored in the inference
	// RecordInferencePrice ensures this is always set to the correct value:
	// - Dynamic price from BeginBlocker (including 0 for grace period)
	// - Legacy fallback price (1000) if dynamic pricing unavailable
	productHigh1, productLow1 := bits.Mul64(inference.CompletionTokenCount, inference.PerTokenPrice)
	productHigh2, productLow2 := bits.Mul64(inference.PromptTokenCount, inference.PerTokenPrice)
	sumLow, carry := bits.Add64(productLow1, productLow2, 0)
	// While this itself could overflow, this is not going to happen with constraints on token count
	sumHigh := productHigh1 + productHigh2 + carry
	if sumHigh != 0 || sumLow > maxInt64Uint64 {
		return 0, sdkerrors.Wrap(types.ErrArithmeticOverflow, "inference cost out of range")
	}
	return int64(sumLow), nil
}

func CalculateEscrow(inference *types.Inference, promptTokens uint64) (int64, error) {
	// Simply use the per-token price stored in the inference
	// RecordInferencePrice ensures this is always set to the correct value:
	// - Dynamic price from BeginBlocker (including 0 for grace period)
	// - Legacy fallback price (1000) if dynamic pricing unavailable
	sumTokens, carry := bits.Add64(inference.MaxTokens, promptTokens, 0)
	if carry != 0 {
		return 0, sdkerrors.Wrap(types.ErrTokenCountOutOfRange, "token count out of range")
	}
	productHigh, productLow := bits.Mul64(sumTokens, inference.PerTokenPrice)
	if productHigh != 0 || productLow > maxInt64Uint64 {
		return 0, sdkerrors.Wrap(types.ErrArithmeticOverflow, "escrow amount out of range")
	}
	return int64(productLow), nil
}
