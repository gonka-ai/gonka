package logging

import (
	"context"
	"log/slog"
	"os"
	"reflect"

	"github.com/productscience/inference/x/inference/types"
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

	// Check for error values and add their types
	for i := 0; i < len(keyvals); i += 2 {
		if i+1 < len(keyvals) {
			if err, ok := keyvals[i+1].(error); ok {
				errorType := reflect.TypeOf(err).String()
				withSubsystem = append(withSubsystem, "error-type", errorType)
			}
		}
	}

	slog.Error(msg, withSubsystem...)
}
func Debug(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	withSubsystem := append([]interface{}{"subsystem", subSystem}, keyvals...)
	slog.Debug(msg, withSubsystem...)
}

const TraceLevel = -8

func Trace(msg string, subSystem types.SubSystem, keyvals ...interface{}) {
	withSubsystem := append([]interface{}{"subsystem", subSystem}, keyvals...)
	slog.Log(context.Background(), TraceLevel, msg, withSubsystem...)
}
