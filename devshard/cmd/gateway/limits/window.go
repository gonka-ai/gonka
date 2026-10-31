package limits

import "devshard/cmd/gateway/internal/safemath"

const growthEarnedAtPeakShare = 0.5

type key struct {
	participant string
	model       string
}

// window is one congestion window and what is in flight against it. Peak is the high-water mark since the last adjustment.
type window struct {
	tokens   float64
	inflight int64
	peak     int64
}

func fits(inflight, cost int64, window float64) bool {
	return float64(inflight)+float64(cost) <= window
}

func (w *window) take(tokens int64) {
	w.inflight = safemath.AddSaturating(w.inflight, tokens)
	if w.inflight > w.peak {
		w.peak = w.inflight
	}
}

func (w *window) give(tokens int64) {
	w.inflight = max(w.inflight-tokens, 0)
}

func (w *window) reopen(tokens int64) {
	w.tokens = float64(tokens)
	w.peak = w.inflight
}

func (w *window) raiseTo(floor int64) {
	w.tokens = max(w.tokens, float64(floor))
}

func (w *window) lowerTo(ceiling int64) {
	w.tokens = min(w.tokens, float64(ceiling))
}

func (w *window) growIfEarned(carried, step int64) {
	if float64(w.peak) < w.tokens*growthEarnedAtPeakShare {
		return
	}
	w.tokens = grownBy(w.tokens, carried, float64(step))
	w.peak = w.inflight
}

func (w *window) narrow(factor float64, floor int64) {
	if factor == 1 {
		return
	}
	w.tokens = narrowedTo(w.tokens, factor, float64(floor))
	w.peak = w.inflight
}
