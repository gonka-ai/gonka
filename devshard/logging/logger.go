package logging

import (
	"log/slog"
	"os"
	"strings"
)

// Logger is the interface for structured logging in the devshard package.
// Callers pass subsystem as a keyval: Info("applied diff", "subsystem", "state", "nonce", 5).
// When dapi integrates, it calls SetLogger() with an adapter that routes to
// dapi's configured slog handler.
type Logger interface {
	Info(msg string, keyvals ...any)
	Error(msg string, keyvals ...any)
	Warn(msg string, keyvals ...any)
	Debug(msg string, keyvals ...any)
}

var current Logger = &slogLogger{}

func SetLogger(l Logger) { current = l }

func Info(msg string, keyvals ...any)  { current.Info(msg, keyvals...) }
func Error(msg string, keyvals ...any) { current.Error(msg, keyvals...) }
func Warn(msg string, keyvals ...any)  { current.Warn(msg, keyvals...) }
func Debug(msg string, keyvals ...any) { current.Debug(msg, keyvals...) }

type slogLogger struct{}

func (s *slogLogger) Info(msg string, kv ...any)  { slog.Info(msg, kv...) }
func (s *slogLogger) Error(msg string, kv ...any) { slog.Error(msg, kv...) }
func (s *slogLogger) Warn(msg string, kv ...any)  { slog.Warn(msg, kv...) }
func (s *slogLogger) Debug(msg string, kv ...any) { slog.Debug(msg, kv...) }

// ConfigureSlogFromEnv sets slog.Default from LOG_LEVEL and TESTENV_JSON_LOGS.
// Testenv compose sets LOG_LEVEL=debug and TESTENV_JSON_LOGS=1 so height-sync
// PoC debug lines reach Loki and container E2E tests can parse JSON log lines.
func ConfigureSlogFromEnv() {
	level := slog.LevelInfo
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_LEVEL"))) {
	case "debug":
		level = slog.LevelDebug
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if os.Getenv("TESTENV_JSON_LOGS") == "1" {
		h = slog.NewJSONHandler(os.Stderr, opts)
	} else {
		h = slog.NewTextHandler(os.Stderr, opts)
	}
	slog.SetDefault(slog.New(h))
}
