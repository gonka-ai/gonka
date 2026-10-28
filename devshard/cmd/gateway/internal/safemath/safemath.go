// Package safemath keeps a counter inside int64 instead of wrapping it. See rules.md, "10. Counters saturate".
package safemath

import "math"

// AddSaturating stops at the int64 bound instead of wrapping past it.
func AddSaturating(first, second int64) int64 {
	switch {
	case second > 0 && first > math.MaxInt64-second:
		return math.MaxInt64
	case second < 0 && first < math.MinInt64-second:
		return math.MinInt64
	}
	return first + second
}

// MulSaturating stops at the int64 bound instead of wrapping past it.
func MulSaturating(first, second int64) int64 {
	if first == 0 || second == 0 {
		return 0
	}
	if (first == math.MinInt64 && second == -1) || (second == math.MinInt64 && first == -1) {
		return math.MaxInt64
	}
	product := first * second
	if product/second != first {
		if (first > 0) == (second > 0) {
			return math.MaxInt64
		}
		return math.MinInt64
	}
	return product
}
