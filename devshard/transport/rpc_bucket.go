package transport

import "math"

// TokenAdmitThreshold is how many tokens must be on hand to start a charge.
// When cost <= burst this is cost (classic bucket). When cost > burst it is
// burst: one oversize RPC may run from a full bucket and drive tokens
// negative; refill still caps at burst (finding 12).
func TokenAdmitThreshold(cost, burst float64) float64 {
	if cost <= 0 {
		return 0
	}
	if burst < 1 {
		burst = 1
	}
	return math.Min(cost, burst)
}
