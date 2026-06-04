package main

import (
	"context"

	"devshard/logging"
)

func ensureRequestLogContext(ctx context.Context) (context.Context, string) {
	return logging.WithRequestID(ctx)
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
