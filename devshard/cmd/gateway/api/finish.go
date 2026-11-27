package api

import (
	"context"
	"time"

	"go.opentelemetry.io/otel/trace"

	"devshard/cmd/gateway/engine"
	"devshard/cmd/gateway/filters"
	"devshard/cmd/gateway/journal"
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

func traceIDOf(ctx context.Context) string {
	if spanContext := trace.SpanContextFromContext(ctx); spanContext.IsValid() {
		return spanContext.TraceID().String()
	}
	return journal.NoTraceID
}
