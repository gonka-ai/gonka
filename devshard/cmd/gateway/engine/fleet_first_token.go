package engine

import (
	"math"
	"slices"
	"sync"
	"time"
)

const (
	fleetScaleUnknown        = 0.0
	fleetWindow              = 30 * time.Second
	fleetMinimumSamples      = 50
	fleetSampleCapacity      = 4096
	fleetScaleRefresh        = time.Second
	fleetHedgeDeviations     = 2.33
	absoluteDeviationToSigma = 1.4826
)

type fleetSample struct {
	at    time.Time
	level float64
}

type fleetWaitingAttempt struct {
	clockStart time.Time
	curve      time.Duration
}

type fleetFirstToken struct {
	mu       sync.Mutex
	samples  [fleetSampleCapacity]fleetSample
	next     int
	filled   int
	waiting  map[*liveAttempt]fleetWaitingAttempt
	scale    float64
	scaledAt time.Time
	scratch  []float64
}

func newFleetFirstToken() *fleetFirstToken {
	return &fleetFirstToken{waiting: map[*liveAttempt]fleetWaitingAttempt{}}
}

type fleetsByModel struct {
	mu      sync.Mutex
	byModel map[string]*fleetFirstToken
}

func newFleetsByModel() *fleetsByModel {
	return &fleetsByModel{byModel: map[string]*fleetFirstToken{}}
}

func (fleets *fleetsByModel) forModel(model string) *fleetFirstToken {
	fleets.mu.Lock()
	defer fleets.mu.Unlock()
	fleet := fleets.byModel[model]
	if fleet == nil {
		fleet = newFleetFirstToken()
		fleets.byModel[model] = fleet
	}
	return fleet
}

func (f *fleetFirstToken) recordDispatch(attempt *liveAttempt, clockStart time.Time, inputTokens uint64) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.waiting[attempt] = fleetWaitingAttempt{clockStart: clockStart, curve: firstTokenCurve(inputTokens)}
}

func (f *fleetFirstToken) recordFirstToken(attempt *liveAttempt, at time.Time) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	waiting, ok := f.waiting[attempt]
	if !ok {
		return
	}
	delete(f.waiting, attempt)
	waited := at.Sub(waiting.clockStart)
	if waited <= 0 {
		return
	}
	f.samples[f.next] = fleetSample{at: at, level: math.Log(float64(waited) / float64(waiting.curve))}
	f.next = (f.next + 1) % fleetSampleCapacity
	f.filled = min(f.filled+1, fleetSampleCapacity)
}

func (f *fleetFirstToken) forget(attempt *liveAttempt) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.waiting, attempt)
}

func (f *fleetFirstToken) hedgeScale(now time.Time) float64 {
	if f == nil {
		return fleetScaleUnknown
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.scaledAt.IsZero() && now.Before(f.scaledAt.Add(fleetScaleRefresh)) {
		return f.scale
	}
	f.scale, f.scaledAt = f.measureLocked(now), now
	return f.scale
}

func (f *fleetFirstToken) measureLocked(now time.Time) float64 {
	for attempt, waiting := range f.waiting {
		if waiting.clockStart.Before(now.Add(-streamingHardTimeout)) {
			delete(f.waiting, attempt)
		}
	}
	since := now.Add(-fleetWindow)
	levels := f.scratch[:0]
	for _, sample := range f.samples[:f.filled] {
		if !sample.at.Before(since) && !sample.at.After(now) {
			levels = append(levels, sample.level)
		}
	}
	defer func() { f.scratch = levels[:0] }()
	if len(levels) < fleetMinimumSamples {
		return fleetScaleUnknown
	}
	typical := math.Exp(medianOf(levels))
	for _, waiting := range f.waiting {
		age := now.Sub(waiting.clockStart)
		if float64(age) > float64(waiting.curve)*typical {
			levels = append(levels, math.Log(float64(age)/float64(waiting.curve)))
		}
	}
	centre := medianOf(levels)
	for index, level := range levels {
		levels[index] = math.Abs(level - centre)
	}
	return math.Exp(centre + fleetHedgeDeviations*absoluteDeviationToSigma*medianOf(levels))
}

func medianOf(values []float64) float64 {
	slices.Sort(values)
	middle := len(values) / 2
	if len(values)%2 == 1 {
		return values[middle]
	}
	return midpoint(values[middle-1], values[middle])
}

func midpoint(low, high float64) float64 {
	return (low + high) / 2
}
