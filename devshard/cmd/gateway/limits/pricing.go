package limits

import (
	"math"
	"time"

	"devshard/cmd/gateway/internal/safemath"
)

const initialInputWindowFloorMultiple = 2

// WindowBounds is one congestion window's floor, starting size and additive step, in tokens.
type WindowBounds struct {
	Min     int64
	Initial int64
	Step    int64
}

// ModelWindows is one model's two congestion windows.
type ModelWindows struct {
	Input  WindowBounds
	Output WindowBounds
}

// RequestBounds is one congestion window's floor and starting size, counted in requests.
type RequestBounds struct {
	Min     int64
	Initial int64
}

// WindowPricing turns a model into the two windows a host gets for it. See capacity.md, "The participant limiter: IOCW".
type WindowPricing struct {
	Input                     RequestBounds
	Output                    RequestBounds
	ConcurrencyPer10000Weight float64
	FallbackContextTokens     int64
	FallbackOutputTokens      int64
	ContextTokensByModel      map[string]int64
	OutputTokensByModel       map[string]int64
}

// ParticipantConfig's IdleEviction of zero keeps every pair for the life of the process.
type ParticipantConfig struct {
	Pricing       WindowPricing
	Factors       CongestionFactors
	Slack         float64
	AfterFailures int64
	BaseOpen      time.Duration
	MaxOpen       time.Duration
	IdleEviction  time.Duration
}

func boundsIn(requests RequestBounds, requestTokens int64) WindowBounds {
	return WindowBounds{
		Min:     safemath.MulSaturating(requests.Min, requestTokens),
		Initial: safemath.MulSaturating(requests.Initial, requestTokens),
		Step:    requestTokens,
	}
}

func namedOr(named map[string]int64, model string, fallback int64) int64 {
	if tokens, present := named[model]; present && tokens > 0 {
		return tokens
	}
	return fallback
}

// windowsForLocked prices one host's model.
func (l *ParticipantLimiter) windowsForLocked(participant, model string) ModelWindows {
	pricing := l.cfg.Pricing
	contextTokens := namedOr(l.reportedContext, model, namedOr(pricing.ContextTokensByModel, model, namedOr(l.observedContext, model, pricing.FallbackContextTokens)))
	outputTokens := namedOr(pricing.OutputTokensByModel, model, pricing.FallbackOutputTokens)
	concurrency := earnedConcurrency(pricing.ConcurrencyPer10000Weight, l.observedWeights[model][participant])
	if concurrency == 0 {
		return ModelWindows{
			Input:  boundsIn(pricing.Input, contextTokens),
			Output: boundsIn(pricing.Output, outputTokens),
		}
	}
	inputFloor := safemath.MulSaturating(concurrency, contextTokens)
	outputFloor := safemath.MulSaturating(concurrency, outputTokens)
	return ModelWindows{
		Input:  WindowBounds{Min: inputFloor, Initial: safemath.MulSaturating(inputFloor, initialInputWindowFloorMultiple), Step: contextTokens},
		Output: WindowBounds{Min: outputFloor, Initial: outputFloor, Step: outputTokens},
	}
}

// earnedConcurrency is how many requests at full price the chain's weight buys a host, never fewer than one.
func earnedConcurrency(per10000, weight float64) int64 {
	if per10000 <= 0 || weight <= 0 {
		return 0
	}
	earned := math.Round(weight * per10000 / concurrencyWeightBase)
	if earned >= math.MaxInt64 {
		return math.MaxInt64
	}
	return max(int64(earned), 1)
}
