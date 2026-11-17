package accounting

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/trace"
)

// dispositionQueueSize bounds the hand-off between the recording goroutine and
// the sink. Recording never blocks on a slow sink: a full queue drops the event
// and increments the drop counter instead.
const dispositionQueueSize = 1024

// TraceRef is a dependency-light snapshot of an OTel span context stored on
// live nonce state and copied onto DispositionEvent at finalize time.
type TraceRef struct {
	TraceID [16]byte
	SpanID  [8]byte
	Sampled bool
}

// DispositionEvent is emitted exactly once when a nonce leaves Live (or when a
// protocol_only nonce is counted). Delivered on the tracker's own goroutine, so
// a sink never runs on the sequencer's diff-commit path.
type DispositionEvent struct {
	EscrowID    string
	Nonce       uint64
	Key         CounterKey
	Trace       TraceRef
	SendAt      time.Time
	ObservedAt  time.Time
	Participant string
	Model       string
}

// DispositionSink receives terminal disposition events. Implementations must
// tolerate being called from a single background goroutine.
type DispositionSink interface {
	OnDisposition(DispositionEvent)
}

// sinkHolder lets the sink be swapped through an atomic pointer, so the
// delivery goroutine never has to take Tracker.mu to read it.
type sinkHolder struct{ sink DispositionSink }

// dispositionItem is either an event to deliver, a barrier to signal, or the
// stop sentinel that retires the delivery goroutine.
type dispositionItem struct {
	event   DispositionEvent
	barrier chan struct{}
	stop    bool
}

// TraceRefFromContext extracts a TraceRef from ctx. Invalid/missing span
// contexts yield a zero TraceRef.
func TraceRefFromContext(ctx context.Context) TraceRef {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return TraceRef{}
	}
	return TraceRef{
		TraceID: [16]byte(sc.TraceID()),
		SpanID:  [8]byte(sc.SpanID()),
		Sampled: sc.IsSampled(),
	}
}

// IsZero reports whether TraceID is unset (orphans / protocol_only).
func (r TraceRef) IsZero() bool {
	var zero [16]byte
	return r.TraceID == zero
}

// TraceIDString returns the hex trace id, or "" when zero.
func (r TraceRef) TraceIDString() string {
	if r.IsZero() {
		return ""
	}
	return trace.TraceID(r.TraceID).String()
}

func (s *nonceState) captureTrace(ref TraceRef) {
	if s == nil || ref.IsZero() {
		return
	}
	var zero [16]byte
	if s.TraceID != zero {
		return
	}
	s.TraceID = ref.TraceID
	s.SpanID = ref.SpanID
	s.Sampled = ref.Sampled
}

func (s *nonceState) traceRef() TraceRef {
	if s == nil {
		return TraceRef{}
	}
	return TraceRef{TraceID: s.TraceID, SpanID: s.SpanID, Sampled: s.Sampled}
}
