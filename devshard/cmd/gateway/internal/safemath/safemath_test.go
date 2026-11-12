package safemath

import (
	"math"
	"testing"
)

// Test flow:
//  1. Build a table of int64 pairs varying ordinary sums, values at or past the int64 ceiling and floor, and operands whose sum would cross both bounds.
//  2. Call AddSaturating for each pair.
//  3. Assert the result matches the case's expected saturated sum.
func TestAddSaturatingStopsAtTheBound(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name          string
		first, second int64
		want          int64
	}{
		{"ordinary", 2, 3, 5},
		{"at the ceiling", math.MaxInt64, 1, math.MaxInt64},
		{"past the ceiling", math.MaxInt64 - 1, 5, math.MaxInt64},
		{"both at the ceiling", math.MaxInt64, math.MaxInt64, math.MaxInt64},
		{"at the floor", math.MinInt64, -1, math.MinInt64},
		{"both at the floor", math.MinInt64, math.MinInt64, math.MinInt64},
		{"the two bounds meet", math.MaxInt64, math.MinInt64, -1},
		{"negative back inside", math.MaxInt64, -1, math.MaxInt64 - 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := AddSaturating(testCase.first, testCase.second); got != testCase.want {
				t.Fatalf("AddSaturating(%d, %d) = %d, want %d: a counter that wraps hands every comparison below it a number it believes",
					testCase.first, testCase.second, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a table of int64 pairs varying ordinary products, multiplication by zero, values at or past the int64 ceiling and floor, and sign combinations that would overflow across a bound.
//  2. Call MulSaturating for each pair.
//  3. Assert the result matches the case's expected saturated product.
func TestMulSaturatingStopsAtTheBound(t *testing.T) {
	t.Parallel()

	for _, testCase := range []struct {
		name          string
		first, second int64
		want          int64
	}{
		{"ordinary", 3, 4, 12},
		{"by zero", math.MaxInt64, 0, 0},
		{"zero by", 0, math.MinInt64, 0},
		{"past the ceiling", math.MaxInt64, 2, math.MaxInt64},
		{"past the floor", math.MinInt64, 2, math.MinInt64},
		{"two negatives overflow upward", math.MinInt64, -2, math.MaxInt64},
		{"the floor negated", math.MinInt64, -1, math.MaxInt64},
		{"the floor negated the other way", -1, math.MinInt64, math.MaxInt64},
		{"the ceiling negated", math.MaxInt64, -1, -math.MaxInt64},
		{"identity at the floor", math.MinInt64, 1, math.MinInt64},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			if got := MulSaturating(testCase.first, testCase.second); got != testCase.want {
				t.Fatalf("MulSaturating(%d, %d) = %d, want %d: a window sized by a wrapped product never admits anything again",
					testCase.first, testCase.second, got, testCase.want)
			}
		})
	}
}

// Test flow:
//  1. Build a set of interesting int64 values spanning the extremes, small magnitudes, and both signs.
//  2. Compute AddSaturating and MulSaturating over every pair drawn from that set.
//  3. Assert a sum of two same-signed values never lands on the other side of either operand.
//  4. Assert a product of two nonzero values has a positive sign only when the operands share a sign, and a negative sign only when they differ.
func TestSaturatingArithmeticNeverWraps(t *testing.T) {
	t.Parallel()

	interesting := []int64{math.MinInt64, math.MinInt64 + 1, -(1 << 40), -2, -1, 0, 1, 2, 1 << 40, math.MaxInt64 - 1, math.MaxInt64}
	for _, first := range interesting {
		for _, second := range interesting {
			if sum := AddSaturating(first, second); (first > 0 && second > 0 && sum < first) || (first < 0 && second < 0 && sum > first) {
				t.Errorf("AddSaturating(%d, %d) = %d wrapped past its own operand", first, second, sum)
			}
			product := MulSaturating(first, second)
			if first != 0 && second != 0 && (first > 0) == (second > 0) && product < 0 {
				t.Errorf("MulSaturating(%d, %d) = %d, want a positive product for two operands of one sign", first, second, product)
			}
			if first != 0 && second != 0 && (first > 0) != (second > 0) && product > 0 {
				t.Errorf("MulSaturating(%d, %d) = %d, want a negative product for operands of opposite signs", first, second, product)
			}
		}
	}
}

// Test flow:
//  1. Table-driven: sums and products below, at and past the uint64 bound.
//  2. Assert each stops at the bound instead of wrapping.
func TestUint64ArithmeticStopsAtTheBound(t *testing.T) {
	for _, testCase := range []struct {
		name string
		got  uint64
		want uint64
	}{
		{"a sum in range", AddSaturatingUint64(2, 3), 5},
		{"a sum at the bound", AddSaturatingUint64(math.MaxUint64-1, 1), math.MaxUint64},
		{"a sum past the bound", AddSaturatingUint64(math.MaxUint64, 1), math.MaxUint64},
		{"a product in range", MulSaturatingUint64(2, 3), 6},
		{"a product with zero", MulSaturatingUint64(0, math.MaxUint64), 0},
		{"a product past the bound", MulSaturatingUint64(math.MaxUint64, 2), math.MaxUint64},
	} {
		if testCase.got != testCase.want {
			t.Errorf("%s = %d, want %d", testCase.name, testCase.got, testCase.want)
		}
	}
}

// Test flow:
//  1. Table-driven: checked sums and products below, at and past the uint64 bound, and products with zero.
//  2. Assert each reports its result when it did not wrap, and 0, false when it did.
func TestCheckedUint64ArithmeticReportsAWrap(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		operation func(first, second uint64) (uint64, bool)
		first     uint64
		second    uint64
		want      uint64
		wantOK    bool
	}{
		{"a sum in range", AddCheckedUint64, 2, 3, 5, true},
		{"a sum at the bound", AddCheckedUint64, math.MaxUint64 - 1, 1, math.MaxUint64, true},
		{"a sum past the bound", AddCheckedUint64, math.MaxUint64, 1, 0, false},
		{"a product in range", MulCheckedUint64, 2, 3, 6, true},
		{"a product with zero", MulCheckedUint64, 0, math.MaxUint64, 0, true},
		{"a product at the bound", MulCheckedUint64, math.MaxUint64, 1, math.MaxUint64, true},
		{"a product past the bound", MulCheckedUint64, math.MaxUint64, 2, 0, false},
		{"a product of two halves past the bound", MulCheckedUint64, 1 << 32, 1 << 32, 0, false},
	} {
		if got, ok := testCase.operation(testCase.first, testCase.second); got != testCase.want || ok != testCase.wantOK {
			t.Errorf("%s: (%d, %d) = %d, %t, want %d, %t: a wrapped price must not read as a small one",
				testCase.name, testCase.first, testCase.second, got, ok, testCase.want, testCase.wantOK)
		}
	}
}
