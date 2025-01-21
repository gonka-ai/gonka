package logging

import (
	"log/slog"
	"os"
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
