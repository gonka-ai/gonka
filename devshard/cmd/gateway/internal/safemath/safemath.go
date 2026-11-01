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

// AddSaturatingUint64 stops at the uint64 bound instead of wrapping past it.
func AddSaturatingUint64(first, second uint64) uint64 {
	if sum := first + second; sum >= first {
		return sum
	}
	return math.MaxUint64
}

// MulSaturatingUint64 stops at the uint64 bound instead of wrapping past it.
func MulSaturatingUint64(first, second uint64) uint64 {
	if first == 0 || second == 0 {
		return 0
	}
	if product := first * second; product/first == second {
		return product
	}
	return math.MaxUint64
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
