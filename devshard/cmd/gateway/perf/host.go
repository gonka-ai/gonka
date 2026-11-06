package perf

import (
	"math"
	"slices"
	"time"
)

const (
	latencyWindowSize    = 64
	latencyWindowMinimum = 10
	baselineRise         = 0.001

	thirdQuartileNumerator   = 3
	thirdQuartileDenominator = 4
)

type hostKey struct {
	participant string
	model       string
}

// hostState is the pair the Tracker keys one map by: the two always live and die together.
type hostState struct {
	perf     hostPerf
	ejection ejectionState
}

func newHostState(halfLife time.Duration) *hostState {
	return &hostState{perf: *newHostPerf(halfLife)}
}

type decayedCounter struct {
	count     float64
	lastTouch time.Time
	halfLife  time.Duration
}

func newDecayedCounter(halfLife time.Duration) decayedCounter {
	return decayedCounter{halfLife: halfLife}
}

func (c *decayedCounter) add(now time.Time) {
	c.count = c.value(now) + 1
	c.lastTouch = now
}

func (c *decayedCounter) value(now time.Time) float64 {
	if c.lastTouch.IsZero() {
		return 0
	}
	elapsed := now.Sub(c.lastTouch)
	if elapsed <= 0 {
		return c.count
	}
	return c.count * decayWeight(elapsed, c.halfLife)
}

func decayWeight(elapsed, halfLife time.Duration) float64 {
	if elapsed < 0 {
		elapsed = 0
	}
	return math.Exp2(-float64(elapsed) / float64(halfLife))
}

// hostPerf is not goroutine-safe; the Tracker locks around all access.
type hostPerf struct {
	success         decayedCounter
	fail            decayedCounter
	consecutiveFail int
	lastSeen        time.Time
	firstContent    latencyWindow
	decode          latencyWindow
}

func newHostPerf(halfLife time.Duration) *hostPerf {
	return &hostPerf{
		success: newDecayedCounter(halfLife),
		fail:    newDecayedCounter(halfLife),
	}
}

func (h *hostPerf) recordSample(s Sample, now time.Time) {
	if s.Responsive {
		h.success.add(now)
		h.consecutiveFail = 0
	} else {
		h.fail.add(now)
		h.consecutiveFail++
	}
	h.lastSeen = now
	if s.FirstContent > 0 {
		h.firstContent.add(s.FirstContent)
	}
	if s.TimePerOutputToken > 0 {
		h.decode.add(s.TimePerOutputToken)
	}
}

func (h *hostPerf) resetOutcomeCounters() {
	h.success = newDecayedCounter(h.success.halfLife)
	h.fail = newDecayedCounter(h.fail.halfLife)
}

func (h *hostPerf) failureRate(now time.Time) (rate, volume float64) {
	failValue := h.fail.value(now)
	volume = h.success.value(now) + failValue
	if volume <= 0 {
		return 0, volume
	}
	return failValue / volume, volume
}

// latencyWindow is a ring, not a decayed counter: the escalation needs a quantile, not a rate.
// baseline is the best p75 the host has held. See README.md, "What a host is measured against".
type latencyWindow struct {
	samples  [latencyWindowSize]time.Duration
	next     int
	filled   int
	baseline time.Duration
}

func (w *latencyWindow) add(sample time.Duration) {
	w.samples[w.next] = sample
	w.next = (w.next + 1) % latencyWindowSize
	if w.filled < latencyWindowSize {
		w.filled++
	}
	w.trackBaseline()
}

// trackBaseline falls to a new best at once and rises by baselineRise of the gap. See README.md, "What a host is measured against".
func (w *latencyWindow) trackBaseline() {
	current, known := w.p75(latencyWindowMinimum)
	if !known {
		return
	}
	if w.baseline <= 0 || current < w.baseline {
		w.baseline = current
		return
	}
	w.baseline += time.Duration(float64(current-w.baseline) * baselineRise)
}

// pressure is the current p75 over the best the host has held; zero while the window is too young to say.
func (w *latencyWindow) pressure() float64 {
	current, known := w.p75(latencyWindowMinimum)
	if !known || w.baseline <= 0 {
		return 0
	}
	return float64(current) / float64(w.baseline)
}

func (w *latencyWindow) p75(minimum int) (time.Duration, bool) {
	if w.filled < minimum {
		return 0, false
	}
	var scratch [latencyWindowSize]time.Duration
	sorted := scratch[:copy(scratch[:], w.samples[:w.filled])]
	slices.Sort(sorted)
	return sorted[(len(sorted)*thirdQuartileNumerator)/thirdQuartileDenominator], true
}
