package main

import (
	"fmt"
	"math"
	"strings"
	"sync"
)

// GatewayLimiter caps gateway-wide in-flight requests and input tokens.
//
// Two cap pairs are tracked:
//   - maxConcurrent / maxInputTokens are the configured baseline
//     (set via NewGatewayLimiter / UpdateLimits).
//   - effectiveMaxConcurrent / effectiveMaxInputTokens are the
//     currently enforced caps after capacity-driven scaling.
//
// Acquire always checks against the effective caps. ApplyScaleFactor
// recomputes them from the baseline so a scale of 1.0 restores the
// configured values exactly.
type GatewayLimiter struct {
	mu                      sync.Mutex
	maxConcurrent           int64
	maxInputTokens          int64
	effectiveMaxConcurrent  int64
	effectiveMaxInputTokens int64
	currentScale            float64
	inFlightRequests        int64
	inFlightInputToks       int64
	models                  map[string]limiterModelCounter
	modelLimits             map[string]limiterModelLimits
}

type limiterModelCounter struct {
	inFlightRequests  int64
	inFlightInputToks int64
}

type limiterModelLimits struct {
	maxConcurrent  int64
	maxInputTokens int64
}

type LimiterSnapshot struct {
	InFlightRequests        int64                           `json:"in_flight_requests"`
	InFlightInputTokens     int64                           `json:"in_flight_input_tokens"`
	MaxConcurrent           int64                           `json:"max_concurrent_requests"`
	MaxInputTokens          int64                           `json:"max_input_tokens_in_flight"`
	EffectiveMaxConcurrent  int64                           `json:"effective_max_concurrent_requests"`
	EffectiveMaxInputTokens int64                           `json:"effective_max_input_tokens_in_flight"`
	ScaleFactor             float64                         `json:"scale_factor"`
	Models                  map[string]LimiterModelSnapshot `json:"models,omitempty"`
}

type LimiterModelSnapshot struct {
	InFlightRequests              int64   `json:"in_flight_requests"`
	InFlightInputTokens           int64   `json:"in_flight_input_tokens"`
	MaxConcurrent                 int64   `json:"max_concurrent_requests"`
	MaxInputTokens                int64   `json:"max_input_tokens_in_flight"`
	EffectiveMaxConcurrent        int64   `json:"effective_max_concurrent_requests"`
	EffectiveMaxInputTokens       int64   `json:"effective_max_input_tokens_in_flight"`
	CapacityCapRequests           int64   `json:"capacity_cap_requests"`
	CurrentCapacityCapRequests    int64   `json:"current_capacity_cap_requests"`
	CapacityCapInputTokens        int64   `json:"capacity_cap_input_tokens"`
	CurrentCapacityCapInputTokens int64   `json:"current_capacity_cap_input_tokens"`
	ScaleFactor                   float64 `json:"scale_factor"`
	CurrentWeight                 float64 `json:"current_weight,omitempty"`
	BaselineWeight                float64 `json:"baseline_weight,omitempty"`
	MaxConcurrentPer10000Weight   float64 `json:"max_concurrent_requests_per_10000_weight,omitempty"`
}

type LimiterModelCapacity struct {
	ScaleFactor                 float64
	CurrentWeight               float64
	BaselineWeight              float64
	MaxConcurrentPer10000Weight float64
}

func NewGatewayLimiter(maxConcurrent, maxInputTokens int64) *GatewayLimiter {
	return &GatewayLimiter{
		maxConcurrent:           maxConcurrent,
		maxInputTokens:          maxInputTokens,
		effectiveMaxConcurrent:  maxConcurrent,
		effectiveMaxInputTokens: maxInputTokens,
		currentScale:            1,
		models:                  map[string]limiterModelCounter{},
		modelLimits:             map[string]limiterModelLimits{},
	}
}

func (l *GatewayLimiter) Snapshot() LimiterSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	return LimiterSnapshot{
		InFlightRequests:        l.inFlightRequests,
		InFlightInputTokens:     l.inFlightInputToks,
		MaxConcurrent:           l.maxConcurrent,
		MaxInputTokens:          l.maxInputTokens,
		EffectiveMaxConcurrent:  l.effectiveMaxConcurrent,
		EffectiveMaxInputTokens: l.effectiveMaxInputTokens,
		ScaleFactor:             l.currentScale,
	}
}

func (l *GatewayLimiter) SnapshotWithModelScales(scales map[string]float64) LimiterSnapshot {
	capacities := make(map[string]LimiterModelCapacity, len(scales))
	for model, scale := range scales {
		capacities[model] = LimiterModelCapacity{ScaleFactor: scale}
	}
	return l.SnapshotWithModelCapacities(capacities)
}

func (l *GatewayLimiter) SnapshotWithModelCapacities(capacities map[string]LimiterModelCapacity) LimiterSnapshot {
	l.mu.Lock()
	defer l.mu.Unlock()
	snap := LimiterSnapshot{
		InFlightRequests:        l.inFlightRequests,
		InFlightInputTokens:     l.inFlightInputToks,
		MaxConcurrent:           l.maxConcurrent,
		MaxInputTokens:          l.maxInputTokens,
		EffectiveMaxConcurrent:  l.effectiveMaxConcurrent,
		EffectiveMaxInputTokens: l.effectiveMaxInputTokens,
		ScaleFactor:             l.currentScale,
	}
	if len(capacities) == 0 {
		return snap
	}
	snap.Models = make(map[string]LimiterModelSnapshot, len(capacities))
	for model, capacity := range capacities {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		counter := l.models[model]
		limits := l.limitsForModelLocked(model)
		effectiveMaxConcurrent, capacityCapRequests := l.concurrentLimitsForCapacityLocked(model, limits, capacity)
		effectiveMaxInputTokens := scaleClampLimit(limits.maxInputTokens, capacity.ScaleFactor)
		snap.Models[model] = LimiterModelSnapshot{
			InFlightRequests:              counter.inFlightRequests,
			InFlightInputTokens:           counter.inFlightInputToks,
			MaxConcurrent:                 capacityCapRequests,
			MaxInputTokens:                limits.maxInputTokens,
			EffectiveMaxConcurrent:        effectiveMaxConcurrent,
			EffectiveMaxInputTokens:       effectiveMaxInputTokens,
			CapacityCapRequests:           capacityCapRequests,
			CurrentCapacityCapRequests:    effectiveMaxConcurrent,
			CapacityCapInputTokens:        limits.maxInputTokens,
			CurrentCapacityCapInputTokens: effectiveMaxInputTokens,
			ScaleFactor:                   capacity.ScaleFactor,
			CurrentWeight:                 capacity.CurrentWeight,
			BaselineWeight:                capacity.BaselineWeight,
			MaxConcurrentPer10000Weight:   capacity.MaxConcurrentPer10000Weight,
		}
	}
	return snap
}

func (l *GatewayLimiter) HasConfiguredLimits() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.maxConcurrent > 0 || l.maxInputTokens > 0 {
		return true
	}
	for _, limits := range l.modelLimits {
		if limits.maxConcurrent > 0 || limits.maxInputTokens > 0 {
			return true
		}
	}
	return false
}

// UpdateLimits replaces the baseline caps and re-derives the
// effective caps using the currently active scale factor. The scale is
// preserved across config reloads so an operator changing the cap
// during a deep PoC scale-down doesn't inadvertently restore full
// capacity at exactly the worst moment.
func (l *GatewayLimiter) UpdateLimits(maxConcurrent, maxInputTokens int64, modelLimits ...[]GatewayModelLimitSettings) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxConcurrent = maxConcurrent
	l.maxInputTokens = maxInputTokens
	if len(modelLimits) > 0 {
		l.modelLimits = limiterModelLimitsByID(modelLimits[0])
	}
	l.effectiveMaxConcurrent = scaleClampLimit(l.maxConcurrent, l.currentScale)
	l.effectiveMaxInputTokens = scaleClampLimit(l.maxInputTokens, l.currentScale)
}

func limiterModelLimitsByID(settings []GatewayModelLimitSettings) map[string]limiterModelLimits {
	settings = normalizeGatewayModelLimits(settings)
	if len(settings) == 0 {
		return map[string]limiterModelLimits{}
	}
	limits := make(map[string]limiterModelLimits, len(settings))
	for _, setting := range settings {
		limits[setting.ModelID] = limiterModelLimits{
			maxConcurrent:  setting.MaxConcurrentRequests,
			maxInputTokens: setting.MaxInputTokensInFlight,
		}
	}
	return limits
}

// ApplyScaleFactor scales the configured baseline caps by `scale`,
// clamped to [0, 1]. A scale of 0 makes the effective caps 0,
// blocking *all* traffic -- this is the correct behavior when the
// capacity state reports W_tot == 0 (no available hosts anywhere). A
// baseline of 0 (meaning "unlimited") is preserved as-is regardless of
// scale, so an operator who chose not to cap concurrency stays
// uncapped.
//
// scale > 1 (over-provisioning) is clamped to 1 -- we never let the
// scale factor lift the gateway above the operator-configured baseline.
func (l *GatewayLimiter) ApplyScaleFactor(scale float64) {
	if scale < 0 {
		scale = 0
	}
	if scale > 1 {
		scale = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.currentScale = scale
	l.effectiveMaxConcurrent = scaleClampLimit(l.maxConcurrent, scale)
	l.effectiveMaxInputTokens = scaleClampLimit(l.maxInputTokens, scale)
}

func scaleClampLimit(base int64, scale float64) int64 {
	if base <= 0 {
		// 0 means "unlimited" in the existing API. Preserve it.
		return base
	}
	scaled := int64(float64(base)*scale + 0.5)
	if scaled < 0 {
		scaled = 0
	}
	if scaled > base {
		scaled = base
	}
	return scaled
}

func (l *GatewayLimiter) Acquire(inputTokens int64) error {
	if inputTokens <= 0 {
		inputTokens = 1
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	return l.acquireLocked("", inputTokens, LimiterModelCapacity{ScaleFactor: l.currentScale})
}

// AcquireForModel uses an independent in-flight counter for model while
// deriving that model's cap from the same operator-configured baseline.
// The supplied scale is usually W_current(model) / W_ref(model), so each
// model gets its own scaled request/token budget.
func (l *GatewayLimiter) AcquireForModel(model string, inputTokens int64, scale float64) error {
	return l.AcquireForModelWithCapacity(model, inputTokens, LimiterModelCapacity{ScaleFactor: scale})
}

func (l *GatewayLimiter) AcquireForModelWithCapacity(model string, inputTokens int64, capacity LimiterModelCapacity) error {
	if inputTokens <= 0 {
		inputTokens = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.acquireLocked(model, inputTokens, capacity)
}

func (l *GatewayLimiter) acquireLocked(model string, inputTokens int64, capacity LimiterModelCapacity) error {
	model = strings.TrimSpace(model)
	if l.models == nil {
		l.models = map[string]limiterModelCounter{}
	}
	counter := l.models[model]
	limits := l.limitsForModelLocked(model)
	effectiveMaxConcurrent, _ := l.concurrentLimitsForCapacityLocked(model, limits, capacity)
	effectiveMaxInputTokens := scaleClampLimit(limits.maxInputTokens, capacity.ScaleFactor)
	concurrentLimited := limits.maxConcurrent > 0 || dynamicConcurrencyEnabled(capacity)
	if concurrentLimited && counter.inFlightRequests+1 > effectiveMaxConcurrent {
		return fmt.Errorf("rate limit exceeded: too many concurrent requests")
	}
	if limits.maxInputTokens > 0 && counter.inFlightInputToks+inputTokens > effectiveMaxInputTokens {
		return fmt.Errorf("rate limit exceeded: too many input tokens in flight")
	}

	counter.inFlightRequests++
	counter.inFlightInputToks += inputTokens
	l.models[model] = counter
	l.inFlightRequests++
	l.inFlightInputToks += inputTokens
	return nil
}

func (l *GatewayLimiter) concurrentLimitsForCapacityLocked(model string, limits limiterModelLimits, capacity LimiterModelCapacity) (effective, baseline int64) {
	if dynamicConcurrencyEnabled(capacity) {
		baseline = weightConcurrencyLimit(capacity.BaselineWeight, capacity.MaxConcurrentPer10000Weight)
		effective = weightConcurrencyLimit(capacity.CurrentWeight, capacity.MaxConcurrentPer10000Weight)
		if effective > baseline {
			effective = baseline
		}
		return effective, baseline
	}
	baseline = limits.maxConcurrent
	effective = scaleClampLimit(limits.maxConcurrent, capacity.ScaleFactor)
	return effective, baseline
}

func dynamicConcurrencyEnabled(capacity LimiterModelCapacity) bool {
	return capacity.MaxConcurrentPer10000Weight > 0 && capacity.BaselineWeight > 0
}

func weightConcurrencyLimit(weight, per10000 float64) int64 {
	if weight <= 0 || per10000 <= 0 || math.IsNaN(weight) || math.IsInf(weight, 0) || math.IsNaN(per10000) || math.IsInf(per10000, 0) {
		return 0
	}
	limit := math.Floor(weight * per10000 / 10000)
	if limit > float64(math.MaxInt64) {
		return math.MaxInt64
	}
	return int64(limit)
}

func (l *GatewayLimiter) limitsForModelLocked(model string) limiterModelLimits {
	if l.modelLimits != nil {
		if limits, ok := l.modelLimits[strings.TrimSpace(model)]; ok {
			return limits
		}
	}
	return limiterModelLimits{maxConcurrent: l.maxConcurrent, maxInputTokens: l.maxInputTokens}
}

func (l *GatewayLimiter) Release(inputTokens int64) {
	if inputTokens <= 0 {
		inputTokens = 1
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseLocked("", inputTokens)
}

func (l *GatewayLimiter) ReleaseForModel(model string, inputTokens int64) {
	if inputTokens <= 0 {
		inputTokens = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.releaseLocked(model, inputTokens)
}

func (l *GatewayLimiter) releaseLocked(model string, inputTokens int64) {
	model = strings.TrimSpace(model)
	counter := l.models[model]
	counter.inFlightRequests--
	if counter.inFlightRequests < 0 {
		counter.inFlightRequests = 0
	}
	counter.inFlightInputToks -= inputTokens
	if counter.inFlightInputToks < 0 {
		counter.inFlightInputToks = 0
	}
	if counter.inFlightRequests == 0 && counter.inFlightInputToks == 0 {
		delete(l.models, model)
	} else {
		l.models[model] = counter
	}
	l.inFlightRequests--
	if l.inFlightRequests < 0 {
		l.inFlightRequests = 0
	}
	l.inFlightInputToks -= inputTokens
	if l.inFlightInputToks < 0 {
		l.inFlightInputToks = 0
	}
}
