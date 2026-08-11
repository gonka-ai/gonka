package observability

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// Operation owns an OTel span. Methods are nil-safe.
type Operation struct {
	ctx   context.Context
	span  trace.Span
	start time.Time
}

// StartOperation begins a span and returns a context that carries it.
func StartOperation(
	ctx context.Context,
	tracer tracerID,
	name spanID,
	kind trace.SpanKind,
	attrs []attribute.KeyValue,
) (context.Context, *Operation) {
	ctx, span := otel.Tracer(string(tracer)).Start(
		ctx, string(name),
		trace.WithSpanKind(kind),
		trace.WithAttributes(attrs...),
	)
	return ctx, &Operation{
		ctx:   ctx,
		span:  span,
		start: time.Now(),
	}
}

// Context returns the span-aware context.
func (o *Operation) Context() context.Context {
	if o == nil {
		return context.Background()
	}
	return o.ctx
}

// SetAttributes attaches attributes to the span.
func (o *Operation) SetAttributes(attrs ...attribute.KeyValue) {
	if o == nil || len(attrs) == 0 {
		return
	}
	o.span.SetAttributes(attrs...)
}

// Finish closes the span.
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
	o.span.End()
	_ = o.start
}

// FinishErr supports defer op.FinishErr(&err).
func (o *Operation) FinishErr(err *error, attrs ...attribute.KeyValue) {
	if err == nil {
		o.Finish(nil, attrs...)
		return
	}
	o.Finish(*err, attrs...)
}
