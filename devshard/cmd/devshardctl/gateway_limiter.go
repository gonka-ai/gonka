package main

import (
	"fmt"
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
}

type limiterModelCounter struct {
	inFlightRequests  int64
	inFlightInputToks int64
}

type LimiterSnapshot struct {
	InFlightRequests        int64   `json:"in_flight_requests"`
	InFlightInputTokens     int64   `json:"in_flight_input_tokens"`
	MaxConcurrent           int64   `json:"max_concurrent_requests"`
	MaxInputTokens          int64   `json:"max_input_tokens_in_flight"`
	EffectiveMaxConcurrent  int64   `json:"effective_max_concurrent_requests"`
	EffectiveMaxInputTokens int64   `json:"effective_max_input_tokens_in_flight"`
	ScaleFactor             float64 `json:"scale_factor"`
}

func NewGatewayLimiter(maxConcurrent, maxInputTokens int64) *GatewayLimiter {
	return &GatewayLimiter{
		maxConcurrent:           maxConcurrent,
		maxInputTokens:          maxInputTokens,
		effectiveMaxConcurrent:  maxConcurrent,
		effectiveMaxInputTokens: maxInputTokens,
		currentScale:            1,
		models:                  map[string]limiterModelCounter{},
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

func (l *GatewayLimiter) HasConfiguredLimits() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.maxConcurrent > 0 || l.maxInputTokens > 0
}

// UpdateLimits replaces the baseline caps and re-derives the
// effective caps using the currently active scale factor. The scale is
// preserved across config reloads so an operator changing the cap
// during a deep PoC scale-down doesn't inadvertently restore full
// capacity at exactly the worst moment.
func (l *GatewayLimiter) UpdateLimits(maxConcurrent, maxInputTokens int64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.maxConcurrent = maxConcurrent
	l.maxInputTokens = maxInputTokens
	l.effectiveMaxConcurrent = scaleClampLimit(l.maxConcurrent, l.currentScale)
	l.effectiveMaxInputTokens = scaleClampLimit(l.maxInputTokens, l.currentScale)
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
	return l.acquireLocked("", inputTokens, l.currentScale)
}

// AcquireForModel uses an independent in-flight counter for model while
// deriving that model's cap from the same operator-configured global
// limits. The supplied scale is usually W_current(model) / W_ref(all
// models), so models share the configured gateway limit by available
// capacity.
func (l *GatewayLimiter) AcquireForModel(model string, inputTokens int64, scale float64) error {
	if inputTokens <= 0 {
		inputTokens = 1
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.acquireLocked(model, inputTokens, scale)
}

func (l *GatewayLimiter) acquireLocked(model string, inputTokens int64, scale float64) error {
	model = strings.TrimSpace(model)
	if l.models == nil {
		l.models = map[string]limiterModelCounter{}
	}
	counter := l.models[model]
	effectiveMaxConcurrent := scaleClampLimit(l.maxConcurrent, scale)
	effectiveMaxInputTokens := scaleClampLimit(l.maxInputTokens, scale)
	if l.maxConcurrent > 0 && counter.inFlightRequests+1 > effectiveMaxConcurrent {
		return fmt.Errorf("rate limit exceeded: too many concurrent requests")
	}
	if l.maxInputTokens > 0 && counter.inFlightInputToks+inputTokens > effectiveMaxInputTokens {
		return fmt.Errorf("rate limit exceeded: too many input tokens in flight")
	}

	counter.inFlightRequests++
	counter.inFlightInputToks += inputTokens
	l.models[model] = counter
	l.inFlightRequests++
	l.inFlightInputToks += inputTokens
	return nil
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
