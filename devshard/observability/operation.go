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

// Operation owns a span plus the metric attribute set used to publish
// duration/error/active counters when Finish is called. Methods are nil-safe
// so call sites can skip defensive nil checks.
type Operation struct {
	ctx         context.Context
	span        trace.Span
	start       time.Time
	metricAttrs []attribute.KeyValue
}

// StartOperation begins a span and records the started-operation tick.
func StartOperation(
	ctx context.Context,
	tracer tracerID,
	name spanID,
	kind trace.SpanKind,
	spanAttrs []attribute.KeyValue,
	metricAttrs []attribute.KeyValue,
) (context.Context, *Operation) {
	ctx, span := otel.Tracer(string(tracer)).Start(
		ctx, string(name),
		trace.WithSpanKind(kind),
		trace.WithAttributes(spanAttrs...),
	)
	attrs := withOperation(metricAttrs, string(name))
	recordOperationStarted(attrs)
	return ctx, &Operation{
		ctx:         ctx,
		span:        span,
		start:       time.Now(),
		metricAttrs: attrs,
	}
}

// Context returns the span-aware context.
func (o *Operation) Context() context.Context {
	if o == nil {
		return context.Background()
	}
	return o.ctx
}

// Span returns the underlying span (no-op when Operation is nil).
func (o *Operation) Span() trace.Span {
	if o == nil {
		return trace.SpanFromContext(context.Background())
	}
	return o.span
}

// SetAttributes attaches attributes to the span.
func (o *Operation) SetAttributes(attrs ...attribute.KeyValue) {
	if o == nil || len(attrs) == 0 {
		return
	}
	o.span.SetAttributes(attrs...)
}

// AddEvent appends a span event.
func (o *Operation) AddEvent(name string, attrs ...attribute.KeyValue) {
	if o == nil {
		return
	}
	o.span.AddEvent(name, trace.WithAttributes(attrs...))
}

// Finish closes the span and publishes duration/error metrics.
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
	recordOperationFinished(o.metricAttrs, o.start, err)
	o.span.End()
}

// FinishErr is the deferred-named-error variant of Finish.
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

// InjectRequestContext writes trace context into outgoing HTTP headers.
func InjectRequestContext(ctx context.Context, headers http.Header) {
	otel.GetTextMapPropagator().Inject(ctx, propagation.HeaderCarrier(headers))
}

func withOperation(attrs []attribute.KeyValue, op string) []attribute.KeyValue {
	out := make([]attribute.KeyValue, 0, len(attrs)+1)
	out = append(out, attribute.String("operation", op))
	out = append(out, attrs...)
	return out
}
