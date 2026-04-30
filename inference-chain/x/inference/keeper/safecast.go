package keeper

import (
	"fmt"
	"math"
)

// safeInt32FromInt64 converts int64 to int32, returning error if the value
// is outside the int32 range. Use this at proto persistence boundaries
// where an int64 value must be stored as int32.
func safeInt32FromInt64(v int64) (int32, error) {
	if v < math.MinInt32 || v > math.MaxInt32 {
		return 0, fmt.Errorf("int64 value %d overflows int32 range [%d, %d]", v, int64(math.MinInt32), int64(math.MaxInt32))
	}
	return int32(v), nil
}

// safeUint32FromInt64 converts int64 to uint32, returning error if the value
// is negative or exceeds MaxUint32.
func safeUint32FromInt64(v int64) (uint32, error) {
	if v < 0 || v > math.MaxUint32 {
		return 0, fmt.Errorf("int64 value %d overflows uint32 range [0, %d]", v, uint64(math.MaxUint32))
	}
	return uint32(v), nil
}

// safeUint32FromUint64 converts uint64 to uint32, returning error if the value
// exceeds MaxUint32.
func safeUint32FromUint64(v uint64) (uint32, error) {
	if v > math.MaxUint32 {
		return 0, fmt.Errorf("uint64 value %d overflows uint32 range [0, %d]", v, uint64(math.MaxUint32))
	}
	return uint32(v), nil
}

// safeInt32FromUint64 converts uint64 to int32, returning error if the value
// exceeds MaxInt32.
func safeInt32FromUint64(v uint64) (int32, error) {
	if v > math.MaxInt32 {
		return 0, fmt.Errorf("uint64 value %d overflows int32 range [0, %d]", v, uint64(math.MaxInt32))
	}
	return int32(v), nil
}

// safeUint8FromUint32 converts uint32 to uint8, returning error if the value
// exceeds MaxUint8.
func safeUint8FromUint32(v uint32) (uint8, error) {
	if v > math.MaxUint8 {
		return 0, fmt.Errorf("uint32 value %d overflows uint8 range [0, %d]", v, math.MaxUint8)
	}
	return uint8(v), nil
}

// clampInt32FromInt converts int to int32, clamping to [MinInt32, MaxInt32].
// Use ONLY for query response fields where overflow is cosmetic, not state-corrupting.
// Logs a warning via the provided function when clamping occurs.
func clampInt32FromInt(v int, logWarn func(msg string, keyvals ...any)) int32 {
	if v > math.MaxInt32 {
		if logWarn != nil {
			logWarn("integer overflow clamped to MaxInt32", "original", v, "clamped", math.MaxInt32)
		}
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		if logWarn != nil {
			logWarn("integer underflow clamped to MinInt32", "original", v, "clamped", math.MinInt32)
		}
		return math.MinInt32
	}
	return int32(v)
}
