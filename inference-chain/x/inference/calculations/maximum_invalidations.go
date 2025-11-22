package calculations

import (
	"github.com/shopspring/decimal"
)

// tanhApprox calculates a deterministic tanh(x) approximation using only decimal.Decimal.
// Formula: tanh(x) = (e^(2x) - 1) / (e^(2x) + 1)
func tanhApprox(x decimal.Decimal) decimal.Decimal {
	limit := decimal.NewFromInt(10)
	if x.GreaterThan(limit) {
		x = limit
	} else if x.LessThan(limit.Neg()) {
		x = limit.Neg()
	}

	two := decimal.NewFromInt(2)
	twoX := x.Mul(two)

	// Approximate e^(2x) using a Taylor expansion
	exp2X := expApprox(twoX, 20) // 20 terms of precision

	num := exp2X.Sub(decimal.NewFromInt(1))
	den := exp2X.Add(decimal.NewFromInt(1))
	return num.Div(den)
}

// expApprox approximates e^x using Taylor series: 1 + x + x^2/2! + x^3/3! + ...
func expApprox(x decimal.Decimal, terms int) decimal.Decimal {
	result := decimal.NewFromInt(1)
	term := decimal.NewFromInt(1)

	for i := 1; i <= terms; i++ {
		n := decimal.NewFromInt(int64(i))
		term = term.Mul(x).Div(n)
		result = result.Add(term)
	}

	return result
}

// CalculateInvalidations computes max concurrent invalidations using a capped tanh curve.
// All operations are deterministic using shopspring/decimal.
func CalculateInvalidations(
	inferences int64,
	weight decimal.Decimal, // 0.0 - 1.0
	reputation int32, // 0 - 100
	maxInvalidations int64,
	curveFactor int64,
	minimumInvalidations int64,
) int64 {
	if minimumInvalidations == 0 {
		minimumInvalidations = 1
	}
	reputationDecimal := decimal.NewFromInt32(reputation)
	if curveFactor <= 0 || maxInvalidations <= 0 {
		return minimumInvalidations
	}

	// I / curveFactor
	x := decimal.NewFromInt(inferences).Div(decimal.NewFromInt(curveFactor))
	curveMultiplier := tanhApprox(x)

	// Scale by W and R
	scaling := weight.Mul(reputationDecimal.Div(decimal.NewFromInt(100)))
	maxInv := decimal.NewFromInt(maxInvalidations)

	// Final result
	inv := curveMultiplier.Mul(scaling).Mul(maxInv).Floor().IntPart()
	if inv < minimumInvalidations {
		return minimumInvalidations
	}
	return inv
}
