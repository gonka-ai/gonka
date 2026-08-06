package observability

import (
	"context"
	"time"

	"devshard/accounting"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// AttemptIdentity is the bounded set of attributes attached when a
// gateway.attempt span opens. Role/start-reason may be filled later via
// SetAttemptRoleReason once the caller assigns them.
type AttemptIdentity struct {
	Nonce          uint64
	EscrowID       string
	SlotID         uint32
	ParticipantKey string
	HostID         string
	Model          string
	QuarantineMode string
	Role           string
	StartReason    string
	AttemptIndex   int
	TriggerNonce   uint64 // 0 for primary
}

// StartGatewayAttempt opens a child span under the active gateway.request.
// Returns a no-op span when tracing is disabled / unsampled context.
func StartGatewayAttempt(ctx context.Context, a AttemptIdentity) (context.Context, trace.Span) {
	attrs := []attribute.KeyValue{
		AttrNonce.Int64(int64(a.Nonce)),
		AttrSlotID.Int(int(a.SlotID)),
		AttrAttemptIndex.Int(a.AttemptIndex),
	}
	if a.EscrowID != "" {
		attrs = append(attrs, AttrEscrowID.String(a.EscrowID))
	}
	if a.ParticipantKey != "" {
		attrs = append(attrs, AttrParticipantKey.String(a.ParticipantKey))
	}
	if a.HostID != "" {
		attrs = append(attrs, AttrHostID.String(a.HostID))
	}
	if a.Model != "" {
		attrs = append(attrs, AttrModel.String(a.Model))
	}
	if a.QuarantineMode != "" {
		attrs = append(attrs, AttrQuarantineMode.String(a.QuarantineMode))
	}
	if a.Role != "" {
		attrs = append(attrs, AttrAttemptRole.String(a.Role))
	}
	if a.StartReason != "" {
		attrs = append(attrs, AttrAttemptStartReason.String(a.StartReason))
	}
	if a.TriggerNonce != 0 {
		attrs = append(attrs, AttrAttemptTriggerNonce.Int64(int64(a.TriggerNonce)))
	}
	return otel.Tracer(string(gatewayTracer)).Start(
		ctx,
		SpanNameGatewayAttempt,
		trace.WithSpanKind(trace.SpanKindInternal),
		trace.WithAttributes(attrs...),
	)
}

// SetAttemptRoleReason stamps role / start_reason / index / trigger after the
// caller assigns them (prepareInflight opens the span before role is known).
func SetAttemptRoleReason(span trace.Span, role, startReason string, attemptIndex int, triggerNonce uint64) {
	if span == nil || !span.IsRecording() {
		return
	}
	attrs := []attribute.KeyValue{
		AttrAttemptIndex.Int(attemptIndex),
	}
	if role != "" {
		attrs = append(attrs, AttrAttemptRole.String(role))
	}
	if startReason != "" {
		attrs = append(attrs, AttrAttemptStartReason.String(startReason))
	}
	if triggerNonce != 0 {
		attrs = append(attrs, AttrAttemptTriggerNonce.Int64(int64(triggerNonce)))
	}
	span.SetAttributes(attrs...)
}

// SetAttemptCounterKeyAttrs stamps taxonomy attributes from an accounting
// CounterKey. Values come from CounterKeyAttrs so span attrs stay byte-identical
// to Prometheus labels (C4).
func SetAttemptCounterKeyAttrs(span trace.Span, key accounting.CounterKey) {
	if span == nil || !span.IsRecording() {
		return
	}
	attrs := CounterKeyAttrs(key)
	if len(attrs) == 0 {
		return
	}
	span.SetAttributes(attrs...)
}

// EndSpan is a nil-safe End for attempt / phase spans.
func EndSpan(span trace.Span, opts ...trace.SpanEndOption) {
	if span == nil {
		return
	}
	span.End(opts...)
}

// StartAttemptDispatch opens attempt.dispatch at sendTime.
func StartAttemptDispatch(ctx context.Context, at time.Time) (context.Context, trace.Span) {
	return startPhaseSpan(ctx, SpanNameAttemptDispatch, at)
}

// StartAttemptPrefill opens attempt.prefill at receipt time.
func StartAttemptPrefill(ctx context.Context, at time.Time) (context.Context, trace.Span) {
	return startPhaseSpan(ctx, SpanNameAttemptPrefill, at)
}

// StartAttemptStream opens attempt.stream at first-token time.
func StartAttemptStream(ctx context.Context, at time.Time) (context.Context, trace.Span) {
	return startPhaseSpan(ctx, SpanNameAttemptStream, at)
}

func startPhaseSpan(ctx context.Context, name string, at time.Time) (context.Context, trace.Span) {
	opts := []trace.SpanStartOption{
		trace.WithSpanKind(trace.SpanKindInternal),
	}
	if !at.IsZero() {
		opts = append(opts, trace.WithTimestamp(at))
	}
	return otel.Tracer(string(gatewayTracer)).Start(ctx, name, opts...)
}

// FinishAttemptStream stamps end-of-stream counters and ends the stream span.
func FinishAttemptStream(span trace.Span, at time.Time, outputChunks, contentChunks, outputBytes, stallCount int64) {
	if span == nil {
		return
	}
	if span.IsRecording() {
		span.SetAttributes(
			AttrOutputChunks.Int64(outputChunks),
			AttrContentChunks.Int64(contentChunks),
			AttrOutputBytes.Int64(outputBytes),
			AttrStallCount.Int64(stallCount),
		)
	}
	if at.IsZero() {
		span.End()
		return
	}
	span.End(trace.WithTimestamp(at))
}

const (
	EventStallDetected  = "stream.stall.detected"
	EventStallRecovered = "stream.stall.recovered"

	EventPickerExhausted        = "attempt.picker_exhausted"
	EventSecondaryPrepareFailed = "attempt.secondary_prepare_failed"
	EventEscalationSkipped      = "attempt.escalation_skipped"
)

// AddStallDetected records stream.stall.detected on the attempt (or stream) span.
func AddStallDetected(span trace.Span, at time.Time, outputChunks, contentChunks, outputBytes int64) {
	addSpanEvent(span, EventStallDetected, at,
		AttrOutputChunks.Int64(outputChunks),
		AttrContentChunks.Int64(contentChunks),
		AttrOutputBytes.Int64(outputBytes),
	)
}

// AddStallRecovered records stream.stall.recovered.
func AddStallRecovered(span trace.Span, at time.Time) {
	addSpanEvent(span, EventStallRecovered, at)
}

// AddPickerExhausted records why a split could not schedule another attempt.
// The counts behind the decision stay in the adjacent log line, which shares
// this span's trace id; the event exists so the no-split path is visible in
// the trace at all.
func AddPickerExhausted(ctx context.Context, reason string) {
	addParentEvent(ctx, EventPickerExhausted, AttrAttemptStartReason.String(reason))
}

// AddSecondaryPrepareFailed records a non-exhaustion prepare failure.
func AddSecondaryPrepareFailed(ctx context.Context, reason string) {
	addParentEvent(ctx, EventSecondaryPrepareFailed, AttrAttemptStartReason.String(reason))
}

// AddEscalationSkipped records that a split was considered but not taken.
func AddEscalationSkipped(ctx context.Context, reason string) {
	addParentEvent(ctx, EventEscalationSkipped, AttrAttemptStartReason.String(reason))
}

func addSpanEvent(span trace.Span, name string, at time.Time, attrs ...attribute.KeyValue) {
	if span == nil || !span.IsRecording() {
		return
	}
	opts := []trace.EventOption{trace.WithAttributes(attrs...)}
	if !at.IsZero() {
		opts = append(opts, trace.WithTimestamp(at))
	}
	span.AddEvent(name, opts...)
}

func addParentEvent(ctx context.Context, name string, attrs ...attribute.KeyValue) {
	span := trace.SpanFromContext(ctx)
	if span == nil || !span.IsRecording() {
		return
	}
	span.AddEvent(name, trace.WithAttributes(attrs...))
}
