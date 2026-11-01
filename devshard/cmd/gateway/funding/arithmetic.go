package funding

import (
	"math"
	"math/bits"
)

func subtractFloor(left, right uint64) uint64 {
	if left < right {
		return 0
	}
	return left - right
}

func ceilDivide(dividend, divisor uint64) uint64 {
	quotient := dividend / divisor
	if dividend%divisor != 0 {
		quotient++
	}
	return quotient
}

// scaledBy is amount × numerator / denominator, saturating instead of wrapping.
func scaledBy(amount, numerator, denominator uint64) uint64 {
	high, low := bits.Mul64(amount, numerator)
	if high >= denominator {
		return math.MaxUint64
	}
	quotient, _ := bits.Div64(high, low, denominator)
	return quotient
}
