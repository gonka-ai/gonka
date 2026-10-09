package events

import (
	"log/slog"

	cmtlog "github.com/cometbft/cometbft/libs/log"
)

// cometSlog routes Comet's WS client (nop by default) into slog so unmarshal
// failures and full subscription channels are visible next to our own logs.
type cometSlog struct{}

func (cometSlog) Debug(msg string, keyvals ...interface{}) {
	slog.Debug("chain events: comet "+msg, slogKVs(keyvals)...)
}

func (cometSlog) Info(msg string, keyvals ...interface{}) {
	slog.Info("chain events: comet "+msg, slogKVs(keyvals)...)
}

func (cometSlog) Error(msg string, keyvals ...interface{}) {
	slog.Error("chain events: comet "+msg, slogKVs(keyvals)...)
}

func (cometSlog) With(keyvals ...interface{}) cmtlog.Logger {
	_ = keyvals
	return cometSlog{}
}

func slogKVs(keyvals []interface{}) []any {
	out := make([]any, 0, len(keyvals)+1)
	for _, kv := range keyvals {
		out = append(out, kv)
	}
	if len(out)%2 == 1 {
		out = append(out, "<missing>")
	}
	return out
}
