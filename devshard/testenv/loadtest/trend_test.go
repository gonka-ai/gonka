package loadtest

import (
	"math"
	"testing"

	"github.com/stretchr/testify/require"
)

// Test flow:
// 1. Fit flat, linear, noisy linear, step and degenerate series.
// 2. Check slope and intercept, and that a late step barely moves the slope.
func TestTheilSen(t *testing.T) {
	xs := make([]float64, 40)
	for i := range xs {
		xs[i] = float64(i)
	}
	series := func(f func(x float64) float64) []float64 {
		ys := make([]float64, len(xs))
		for i, x := range xs {
			ys[i] = f(x)
		}
		return ys
	}
	tests := []struct {
		name          string
		xs, ys        []float64
		slope, offset float64
		tolerance     float64
		ok            bool
	}{
		{name: "flat", xs: xs, ys: series(func(float64) float64 { return 7 }), slope: 0, offset: 7, ok: true},
		{name: "linear", xs: xs, ys: series(func(x float64) float64 { return 3*x + 2 }), slope: 3, offset: 2, ok: true},
		{name: "linear with outliers", xs: xs, ys: series(func(x float64) float64 {
			if int(x)%10 == 0 {
				return 1000
			}
			return 2*x + 1
		}), slope: 2, offset: 1, tolerance: 0.3, ok: true},
		{name: "step", xs: xs, ys: series(func(x float64) float64 {
			if x >= 35 {
				return 100
			}
			return 10
		}), slope: 0, offset: 10, ok: true},
		{name: "single point", xs: []float64{1}, ys: []float64{1}, ok: false},
		{name: "same x", xs: []float64{2, 2, 2}, ys: []float64{1, 5, 9}, ok: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			slope, offset, ok := theilSen(tt.xs, tt.ys)
			require.Equal(t, tt.ok, ok)
			if !ok {
				return
			}
			require.InDelta(t, tt.slope, slope, tt.tolerance+1e-9)
			require.InDelta(t, tt.offset, offset, 10*tt.tolerance+1e-9)
		})
	}
}

// Test flow:
// 1. Take medians of odd, even and empty slices.
// 2. Check the input slice stays unsorted.
func TestMedian(t *testing.T) {
	values := []float64{5, 1, 3}
	require.Equal(t, 3.0, median(values))
	require.Equal(t, []float64{5, 1, 3}, values)
	require.Equal(t, 2.5, median([]float64{4, 1, 2, 3}))
	require.True(t, math.IsNaN(median(nil)))
}
