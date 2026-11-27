package api

import (
	"context"
	"time"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/journal"

	"go.opentelemetry.io/otel/trace"
)

// finishRequest hands the journal what a finished request can no longer be asked. See README.md, "What a finished request records".
func (s *Server) finishRequest(ctx context.Context, requestID, apiKeyID string, normalized filters.Result, outcome engine.RaceOutcome, verdict string, stream *clientStream, elapsed time.Duration, raceErr, deliverErr error) {
	written, terminated := stream.delivered()
	s.events.RequestFinished(journal.RequestLine{
		RequestID:    requestID,
		TraceID:      traceIDOf(ctx),
		Model:        normalized.Model,
		APIKeyID:     apiKeyID,
		EscrowID:     outcome.EscrowID,
		ClientStream: normalized.ClientStream,
		Outcome:      outcome,
		Verdict:      verdict,
		Bytes:        written,
		Terminated:   terminated,
		Elapsed:      elapsed,
		RaceErr:      raceErr,
		DeliverErr:   deliverErr,
	})
}

func outputTokenBudget(normalized filters.Result) uint64 {
	if normalized.MaxTokens > 0 {
		return normalized.MaxTokens
	}
	return normalized.MaxCompletionTokens
}

// traceIDOf is the request span's trace id, or empty while tracing is off.
func traceIDOf(ctx context.Context) string {
	if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
		return spanContext.TraceID().String()
	}
	return ""
}
