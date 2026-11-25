// Package limits provides capacity-scaled gateway admission and a per-host AIMD concurrency limiter.
package limits

import "math"

const concurrencyWeightBase = 10_000

// weightConcurrencyLimit = floor(weight * per10000 / 10000); 0 when per10000<=0 or weight<=0.
func weightConcurrencyLimit(weight, per10000 float64) int64 {
	if nonPositiveOrNotFinite(weight) || nonPositiveOrNotFinite(per10000) {
		return 0
	}
	limit := math.Floor(weight * per10000 / concurrencyWeightBase)
	if limit >= float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(limit)
}

// nonPositiveOrNotFinite: non-positive or not finite. See README.md, "The capacity model".
func nonPositiveOrNotFinite(value float64) bool {
	return value <= 0 || math.IsNaN(value) || math.IsInf(value, 0)
}

// scaleFactor = clamp(currentAvailable/full, 0, 1); returns 1.0 when full<=0 (baseline unlimited).
func scaleFactor(currentAvailable, full float64) float64 {
	if full <= 0 {
		return 1
	}
	return clampUnit(currentAvailable / full)
}

// escrowWeight = Σ over available hosts of currentWeight[h] * hostShares[h]. See README.md, "The capacity model".
func escrowWeight(currentWeights, hostShares map[string]float64, available func(host string) bool) float64 {
	var sum float64
	for host, share := range hostShares {
		if available != nil && !available(host) {
			continue
		}
		sum += currentWeights[host] * share
	}
	return sum
}

// availableShare = Σ over available hosts of hostShares[h]: escrowWeight with every weight taken as one.
func availableShare(hostShares map[string]float64, available func(host string) bool) float64 {
	var sum float64
	for host, share := range hostShares {
		if available != nil && !available(host) {
			continue
		}
		sum += share
	}
	return sum
}
