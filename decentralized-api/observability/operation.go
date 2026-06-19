package observability

import (
	"context"
	"net/http"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// Operation is the unit of instrumentation. It owns the OTel span and the
// metric attribute set so callers can finish both with a single Finish() call.
//
// Methods on a nil receiver are valid no-ops; this lets call sites skip
// defensive nil checks when an Operation is optional.
type Operation struct {
	ctx         context.Context
	span        trace.Span
	name        string
	start       time.Time
	metricAttrs []attribute.KeyValue
}

// StartOperation begins a new span and registers a started-operation tick on
// both Prometheus and OTel. The returned context carries the span; pass it to
// downstream calls so child spans link correctly.
func StartOperation(
	ctx context.Context,
	tracer tracerID,
	name spanID,
	kind trace.SpanKind,
	spanAttrs []attribute.KeyValue,
	metricAttrs []attribute.KeyValue,
) (context.Context, *Operation) {
	initInstruments()
	ctx, span := otel.Tracer(string(tracer)).Start(
		ctx, string(name),
		trace.WithSpanKind(kind),
		trace.WithAttributes(spanAttrs...),
	)
	attrs := withOperation(metricAttrs, string(name))
	recordOTelOperationStarted(ctx, attrs)
	recordPrometheusOperationStarted(attrs)
	op := &Operation{
		ctx:         ctx,
		span:        span,
		name:        string(name),
		start:       time.Now(),
		metricAttrs: attrs,
	}
	ctx = context.WithValue(ctx, operationContextKey{}, op)
	op.ctx = ctx
	return ctx, op
}

// operationContextKey is a private type used as the context key for the
// currently-active Operation. Private so external code can't poke into the
// context without going through OperationFromContext.
type operationContextKey struct{}

// OperationFromContext returns the active Operation stored in ctx, or nil.
// All Operation methods are nil-safe so callers don't need to check.
func OperationFromContext(ctx context.Context) *Operation {
	if ctx == nil {
		return nil
	}
	op, _ := ctx.Value(operationContextKey{}).(*Operation)
	return op
}

// Context returns the span-aware context. Returns context.Background() when
// the operation is nil.
func (o *Operation) Context() context.Context {
	if o == nil {
		return context.Background()
	}
	return o.ctx
}

// Span returns the underlying OTel span, or a no-op span on nil.
func (o *Operation) Span() trace.Span {
	if o == nil {
		return trace.SpanFromContext(context.Background())
	}
	return o.span
}

// AddEvent records a span event. Useful for sub-step markers (e.g. retries)
// that aren't worth their own span.
func (o *Operation) AddEvent(name string, attrs ...attribute.KeyValue) {
	if o == nil {
		return
	}
	o.span.AddEvent(name, trace.WithAttributes(attrs...))
}

// SetAttributes attaches attributes to the underlying span.
func (o *Operation) SetAttributes(attrs ...attribute.KeyValue) {
	if o == nil || len(attrs) == 0 {
		return
	}
	o.span.SetAttributes(attrs...)
}

// RecordTokens publishes prompt/completion/total token counts to both metric
// pipelines and the span. The extra attrs argument lets callers add per-call
// dimensions (e.g. an explicit model attribute on transfer paths).
func (o *Operation) RecordTokens(prompt, completion uint64, attrs ...attribute.KeyValue) {
	if o == nil {
		return
	}
	merged := append(append([]attribute.KeyValue{}, o.metricAttrs...), attrs...)
	recordOTelOperationTokens(o.ctx, merged, prompt, completion)
	recordPrometheusOperationTokens(merged, prompt, completion)
	o.span.SetAttributes(
		attribute.Int64("inference.tokens.prompt", int64(prompt)),
		attribute.Int64("inference.tokens.completion", int64(completion)),
		attribute.Int64("inference.tokens.total", int64(prompt+completion)),
	)
}

// Finish closes the span and records duration/error metrics. Idempotent
// callers should not call Finish twice on the same Operation.
func (o *Operation) Finish(err error, attrs ...attribute.KeyValue) {
	if o == nil {
		return
	}
	if len(attrs) > 0 {
		o.span.SetAttributes(attrs...)
	}
	if err != nil {
		o.span.RecordError(err)
		o.span.SetStatus(codes.Error, err.Error())
	} else {
		o.span.SetStatus(codes.Ok, "")
	}
	recordOTelOperationFinished(o.ctx, o.metricAttrs, o.start, err)
	recordPrometheusOperationFinished(o.metricAttrs, o.start, err)
	o.span.End()
}

// FinishErr is a convenience for `defer op.FinishErr(&err)` patterns combined
// with Go's named-return error: the dereferenced error is captured at the time
// the deferred call runs, so any subsequent assignment to err is recorded.
func (o *Operation) FinishErr(err *error, attrs ...attribute.KeyValue) {
	if err == nil {
		o.Finish(nil, attrs...)
		return
	}
	o.Finish(*err, attrs...)
}

// ExtractRequestContext lifts trace context from incoming HTTP headers.
func ExtractRequestContext(ctx context.Context, headers http.Header) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(headers))
}

// InjectRequestContext writes the active span's W3C trace context into outgoing
// HTTP headers, allowing downstream services to continue the trace.
func InjectRequestContext(ctx context.Context, headers http.Header) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(headers))
}

// withOperation prepends a normalized "operation" label so every metric series
// is partitioned by the span name even when the caller forgot to pass it.
func withOperation(attrs []attribute.KeyValue, op string) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(attrs)+1)
	out = append(out, attribute.String("operation", op))
	out = append(out, attrs...)
	return out
}
