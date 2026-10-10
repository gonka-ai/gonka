package main

import (
	"context"
	"os"

	"devshard/logging"
	"devshard/observability"
)

func gatewayLoggerOptions(format string) observability.LoggerOptions {
	return observability.LoggerOptions{
		Format: format,
		Level:  observability.ParseLogLevel(os.Getenv("DEVSHARD_LOG_LEVEL")),
	}
}

// installGatewayLogger installs the process logger. DEVSHARD_LOG_LEVEL selects
// the minimum level; unset stays at Info.
func installGatewayLogger(format string) {
	observability.InstallLoggerWithOptions(gatewayLoggerOptions(format))
}

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

// logInferenceWarn is logInferenceStage at warning level, for a stage that names a fault rather than a step.
func logInferenceWarn(ctx context.Context, escrowID string, nonce uint64, stage string, kv ...any) {
	fields := make([]any, 0, 6+len(kv))
	if requestID, ok := requestLogFromContext(ctx); ok {
		fields = append(fields, "request", requestID)
	}
	fields = append(fields, "escrow", escrowID, "nonce", nonce)
	fields = append(fields, kv...)
	logging.Warn(stage, fields...)
}

func logInferenceStage(ctx context.Context, escrowID string, nonce uint64, stage string, kv ...any) {
	fields := make([]any, 0, 4+len(kv))
	fields = append(fields, "escrow", escrowID, "nonce", nonce)
	fields = append(fields, kv...)
	logging.Stage(ctx, stage, fields...)
}
