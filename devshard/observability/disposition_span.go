package observability

import (
	"context"
	"time"

	"devshard/accounting"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// DispositionReparentWindow is Tempo's max_trace_idle. A verdict observed
// within it can still join the originating trace as a child; past it the trace
// has been cut into a block, so a late child would be stitched into nothing and
// the verdict becomes a root span linked back to the attempt instead.
const DispositionReparentWindow = 10 * time.Second

// DispositionLag is how long after dispatch the verdict was observed. Zero when
// either timestamp is unset — a ghost was never sent, and a protocol_only nonce
// has no live state at all.
func DispositionLag(ev accounting.DispositionEvent) time.Duration {
	if ev.SendAt.IsZero() || ev.ObservedAt.IsZero() {
		return 0
	}
	return ev.ObservedAt.Sub(ev.SendAt)
}

// EmitDispositionSpan records a terminal accounting verdict as its own span,
// carrying the whole CounterKey so TraceQL can find it without a cross-span
// join. Nonces whose request went untraced still produce an unparented span:
// the missing origin is the signal that the verdict is an orphan.
func EmitDispositionSpan(ev accounting.DispositionEvent) {
	origin, hasOrigin := DispositionOrigin(ev.Trace)
	// Minting a span for an unsampled request would leave a single-span trace
	// that no request trace exists to link back to.
	if hasOrigin && !ev.Trace.Sampled {
		return
	}

	ctx := context.Background()
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(dispositionSpanAttrs(ev)...),
	}
	switch {
	case !hasOrigin:
		// Orphan: root span, nothing to link to.
	case DispositionLag(ev) < DispositionReparentWindow:
		ctx = trace.ContextWithRemoteSpanContext(ctx, origin)
	default:
		opts = append(opts, trace.WithLinks(trace.Link{SpanContext: origin}))
	}
	if !ev.ObservedAt.IsZero() {
		opts = append(opts, trace.WithTimestamp(ev.ObservedAt))
	}

	_, span := otel.Tracer(string(gatewayTracer)).Start(ctx, SpanNameNonceDisposition, opts...)
	if ev.ObservedAt.IsZero() {
		span.End()
		return
	}
	span.End(trace.WithTimestamp(ev.ObservedAt))
}

// DispositionOrigin rebuilds the span context of the request a nonce belonged
// to. Reports false for orphans, which carry no trace at all.
func DispositionOrigin(ref accounting.TraceRef) (trace.SpanContext, bool) {
	if ref.IsZero() {
		return trace.SpanContext{}, false
	}
	flags := trace.TraceFlags(0)
	if ref.Sampled {
		flags = trace.FlagsSampled
	}
	sc := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    ref.TraceID,
		SpanID:     ref.SpanID,
		TraceFlags: flags,
		Remote:     true,
	})
	if !sc.IsValid() {
		return trace.SpanContext{}, false
	}
	return sc, true
}

func dispositionSpanAttrs(ev accounting.DispositionEvent) []attribute.KeyValue {
	attrs := append(CounterKeyAttrs(ev.Key), AttrNonce.Int64(int64(ev.Nonce)))
	if ev.EscrowID != "" {
		attrs = append(attrs, AttrEscrowID.String(ev.EscrowID))
	}
	if ev.Participant != "" {
		attrs = append(attrs, AttrParticipantKey.String(ev.Participant))
	}
	if ev.Model != "" {
		attrs = append(attrs, AttrModel.String(ev.Model))
	}
	if id := ev.Trace.TraceIDString(); id != "" {
		attrs = append(attrs, AttrOriginTraceID.String(id))
	}
	return attrs
}
