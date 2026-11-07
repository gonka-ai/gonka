package main

import (
	"math"
	"sync"
	"sync/atomic"
)

// hostCapGate lets the picker keep one participant from receiving more real
// requests than its share of the PoC concurrency budget.
type hostCapGate interface {
	// blocked reports whether the participant is at its cap right now.
	blocked(model, participantKey string) bool
	// reserve takes one in-flight slot, or returns false when the participant is at its cap.
	reserve(model, participantKey string) bool
	release(participantKey string)
}

// pocHostCap counts the real requests this gateway has in flight per
// participant, across every escrow runtime (a host's capacity is shared by
// all of them). During PoC generation the per-model concurrency budget is
// split by weight, but nothing stopped a small preserved host from receiving
// a whole session's stream while the other slots were ghosted. The cap is
// that host's weight share of the same budget. Outside PoC generation, or
// when the host's weight is unknown, it never blocks.
//
// mu is a leaf lock: the picker calls in while holding its own and the
// session's, so nothing here may call back into either.
type pocHostCap struct {
	mu       sync.Mutex
	inFlight map[string]int64

	active func() bool
	weight func(model, participantKey string) float64
	// per10000Bits is math.Float64bits of the PoC concurrency per 10000
	// weight. It is mirrored here so the check does not take Gateway.mu.
	per10000Bits atomic.Uint64
}

func newPoCHostCap(active func() bool, weight func(model, participantKey string) float64) *pocHostCap {
	return &pocHostCap{
		inFlight: make(map[string]int64),
		active:   active,
		weight:   weight,
	}
}

func (c *pocHostCap) setPer10000Weight(v float64) {
	c.per10000Bits.Store(math.Float64bits(v))
}

// limit returns the participant's cap, or 0 when no cap applies.
func (c *pocHostCap) limit(model, participantKey string) int64 {
	if c == nil || c.active == nil || c.weight == nil || participantKey == "" || !c.active() {
		return 0
	}
	per10000 := math.Float64frombits(c.per10000Bits.Load())
	w := c.weight(model, participantKey)
	if w <= 0 || per10000 <= 0 {
		return 0
	}
	return max(1, weightConcurrencyLimit(w, per10000))
}

func (c *pocHostCap) blocked(model, participantKey string) bool {
	limit := c.limit(model, participantKey)
	if limit == 0 {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inFlight[participantKey] >= limit
}

func (c *pocHostCap) reserve(model, participantKey string) bool {
	limit := c.limit(model, participantKey)
	c.mu.Lock()
	defer c.mu.Unlock()
	if limit > 0 && c.inFlight[participantKey] >= limit {
		return false
	}
	c.inFlight[participantKey]++
	return true
}

// release gives back one slot. An unmatched release is ignored so the count
// can never go negative.
func (c *pocHostCap) release(participantKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch n := c.inFlight[participantKey]; {
	case n > 1:
		c.inFlight[participantKey] = n - 1
	case n == 1:
		delete(c.inFlight, participantKey)
	}
}

func (c *pocHostCap) inFlightFor(participantKey string) int64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inFlight[participantKey]
}

// syncPoCHostCap mirrors the PoC concurrency setting into the cap.
func (g *Gateway) syncPoCHostCap() {
	if g == nil || g.pocCap == nil {
		return
	}
	g.pocCap.setPer10000Weight(g.settings.WithTuningDefaults().PoCMaxConcurrentPer10000Weight)
}

func (g *Gateway) attachPoCHostCap(rt *devshardRuntime) {
	if g == nil || g.pocCap == nil || rt == nil || rt.proxy == nil || rt.proxy.redundancy == nil || rt.proxy.redundancy.picker == nil {
		return
	}
	g.syncPoCHostCap()
	rt.proxy.redundancy.picker.setHostCap(g.pocCap)
}
