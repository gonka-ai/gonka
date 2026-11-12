package loadtest

import (
	"math"
	"sort"
)

// theilSen fits y = slope*x + intercept as the median of all pairwise slopes,
// so a single step or outlier moves it far less than least squares would.
// ok is false when fewer than two distinct x values exist.
func theilSen(xs, ys []float64) (slope, intercept float64, ok bool) {
	n := min(len(xs), len(ys))
	slopes := make([]float64, 0, n*(n-1)/2)
	for i := 0; i < n; i++ {
		for j := i + 1; j < n; j++ {
			if dx := xs[j] - xs[i]; dx != 0 {
				slopes = append(slopes, (ys[j]-ys[i])/dx)
			}
		}
	}
	if len(slopes) == 0 {
		return 0, 0, false
	}
	slope = median(slopes)
	residuals := make([]float64, n)
	for i := 0; i < n; i++ {
		residuals[i] = ys[i] - slope*xs[i]
	}
	return slope, median(residuals), true
}

// median returns NaN for an empty slice. It sorts a copy.
func median(values []float64) float64 {
	if len(values) == 0 {
		return math.NaN()
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}
