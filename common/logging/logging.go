package logging

import (
	"context"
	"log/slog"
	"os"
	"reflect"
)

func setNoopLogger() {
	var logLevel slog.LevelVar
	// Set the level above all normal levels
	logLevel.Set(slog.Level(100))

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: &logLevel,
	}))
	slog.SetDefault(logger)
}

func WithNoopLogger(action func() (any, error)) (any, error) {
	currentLogger := slog.Default()
	defer slog.SetDefault(currentLogger)

	setNoopLogger()
	return action()
}

func Warn(msg string, subSystem any, keyvals ...any) {
	WarnCtx(context.Background(), msg, subSystem, keyvals...)
}

func Info(msg string, subSystem any, keyvals ...any) {
	InfoCtx(context.Background(), msg, subSystem, keyvals...)
}

func Error(msg string, subSystem any, keyvals ...any) {
	ErrorCtx(context.Background(), msg, subSystem, keyvals...)
}

func Debug(msg string, subSystem any, keyvals ...any) {
	DebugCtx(context.Background(), msg, subSystem, keyvals...)
}

const TraceLevel = -8

func Trace(msg string, subSystem any, keyvals ...any) {
	TraceCtx(context.Background(), msg, subSystem, keyvals...)
}

// Ctx-aware variants forward the request context so a TraceHandler (or any
// slog handler that reads ctx) can stamp trace_id/span_id/request_id.
func WarnCtx(ctx context.Context, msg string, subSystem any, keyvals ...any) {
	withSubsystem := append([]any{"subsystem", subSystem}, keyvals...)
	slog.WarnContext(ctx, msg, withSubsystem...)
}

func InfoCtx(ctx context.Context, msg string, subSystem any, keyvals ...any) {
	withSubsystem := append([]any{"subsystem", subSystem}, keyvals...)
	slog.InfoContext(ctx, msg, withSubsystem...)
}

func ErrorCtx(ctx context.Context, msg string, subSystem any, keyvals ...any) {
	withSubsystem := append([]any{"subsystem", subSystem}, keyvals...)

	for i := 0; i < len(keyvals); i += 2 {
		if i+1 < len(keyvals) {
			if err, ok := keyvals[i+1].(error); ok {
				errorType := reflect.TypeOf(err).String()
				withSubsystem = append(withSubsystem, "error-type", errorType)
			}
		}
	}

	slog.ErrorContext(ctx, msg, withSubsystem...)
}

func DebugCtx(ctx context.Context, msg string, subSystem any, keyvals ...any) {
	withSubsystem := append([]any{"subsystem", subSystem}, keyvals...)
	slog.DebugContext(ctx, msg, withSubsystem...)
}

func TraceCtx(ctx context.Context, msg string, subSystem any, keyvals ...any) {
	withSubsystem := append([]any{"subsystem", subSystem}, keyvals...)
	slog.Log(ctx, TraceLevel, msg, withSubsystem...)
}
