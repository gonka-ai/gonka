package main

import (
	"context"

	"devshard/logging"
	"devshard/observability"
)

func ensureRequestLogContext(ctx context.Context) (context.Context, string) {
	return logging.WithRequestID(ctx)
}

// bindGatewayRequestSpan starts the gateway.request root span. Call at the
// HTTP entry point together with ensureRequestLogContext so request_id and the
// span are born on the same context. The returned end func must be deferred.
func bindGatewayRequestSpan(ctx context.Context) (context.Context, func()) {
	ctx, span := observability.StartGatewayRequest(ctx)
	return ctx, func() {
		if span != nil {
			span.End()
		}
	}
}

func requestLogFromContext(ctx context.Context) (string, bool) {
	return logging.RequestID(ctx)
}

func logRequestStage(ctx context.Context, stage string, kv ...any) {
	logging.Stage(ctx, stage, kv...)
}

func logInferenceStage(ctx context.Context, escrowID string, nonce uint64, stage string, kv ...any) {
	fields := make([]any, 0, 4+len(kv))
	fields = append(fields, "escrow", escrowID, "nonce", nonce)
	fields = append(fields, kv...)
	logging.Stage(ctx, stage, fields...)
}
