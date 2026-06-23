package observability

import (
	"context"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

const meterName = "decentralized-api/observability"

// otelInstruments groups every OTel instrument managed by this package so we
// can rebind them atomically when the global MeterProvider is replaced (which
// happens once during Init).
type otelInstruments struct {
	provider          metric.MeterProvider
	activeOperations  metric.Int64UpDownCounter
	operationDuration metric.Float64Histogram
	operationErrors   metric.Int64Counter
	promptTokens      metric.Int64Histogram
	completionTokens  metric.Int64Histogram
	totalTokens       metric.Int64Histogram
}

var (
	otelMu          sync.Mutex
	otelInstrument  otelInstruments
	otelInitialized bool
)

// initInstruments rebinds OTel instruments against the current global
// MeterProvider. Safe to call from any code path: it short-circuits when the
// provider hasn't changed since the last bind.
func initInstruments() {
	provider := otel.GetMeterProvider()

	otelMu.Lock()
	defer otelMu.Unlock()

	if otelInitialized && otelInstrument.provider == provider {
		return
	}

	meter := provider.Meter(meterName)
	next := otelInstruments{provider: provider}

	var err error
	if next.activeOperations, err = meter.Int64UpDownCounter(
		"decentralized_api.inference.active_operations",
		metric.WithDescription("In-flight decentralized-api inference operations."),
	); err != nil {
		logObservabilityError("metrics.init_active_operations_failed", "Failed to init active operations metric", err)
		return
	}
	if next.operationDuration, err = meter.Float64Histogram(
		"decentralized_api.inference.operation.duration_seconds",
		metric.WithDescription("Duration of decentralized-api inference operations."),
		metric.WithUnit("s"),
	); err != nil {
		logObservabilityError("metrics.init_duration_failed", "Failed to init duration metric", err)
		return
	}
	if next.operationErrors, err = meter.Int64Counter(
		"decentralized_api.inference.operation.errors",
		metric.WithDescription("Decentralized-api inference operations that ended with an error."),
	); err != nil {
		logObservabilityError("metrics.init_errors_failed", "Failed to init errors metric", err)
		return
	}
	if next.promptTokens, err = meter.Int64Histogram(
		"decentralized_api.inference.prompt_tokens",
		metric.WithDescription("Prompt tokens recorded by inference operations."),
	); err != nil {
		logObservabilityError("metrics.init_prompt_tokens_failed", "Failed to init prompt tokens metric", err)
		return
	}
	if next.completionTokens, err = meter.Int64Histogram(
		"decentralized_api.inference.completion_tokens",
		metric.WithDescription("Completion tokens recorded by inference operations."),
	); err != nil {
		logObservabilityError("metrics.init_completion_tokens_failed", "Failed to init completion tokens metric", err)
		return
	}
	if next.totalTokens, err = meter.Int64Histogram(
		"decentralized_api.inference.total_tokens",
		metric.WithDescription("Total tokens (prompt + completion) recorded by inference operations."),
	); err != nil {
		logObservabilityError("metrics.init_total_tokens_failed", "Failed to init total tokens metric", err)
		return
	}

	otelInstrument = next
	otelInitialized = true
}

// snapshotInstruments returns a value copy under the lock so callers can
// release the lock before recording (avoiding holding it across the SDK call).
func snapshotInstruments() (otelInstruments, bool) {
	otelMu.Lock()
	defer otelMu.Unlock()
	return otelInstrument, otelInitialized
}

func recordOTelOperationStarted(ctx context.Context, attrs []attribute.KeyValue) {
	initInstruments()
	inst, ok := snapshotInstruments()
	if !ok {
		return
	}
	inst.activeOperations.Add(ctx, 1, metric.WithAttributes(attrs...))
}

func recordOTelOperationTokens(ctx context.Context, attrs []attribute.KeyValue, prompt, completion uint64) {
	initInstruments()
	inst, ok := snapshotInstruments()
	if !ok {
		return
	}
	if prompt > 0 {
		inst.promptTokens.Record(ctx, int64(prompt), metric.WithAttributes(attrs...))
	}
	if completion > 0 {
		inst.completionTokens.Record(ctx, int64(completion), metric.WithAttributes(attrs...))
	}
	if total := prompt + completion; total > 0 {
		inst.totalTokens.Record(ctx, int64(total), metric.WithAttributes(attrs...))
	}
}

func recordOTelOperationFinished(ctx context.Context, attrs []attribute.KeyValue, startedAt time.Time, err error) {
	initInstruments()
	inst, ok := snapshotInstruments()
	if !ok {
		return
	}
	if err != nil {
		inst.operationErrors.Add(ctx, 1, metric.WithAttributes(attrs...))
	}
	inst.operationDuration.Record(ctx, time.Since(startedAt).Seconds(), metric.WithAttributes(attrs...))
	inst.activeOperations.Add(ctx, -1, metric.WithAttributes(attrs...))
}
