package logging

import (
	"github.com/productscience/inference/x/inference/types"
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

func Warn(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	withSubsystem := append([]interface{}{"subsystem", subSystem}, keyvals...)
	slog.Warn(msg, withSubsystem...)
}

func Info(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	withSubsystem := append([]interface{}{"subsystem", subSystem}, keyvals...)
	slog.Info(msg, withSubsystem...)
}
func Error(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	withSubsystem := append([]interface{}{"subsystem", subSystem}, keyvals...)
	slog.Error(msg, withSubsystem...)
}
func Debug(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	withSubsystem := append([]interface{}{"subsystem", subSystem}, keyvals...)
	slog.Debug(msg, withSubsystem...)
}
