package limits

import (
	"maps"
	"sync"

	"devshard/cmd/gateway/chain"
)

// Capacity scores escrows against the chain's weight views; the observer has already applied Preserved.
type Capacity struct {
	mu         sync.RWMutex
	snapshot   chain.PhaseSnapshot
	available  func(participant, model string) bool
	membership map[string]map[string]float64
}

// ModelWeights is one model's weights and scale factor, read in one walk.
type ModelWeights struct {
	ScaleFactor       float64
	CurrentWeight     float64
	BaselineWeight    float64
	WeightsUnobserved bool
}

func NewCapacity(available func(participant, model string) bool) *Capacity {
	return &Capacity{
		available:  available,
		membership: map[string]map[string]float64{},
	}
}

func (c *Capacity) Update(s chain.PhaseSnapshot) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.snapshot = s
}

// hostShares[h] is slots(h,escrowID)/totalSlots(h, model), so a shared participant is split across its escrows of that model, not counted twice.
func (c *Capacity) SetEscrowMembership(escrowID string, hostShares map[string]float64) {
	clean := make(map[string]float64, len(hostShares))
	maps.Copy(clean, hostShares)
	c.mu.Lock()
	defer c.mu.Unlock()
	c.membership[escrowID] = clean
}

func (c *Capacity) RemoveEscrow(escrowID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.membership, escrowID)
}

// ModelWeights takes the EFFECTIVE blocking state, never the chain's raw one. See README.md, "The capacity model".
func (c *Capacity) ModelWeights(model string, blocked bool) ModelWeights {
	c.mu.RLock()
	served := c.modelServedLocked(model)
	current, full := c.currentWeightsLocked(model), c.fullWeightsLocked(model)
	c.mu.RUnlock()
	if !served {
		return ModelWeights{}
	}
	weights := ModelWeights{
		CurrentWeight:     c.sumAvailable(current, model),
		BaselineWeight:    sumWeights(full),
		WeightsUnobserved: weightsUnobserved(current, full),
	}
	if !blocked {
		weights.ScaleFactor = scaleFactor(weights.CurrentWeight, weights.BaselineWeight)
	}
	return weights
}

// EscrowWeight falls back to the membership share. See capacity.md, "Two fail-safes with opposite directions".
func (c *Capacity) EscrowWeight(escrowID, model string) float64 {
	c.mu.RLock()
	served := c.modelServedLocked(model)
	current, full := c.currentWeightsLocked(model), c.fullWeightsLocked(model)
	shares := c.membership[escrowID]
	c.mu.RUnlock()
	if !served {
		return 0
	}
	var availableForModel func(string) bool
	if c.available != nil {
		availableForModel = func(host string) bool { return c.available(host, model) }
	}
	if weightsUnobserved(current, full) {
		return availableShare(shares, availableForModel)
	}
	return escrowWeight(current, shares, availableForModel)
}

// ParticipantWeight is what one host earned for one model, the lower of the chain's two views.
func (c *Capacity) ParticipantWeight(participant, model string) float64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if !c.modelServedLocked(model) {
		return 0
	}
	return min(c.fullWeightsLocked(model)[participant], c.currentWeightsLocked(model)[participant])
}

// A model absent from a populated by-model view is served by nobody, so it must not inherit the generic view.
func (c *Capacity) modelServedLocked(model string) bool {
	if len(c.snapshot.CurrentWeightsByModel) == 0 && len(c.snapshot.FullWeightsByModel) == 0 {
		return true
	}
	if _, ok := c.snapshot.CurrentWeightsByModel[model]; ok {
		return true
	}
	_, ok := c.snapshot.FullWeightsByModel[model]
	return ok
}

// The two sides fall back independently, so a missing full-by-model entry can't suppress a present current one.
func (c *Capacity) currentWeightsLocked(model string) map[string]float64 {
	if weights, ok := c.snapshot.CurrentWeightsByModel[model]; ok {
		return weights
	}
	return c.snapshot.CurrentWeights
}

func (c *Capacity) fullWeightsLocked(model string) map[string]float64 {
	if weights, ok := c.snapshot.FullWeightsByModel[model]; ok {
		return weights
	}
	return c.snapshot.FullWeights
}

func (c *Capacity) sumAvailable(weights map[string]float64, model string) float64 {
	var sum float64
	for host, weight := range weights {
		if c.available != nil && !c.available(host, model) {
			continue
		}
		sum += weight
	}
	return sum
}

// An empty view means the chain named nobody, not that everybody weighs nothing.
func weightsUnobserved(current, full map[string]float64) bool {
	return len(current) == 0 && len(full) == 0
}

func sumWeights(weights map[string]float64) float64 {
	var sum float64
	for _, weight := range weights {
		sum += weight
	}
	return sum
}
