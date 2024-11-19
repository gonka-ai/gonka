package types

type InferenceLogger interface {
	LogInfo(msg string, keyvals ...interface{})
	LogError(msg string, keyvals ...interface{})
	LogWarn(msg string, keyvals ...interface{})
	LogDebug(msg string, keyvals ...interface{})
}
